package bot

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/fetch"
	"github.com/ungattocinereo/TeleVideoConverter/internal/tg"
)

const (
	statusPollInterval = 3 * time.Second
	jokeUpdateInterval = 12 * time.Second
	progressBarWidth   = 16
)

var statusTitles = map[string][2]string{
	"queued":     {"🥟", "Ссылка в пельменной очереди"},
	"working":    {"🥟", "Пельмени скачивают и приводят видео в порядок"},
	"delivering": {"🥐", "Пельменная доставка готовит отправку"},
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

var (
	statusJokes = newMessageBag(map[string][]string{
		"queued": queuedMessages, "working": workingMessages, "delivering": deliveringMessages,
	})
	terminalJokes = newMessageBag(map[string][]string{"done": successMessages, "error": errorMessages})
)

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

func progressBar(percent *float64, elapsed time.Duration) string {
	if percent != nil {
		p := math.Max(0, math.Min(100, *percent))
		filled := int(math.RoundToEven(progressBarWidth * p / 100))
		return fmt.Sprintf("[%s%s] %5.1f%%", strings.Repeat("█", filled), strings.Repeat("░", progressBarWidth-filled), p)
	}
	const pulse = 3
	travel := progressBarWidth - pulse
	step := int(elapsed / statusPollInterval)
	pos := step % (travel * 2)
	start := pos
	if pos > travel {
		start = travel*2 - pos
	}
	return "[" + strings.Repeat("░", start) + strings.Repeat("▓", pulse) + strings.Repeat("░", progressBarWidth-start-pulse) + "]"
}

func progressDetails(state string, elapsed time.Duration, p *fetch.Progress) string {
	full := 100.0
	switch state {
	case "queued":
		return progressBar(nil, elapsed) + "\n🥨 Кастрюля ищет свободную конфорку"
	case "delivering":
		return progressBar(&full, elapsed) + "\n🥐 Скачивание завершено · пельмени несут файл в Telegram"
	}
	if p == nil {
		p = &fetch.Progress{}
	}
	if p.Phase == "processing" {
		return progressBar(&full, elapsed) + "\n🥧 Скачивание завершено · перекодирую и проверяю начинку"
	}
	lines := []string{progressBar(p.Percent, elapsed)}
	switch p.Phase {
	case "video":
		lines = append(lines, "🥟 Качаю видеослой")
	case "audio":
		lines = append(lines, "🥨 Качаю аудиослой")
	case "media":
		lines = append(lines, "🥖 Качаю файл")
	default:
		lines = append(lines, "🥯 Качаю то, что удалось поймать шумовкой")
	}
	var metrics []string
	if p.Downloaded != nil {
		amount := formatProgressSize(float64(*p.Downloaded))
		if p.Total != nil && *p.Total > 0 {
			amount += " / " + formatProgressSize(float64(*p.Total))
		}
		metrics = append(metrics, amount)
	}
	if p.Speed != nil && *p.Speed > 0 {
		metrics = append(metrics, formatProgressSize(*p.Speed)+"/s")
	}
	if p.ETA != nil {
		metrics = append(metrics, "ещё ≈ "+formatElapsed(time.Duration(*p.ETA)*time.Second))
	}
	if len(metrics) > 0 {
		lines = append(lines, strings.Join(metrics, " · "))
	}
	return strings.Join(lines, "\n")
}

func statusMessage(state string, elapsed time.Duration, p *fetch.Progress, joke string) string {
	t, ok := statusTitles[state]
	if !ok {
		t = statusTitles["working"]
	}
	return fmt.Sprintf("%s %s · %s\n%s\n\n%s", t[0], t[1], formatElapsed(elapsed), progressDetails(state, elapsed, p), joke)
}

// -------------------------------------------------------------- tracker ---

// jobStatus is the live state of one download. Its mutex also serialises
// edits of the Telegram status message, so the final text can never be
// overwritten by a late animation frame.
type jobStatus struct {
	mu        sync.Mutex
	state     string
	progress  *fetch.Progress
	terminal  bool
	stopped   chan struct{}
	started   time.Time
	chatID    int64
	messageID int64
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
func (t *Tracker) Start(ctx context.Context, jobID, chatID, messageID int64, started time.Time, firstJoke string) {
	js := &jobStatus{state: "queued", started: started, chatID: chatID, messageID: messageID, stopped: make(chan struct{})}
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

// Set publishes a non-terminal state and optional download progress.
func (t *Tracker) Set(jobID int64, state string, p *fetch.Progress) {
	if js := t.get(jobID); js != nil {
		js.mu.Lock()
		js.state, js.progress = state, p
		js.mu.Unlock()
	}
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
		joke = statusJokes.pick("queued")
	}
	jokeState, lastJoke := "queued", time.Now()
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
		state := js.state
		if _, ok := statusTitles[state]; !ok {
			state = "working"
		}
		if state != jokeState || time.Since(lastJoke) >= jokeUpdateInterval {
			joke, jokeState, lastJoke = statusJokes.pick(state), state, time.Now()
		}
		text := statusMessage(state, time.Since(js.started), js.progress, joke)
		ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := t.tg.EditMessageText(ectx, js.chatID, js.messageID, text, false); err != nil {
			slog.Warn("could not refresh Telegram status message", "err", err)
		}
		cancel()
		js.mu.Unlock()
	}
}
