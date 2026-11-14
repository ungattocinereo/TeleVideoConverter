// Package web serves a single server-rendered page plus the JSON API the old
// React frontend used (same paths), with no Node, nginx or WebSocket.
package web

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/config"
	"github.com/ungattocinereo/TeleVideoConverter/internal/store"
)

//go:embed page.html
var pageHTML string

type Enqueuer interface {
	EnqueueWeb(ctx context.Context, url, quality string) error
}

type Server struct {
	cfg   *config.Config
	store *store.Store
	queue Enqueuer
	page  *template.Template
}

func New(cfg *config.Config, s *store.Store, q Enqueuer) *Server {
	funcs := template.FuncMap{"size": humanSize, "left": timeLeft, "str": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}}
	return &Server{cfg: cfg, store: s, queue: q, page: template.Must(template.New("page").Funcs(funcs).Parse(pageHTML))}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/videos", s.listVideos)
	mux.HandleFunc("GET /api/videos/{id}", s.getVideo)
	mux.HandleFunc("GET /api/videos/{id}/download", s.download)
	mux.HandleFunc("DELETE /api/videos/{id}", s.deleteVideo)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/thumbnails/{file}", s.thumbnail)
	mux.HandleFunc("POST /api/submit-url", s.submit)
	return s.secure(mux)
}

// secure adds basic headers and, when WEB_PASSWORD is set, HTTP basic auth
// (the health check stays open for the container healthcheck).
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if s.cfg.WebPassword != "" && r.URL.Path != "/healthz" {
			_, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.WebPassword)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="TeleVideo", charset="UTF-8"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

type apiVideo struct {
	store.Video
	TimeRemaining string  `json:"time_remaining"`
	ThumbnailURL  *string `json:"thumbnail_url"`
}

func decorate(vs []store.Video) []apiVideo {
	out := make([]apiVideo, 0, len(vs))
	for _, v := range vs {
		a := apiVideo{Video: v, TimeRemaining: timeLeft(v.DeleteTimestamp)}
		if v.ThumbnailPath != nil && *v.ThumbnailPath != "" {
			u := "/api/thumbnails/" + v.VideoID + ".jpg"
			a.ThumbnailURL = &u
		}
		out = append(out, a)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	if status >= 500 {
		slog.Error("web", "err", err)
	}
	writeJSON(w, status, map[string]string{"detail": err.Error()})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var vs []store.Video
	var err error
	if q != "" {
		vs, err = s.store.SearchVideos(r.Context(), -1, q)
	} else {
		vs, err = s.store.ListVideos(r.Context(), -1)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st, _ := s.store.GetStats(r.Context(), -1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; media-src 'self'")
	err = s.page.Execute(w, map[string]any{
		"Videos": decorate(vs), "Query": q, "Stats": st,
		"UsedGB": float64(st.UsedSpaceBytes) / (1 << 30), "MaxGB": s.cfg.MaxStorageGB,
	})
	if err != nil {
		slog.Error("render page", "err", err)
	}
}

func (s *Server) listVideos(w http.ResponseWriter, r *http.Request) {
	vs, err := s.store.ListVideos(r.Context(), -1)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, decorate(vs))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	vs, err := s.store.SearchVideos(r.Context(), -1, r.URL.Query().Get("q"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, decorate(vs))
}

func (s *Server) getVideo(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.GetVideo(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	if v == nil {
		fail(w, 404, fmt.Errorf("Video not found"))
		return
	}
	writeJSON(w, 200, decorate([]store.Video{*v})[0])
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N} ._()\-]+`)

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.GetVideo(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	if v == nil {
		fail(w, 404, fmt.Errorf("Video not found"))
		return
	}
	f, err := os.Open(v.FilePath)
	if err != nil {
		fail(w, 404, fmt.Errorf("Video file not found"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, 500, err)
		return
	}
	name := strings.TrimSpace(unsafeName.ReplaceAllString(v.Title, ""))
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	if name == "" {
		name = v.VideoID
	}
	ext := filepath.Ext(v.FilePath)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ext}))
	http.ServeContent(w, r, name+ext, st.ModTime(), f)
}

func (s *Server) deleteVideo(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.GetVideo(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	if v == nil {
		fail(w, 404, fmt.Errorf("Video not found"))
		return
	}
	RemoveFiles(v)
	if _, err := s.store.DeleteVideo(r.Context(), v.VideoID); err != nil {
		fail(w, 500, err)
		return
	}
	slog.Info("deleted video via web", "video", v.VideoID)
	writeJSON(w, 200, map[string]bool{"success": true})
}

// RemoveFiles deletes a video's file and thumbnail (missing files are fine).
func RemoveFiles(v *store.Video) {
	os.Remove(v.FilePath)
	if v.ThumbnailPath != nil && *v.ThumbnailPath != "" {
		os.Remove(*v.ThumbnailPath)
	}
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetStats(r.Context(), -1)
	if err != nil {
		fail(w, 500, err)
		return
	}
	used := float64(st.UsedSpaceBytes) / (1 << 30)
	round := func(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
	writeJSON(w, 200, map[string]any{
		"total_videos": st.TotalVideos, "used_space_bytes": st.UsedSpaceBytes, "used_space_gb": round(used),
		"max_space_gb": s.cfg.MaxStorageGB, "free_space_gb": round(s.cfg.MaxStorageGB - used),
		"downloads_7d": st.Downloads7d, "downloads_30d": st.Downloads30d,
	})
}

func (s *Server) thumbnail(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("file"))
	if !strings.HasSuffix(name, ".jpg") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, filepath.Join(s.cfg.StoragePath, "thumbnails", name))
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL     string `json:"url"`
		Quality string `json:"quality"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		fail(w, 400, fmt.Errorf("Invalid request"))
		return
	}
	body.URL = strings.TrimSpace(body.URL)
	if !strings.HasPrefix(body.URL, "http://") && !strings.HasPrefix(body.URL, "https://") {
		fail(w, 400, fmt.Errorf("Invalid URL format"))
		return
	}
	quality := "best"
	switch body.Quality {
	case "audio", "Audio Only":
		quality = "audio"
	case "", "best", "720p", "1080p", "4K":
	default:
		fail(w, 400, fmt.Errorf("Invalid quality option"))
		return
	}
	if err := s.queue.EnqueueWeb(r.Context(), body.URL, quality); err != nil {
		fail(w, 500, err)
		return
	}
	slog.Info("queued download from web", "quality", quality)
	writeJSON(w, 200, map[string]any{"success": true, "message": "Download queued successfully"})
}

func humanSize(n int64) string {
	v := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
		v /= 1024
	}
	return fmt.Sprintf("%.1f TB", v)
}

func timeLeft(deleteAt int64) string {
	secs := deleteAt - time.Now().Unix()
	if secs <= 0 {
		return "Expired"
	}
	d, h, m := secs/86400, secs%86400/3600, secs%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
