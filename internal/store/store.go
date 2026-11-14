// Package store keeps videos, stats, user settings and the download queue in
// the same SQLite file (and schema) the Python services used.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type Video struct {
	ID                int64    `json:"id"`
	VideoID           string   `json:"video_id"`
	TelegramUserID    int64    `json:"telegram_user_id"`
	OriginalURL       string   `json:"original_url"`
	Title             string   `json:"title"`
	OriginalQuality   *string  `json:"original_quality"`
	DownloadedQuality string   `json:"downloaded_quality"`
	FileSize          int64    `json:"file_size"`
	ProcessingTime    int64    `json:"processing_time"`
	Format            string   `json:"format"`
	Codec             *string  `json:"codec"`
	SourcePlatform    *string  `json:"source_platform"`
	FilePath          string   `json:"file_path"`
	ThumbnailPath     *string  `json:"thumbnail_path"`
	DownloadTimestamp int64    `json:"download_timestamp"`
	DeleteTimestamp   int64    `json:"delete_timestamp"`
	DownloadSeconds   *float64 `json:"download_seconds"`
	ProcessSeconds    *float64 `json:"process_seconds"`
	SendSeconds       *float64 `json:"send_seconds"`
}

type Stats struct {
	TotalVideos    int64 `json:"total_videos"`
	UsedSpaceBytes int64 `json:"used_space_bytes"`
	Downloads7d    int64 `json:"downloads_7d"`
	Downloads30d   int64 `json:"downloads_30d"`
}

type Job struct {
	ID              int64
	URL             string
	Quality         string
	UserID          int64
	ChatID          int64
	MessageID       int64
	StatusMessageID int64
	CreatedAt       time.Time
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// One process owns the file now; a single connection serialises writes
	// and is plenty for a few downloads a day.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS videos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			video_id TEXT UNIQUE NOT NULL,
			telegram_user_id INTEGER NOT NULL,
			original_url TEXT NOT NULL,
			title TEXT NOT NULL,
			original_quality TEXT,
			downloaded_quality TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			processing_time INTEGER NOT NULL,
			format TEXT NOT NULL,
			codec TEXT,
			source_platform TEXT,
			file_path TEXT NOT NULL,
			thumbnail_path TEXT,
			download_timestamp INTEGER NOT NULL,
			delete_timestamp INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_delete_timestamp ON videos(delete_timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_user_id ON videos(telegram_user_id)`,
		`CREATE TABLE IF NOT EXISTS download_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			telegram_user_id INTEGER NOT NULL,
			video_id TEXT NOT NULL,
			action TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_stats_timestamp ON download_stats(timestamp)`,
		`CREATE TABLE IF NOT EXISTS user_settings (
			telegram_user_id INTEGER PRIMARY KEY,
			send_description INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT NOT NULL,
			quality TEXT NOT NULL,
			user_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL DEFAULT 0,
			status_message_id INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'queued',
			error TEXT,
			created_at INTEGER NOT NULL,
			started_at INTEGER,
			finished_at INTEGER)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state, id)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// Stage timings, added by the Go version. Old code ignores extra columns.
	for _, col := range []string{"download_seconds", "process_seconds", "send_seconds"} {
		if err := s.addColumnIfMissing(ctx, "videos", col, "REAL"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addColumnIfMissing(ctx context.Context, table, column, typ string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, typ))
	return err
}

// ---------------------------------------------------------------- videos ---

const videoCols = `id, video_id, telegram_user_id, original_url, title, original_quality,
	downloaded_quality, file_size, processing_time, format, codec, source_platform,
	file_path, thumbnail_path, download_timestamp, delete_timestamp,
	download_seconds, process_seconds, send_seconds`

func scanVideos(rows *sql.Rows) ([]Video, error) {
	defer rows.Close()
	out := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.VideoID, &v.TelegramUserID, &v.OriginalURL, &v.Title,
			&v.OriginalQuality, &v.DownloadedQuality, &v.FileSize, &v.ProcessingTime, &v.Format,
			&v.Codec, &v.SourcePlatform, &v.FilePath, &v.ThumbnailPath, &v.DownloadTimestamp,
			&v.DeleteTimestamp, &v.DownloadSeconds, &v.ProcessSeconds, &v.SendSeconds); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaveVideo inserts a video or refreshes an existing row with the same
// video_id (re-downloads), and records a "download" stat, like the old worker.
func (s *Store) SaveVideo(ctx context.Context, v *Video) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO videos (video_id, telegram_user_id, original_url, title, original_quality,
			downloaded_quality, file_size, processing_time, format, codec, source_platform,
			file_path, thumbnail_path, download_timestamp, delete_timestamp,
			download_seconds, process_seconds, send_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(video_id) DO UPDATE SET
			telegram_user_id = excluded.telegram_user_id, original_url = excluded.original_url,
			title = excluded.title, original_quality = excluded.original_quality,
			downloaded_quality = excluded.downloaded_quality, file_size = excluded.file_size,
			processing_time = excluded.processing_time, format = excluded.format,
			codec = excluded.codec, source_platform = excluded.source_platform,
			file_path = excluded.file_path, thumbnail_path = excluded.thumbnail_path,
			download_timestamp = excluded.download_timestamp, delete_timestamp = excluded.delete_timestamp,
			download_seconds = excluded.download_seconds, process_seconds = excluded.process_seconds,
			send_seconds = excluded.send_seconds`,
		v.VideoID, v.TelegramUserID, v.OriginalURL, v.Title, v.OriginalQuality,
		v.DownloadedQuality, v.FileSize, v.ProcessingTime, v.Format, v.Codec, v.SourcePlatform,
		v.FilePath, v.ThumbnailPath, v.DownloadTimestamp, v.DeleteTimestamp,
		v.DownloadSeconds, v.ProcessSeconds, v.SendSeconds)
	if err != nil {
		return err
	}
	return s.AddStat(ctx, v.TelegramUserID, v.VideoID, "download")
}

func (s *Store) SetSendSeconds(ctx context.Context, videoID string, secs float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE videos SET send_seconds = ? WHERE video_id = ?`, secs, videoID)
	return err
}

