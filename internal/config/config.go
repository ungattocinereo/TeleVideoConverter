// Package config reads TeleVideoConverter settings from the environment.
// Variable names match the previous Python services so the same .env works.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken     string
	AllowedUsers map[int64]bool

	StoragePath  string
	DatabasePath string
	CookiesPath  string

	MaxStorageGB    float64
	RetentionDays   int
	SendDescription bool // default for users without a stored preference

	SendLimitBytes  int64
	TargetBytes     int64
	MinVideoKbps    int
	AudioKbps       int
	LargeFilePolicy string
	H264Profile     string // profile used when a video has to be re-encoded

	WebPublicURL string
	ListenAddr   string
	WebPassword  string // optional HTTP basic auth for the web UI

	RetryAttempts int
	RetryBase     time.Duration
	RetryMax      time.Duration

	YtDlp   string
	FFmpeg  string
	FFprobe string
	QJS     string

	MaxDownloadsPerHour int
	JobTimeout          time.Duration
	LogLevel            string
}

func (c *Config) MaxStorageBytes() int64 {
	return int64(c.MaxStorageGB * 1024 * 1024 * 1024)
}

func (c *Config) Retention() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}

func Load() (*Config, error) {
	c := &Config{
		BotToken:            os.Getenv("TELEGRAM_BOT_TOKEN"),
		AllowedUsers:        parseIDs(os.Getenv("TELEGRAM_USER_IDS")),
		StoragePath:         str("STORAGE_PATH", "/storage"),
		DatabasePath:        str("DATABASE_PATH", "/db/televideo.db"),
		CookiesPath:         str("COOKIES_PATH", "/cookies"),
		MaxStorageGB:        float("MAX_STORAGE_GB", 5),
		RetentionDays:       integer("RETENTION_DAYS", 3),
		SendDescription:     strings.ToLower(str("SEND_POST_DESCRIPTION", "true")) == "true",
		SendLimitBytes:      int64(float("TELEGRAM_SEND_LIMIT_MB", 50) * 1024 * 1024),
		TargetBytes:         int64(float("TELEGRAM_TARGET_MB", 48) * 1024 * 1024),
		MinVideoKbps:        integer("TELEGRAM_MIN_VIDEO_BITRATE_K", 250),
		AudioKbps:           integer("TELEGRAM_AUDIO_BITRATE_K", 64),
		LargeFilePolicy:     str("TELEGRAM_LARGE_FILE_POLICY", "compress_then_split"),
		H264Profile:         str("H264_PROFILE", "high"),
		WebPublicURL:        str("WEB_PUBLIC_URL", "https://televideo.cnr.pw"),
		ListenAddr:          str("LISTEN_ADDR", ":3000"),
		WebPassword:         os.Getenv("WEB_PASSWORD"),
		RetryAttempts:       max(1, integer("TELEGRAM_RETRY_ATTEMPTS", 5)),
		RetryBase:           seconds("TELEGRAM_RETRY_BASE_DELAY_SECONDS", 1),
		RetryMax:            seconds("TELEGRAM_RETRY_MAX_DELAY_SECONDS", 15),
		YtDlp:               str("YTDLP_PATH", "yt-dlp"),
		FFmpeg:              str("FFMPEG_PATH", "ffmpeg"),
		FFprobe:             str("FFPROBE_PATH", "ffprobe"),
		QJS:                 str("QJS_PATH", "/usr/local/bin/qjs"),
		MaxDownloadsPerHour: integer("MAX_DOWNLOADS_PER_HOUR", 10),
		JobTimeout:          time.Duration(integer("JOB_TIMEOUT_MINUTES", 45)) * time.Minute,
		LogLevel:            str("LOG_LEVEL", "INFO"),
	}
	switch c.H264Profile {
	case "baseline", "main", "high":
	default:
		return nil, fmt.Errorf("H264_PROFILE must be baseline, main or high, got %q", c.H264Profile)
	}
	return c, nil
}

func parseIDs(s string) map[int64]bool {
	ids := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			ids[id] = true
		}
	}
	return ids
}

func str(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func integer(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return def
}

func float(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64); err == nil {
		return v
	}
	return def
}

func seconds(key string, def float64) time.Duration {
	return time.Duration(float(key, def) * float64(time.Second))
}
