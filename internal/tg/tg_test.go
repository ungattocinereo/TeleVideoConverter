package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendVideoMultipartAndRetryOn429(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mp4")
	thumb := filepath.Join(dir, "t.jpg")
	os.WriteFile(video, []byte(strings.Repeat("v", 3<<20)), 0o644)
	os.WriteFile(thumb, []byte("jpeg"), 0o644)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendVideo") {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if r.ContentLength <= 0 || r.ContentLength != int64(len(raw)) {
			t.Errorf("Content-Length %d, body %d bytes", r.ContentLength, len(raw))
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if calls.Add(1) == 1 { // first attempt: rate limited
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 429, "description": "Too Many Requests",
				"parameters": map[string]int{"retry_after": 1}})
			return
		}
		if r.FormValue("chat_id") != "42" || r.FormValue("supports_streaming") != "true" ||
			r.FormValue("width") != "720" || r.FormValue("duration") != "12" || r.FormValue("caption") != "Part 1/2" {
			t.Errorf("fields %v", r.MultipartForm.Value)
		}
		f, _, err := r.FormFile("video")
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, f)
		if n != 3<<20 {
			t.Errorf("video size %d", n)
		}
		if _, _, err := r.FormFile("thumb_file"); err != nil || r.FormValue("thumbnail") != "attach://thumb_file" {
			t.Error("thumbnail missing")
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"video": map[string]string{"file_id": "FID"}}})
	}))
	defer srv.Close()

	c := New("123:SECRET", 3, 10*time.Millisecond, 2*time.Second)
	c.base = srv.URL + "/bot123:SECRET/"
	fid, err := c.SendVideo(context.Background(), 42, video, VideoParams{Width: 720, Height: 1280, Duration: 12, Caption: "Part 1/2", Thumbnail: thumb})
	if err != nil || fid != "FID" {
		t.Fatalf("fid=%q err=%v", fid, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", calls.Load())
	}
}

func TestErrorsNeverContainToken(t *testing.T) {
	c := New("123:SECRET", 1, time.Millisecond, time.Millisecond)
	c.base = "http://127.0.0.1:1/bot123:SECRET/" // connection refused
	_, err := c.SendMessage(context.Background(), 1, "hi", SendOpts{})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"})
	}))
	defer srv.Close()
	c := New("t", 5, time.Millisecond, time.Millisecond)
	c.base = srv.URL + "/bott/"
	if _, err := c.SendMessage(context.Background(), 1, "hi", SendOpts{}); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
