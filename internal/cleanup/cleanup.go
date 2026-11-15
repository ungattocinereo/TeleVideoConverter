// Package cleanup enforces retention and the storage cap, and removes files
// no database row points at (left behind by older versions or crashes).
package cleanup

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/config"
	"github.com/ungattocinereo/TeleVideoConverter/internal/store"
	"github.com/ungattocinereo/TeleVideoConverter/internal/web"
)

type Busy interface{ Busy() bool }

// Run cleans once at start and then every hour.
func Run(ctx context.Context, cfg *config.Config, s *store.Store, worker Busy) {
	for {
		Once(ctx, cfg, s, worker)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func Once(ctx context.Context, cfg *config.Config, s *store.Store, worker Busy) {
	expired, err := s.ExpiredVideos(ctx, time.Now())
	if err != nil {
		slog.Error("cleanup: expired query", "err", err)
		return
	}
	for i := range expired {
		web.RemoveFiles(&expired[i])
		s.DeleteVideo(ctx, expired[i].VideoID)
	}
	if len(expired) > 0 {
		slog.Info("cleanup: removed expired videos", "count", len(expired))
	}

	// Orphans: never while a download is writing files.
	if worker == nil || !worker.Busy() {
		removeOrphans(ctx, cfg, s)
	}

	limit := cfg.MaxStorageBytes()
	used := dirSize(filepath.Join(cfg.StoragePath, "videos")) + dirSize(filepath.Join(cfg.StoragePath, "thumbnails"))
	if used > limit {
		target := int64(float64(limit) * 0.9)
		vs, err := s.VideosOldestFirst(ctx)
		if err != nil {
			slog.Error("cleanup: list videos", "err", err)
			return
		}
		removed := 0
		for i := range vs {
			if used <= target {
				break
			}
			used -= fileSize(vs[i].FilePath)
			if vs[i].ThumbnailPath != nil {
				used -= fileSize(*vs[i].ThumbnailPath)
			}
			web.RemoveFiles(&vs[i])
			s.DeleteVideo(ctx, vs[i].VideoID)
			removed++
		}
		slog.Info("cleanup: storage over limit, removed oldest videos", "count", removed)
	}
	s.PruneJobs(ctx, 30*24*time.Hour)
}

// removeOrphans deletes files older than the retention period that no video
// row references, plus stale Telegram upload copies.
func removeOrphans(ctx context.Context, cfg *config.Config, s *store.Store) {
	refs, err := s.ReferencedFiles(ctx)
	if err != nil {
		slog.Error("cleanup: referenced files", "err", err)
		return
	}
	cutoff := time.Now().Add(-cfg.Retention())
	removed := 0
	for _, dir := range []string{"videos", "thumbnails", "telegram"} {
		root := filepath.Join(cfg.StoragePath, dir)
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || path == root {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || info.ModTime().After(cutoff) && dir != "telegram" {
				return nil
			}
			if dir == "telegram" && info.ModTime().After(time.Now().Add(-6*time.Hour)) {
				return nil
			}
			if d.IsDir() {
				if os.RemoveAll(path) == nil {
					removed++
				}
				return filepath.SkipDir
			}
			if refs[path] || d.Name() == ".gitkeep" {
				return nil
			}
			if os.Remove(path) == nil {
				removed++
			}
			return nil
		})
	}
	if removed > 0 {
		slog.Info("cleanup: removed orphaned files", "count", removed)
	}
}

func dirSize(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}
