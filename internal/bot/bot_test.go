package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/fetch"
)

func TestMessagePools(t *testing.T) {
	if len(queuedMessages) != 12 || len(workingMessages) != 28 || len(deliveringMessages) != 12 ||
		len(successMessages) != 12 || len(errorMessages) != 8 {
		t.Fatal("pool sizes changed")
	}
	seen := map[string]bool{}
	for _, m := range append(append(append([]string{}, queuedMessages...), workingMessages...), deliveringMessages...) {
		seen[m] = true
	}
	if len(seen) != 52 {
		t.Fatalf("expected 52 unique status messages, got %d", len(seen))
	}
}

func TestNoRepeatsBeforeExhausted(t *testing.T) {
	bag := newMessageBag(map[string][]string{"queued": queuedMessages})
	got := map[string]bool{}
	for range queuedMessages {
		got[bag.pick("queued")] = true
	}
	if len(got) != len(queuedMessages) {
		t.Fatalf("repeats within one cycle: %d unique", len(got))
	}
}

func TestFormatElapsed(t *testing.T) {
	for secs, want := range map[int]string{0: "00:00", 73: "01:13", 3671: "1:01:11"} {
		if got := formatElapsed(time.Duration(secs) * time.Second); got != want {
			t.Errorf("%d -> %q, want %q", secs, got, want)
		}
	}
}

func TestProgressMessage(t *testing.T) {
	pct, dl, total, speed, eta := 40.0, int64(40<<20), int64(100<<20), float64(2<<20), int64(30)
	msg := statusMessage("working", 12*time.Second, &fetch.Progress{Phase: "video", Percent: &pct, Downloaded: &dl, Total: &total, Speed: &speed, ETA: &eta}, "joke")
	for _, want := range []string{"40.0%", "40.0 MB / 100.0 MB", "2.0 MB/s", "ещё ≈ 00:30", "Качаю видеослой", "Пельмени скачивают"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if bar := progressBar(nil, 9*time.Second); !strings.Contains(bar, "▓▓▓") {
		t.Errorf("indeterminate bar: %s", bar)
	}
}

func TestPythonCompatibleFormatting(t *testing.T) {
	if pyFloat(5) != "5.0" || pyFloat(2.5) != "2.5" {
		t.Fatal("pyFloat")
	}
	if formatSize(1536) != "1.5 KB" || formatDuration(75) != "1m 15s" || formatDuration(3700) != "1h 1m" {
		t.Fatal("formatting")
	}
}
