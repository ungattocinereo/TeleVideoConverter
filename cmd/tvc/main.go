// Command tvc is TeleVideoConverter: Telegram bot, download worker, web UI
// and cleanup in one process.
//
//	tvc serve        run everything (default)
//	tvc healthcheck  exit 0 if the local server answers (container healthcheck)
//	tvc doctor       print versions of the bundled tools
//	tvc prepare FILE show how FILE would be compressed/split for Telegram
//	tvc version
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/ungattocinereo/TeleVideoConverter/internal/bot"
	"github.com/ungattocinereo/TeleVideoConverter/internal/cleanup"
	"github.com/ungattocinereo/TeleVideoConverter/internal/config"
	"github.com/ungattocinereo/TeleVideoConverter/internal/fetch"
	"github.com/ungattocinereo/TeleVideoConverter/internal/media"
	"github.com/ungattocinereo/TeleVideoConverter/internal/store"
	"github.com/ungattocinereo/TeleVideoConverter/internal/tg"
	"github.com/ungattocinereo/TeleVideoConverter/internal/web"
)

var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
	case "healthcheck":
		os.Exit(healthcheck())
	case "doctor":
		doctor()
	case "prepare":
		os.Exit(prepare(os.Args[2:]))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "usage: tvc [serve|healthcheck|doctor|prepare FILE|version]\n")
		os.Exit(2)
	}
}

func setupLogging(level string) {
	var l slog.Level
	switch strings.ToUpper(level) {
	case "DEBUG":
		l = slog.LevelDebug
	case "WARNING", "WARN":
		l = slog.LevelWarn
	case "ERROR":
		l = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}

// app adapts the worker queue for the web form (web jobs have chat 0).
type app struct {
	store  *store.Store
	worker *bot.Worker
}

func (a *app) EnqueueWeb(ctx context.Context, url, quality string) error {
	if err := a.store.EnqueueJob(ctx, &store.Job{URL: url, Quality: quality}); err != nil {
		return err
	}
	a.worker.Wake()
	return nil
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	if cfg.BotToken == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is not set")
	}
	for _, d := range []string{"videos", "thumbnails", "telegram"} {
		if err := os.MkdirAll(filepath.Join(cfg.StoragePath, d), 0o755); err != nil {
			return err
		}
	}
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer st.Close()

	ffmpegPath, err := exec.LookPath(cfg.FFmpeg)
	if err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	ffprobePath, err := exec.LookPath(cfg.FFprobe)
	if err != nil {
		return fmt.Errorf("ffprobe: %w", err)
	}
	tools := media.Tools{FFmpeg: ffmpegPath, FFprobe: ffprobePath, Profile: cfg.H264Profile}
	client := tg.New(cfg.BotToken, cfg.RetryAttempts, cfg.RetryBase, cfg.RetryMax)
	fetcher := &fetch.Fetcher{
		YtDlp: cfg.YtDlp, QJS: cfg.QJS, FFmpegDir: filepath.Dir(ffmpegPath),
		VideosDir: filepath.Join(cfg.StoragePath, "videos"), CookiesDir: cfg.CookiesPath,
		Media: tools, HTTP: &http.Client{Timeout: 60 * time.Second},
	}
	tracker := bot.NewTracker(client)
	worker := bot.NewWorker(cfg, st, client, fetcher, tools, tracker)
	b := bot.New(cfg, client, st, tracker, worker)
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           web.New(cfg, st, &app{store: st, worker: worker}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	slog.Info("starting TeleVideoConverter", "version", version, "listen", cfg.ListenAddr,
		"h264_profile", cfg.H264Profile, "retention_days", cfg.RetentionDays, "max_storage_gb", cfg.MaxStorageGB)

	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	run(func() { worker.Run(ctx) })
	run(func() { b.Run(ctx) })
	run(func() { cleanup.Run(ctx, cfg, st, worker) })
	srvErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-srvErr:
		stop()
		wg.Wait()
		return err
	}
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	wg.Wait()
	slog.Info("stopped")
	return nil
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":3000"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "3000"
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func doctor() {
	fmt.Println("tvc", version)
	for _, c := range [][]string{
		{"yt-dlp", "--version"}, {"ffmpeg", "-hide_banner", "-version"}, {"ffprobe", "-hide_banner", "-version"},
	} {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		if err != nil {
			line = "ERROR " + err.Error()
		}
		fmt.Printf("%-8s %s\n", c[0], line)
	}
	out, _ := exec.Command("/usr/local/bin/qjs", "--help").CombinedOutput()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	fmt.Printf("%-8s %s\n", "qjs", line)
}

// prepare runs the Telegram size preparation on a file and keeps the result
// in /tmp, to check the compression/splitting path without sending anything.
func prepare(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: tvc prepare FILE")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tools := media.Tools{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe, Profile: cfg.H264Profile}
	l := media.Limits{SendLimitBytes: cfg.SendLimitBytes, TargetBytes: cfg.TargetBytes,
		MinVideoKbps: cfg.MinVideoKbps, AudioKbps: cfg.AudioKbps, Policy: cfg.LargeFilePolicy}
	start := time.Now()
	parts, err := tools.PrepareForTelegram(context.Background(), l, args[0], "/tmp/tvc-prepare")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	for _, p := range parts {
		st, _ := os.Stat(p.Path)
		fmt.Printf("part %d/%d  %dx%d  %.1fs  %.1f MB  %s\n", p.Index, p.Count, p.Width, p.Height, p.Duration,
			float64(st.Size())/(1<<20), p.Path)
	}
	fmt.Printf("took %.1fs\n", time.Since(start).Seconds())
	return 0
}
