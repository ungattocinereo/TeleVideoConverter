// Package fetch downloads media with yt-dlp (run as a subprocess) plus the
// site-specific workarounds the Python version had: Threads via its web API,
// Instagram carousels, and TikTok photo posts / embed fallback.
package fetch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ungattocinereo/TeleVideoConverter/internal/media"
)

type Fetcher struct {
	YtDlp      string
	QJS        string
	FFmpegDir  string
	VideosDir  string
	CookiesDir string
	Media      media.Tools
	HTTP       *http.Client
}

type Progress struct {
	Phase      string   `json:"phase,omitempty"` // video|audio|media|processing
	Percent    *float64 `json:"percent,omitempty"`
	Downloaded *int64   `json:"downloaded_bytes,omitempty"`
	Total      *int64   `json:"total_bytes,omitempty"`
	Speed      *float64 `json:"speed,omitempty"`
	ETA        *int64   `json:"eta,omitempty"`
}

type Result struct {
	VideoID     string
	Title       string
	Description string
	Extractor   string
	FilePath    string // downloaded file, not yet normalised
	ThumbSource string // downloaded thumbnail image, if any
	Width       int
	Height      int
	VCodec      string // codec as reported by the site (stored in DB)
	Ready       bool   // already a Telegram-ready MP4 (rendered slideshow)
	Audio       bool
}

const desktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var defaultHeaders = [][2]string{
	{"User-Agent", desktopUA},
	{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8"},
	{"Accept-Language", "en-US,en;q=0.9"},
	{"Accept-Encoding", "gzip, deflate, br"},
	{"DNT", "1"},
	{"Connection", "keep-alive"},
	{"Upgrade-Insecure-Requests", "1"},
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func hostIs(raw string, domains ...string) bool {
	h := hostOf(raw)
	for _, d := range domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func (f *Fetcher) cookieFile(rawURL string) string {
	if f.CookiesDir == "" {
		return ""
	}
	lower := strings.ToLower(rawURL)
	for _, m := range [][2]string{
		{"instagram.com", "instagram.txt"}, {"facebook.com", "facebook.txt"}, {"fb.watch", "facebook.txt"},
		{"twitter.com", "twitter.txt"}, {"x.com", "twitter.txt"}, {"tiktok.com", "tiktok.txt"},
	} {
		if strings.Contains(lower, m[0]) {
			p := filepath.Join(f.CookiesDir, m[1])
			if _, err := os.Stat(p); err == nil {
				return p
			}
			return ""
		}
	}
	return ""
}

// Download fetches rawURL. quality is "best" or "audio".
func (f *Fetcher) Download(ctx context.Context, rawURL, quality string, progress func(Progress)) (*Result, error) {
	if err := os.MkdirAll(f.VideosDir, 0o755); err != nil {
		return nil, err
	}
	audio := quality == "audio"
	res, err := f.download(ctx, rawURL, audio, progress)
	if err != nil {
		return nil, explain(rawURL, err, f.cookieFile(rawURL) != "")
	}
	res.Audio = audio
	return res, nil
}

func (f *Fetcher) download(ctx context.Context, rawURL string, audio bool, progress func(Progress)) (*Result, error) {
	switch {
	case hostIs(rawURL, "threads.com", "threads.net"):
		return f.threads(ctx, rawURL, audio, progress)
	case hostIs(rawURL, "instagram.com"):
		return f.instagram(ctx, rawURL, audio, progress)
	case hostIs(rawURL, "tiktok.com") && tiktokPostRe.MatchString(rawURL) && tiktokPostRe.FindStringSubmatch(rawURL)[1] == "photo":
		return f.tiktokSlideshow(ctx, tiktokPostRe.FindStringSubmatch(rawURL)[2], rawURL, audio)
	}
	res, err := f.ytdlp(ctx, f.baseArgs(rawURL, audio, true), rawURL, progress)
	if err == nil || !hostIs(rawURL, "tiktok.com") {
		return res, err
	}
	// TikTok: photo posts behind short links, or login walls on public videos.
	res2, ferr := f.tiktokFallback(ctx, rawURL, audio, err, progress)
	if ferr != nil {
		if !errors.Is(ferr, errNoFallback) {
			slog.Warn("tiktok fallback failed", "err", ferr)
		}
		return nil, err
	}
	return res2, nil
}

// baseArgs are the yt-dlp options shared by all downloads. withSiteAuth adds
// cookies and the desktop headers (not for Facebook, which dislikes them).
func (f *Fetcher) baseArgs(rawURL string, audio, withSiteAuth bool) []string {
	args := []string{
		"--ignore-config", "--no-color", "--newline", "--progress", "--progress-delta", "1",
		"--progress-template", "download:TVCPROG %(info.vcodec)s|%(info.acodec)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s",
		"--no-simulate",
		"-O", "after_move:TVCINFO %(.{id,title,description,width,height,vcodec,acodec,extractor_key,ext,filepath,duration})j",
		"--no-playlist", "-I", "1",
		"--js-runtimes", "quickjs:" + f.QJS,
		"--ffmpeg-location", f.FFmpegDir,
		"-o", filepath.Join(f.VideosDir, "%(id)s.%(ext)s"),
		"--socket-timeout", "180", "--retries", "20", "--fragment-retries", "30",
		"--extractor-retries", "5", "--file-access-retries", "5",
		"--retry-sleep", "http:exp=3:60:3", "--retry-sleep", "fragment:exp=3:60:3",
		"--retry-sleep", "file_access:exp=3:60:3", "--retry-sleep", "extractor:exp=3:60:3",
		"--http-chunk-size", "5M", "--no-check-certificates",
	}
	if audio {
		args = append(args, "-f", "bestaudio/best", "-x", "--audio-format", "m4a")
	} else {
		args = append(args,
			"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/bestvideo+bestaudio/best",
			// Prefer H.264 up to 1080p so most downloads only need a remux.
			"-S", "res:1080,vcodec:h264,acodec:aac",
			"--merge-output-format", "mp4", "--remux-video", "mp4",
			"--write-thumbnail")
	}
	if withSiteAuth {
		if c := f.cookieFile(rawURL); c != "" {
			args = append(args, "--cookies", c)
		}
		if !hostIs(rawURL, "facebook.com", "fb.watch") {
			for _, h := range defaultHeaders {
				args = append(args, "--add-header", h[0]+":"+h[1])
			}
		}
	}
	return args
}

type ytInfo struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	VCodec      string  `json:"vcodec"`
	ACodec      string  `json:"acodec"`
	Extractor   string  `json:"extractor_key"`
	Ext         string  `json:"ext"`
	FilePath    string  `json:"filepath"`
	Duration    float64 `json:"duration"`
}

// ytdlp runs yt-dlp with args (and target, a URL, unless args already use
// --load-info-json) and turns its after_move JSON into a Result.
func (f *Fetcher) ytdlp(ctx context.Context, args []string, target string, progress func(Progress)) (*Result, error) {
	if target != "" {
		args = append(args, "--", target)
	}
	cmd := exec.CommandContext(ctx, f.YtDlp, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var (
		mu      sync.Mutex
		info    *ytInfo
		errLine string
		tail    []string
		wg      sync.WaitGroup
	)
	consume := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "TVCPROG "):
				if progress != nil {
					progress(parseProgress(strings.TrimPrefix(line, "TVCPROG ")))
				}
			case strings.HasPrefix(line, "TVCINFO "):
				var in ytInfo
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "TVCINFO ")), &in); err == nil {
					mu.Lock()
					info = &in
					mu.Unlock()
				}
			default:
				slog.Debug("yt-dlp", "line", line)
				mu.Lock()
				if strings.HasPrefix(line, "ERROR:") {
					errLine = line
				}
				tail = append(tail, line)
				if len(tail) > 30 {
					tail = tail[1:]
				}
				mu.Unlock()
			}
		}
	}
	wg.Add(2)
	go consume(stdout)
	go consume(stderr)
	wg.Wait()
	waitErr := cmd.Wait()

	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil || info == nil {
		msg := errLine
		if msg == "" {
			msg = media.Tail(strings.Join(tail, "\n"), 1000)
		}
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		if msg == "" {
			msg = "yt-dlp finished without downloading anything"
		}
		return nil, errors.New(msg)
	}
	path := info.FilePath
	if path == "" || !exists(path) {
		path = f.findFile(info.ID, ".mp4", ".mkv", ".webm", ".mov", ".m4v", ".flv", ".m4a", ".mp3", ".opus")
	}
	if path == "" {
		return nil, fmt.Errorf("downloaded file for %s not found", info.ID)
	}
	return &Result{
		VideoID: info.ID, Title: info.Title, Description: info.Description, Extractor: info.Extractor,
		FilePath: path, ThumbSource: f.findFile(info.ID, ".jpg", ".jpeg", ".png", ".webp", ".image"),
		Width: info.Width, Height: info.Height, VCodec: info.VCodec,
	}, nil
}

