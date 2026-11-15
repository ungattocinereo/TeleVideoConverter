package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/config"
	"github.com/ungattocinereo/TeleVideoConverter/internal/fetch"
	"github.com/ungattocinereo/TeleVideoConverter/internal/media"
	"github.com/ungattocinereo/TeleVideoConverter/internal/store"
	"github.com/ungattocinereo/TeleVideoConverter/internal/tg"
)

type Worker struct {
	cfg     *config.Config
	store   *store.Store
	tg      *tg.Client
	fetch   *fetch.Fetcher
	media   media.Tools
	tracker *Tracker
	wake    chan struct{}

	mu      sync.Mutex
	current int64 // running job id, 0 if idle
}

func NewWorker(cfg *config.Config, s *store.Store, c *tg.Client, f *fetch.Fetcher, m media.Tools, t *Tracker) *Worker {
	return &Worker{cfg: cfg, store: s, tg: c, fetch: f, media: m, tracker: t, wake: make(chan struct{}, 1)}
}

// Wake tells the worker a job was queued.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) Busy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current != 0
}

// Run processes queued jobs one at a time until ctx is cancelled. Jobs
// interrupted by a shutdown are put back in the queue and resumed on start.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.store.ResetRunningJobs(ctx); err != nil {
		slog.Error("reset running jobs", "err", err)
	} else if n > 0 {
		slog.Info("resuming interrupted jobs", "count", n)
	}
	if queued, err := w.store.QueuedJobs(ctx); err == nil {
		for _, j := range queued {
			w.tracker.Start(ctx, j.ID, j.ChatID, j.StatusMessageID, j.URL, j.CreatedAt, "")
		}
	}
	for {
		job, err := w.store.ClaimNextJob(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("claim job", "err", err)
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
			case <-time.After(30 * time.Second):
			}
			continue
		}
		w.mu.Lock()
		w.current = job.ID
		w.mu.Unlock()
		w.process(ctx, job)
		w.mu.Lock()
		w.current = 0
		w.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
	}
}

func (w *Worker) process(ctx context.Context, job *store.Job) {
	log := slog.With("job", job.ID, "user", job.UserID)
	log.Info("processing download", "quality", job.Quality)
	if w.tracker.get(job.ID) == nil { // web jobs and resumed jobs without a tracker
		w.tracker.Start(ctx, job.ID, job.ChatID, job.StatusMessageID, job.URL, job.CreatedAt, "")
	}
	start := time.Now()
	w.tracker.Set(job.ID, "working", nil)

	jctx, cancel := context.WithTimeout(ctx, w.cfg.JobTimeout)
	defer cancel()

	var lastEmit time.Time
	var lastPhase string
	res, err := w.fetch.Download(jctx, job.URL, job.Quality, func(p fetch.Progress) {
		if p.Phase != lastPhase || time.Since(lastEmit) >= time.Second {
			lastEmit, lastPhase = time.Now(), p.Phase
			w.tracker.Set(job.ID, "working", &p)
		}
	})
	if ctx.Err() != nil {
		w.interrupted(job)
		return
	}
	if err != nil {
		w.fail(ctx, job, err.Error())
		return
	}
	downloadDur := time.Since(start)
	full := 100.0
	w.tracker.Set(job.ID, "working", &fetch.Progress{Phase: "processing", Percent: &full})

	procStart := time.Now()
	v, filePath, width, height, duration, err := w.finalize(jctx, res, log, func(phase string) { w.tracker.Phase(job.ID, "working", phase) })
	if ctx.Err() != nil {
		w.interrupted(job)
		return
	}
	if err != nil {
		w.fail(ctx, job, err.Error())
		return
	}
	processDur := time.Since(procStart)
	processing := int64(time.Since(start).Seconds())
	now := time.Now()
	v.TelegramUserID, v.OriginalURL, v.DownloadedQuality = job.UserID, job.URL, job.Quality
	v.ProcessingTime = processing
	v.DownloadTimestamp, v.DeleteTimestamp = now.Unix(), now.Add(w.cfg.Retention()).Unix()
	dl, pr := downloadDur.Seconds(), processDur.Seconds()
	v.DownloadSeconds, v.ProcessSeconds = &dl, &pr
	if err := w.store.SaveVideo(ctx, v); err != nil {
		w.fail(ctx, job, "database: "+err.Error())
		return
	}
	log.Info("download ready", "video", v.VideoID, "size", formatSize(v.FileSize),
		"download_s", fmt.Sprintf("%.1f", dl), "process_s", fmt.Sprintf("%.1f", pr))

	if job.ChatID != 0 {
		sendStart := time.Now()
		w.tracker.Set(job.ID, "delivering", nil)
		if err := w.sendStats(ctx, job.ChatID, v, duration); err != nil {
			log.Warn("could not send stats", "err", err)
		}
		statsDone := time.Now()
		cached := ""
		if row, err := w.store.GetVideo(ctx, v.VideoID); err == nil && row != nil &&
			row.TelegramFileID != nil && row.TelegramFileSize != nil && *row.TelegramFileSize == v.FileSize {
			cached = *row.TelegramFileID
		}
		how := w.sendFile(ctx, job.ChatID, v.VideoID, cached, filePath, deref(v.ThumbnailPath), width, height, duration, res.Audio, v.Title,
			func(phase string) { w.tracker.Phase(job.ID, "delivering", phase) })
		fileDone := time.Now()
		if w.wantsDescription(ctx, job.UserID) {
			w.sendDescription(ctx, job.ChatID, res.Description)
		}
		sendSecs := time.Since(sendStart).Seconds()
		w.store.SetSendSeconds(ctx, v.VideoID, sendSecs)
		log.Info("delivered", "video", v.VideoID, "send_s", fmt.Sprintf("%.1f", sendSecs), "how", how,
			"stats_s", fmt.Sprintf("%.1f", statsDone.Sub(sendStart).Seconds()),
			"file_s", fmt.Sprintf("%.1f", fileDone.Sub(statsDone).Seconds()),
			"description_s", fmt.Sprintf("%.1f", time.Since(fileDone).Seconds()))
		text := fmt.Sprintf("🥟 Пельмени всплыли · %s\n\n%s", ruDuration(processing), jokes.pick("done"))
		if !w.tracker.Finish(ctx, job.ID, text) && job.StatusMessageID != 0 {
			w.tg.SendMessage(ctx, job.ChatID, text, tg.SendOpts{})
		}
	} else {
		w.tracker.Drop(job.ID)
	}
	w.store.FinishJob(ctx, job.ID, "done", "")
}