// ListVideos returns videos newest first; userID < 0 means all users.
func (s *Store) ListVideos(ctx context.Context, userID int64) ([]Video, error) {
	if userID < 0 {
		return s.query(ctx, `SELECT `+videoCols+` FROM videos ORDER BY download_timestamp DESC`)
	}
	return s.query(ctx, `SELECT `+videoCols+` FROM videos WHERE telegram_user_id = ? ORDER BY download_timestamp DESC`, userID)
}

// SearchVideos matches titles with LIKE; userID < 0 means all users.
func (s *Store) SearchVideos(ctx context.Context, userID int64, keyword string) ([]Video, error) {
	like := "%" + keyword + "%"
	if userID < 0 {
		return s.query(ctx, `SELECT `+videoCols+` FROM videos WHERE title LIKE ? ORDER BY download_timestamp DESC`, like)
	}
	return s.query(ctx, `SELECT `+videoCols+` FROM videos WHERE telegram_user_id = ? AND title LIKE ? ORDER BY download_timestamp DESC`, userID, like)
}

func (s *Store) ExpiredVideos(ctx context.Context, now time.Time) ([]Video, error) {
	return s.query(ctx, `SELECT `+videoCols+` FROM videos WHERE delete_timestamp <= ?`, now.Unix())
}

func (s *Store) VideosOldestFirst(ctx context.Context) ([]Video, error) {
	return s.query(ctx, `SELECT `+videoCols+` FROM videos ORDER BY download_timestamp ASC`)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Video, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanVideos(rows)
}

func (s *Store) GetVideo(ctx context.Context, videoID string) (*Video, error) {
	vs, err := s.query(ctx, `SELECT `+videoCols+` FROM videos WHERE video_id = ?`, videoID)
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return &vs[0], nil
}

// DeleteVideo removes the row and records a "delete" stat. Files are the
// caller's job. Returns false when the video did not exist.
func (s *Store) DeleteVideo(ctx context.Context, videoID string) (bool, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT telegram_user_id FROM videos WHERE video_id = ?`, videoID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM videos WHERE video_id = ?`, videoID); err != nil {
		return false, err
	}
	return true, s.AddStat(ctx, userID, videoID, "delete")
}

