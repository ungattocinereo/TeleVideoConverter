// Package bot is the Telegram side: polling, commands, the download worker
// and the animated "pelmeni" status messages.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/config"
	"github.com/ungattocinereo/TeleVideoConverter/internal/store"
	"github.com/ungattocinereo/TeleVideoConverter/internal/tg"
)

type Bot struct {
	cfg     *config.Config
	tg      *tg.Client
	store   *store.Store
	tracker *Tracker
	worker  *Worker

	rateMu sync.Mutex
	recent map[int64][]time.Time
}

func New(cfg *config.Config, c *tg.Client, s *store.Store, t *Tracker, w *Worker) *Bot {
	return &Bot{cfg: cfg, tg: c, store: s, tracker: t, worker: w, recent: map[int64][]time.Time{}}
}

// Run long-polls Telegram until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	if me, err := b.tg.GetMe(ctx); err == nil {
		slog.Info("telegram bot started", "username", me.Username, "authorized_users", len(b.cfg.AllowedUsers))
	} else {
		slog.Error("telegram getMe failed", "err", err)
	}
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		ups, err := b.tg.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("telegram getUpdates failed", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			offset = u.UpdateID + 1
			if u.Message != nil && u.Message.From != nil {
				b.handle(ctx, u.Message)
			}
		}
	}
}

func (b *Bot) allowed(id int64) bool { return b.cfg.AllowedUsers[id] }

func (b *Bot) reply(ctx context.Context, m *tg.Message, text string) {
	if _, err := b.tg.SendMessage(ctx, m.Chat.ID, text, tg.SendOpts{}); err != nil {
		slog.Warn("could not reply", "err", err)
	}
}

func (b *Bot) handle(ctx context.Context, m *tg.Message) {
	text := strings.TrimSpace(m.Text)
	user := m.From.ID
	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		cmd, _, _ := strings.Cut(strings.TrimPrefix(fields[0], "/"), "@")
		args := fields[1:]
		if !b.allowed(user) {
			slog.Warn("unauthorized command", "user", user, "cmd", cmd)
			return
		}
		switch cmd {
		case "start":
			b.reply(ctx, m, welcome)
		case "list":
			b.list(ctx, m)
		case "search":
			b.search(ctx, m, args)
		case "stats":
			b.stats(ctx, m)
		case "description":
			b.toggleDescription(ctx, m)
		case "pelmen", "pelmeni", "fact":
			b.reply(ctx, m, "🥟 "+jokes.pick("facts"))
		}
		return
	}
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		if !b.allowed(user) {
			slog.Warn("unauthorized message", "user", user)
			return
		}
		b.handleURL(ctx, m, text)
	}
}

const welcome = `🥟 Добро пожаловать в Пельменную!

Здесь видео не скачивают — здесь его лепят, варят и подают горячим.
Кидай ссылку из YouTube, TikTok, Instagram, X, Threads, Reddit, Vimeo
и ещё тысячи мест — пельмени разберутся. Почти всегда.

📜 Меню:
/list — что лежит в морозилке
/search слово — найти пельмень по названию
/stats — пельменная статистика
/description — подавать ли описание поста (сейчас: решают пельмени)
/pelmen — случайный пельменный факт

Видео хранятся несколько дней, потом тают. Это не баг, это свежесть.`