func (f *Fetcher) findFile(id string, exts ...string) string {
	for _, ext := range exts {
		p := filepath.Join(f.VideosDir, id+ext)
		if exists(p) {
			return p
		}
	}
	return ""
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func parseProgress(s string) Progress {
	fields := strings.Split(s, "|")
	for len(fields) < 7 {
		fields = append(fields, "NA")
	}
	num := func(v string) *float64 {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil
		}
		return &x
	}
	toInt := func(v string) *int64 {
		x := num(v)
		if x == nil {
			return nil
		}
		n := int64(*x)
		return &n
	}
	vcodec, acodec := fields[0], fields[1]
	p := Progress{Phase: "media", Downloaded: toInt(fields[2]), Speed: num(fields[5]), ETA: toInt(fields[6])}
	switch {
	case vcodec == "none" && acodec != "none" && acodec != "NA":
		p.Phase = "audio"
	case acodec == "none" && vcodec != "none" && vcodec != "NA":
		p.Phase = "video"
	}
	p.Total = toInt(fields[3])
	if p.Total == nil || *p.Total == 0 {
		p.Total = toInt(fields[4])
	}
	if p.Total != nil && *p.Total > 0 && p.Downloaded != nil {
		pct := float64(*p.Downloaded) / float64(*p.Total) * 100
		p.Percent = &pct
	}
	return p
}

// runInfoJSON downloads from an info dict we built or picked ourselves,
// like the old code's ydl.process_ie_result(info, download=True).
func (f *Fetcher) runInfoJSON(ctx context.Context, rawURL string, info map[string]any, audio bool, progress func(Progress)) (*Result, error) {
	tmp, err := os.CreateTemp("", "tvc-info-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := json.NewEncoder(tmp).Encode(info); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	args := f.baseArgs(rawURL, audio, true)
	// The info JSON replaces the URL; --no-playlist/-I do not apply.
	args = append(args, "--load-info-json", tmp.Name())
	return f.ytdlp(ctx, args, "", progress)
}

// explain adds the old setup hints to authentication failures.
func explain(rawURL string, err error, haveCookies bool) error {
	msg := err.Error()
	lower, lowerURL := strings.ToLower(msg), strings.ToLower(rawURL)
	switch {
	case (strings.Contains(lowerURL, "twitter.com") || strings.Contains(lowerURL, "x.com")) &&
		strings.Contains(lower, "no video could be found") && !haveCookies:
		return errors.New("Twitter/X requires authentication to access videos.\n\n" +
			"To fix this:\n1. Install 'Get cookies.txt LOCALLY' browser extension\n" +
			"2. Log into x.com in your browser\n3. Export cookies and save as 'cookies/twitter.txt'\n" +
			"4. Restart the container")
	case strings.Contains(lowerURL, "instagram.com") &&
		(strings.Contains(lower, "login") || strings.Contains(lower, "private") || strings.Contains(lower, "authentication")):
		return fmt.Errorf("Instagram authentication required.\n\nOriginal error: %s\n\n"+
			"To fix this:\n1. Export Instagram cookies using a browser extension\n"+
			"2. Save as 'instagram.txt' in the cookies directory\n3. Restart the container\n\n"+
			"See cookies/README.md for detailed instructions.", msg)
	}
	return err
}

func (f *Fetcher) get(ctx context.Context, rawURL string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", hostOf(rawURL), resp.StatusCode)
	}
	return resp, nil
}

// downloadFirst saves the first candidate URL that works to dst.
func (f *Fetcher) downloadFirst(ctx context.Context, urls []string, dst, referer string) error {
	var last error = errors.New("no URLs")
	for _, u := range urls {
		if u == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := func() error {
			resp, err := f.get(cctx, u, map[string]string{"User-Agent": desktopUA, "Referer": referer})
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			out, err := os.Create(dst)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, resp.Body)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		}()
		cancel()
		if err == nil {
			return nil
		}
		last = err
	}
	return fmt.Errorf("all CDN URLs failed: %w", last)
}
