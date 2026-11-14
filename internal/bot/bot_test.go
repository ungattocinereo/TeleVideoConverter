package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/fetch"
)

func TestClassicPoolsKept(t *testing.T) {
	if len(queuedMessages) != 12 || len(workingMessages) != 28 || len(deliveringMessages) != 12 ||
		len(successMessages) != 12 || len(errorMessages) != 8 {
		t.Fatal("original Python pools changed")
	}
}

func TestPoolsAreFullAndUnique(t *testing.T) {
	seen := map[string]string{}
	for kind, pool := range jokePools {
		if len(pool) < 5 {
			t.Errorf("pool %q has only %d jokes", kind, len(pool))
		}
		for _, m := range pool {
			if prev, dup := seen[m]; dup {
				t.Errorf("duplicate joke in %q and %q: %s", prev, kind, m)
			}
			seen[m] = kind
		}
	}
}

func TestNoRepeatsBeforeExhausted(t *testing.T) {
	bag := newMessageBag(map[string][]string{"queued": jokePools["queued"]})
	got := map[string]bool{}
	for range jokePools["queued"] {
		got[bag.pick("queued")] = true
	}
	if len(got) != len(jokePools["queued"]) {
		t.Fatalf("repeats within one cycle: %d unique", len(got))
	}
}

func TestJokeKind(t *testing.T) {
	if k := jokeKind("queued", nil, 0); k != "queued" {
		t.Error(k)
	}
	if k := jokeKind("working", &fetch.Progress{Phase: "encoding"}, time.Hour); k != "encoding" {
		t.Error(k)
	}
	if k := jokeKind("delivering", &fetch.Progress{Phase: "compressing"}, 0); k != "compressing" {
		t.Error(k)
	}
	kinds := map[string]bool{}
	for range 500 {
		kinds[jokeKind("working", &fetch.Progress{Phase: "video"}, 5*time.Minute)] = true
	}
	for _, want := range []string{"veryslow", "video", "working"} {
		if !kinds[want] {
			t.Errorf("long video download never picked %q (%v)", want, kinds)
		}
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
	msg := statusMessage("working", 12*time.Second, &fetch.Progress{Phase: "video", Percent: &pct, Downloaded: &dl, Total: &total, Speed: &speed, ETA: &eta},
		quipFor("https://vm.tiktok.com/x/"), "joke")
	for _, want := range []string{"🥟🥟🥟🥟◦◦◦◦◦◦", "40.0%", "40.0 MB / 100.0 MB", "160 пельменей", "2.0 MB/s", "ещё ≈ 00:30",
		"Качаю видеослой", "бульон 52 °C", "TikTok", "joke"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if bar := progressBar(nil, 9*time.Second); !strings.Contains(bar, "🥟") || !strings.Contains(bar, "〰") {
		t.Errorf("indeterminate bar: %s", bar)
	}
	enc := statusMessage("working", time.Minute, &fetch.Progress{Phase: "encoding"}, "", "j")
	if !strings.Contains(enc, "варят видео заново") {
		t.Errorf("encoding title missing:\n%s", enc)
	}
}

func TestRussianFormatting(t *testing.T) {
	cases := map[int64]string{1: "1 пельмень", 3: "3 пельменя", 5: "5 пельменей", 11: "11 пельменей", 21: "21 пельмень", 112: "112 пельменей"}
	for n, want := range cases {
		if got := pelmeni(float64(n * pelmenBytes)); got != want {
			t.Errorf("%d -> %q, want %q", n, got, want)
		}
	}
	if ruDuration(7) != "7 с" || ruDuration(75) != "1 мин 15 с" || ruDuration(3700) != "1 ч 1 мин" || ruDuration(120) != "2 мин" {
		t.Fatal("ruDuration")
	}
	if pyFloat(5) != "5.0" || formatSize(1536) != "1.5 KB" {
		t.Fatal("formatting")
	}
}

func TestErrorQuips(t *testing.T) {
	if !strings.Contains(errorQuip("ERROR: [Instagram] login required"), "паспорт") {
		t.Error("login")
	}
	if !strings.Contains(errorQuip("ERROR: Unsupported URL: https://example.com"), "ни одного видео") {
		t.Error("unsupported")
	}
	if errorQuip("something we cannot generate a quip for") != "" {
		t.Error("unexpected quip")
	}
}

func TestPlatformQuip(t *testing.T) {
	if !strings.Contains(platformQuip("www.youtube.com"), "YouTube") || !strings.Contains(platformQuip("youtu.be"), "YouTube") {
		t.Error("youtube")
	}
	if !strings.Contains(platformQuip("notyoutube.com"), "Незнакомый") {
		t.Error("suffix match must need a dot")
	}
}
