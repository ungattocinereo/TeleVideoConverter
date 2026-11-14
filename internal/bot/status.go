package bot

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/fetch"
	"github.com/ungattocinereo/TeleVideoConverter/internal/tg"
)

const (
	statusPollInterval = 3 * time.Second
	jokeUpdateInterval = 9 * time.Second
	barSlots           = 10
	slowAfter          = 45 * time.Second
	verySlowAfter      = 3 * time.Minute
	telegramSlowAfter  = 20 * time.Second
	maxEditBackoff     = time.Minute
	// One "standard pelmen" of data, per the Ministry of Filling's GOST.
	pelmenBytes = 256 * 1024
)

var statusTitles = map[string][2]string{
	"queued":     {"🥟", "Ссылка в пельменной очереди"},
	"working":    {"🥟", "Пельмени скачивают видео"},
	"remuxing":   {"🍽", "Пельмени сервируют видео"},
	"encoding":   {"🔥", "Пельмени варят видео заново"},
	"delivering": {"🥐", "Пельменная доставка в пути"},
}

// messageBag hands out shuffled messages without early repeats.
type messageBag struct {
	mu    sync.Mutex
	pools map[string][]string
	bags  map[string][]string
}

func newMessageBag(pools map[string][]string) *messageBag {
	return &messageBag{pools: pools, bags: map[string][]string{}}
}

func (b *messageBag) pick(kind string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pools[kind]) == 0 {
		kind = "working"
	}
	if len(b.bags[kind]) == 0 {
		bag := append([]string(nil), b.pools[kind]...)
		rand.Shuffle(len(bag), func(i, j int) { bag[i], bag[j] = bag[j], bag[i] })
		b.bags[kind] = bag
	}
	n := len(b.bags[kind])
	msg := b.bags[kind][n-1]
	b.bags[kind] = b.bags[kind][:n-1]
	return msg
}

var jokes = newMessageBag(jokePools)

// jokeKind picks the pool for the current moment. Download phases mix their
// own jokes with the general ones, and long downloads get increasingly
// unhinged.
func jokeKind(state string, p *fetch.Progress, elapsed, inState time.Duration) string {
	phase := ""
	if p != nil {
		phase = p.Phase
	}
	switch state {
	case "queued":
		return "queued"
	case "delivering":
		if phase == "compressing" {
			return "compressing"
		}
		if inState > telegramSlowAfter && rand.Float64() < 0.6 {
			return "tgslow"
		}
		return "delivering"
	}
	switch phase {
	case "remuxing":
		return "remux"
	case "encoding":
		return "encoding"
	}
	r := rand.Float64()
	switch {
	case elapsed > verySlowAfter && r < 0.5:
		return "veryslow"
	case elapsed > slowAfter && r < 0.4:
		return "slow"
	case (phase == "video" || phase == "audio") && r < 0.6:
		return phase
	}
	return "working"
}

func formatElapsed(d time.Duration) string {
	total := max(0, int(d.Seconds()))
	h, m, s := total/3600, total/60%60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatProgressSize(v float64) string {
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if v < 1024 || unit == "GB" {
			return fmt.Sprintf("%.1f %s", v, unit)
		}
		v /= 1024
	}
	return fmt.Sprintf("%.1f GB", v)
}

// plural picks the Russian word form for n (1 пельмень, 2 пельменя, 5 пельменей).
func plural(n int64, one, few, many string) string {
	n = max(n, -n) % 100
	switch {
	case n >= 11 && n <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	}
	return many
}

func pelmeni(bytes float64) string {
	n := int64(math.Round(bytes / pelmenBytes))
	return fmt.Sprintf("%d %s", n, plural(n, "пельмень", "пельменя", "пельменей"))
}

// progressBar draws dumplings for known progress, or one dumpling bobbing in
// the broth when the total size is unknown.
func progressBar(percent *float64, elapsed time.Duration) string {
	if percent != nil {
		p := math.Max(0, math.Min(100, *percent))
		filled := int(math.RoundToEven(barSlots * p / 100))
		return fmt.Sprintf("%s%s %5.1f%%", strings.Repeat("🥟", filled), strings.Repeat("◦", barSlots-filled), p)
	}
	travel := barSlots - 1
	pos := int(elapsed/statusPollInterval) % (travel * 2)
	if pos > travel {
		pos = travel*2 - pos
	}
	return strings.Repeat("〰", pos) + "🥟" + strings.Repeat("〰", travel-pos)
}