func (b *Bot) rateLimited(user int64) bool {
	b.rateMu.Lock()
	defer b.rateMu.Unlock()
	now := time.Now()
	keep := b.recent[user][:0]
	for _, t := range b.recent[user] {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	if len(keep) >= b.cfg.MaxDownloadsPerHour {
		b.recent[user] = keep
		return true
	}
	b.recent[user] = append(keep, now)
	return false
}

func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func (b *Bot) handleURL(ctx context.Context, m *tg.Message, link string) {
	slog.Info("received link", "user", m.From.ID)
	if !validURL(link) {
		b.reply(ctx, m, "❌ Это не ссылка, это сырое тесто. Пришли нормальный адрес, начинающийся с http.")
		return
	}
	if b.rateLimited(m.From.ID) {
		b.reply(ctx, m, fmt.Sprintf("♨️ Кастрюля перегрелась: не больше %d ссылок в час. Пельмени ушли на перекур, скоро вернутся.", b.cfg.MaxDownloadsPerHour))
		return
	}
	if st, err := b.store.GetStats(ctx, m.From.ID); err == nil && float64(st.UsedSpaceBytes)/(1<<30) >= b.cfg.MaxStorageGB {
		b.reply(ctx, m, fmt.Sprintf("🧊 Морозилка забита под завязку (%s ГБ). Подожди, пока старые пельмени растают.", pyFloat(b.cfg.MaxStorageGB)))
		return
	}

	// One status message is edited throughout the download. If Telegram is
	// down we still queue the link.
	started := time.Now()
	joke := jokes.pick("queued")
	var statusID int64
	if sm, err := b.tg.SendMessage(ctx, m.Chat.ID, statusMessage("queued", 0, nil, quipFor(link), joke), tg.SendOpts{}); err == nil {
		statusID = sm.MessageID
	} else {
		slog.Error("could not send queued status; queueing download without it", "err", err)
	}
	job := &store.Job{URL: link, Quality: "best", UserID: m.From.ID, ChatID: m.Chat.ID, MessageID: m.MessageID, StatusMessageID: statusID}
	if err := b.store.EnqueueJob(ctx, job); err != nil {
		slog.Error("could not queue download", "err", err)
		if statusID != 0 {
			b.tg.EditMessageText(ctx, m.Chat.ID, statusID,
				"❌ Пельменная очередь исчезла вместе с номерками.\n\nТехническая причина: "+truncate(err.Error(), 800), true)
		}
		return
	}
	b.tracker.Start(ctx, job.ID, job.ChatID, statusID, link, started, joke)
	b.worker.Wake()
	slog.Info("queued download", "job", job.ID, "user", m.From.ID)
}

func remaining(deleteAt int64) string {
	secs := deleteAt - time.Now().Unix()
	if secs <= 0 {
		return "вот-вот растает"
	}
	return "растает через " + ruDuration(secs)
}

func (b *Bot) list(ctx context.Context, m *tg.Message) {
	vs, err := b.store.ListVideos(ctx, m.From.ID)
	if err != nil {
		slog.Error("list videos", "err", err)
		return
	}
	if len(vs) == 0 {
		b.reply(ctx, m, "🧊 Морозилка пуста. Пельмени скучают и смотрят на дверь.")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧊 В морозилке %d %s:\n\n", len(vs), plural(int64(len(vs)), "видео", "видео", "видео"))
	for _, v := range vs {
		fmt.Fprintf(&sb, "📹 %s\n📦 %s (≈ %s) · 🗑 %s\n📅 %s\n\n", runes(v.Title, 50), formatSize(v.FileSize),
			pelmeni(float64(v.FileSize)), remaining(v.DeleteTimestamp), ruDate(v.DownloadTimestamp))
	}
	b.sendLong(ctx, m, sb.String())
}

func (b *Bot) search(ctx context.Context, m *tg.Message, args []string) {
	if len(args) == 0 {
		b.reply(ctx, m, "🔍 Что ищем? Напиши так: /search котики")
		return
	}
	kw := strings.Join(args, " ")
	vs, err := b.store.SearchVideos(ctx, m.From.ID, kw)
	if err != nil {
		slog.Error("search videos", "err", err)
		return
	}
	if len(vs) == 0 {
		b.reply(ctx, m, "🔦 Пельмени перерыли всю морозилку — ничего про «"+kw+"». Только лёд и одинокий вареник.")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔍 Нашлось по запросу «%s»:\n\n", kw)
	for _, v := range vs {
		fmt.Fprintf(&sb, "📹 %s\n📦 %s · 🗑 %s\n\n", runes(v.Title, 50), formatSize(v.FileSize), remaining(v.DeleteTimestamp))
	}
	b.sendLong(ctx, m, sb.String())
}

// sendLong splits text at Telegram's 4096-character limit.
func (b *Bot) sendLong(ctx context.Context, m *tg.Message, text string) {
	r := []rune(text)
	for len(r) > 0 {
		n := min(len(r), 4000)
		if n < len(r) {
			if i := strings.LastIndex(string(r[:n]), "\n\n"); i > 0 {
				n = len([]rune(string(r[:n])[:i]))
			}
		}
		b.reply(ctx, m, string(r[:n]))
		r = []rune(strings.TrimLeft(string(r[n:]), "\n"))
	}
}

func (b *Bot) stats(ctx context.Context, m *tg.Message) {
	st, err := b.store.GetStats(ctx, m.From.ID)
	if err != nil {
		slog.Error("stats", "err", err)
		return
	}
	used := float64(st.UsedSpaceBytes) / (1 << 30)
	verdict := "Пельмени довольны и слегка округлились."
	switch {
	case st.Downloads7d == 0:
		verdict = "За неделю ни одной ссылки. Пельмени начинают подозревать, что ты ходишь в другую пельменную."
	case st.Downloads7d >= 20:
		verdict = "Ты кормишь пельменей лучше, чем родная бабушка. Министерство Начинки впечатлено."
	}
	b.reply(ctx, m, fmt.Sprintf(`📊 Пельменная статистика

🧊 В морозилке: %.2f из %s ГБ (≈ %s)
🕳 Свободно: %.2f ГБ
📹 Видео сейчас: %d
🥟 Слеплено за 7 дней: %d
🥟 Слеплено за 30 дней: %d

%s`, used, pyFloat(b.cfg.MaxStorageGB), pelmeni(float64(st.UsedSpaceBytes)), b.cfg.MaxStorageGB-used,
		st.TotalVideos, st.Downloads7d, st.Downloads30d, verdict))
}

func (b *Bot) toggleDescription(ctx context.Context, m *tg.Message) {
	cur, ok, err := b.store.SendDescription(ctx, m.From.ID)
	if err != nil {
		slog.Error("get setting", "err", err)
		return
	}
	if !ok {
		cur = b.cfg.SendDescription
	}
	if err := b.store.SetSendDescription(ctx, m.From.ID, !cur); err != nil {
		slog.Error("set setting", "err", err)
		return
	}
	if cur {
		b.reply(ctx, m, "🤐 Описание постов выключено ❌ Пельмени дали обет молчания и подают видео без лишних слов.")
		return
	}
	b.reply(ctx, m, "📝 Описание постов включено ✅ Пельмени будут зачитывать подписи вслух, с выражением.")
}

// ------------------------------------------------------------- formatting ---

func formatSize(n int64) string {
	v := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, unit)
		}
		v /= 1024
	}
	return fmt.Sprintf("%.1f TB", v)
}

// ruDuration prints 7 с, 1 мин 15 с, 2 ч 3 мин.
func ruDuration(secs int64) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%d с", secs)
	case secs < 3600:
		if secs%60 == 0 {
			return fmt.Sprintf("%d мин", secs/60)
		}
		return fmt.Sprintf("%d мин %d с", secs/60, secs%60)
	case secs < 86400:
		return fmt.Sprintf("%d ч %d мин", secs/3600, secs%3600/60)
	}
	return fmt.Sprintf("%d д %d ч", secs/86400, secs%86400/3600)
}

var ruMonths = [...]string{"янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

// ruDate prints 17 ноя, 21:31 in the container's local time.
func ruDate(unix int64) string {
	t := time.Unix(unix, 0)
	return fmt.Sprintf("%d %s, %s", t.Day(), ruMonths[t.Month()-1], t.Format("15:04"))
}

// pyFloat prints a float the way Python's str() does for simple values (5.0).
func pyFloat(f float64) string {
	if f == math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func runes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func truncate(s string, n int) string { return runes(s, n) }