// finalize normalises the downloaded file, makes the thumbnail and builds
// the database row.
func (w *Worker) finalize(ctx context.Context, res *fetch.Result, log *slog.Logger, phase func(string)) (v *store.Video, path string, width, height int, duration float64, err error) {
	path = res.FilePath
	probedCodec := ""
	format := strings.TrimPrefix(filepath.Ext(path), ".")
	if !res.Audio {
		out := filepath.Join(w.cfg.StoragePath, "videos", res.VideoID+".mp4")
		if res.Ready {
			if p, perr := w.media.Probe(ctx, path); perr == nil {
				width, height, duration = p.Width, p.Height, p.Duration
			}
		} else {
			// Tell the status message whether we only serve or really cook.
			size := int64(0)
			if st, serr := os.Stat(path); serr == nil {
				size = st.Size()
			}
			if p, perr := w.media.Probe(ctx, path); perr == nil && p.WillEncode(size, w.cfg.TargetBytes) {
				phase("encoding")
			} else {
				phase("remuxing")
			}
			if nr, nerr := w.media.Normalize(ctx, path, out, w.cfg.TargetBytes); nerr == nil {
				path = nr.Path
				width, height, duration = nr.Probe.Width, nr.Probe.Height, nr.Probe.Duration
				probedCodec = nr.Probe.VideoCodec
				log.Info("normalized", "action", nr.Action, "codec", nr.Probe.VideoCodec, "took_s", fmt.Sprintf("%.1f", nr.Elapsed.Seconds()))
			} else {
				if ctx.Err() != nil {
					return nil, "", 0, 0, 0, ctx.Err()
				}
				// Same as before: a failed conversion still delivers the original.
				log.Warn("normalize failed, keeping original file", "err", nerr)
			}
		}
		format = strings.TrimPrefix(filepath.Ext(path), ".")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, "", 0, 0, 0, fmt.Errorf("downloaded file missing: %w", err)
	}

	v = &store.Video{VideoID: res.VideoID, Title: res.Title, FileSize: st.Size(), Format: format, FilePath: path}
	if v.Title == "" {
		v.Title = res.VideoID
	}
	if res.Extractor != "" {
		v.SourcePlatform = &res.Extractor
	} else {
		unknown := "Unknown"
		v.SourcePlatform = &unknown
	}
	if !res.Audio {
		codec := res.VCodec
		if codec == "" || codec == "none" {
			codec = probedCodec
		}
		if codec == "" {
			codec = "unknown"
		}
		v.Codec = &codec
		if res.Width > 0 && res.Height > 0 {
			q := fmt.Sprintf("%dx%d (%dp)", res.Width, res.Height, min(res.Width, res.Height))
			v.OriginalQuality = &q
		}
		thumbs := filepath.Join(w.cfg.StoragePath, "thumbnails")
		os.MkdirAll(thumbs, 0o755)
		thumb := filepath.Join(thumbs, res.VideoID+".jpg")
		var terr error
		if res.ThumbSource != "" {
			terr = w.media.Thumbnail(ctx, res.ThumbSource, thumb)
		}
		if res.ThumbSource == "" || terr != nil {
			terr = w.media.FrameThumbnail(ctx, path, thumb, duration)
		}
		if terr == nil {
			v.ThumbnailPath = &thumb
		} else {
			log.Warn("no thumbnail", "err", terr)
		}
	}
	return v, path, width, height, duration, nil
}