// broth turns progress into a broth temperature: boiling at 100%.
func broth(percent float64) string {
	t := int(20 + 0.8*math.Max(0, math.Min(100, percent)))
	switch {
	case t >= 100:
		return "🌡 бульон кипит, пельмени всплывают"
	case t >= 80:
		return fmt.Sprintf("🌡 бульон %d °C, пельмени волнуются", t)
	case t >= 50:
		return fmt.Sprintf("🌡 бульон %d °C, пахнет надеждой", t)
	}
	return fmt.Sprintf("🌡 бульон %d °C, пельмени ждут", t)
}

func progressDetails(state string, elapsed time.Duration, p *fetch.Progress) string {
	full := 100.0
	if p == nil {
		p = &fetch.Progress{}
	}
	switch state {
	case "queued":
		return progressBar(nil, elapsed) + "\n🥨 Кастрюля ищет свободную конфорку"
	case "delivering":
		if p.Phase == "compressing" {
			return progressBar(nil, elapsed) + "\n🗜 Ужимаю под лимит Telegram в 50 МБ"
		}
		return progressBar(&full, elapsed) + "\n🥐 Пельмени несут файл в Telegram"
	}
	switch p.Phase {
	case "processing":
		return progressBar(&full, elapsed) + "\n🥧 Скачано · пельмени осматривают начинку"
	case "remuxing":
		return progressBar(&full, elapsed) + "\n🍽 Скачано · видео уже годное, перекладываю в тарелку"
	case "encoding":
		return progressBar(nil, elapsed) + "\n🔥 Скачано · формат упрямый, варю в H.264"
	}
	lines := []string{progressBar(p.Percent, elapsed)}
	switch p.Phase {
	case "video":
		lines = append(lines, "🥟 Качаю видеослой")
	case "audio":
		lines = append(lines, "🥨 Качаю аудиослой")
	case "media":
		lines = append(lines, "🥖 Качаю файл целиком")
	default:
		lines = append(lines, "🥯 Ловлю шумовкой всё, что плавает")
	}
	var metrics []string
	if p.Downloaded != nil {
		amount := formatProgressSize(float64(*p.Downloaded))
		if p.Total != nil && *p.Total > 0 {
			amount += " / " + formatProgressSize(float64(*p.Total))
		}
		metrics = append(metrics, "📦 "+amount+" (≈ "+pelmeni(float64(*p.Downloaded))+")")
	}
	if p.Speed != nil && *p.Speed > 0 {
		metrics = append(metrics, "🚀 "+formatProgressSize(*p.Speed)+"/s")
	}
	if p.ETA != nil {
		metrics = append(metrics, "⏳ ещё ≈ "+formatElapsed(time.Duration(*p.ETA)*time.Second))
	}
	if len(metrics) > 0 {
		lines = append(lines, strings.Join(metrics, " · "))
	}
	if p.Percent != nil {
		lines = append(lines, broth(*p.Percent))
	}
	return strings.Join(lines, "\n")
}

func titleFor(state string, p *fetch.Progress) [2]string {
	if state == "working" && p != nil {
		if t, ok := statusTitles[p.Phase]; ok {
			return t
		}
	}
	if t, ok := statusTitles[state]; ok {
		return t
	}
	return statusTitles["working"]
}

func statusMessage(state string, elapsed time.Duration, p *fetch.Progress, quip, joke string) string {
	t := titleFor(state, p)
	head := fmt.Sprintf("%s %s · %s", t[0], t[1], formatElapsed(elapsed))
	if quip != "" {
		head += "\n" + quip
	}
	return fmt.Sprintf("%s\n\n%s\n\n%s", head, progressDetails(state, elapsed, p), joke)
}

func quipFor(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return platformQuip(strings.ToLower(u.Hostname()))
}

func lower(s string) string { return strings.ToLower(s) }

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------- tracker ---

