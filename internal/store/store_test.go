package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestVideosStatsAndJobs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	v := &Video{VideoID: "abc", TelegramUserID: 7, OriginalURL: "https://x", Title: "Cats", DownloadedQuality: "best",
		FileSize: 100, Format: "mp4", FilePath: "/storage/videos/abc.mp4",
		DownloadTimestamp: now.Unix(), DeleteTimestamp: now.Add(time.Hour).Unix()}
	if err := s.SaveVideo(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.FileSize = 200 // re-download of the same id updates the row
	if err := s.SaveVideo(ctx, v); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetStats(ctx, 7)
	if st.TotalVideos != 1 || st.UsedSpaceBytes != 200 || st.Downloads7d != 2 {
		t.Fatalf("stats %+v", st)
	}
	if vs, _ := s.SearchVideos(ctx, -1, "at"); len(vs) != 1 {
		t.Fatal("search")
	}
	if ok, _ := s.DeleteVideo(ctx, "abc"); !ok {
		t.Fatal("delete")
	}

	if _, ok, _ := s.SendDescription(ctx, 7); ok {
		t.Fatal("setting should be unset")
	}
	s.SetSendDescription(ctx, 7, false)
	if on, ok, _ := s.SendDescription(ctx, 7); !ok || on {
		t.Fatal("setting")
	}

	j := &Job{URL: "https://a", Quality: "best", UserID: 7, ChatID: 7}
	s.EnqueueJob(ctx, j)
	got, err := s.ClaimNextJob(ctx)
	if err != nil || got == nil || got.ID != j.ID {
		t.Fatalf("claim %v %v", got, err)
	}
	if again, _ := s.ClaimNextJob(ctx); again != nil {
		t.Fatal("job claimed twice")
	}
	if n, _ := s.ResetRunningJobs(ctx); n != 1 {
		t.Fatal("reset")
	}
}