func (w *Worker) interrupted(job *store.Job) {
	slog.Info("job interrupted by shutdown, requeued", "job", job.ID)
	w.tracker.Drop(job.ID)
	w.store.RequeueJob(context.Background(), job.ID)
}

func (w *Worker) fail(ctx context.Context, job *store.Job, reason string) {
	slog.Error("download failed", "job", job.ID, "err", reason)
	if reason == "" {
		reason = "Unknown error"
	}
	text := "🥟 Пельменный ритуал сорвался\n\n" + jokes.pick("error") + "\n\n"
	if q := errorQuip(reason); q != "" {
		text += q + "\n\n"
	}
	text += "Техническая причина: " + truncate(reason, 1200)
	if !w.tracker.Finish(ctx, job.ID, text) && job.ChatID != 0 {
		if _, err := w.tg.SendMessage(ctx, job.ChatID, text, tg.SendOpts{}); err != nil {
			slog.Error("could not report failure", "err", err)
		}
	}
	w.store.FinishJob(ctx, job.ID, "error", truncate(reason, 2000))
}

func (w *Worker) wantsDescription(ctx context.Context, user int64) bool {
	if on, ok, err := w.store.SendDescription(ctx, user); err == nil && ok {
		return on
	}
	return w.cfg.SendDescription
}

func code(v string) string { return "<code>" + html.EscapeString(v) + "</code>" }

var platformNames = map[string]string{
	"Twitter": "X", "Youtube": "YouTube", "TikTok": "TikTok", "Instagram": "Instagram", "Threads": "Threads",
	"Reddit": "Reddit", "Facebook": "Facebook", "Vimeo": "Vimeo", "Twitch": "Twitch", "VK": "VK",
}

// sendStats posts the short card that precedes the video: facts only,
// no jokes (those live in the status message).
func (w *Worker) sendStats(ctx context.Context, chat int64, v *store.Video, duration float64) error {
	source := derefOr(v.SourcePlatform, "")
	if name, ok := platformNames[source]; ok {
		source = name
	}
	head := "🥟 <b>Готово за " + ruDuration(v.ProcessingTime) + "</b>"
	if source != "" && source != "Generic" && source != "Unknown" {
		head += " · " + html.EscapeString(source)
	}
	var facts []string
	if duration > 0 {
		facts = append(facts, "🎞 "+formatElapsed(time.Duration(duration*float64(time.Second))))
	}
	if q := derefOr(v.OriginalQuality, ""); q != "" {
		res, _, _ := strings.Cut(q, " ")
		facts = append(facts, "📐 "+strings.Replace(res, "x", "×", 1))
	}
	facts = append(facts, "💾 "+formatSize(v.FileSize))
	text := head + "\n" + strings.Join(facts, " · ") + "\n🧊 хранится до " + ruDate(v.DeleteTimestamp)
	// A link back to the original post; the preview is disabled below.
	if strings.HasPrefix(v.OriginalURL, "http://") || strings.HasPrefix(v.OriginalURL, "https://") {
		text += "\n🔗 <a href=\"" + html.EscapeString(v.OriginalURL) + "\">Оригинал</a>"
	}
	_, err := w.tg.SendMessage(ctx, chat, text, tg.SendOpts{ParseMode: "HTML", DisableWebPagePreview: true})
	return err
}