// jobStatus is the live state of one download. Its mutex also serialises
// edits of the Telegram status message, so the final text can never be
// overwritten by a late animation frame.
type jobStatus struct {
	mu        sync.Mutex
	state     string
	since     time.Time // when state last changed
	progress  *fetch.Progress
	terminal  bool
	stopped   chan struct{}
	started   time.Time
	chatID    int64
	messageID int64
	quip      string
}

type Tracker struct {
	tg   *tg.Client
	mu   sync.Mutex
	jobs map[int64]*jobStatus
}

func NewTracker(c *tg.Client) *Tracker {
	return &Tracker{tg: c, jobs: map[int64]*jobStatus{}}
}

// Start begins animating a job's status message (if it has one).
func (t *Tracker) Start(ctx context.Context, jobID, chatID, messageID int64, rawURL string, started time.Time, firstJoke string) {
	js := &jobStatus{state: "queued", since: time.Now(), started: started, chatID: chatID, messageID: messageID,
		quip: quipFor(rawURL), stopped: make(chan struct{})}
	t.mu.Lock()
	t.jobs[jobID] = js
	t.mu.Unlock()
	if messageID == 0 {
		close(js.stopped)
		return
	}
	go t.animate(ctx, js, firstJoke)
}

func (t *Tracker) get(jobID int64) *jobStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.jobs[jobID]
}

// Set publishes a non-terminal state and optional progress/phase.
func (t *Tracker) Set(jobID int64, state string, p *fetch.Progress) {
	if js := t.get(jobID); js != nil {
		js.mu.Lock()
		if js.state != state {
			js.since = time.Now()
		}
		js.state, js.progress = state, p
		js.mu.Unlock()
	}
}

// Phase changes only the phase, keeping the current state.
func (t *Tracker) Phase(jobID int64, state, phase string) {
	t.Set(jobID, state, &fetch.Progress{Phase: phase})
}

// Finish stops the animation and replaces the status message with text.
// It reports whether the message was edited (false: caller should send it).
func (t *Tracker) Finish(ctx context.Context, jobID int64, text string) bool {
	js := t.get(jobID)
	t.mu.Lock()
	delete(t.jobs, jobID)
	t.mu.Unlock()
	if js == nil || js.messageID == 0 {
		return false
	}
	js.mu.Lock()
	defer js.mu.Unlock()
	js.terminal = true
	if err := t.tg.EditMessageText(ctx, js.chatID, js.messageID, text, true); err != nil {
		slog.Warn("could not edit terminal Telegram status", "err", err)
		return false
	}
	return true
}

// Drop stops a job's animation without touching its message (shutdown).
func (t *Tracker) Drop(jobID int64) {
	if js := t.get(jobID); js != nil {
		js.mu.Lock()
		js.terminal = true
		js.mu.Unlock()
	}
	t.mu.Lock()
	delete(t.jobs, jobID)
	t.mu.Unlock()
}

func (t *Tracker) animate(ctx context.Context, js *jobStatus, joke string) {
	defer close(js.stopped)
	if joke == "" {
		joke = jokes.pick("queued")
	}
	moment, lastJoke := "queued", time.Now()
	// When Telegram struggles, back off instead of editing every 3 s.
	failures, nextEdit := 0, time.Time{}
	tick := time.NewTicker(statusPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		js.mu.Lock()
		if js.terminal {
			js.mu.Unlock()
			return
		}
		elapsed := time.Since(js.started)
		now := js.state
		if js.progress != nil {
			now += "/" + js.progress.Phase
		}
		if now != moment || time.Since(lastJoke) >= jokeUpdateInterval {
			joke, moment, lastJoke = jokes.pick(jokeKind(js.state, js.progress, elapsed, time.Since(js.since))), now, time.Now()
		}
		if time.Now().Before(nextEdit) {
			js.mu.Unlock()
			continue
		}
		text := statusMessage(js.state, elapsed, js.progress, js.quip, joke)
		ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := t.tg.EditMessageText(ectx, js.chatID, js.messageID, text, false); err != nil {
			failures++
			wait := min(maxEditBackoff, statusPollInterval<<failures)
			nextEdit = time.Now().Add(wait)
			slog.Warn("could not refresh Telegram status message", "err", err, "next_try_in", wait)
		} else {
			failures = 0
		}
		cancel()
		js.mu.Unlock()
	}
}
