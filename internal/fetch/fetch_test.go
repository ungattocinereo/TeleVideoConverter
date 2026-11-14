package fetch

import "testing"

func TestDecodeThreadsShortcode(t *testing.T) {
	got, err := DecodeThreadsShortcode("Dbv0cqSEQKk")
	if err != nil || got != "3958613265446142628" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := DecodeThreadsShortcode("bad!"); err == nil {
		t.Fatal("expected error for invalid shortcode")
	}
}

func TestParseProgress(t *testing.T) {
	p := parseProgress("avc1.64001F|none|41943040|104857600|NA|2097152.0|30")
	if p.Phase != "video" || p.Percent == nil || *p.Percent != 40 || *p.ETA != 30 || *p.Speed != 2097152 {
		t.Fatalf("unexpected %+v", p)
	}
	p = parseProgress("none|mp4a.40.2|100|NA|400|NA|NA")
	if p.Phase != "audio" || p.Total == nil || *p.Total != 400 || *p.Percent != 25 || p.ETA != nil {
		t.Fatalf("estimate fallback: %+v", p)
	}
	p = parseProgress("NA|NA|100|NA|NA|NA|NA")
	if p.Phase != "media" || p.Percent != nil {
		t.Fatalf("unknown: %+v", p)
	}
}

func TestHostMatching(t *testing.T) {
	if !hostIs("https://www.threads.com/@a/post/X", "threads.com", "threads.net") {
		t.Fatal("threads.com")
	}
	if hostIs("https://evilthreads.com/post/X", "threads.com") {
		t.Fatal("suffix without dot must not match")
	}
	if !hostIs("https://vm.tiktok.com/ZGd/", "tiktok.com") {
		t.Fatal("tiktok short link")
	}
}