func (w *Worker) limits() media.Limits {
	return media.Limits{SendLimitBytes: w.cfg.SendLimitBytes, TargetBytes: w.cfg.TargetBytes,
		MinVideoKbps: w.cfg.MinVideoKbps, AudioKbps: w.cfg.AudioKbps, Policy: w.cfg.LargeFilePolicy}
}

// sendFile delivers the video (or audio) and reports how: "file_id",
// "upload", "parts" or "fallback".
func (w *Worker) sendFile(ctx context.Context, chat int64, videoID, cachedFileID, path, thumb string, width, height int,
	duration float64, audio bool, title string, phase func(string)) string {
	st, err := os.Stat(path)
	if err != nil {
		w.webFallback(ctx, chat, 0, err.Error())
		return "fallback"
	}
	if audio {
		if st.Size() > w.cfg.SendLimitBytes {
			w.webFallback(ctx, chat, st.Size(), "аудиофайл больше лимита Telegram для ботов")
			return "fallback"
		}
		if _, err := w.tg.SendAudio(ctx, chat, path, title); err != nil {
			w.webFallback(ctx, chat, st.Size(), err.Error())
			return "fallback"
		}
		return "upload"
	}
	// Telegram already has this exact file: re-send it without uploading.
	if cachedFileID != "" {
		if err := w.tg.SendVideoByID(ctx, chat, cachedFileID, tg.VideoParams{}); err == nil {
			return "file_id"
		} else {
			slog.Warn("re-send by file_id failed, uploading again", "err", err)
		}
	}
	if st.Size() > w.cfg.TargetBytes {
		phase("compressing")
		w.tg.SendMessage(ctx, chat, fmt.Sprintf("🗜 %s → ужимаю под лимит Telegram (%s)", formatSize(st.Size()), formatSize(w.cfg.SendLimitBytes)), tg.SendOpts{})
	}
	parts, err := w.media.PrepareForTelegram(ctx, w.limits(), path, filepath.Join(w.cfg.StoragePath, "telegram"))
	if err != nil {
		w.webFallback(ctx, chat, st.Size(), err.Error())
		return "fallback"
	}
	defer media.Cleanup(parts)
	phase("uploading")
	for _, p := range parts {
		vp := tg.VideoParams{Width: p.Width, Height: p.Height, Duration: int(p.Duration + 0.5), Thumbnail: thumb}
		if vp.Width == 0 || vp.Height == 0 {
			vp.Width, vp.Height = width, height
		}
		if vp.Duration == 0 {
			vp.Duration = int(duration + 0.5)
		}
		if p.Count > 1 {
			vp.Caption = fmt.Sprintf("🎞 Часть %d/%d", p.Index, p.Count)
		}
		fileID, err := w.tg.SendVideo(ctx, chat, p.Path, vp)
		if err != nil {
			var apiErr *tg.APIError
			reason := err.Error()
			if errors.As(err, &apiErr) {
				reason = apiErr.Description
			}
			w.webFallback(ctx, chat, st.Size(), reason)
			return "fallback"
		}
		if p.Count == 1 && fileID != "" {
			w.store.SetTelegramFile(ctx, videoID, fileID, st.Size())
		}
	}
	if len(parts) > 1 {
		return "parts"
	}
	return "upload"
}

func (w *Worker) webFallback(ctx context.Context, chat int64, size int64, reason string) {
	if reason == "" {
		reason = "причина неизвестна"
	}
	slog.Warn("sending web fallback", "reason", reason)
	text := fmt.Sprintf("⚠️ Не отправилось в Telegram (%.1f MB)\n%s\n📥 Скачать: %s",
		float64(size)/(1024*1024), truncate(reason, 1200), w.cfg.WebPublicURL)
	w.tg.SendMessage(ctx, chat, text, tg.SendOpts{})
}

func (w *Worker) sendDescription(ctx context.Context, chat int64, desc string) {
	if strings.TrimSpace(desc) == "" {
		return
	}
	if r := []rune(desc); len(r) > 4000 {
		desc = string(r[:4000]) + "..."
	}
	if _, err := w.tg.SendMessage(ctx, chat, "📝 Начинка поста:\n\n"+desc, tg.SendOpts{}); err != nil {
		slog.Warn("could not send description", "err", err)
	}
}

func deref(s *string) string { return derefOr(s, "") }

func derefOr(s *string, def string) string {
	if s == nil || *s == "" {
		return def
	}
	return *s
}
