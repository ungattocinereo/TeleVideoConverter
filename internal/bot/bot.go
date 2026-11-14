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

const welcome = `
🎬 Welcome to TeleVideo Converter Bot!

Send me a video URL and I'll download it for you.

📝 Commands:
/start - Show this message
/list - Show all saved videos
/search <keyword> - Search videos
/stats - Show usage statistics
/description - Toggle post description

Supported platforms: YouTube, Vimeo, and many more!
`

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
		b.reply(ctx, m, "❌ Invalid URL")
		return
	}
	if b.rateLimited(m.From.ID) {
		b.reply(ctx, m, fmt.Sprintf("⚠️ Rate limit exceeded. Maximum %d downloads per hour.", b.cfg.MaxDownloadsPerHour))
		return
	}
	if st, err := b.store.GetStats(ctx, m.From.ID); err == nil && float64(st.UsedSpaceBytes)/(1<<30) >= b.cfg.MaxStorageGB {
		b.reply(ctx, m, fmt.Sprintf("❌ Storage limit reached (%sGB). Please wait for old files to be deleted.", pyFloat(b.cfg.MaxStorageGB)))
		return
	}

	// One status message is edited throughout the download. If Telegram is
	// down we still queue the link.
	started := time.Now()
	joke := statusJokes.pick("queued")
	var statusID int64
	if sm, err := b.tg.SendMessage(ctx, m.Chat.ID, statusMessage("queued", 0, nil, joke), tg.SendOpts{}); err == nil {
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
	b.tracker.Start(ctx, job.ID, job.ChatID, statusID, started, joke)
	b.worker.Wake()
	slog.Info("queued download", "job", job.ID, "user", m.From.ID)
}

func remaining(deleteAt int64) string {
	secs := float64(deleteAt - time.Now().Unix())
	h := int(math.Floor(secs / 3600))
	mins := int(math.Floor(math.Mod(math.Mod(secs, 3600)+3600, 3600) / 60))
	return fmt.Sprintf("%dh %dm", h, mins)
}

func (b *Bot) list(ctx context.Context, m *tg.Message) {
	vs, err := b.store.ListVideos(ctx, m.From.ID)
	if err != nil {
		slog.Error("list videos", "err", err)
		return
	}
	if len(vs) == 0 {
		b.reply(ctx, m, "📁 No videos found")
		return
	}
	var sb strings.Builder
	sb.WriteString("📁 Your videos:\n\n")
	for _, v := range vs {
		fmt.Fprintf(&sb, "📹 %s\n📦 %s\n🗑 Deletes in: %s\n📅 %s\n\n", runes(v.Title, 50), formatSize(v.FileSize),
			remaining(v.DeleteTimestamp), time.Unix(v.DownloadTimestamp, 0).Format("Jan 02, 03:04 PM"))
	}
	b.sendLong(ctx, m, sb.String())
}

func (b *Bot) search(ctx context.Context, m *tg.Message, args []string) {
	if len(args) == 0 {
		b.reply(ctx, m, "❌ Please provide a search keyword\nUsage: /search <keyword>")
		return
	}
	kw := strings.Join(args, " ")
	vs, err := b.store.SearchVideos(ctx, m.From.ID, kw)
	if err != nil {
		slog.Error("search videos", "err", err)
		return
	}
	if len(vs) == 0 {
		b.reply(ctx, m, "🔍 No videos found for: "+kw)
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔍 Search results for '%s':\n\n", kw)
	for _, v := range vs {
		fmt.Fprintf(&sb, "📹 %s\n📦 %s\n🗑 Deletes in: %s\n\n", runes(v.Title, 50), formatSize(v.FileSize), remaining(v.DeleteTimestamp))
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
	b.reply(ctx, m, fmt.Sprintf(`
📊 Usage Statistics

💾 Storage: %.2f GB / %s GB
📁 Free space: %.2f GB
📹 Total videos: %d
⬇️ Downloads (7 days): %d
⬇️ Downloads (30 days): %d
`, used, pyFloat(b.cfg.MaxStorageGB), b.cfg.MaxStorageGB-used, st.TotalVideos, st.Downloads7d, st.Downloads30d))
}

func (b *Bot) toggleDescription(ctx context.Context, m *tg.Message) {
	cur, ok, err := b.store.SendDescription(ctx, m.From.ID)
	if err != nil {
		slog.Error("get setting", "err", err)
		return
	}
	if !ok {
		cur = true
	}
	if err := b.store.SetSendDescription(ctx, m.From.ID, !cur); err != nil {
		slog.Error("set setting", "err", err)
		return
	}
	status := "enabled ✅"
	if cur {
		status = "disabled ❌"
	}
	b.reply(ctx, m, "📝 Post description is now "+status)
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

func formatDuration(secs int64) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh %dm", secs/3600, secs%3600/60)
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