func (s *Store) AddStat(ctx context.Context, userID int64, videoID, action string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO download_stats (telegram_user_id, video_id, action, timestamp) VALUES (?, ?, ?, ?)`,
		userID, videoID, action, time.Now().Unix())
	return err
}

// GetStats summarises one user (userID >= 0) or everyone (userID < 0).
func (s *Store) GetStats(ctx context.Context, userID int64) (Stats, error) {
	var st Stats
	where, args := "", []any{}
	if userID >= 0 {
		where, args = " WHERE telegram_user_id = ?", []any{userID}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(file_size), 0) FROM videos`+where, args...).
		Scan(&st.TotalVideos, &st.UsedSpaceBytes); err != nil {
		return st, err
	}
	count := func(since time.Duration) (int64, error) {
		q := `SELECT COUNT(*) FROM download_stats WHERE action = 'download' AND timestamp >= ?`
		a := []any{time.Now().Add(-since).Unix()}
		if userID >= 0 {
			q += ` AND telegram_user_id = ?`
			a = append(a, userID)
		}
		var n int64
		return n, s.db.QueryRowContext(ctx, q, a...).Scan(&n)
	}
	var err error
	if st.Downloads7d, err = count(7 * 24 * time.Hour); err != nil {
		return st, err
	}
	st.Downloads30d, err = count(30 * 24 * time.Hour)
	return st, err
}

// SendDescription returns the user's stored preference, or ok=false if unset.
func (s *Store) SendDescription(ctx context.Context, userID int64) (value bool, ok bool, err error) {
	var v sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT send_description FROM user_settings WHERE telegram_user_id = ?`, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil || !v.Valid {
		return false, false, err
	}
	return v.Int64 == 1, true, nil
}

func (s *Store) SetSendDescription(ctx context.Context, userID int64, on bool) error {
	val := 0
	if on {
		val = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_settings (telegram_user_id, send_description, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(telegram_user_id) DO UPDATE SET
			send_description = excluded.send_description, updated_at = CURRENT_TIMESTAMP`, userID, val)
	return err
}

// ------------------------------------------------------------------ jobs ---

func (s *Store) EnqueueJob(ctx context.Context, j *Job) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (url, quality, user_id, chat_id, message_id, status_message_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.URL, j.Quality, j.UserID, j.ChatID, j.MessageID, j.StatusMessageID, time.Now().Unix())
	if err != nil {
		return err
	}
	j.ID, err = res.LastInsertId()
	return err
}

// ResetRunningJobs puts jobs interrupted by a restart back in the queue.
func (s *Store) ResetRunningJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'queued', started_at = NULL WHERE state = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) QueuedJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, url, quality, user_id, chat_id, message_id, status_message_id, created_at
		FROM jobs WHERE state = 'queued' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var created int64
		if err := rows.Scan(&j.ID, &j.URL, &j.Quality, &j.UserID, &j.ChatID, &j.MessageID, &j.StatusMessageID, &created); err != nil {
			return nil, err
		}
		j.CreatedAt = time.Unix(created, 0)
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimNextJob marks the oldest queued job as running and returns it.
func (s *Store) ClaimNextJob(ctx context.Context) (*Job, error) {
	var j Job
	var created int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE jobs SET state = 'running', started_at = ?
		WHERE id = (SELECT id FROM jobs WHERE state = 'queued' ORDER BY id LIMIT 1)
		RETURNING id, url, quality, user_id, chat_id, message_id, status_message_id, created_at`,
		time.Now().Unix()).Scan(&j.ID, &j.URL, &j.Quality, &j.UserID, &j.ChatID, &j.MessageID, &j.StatusMessageID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.CreatedAt = time.Unix(created, 0)
	return &j, nil
}

func (s *Store) FinishJob(ctx context.Context, id int64, state, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = ?, error = NULLIF(?, ''), finished_at = ? WHERE id = ?`,
		state, errText, time.Now().Unix(), id)
	return err
}

// RequeueJob is used when shutdown interrupts a job so it runs again later.
func (s *Store) RequeueJob(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'queued', started_at = NULL WHERE id = ?`, id)
	return err
}

// PruneJobs deletes finished jobs older than the given age.
func (s *Store) PruneJobs(ctx context.Context, olderThan time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE state IN ('done', 'error') AND finished_at < ?`,
		time.Now().Add(-olderThan).Unix())
	return err
}

// ReferencedFiles returns every file path the videos table points at.
func (s *Store) ReferencedFiles(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path, COALESCE(thumbnail_path, '') FROM videos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var f, t string
		if err := rows.Scan(&f, &t); err != nil {
			return nil, err
		}
		out[f] = true
		if t != "" {
			out[t] = true
		}
	}
	return out, rows.Err()
}
