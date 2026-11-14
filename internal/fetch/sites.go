package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ------------------------------------------------------------- Instagram ---

// instagram picks the first video of a carousel before downloading, so
// photo-first carousels still work (yt-dlp alone would pick entry 1).
func (f *Fetcher) instagram(ctx context.Context, rawURL string, audio bool, progress func(Progress)) (*Result, error) {
	args := []string{"--ignore-config", "--no-color", "-J", "--js-runtimes", "quickjs:" + f.QJS}
	if c := f.cookieFile(rawURL); c != "" {
		args = append(args, "--cookies", c)
	}
	for _, h := range defaultHeaders {
		args = append(args, "--add-header", h[0]+":"+h[1])
	}
	args = append(args, "--", rawURL)
	out, err := runQuiet(ctx, f.YtDlp, 3*time.Minute, args...)
	if err != nil {
		return nil, err
	}
	var info map[string]any
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("instagram info: %w", err)
	}
	if entries, ok := info["entries"].([]any); ok {
		var pick map[string]any
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok && hasVideo(m) {
				pick = m
				break
			}
		}
		if pick == nil {
			return nil, errors.New("Instagram post contains no downloadable videos (photo-only carousel)")
		}
		for _, k := range []string{"title", "description", "uploader", "extractor_key", "extractor"} {
			if s, _ := pick[k].(string); s == "" {
				if v, ok := info[k]; ok {
					pick[k] = v
				}
			}
		}
		info = pick
	}
	return f.runInfoJSON(ctx, rawURL, info, audio, progress)
}

func hasVideo(m map[string]any) bool {
	if formats, ok := m["formats"].([]any); ok {
		for _, x := range formats {
			if fm, ok := x.(map[string]any); ok {
				if vc, _ := fm["vcodec"].(string); vc != "none" {
					return true
				}
			}
		}
		return false
	}
	_, ok := m["url"]
	return ok
}

// --------------------------------------------------------------- Threads ---

const threadsAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

var threadsPostRe = regexp.MustCompile(`/(?:post|t)/([A-Za-z0-9_-]+)`)

var threadsBoolProviders = []string{
	"BarcelonaHasDearAlgoConsumption", "BarcelonaIsLoggedIn", "BarcelonaHasEventBadge",
	"BarcelonaThreadsWebCachingImprovements", "BarcelonaIsSearchDiscoveryEnabled", "BarcelonaHasCommunities",
	"BarcelonaHasGameScoreShare", "BarcelonaHasPublicViewCountCard", "BarcelonaHasScorecardCommunity",
	"BarcelonaHasMusic", "BarcelonaHasGhostPostEmojiActivation", "BarcelonaOptionalCookiesEnabled",
	"BarcelonaHasDearAlgoWebProduction", "BarcelonaIsCrawler", "BarcelonaHasDisplayNames",
	"BarcelonaHasCommunityTopContributors", "BarcelonaCanSeeSponsoredContent",
	"BarcelonaShouldShowFediverseM075Features", "BarcelonaIsInternalUser",
}

// DecodeThreadsShortcode converts a Threads shortcode to its numeric post ID.
func DecodeThreadsShortcode(code string) (string, error) {
	n, base := new(big.Int), big.NewInt(int64(len(threadsAlphabet)))
	for _, ch := range code {
		v := strings.IndexRune(threadsAlphabet, ch)
		if v < 0 {
			return "", fmt.Errorf("invalid Threads shortcode: %s", code)
		}
		n.Mul(n, base).Add(n, big.NewInt(int64(v)))
	}
	return n.String(), nil
}

func (f *Fetcher) threads(ctx context.Context, rawURL string, audio bool, progress func(Progress)) (*Result, error) {
	canonical, code := rawURL, ""
	if m := threadsPostRe.FindStringSubmatch(rawURL); m != nil {
		code = m[1]
	} else {
		resp, err := f.get(ctx, rawURL, nil)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		canonical = resp.Request.URL.String()
		m := threadsPostRe.FindStringSubmatch(canonical)
		if m == nil {
			return nil, errors.New("Unable to resolve Threads post shortcode from share URL")
		}
		code = m[1]
	}
	postID, err := DecodeThreadsShortcode(code)
	if err != nil {
		return nil, err
	}
	vars := map[string]any{"postID": postID}
	for _, p := range threadsBoolProviders {
		vars["__relay_internal__pv__"+p+"relayprovider"] = false
	}
	varsJSON, _ := json.Marshal(vars)
	form := url.Values{"lsd": {"1"}, "variables": {string(varsJSON)}, "doc_id": {"26742885138663415"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://www.threads.com/api/graphql", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("X-FB-Friendly-Name", "BarcelonaPermalinkMobilePostColumnPageQuery")
	req.Header.Set("X-FB-LSD", "1")
	req.Header.Set("X-IG-App-ID", "238260118697367")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Threads API: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			Media *threadsMedia `json:"media"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("Threads API: %w", err)
	}
	media := payload.Data.Media
	if media == nil {
		if len(payload.Errors) > 0 && payload.Errors[0].Message != "" {
			return nil, errors.New(payload.Errors[0].Message)
		}
		return nil, errors.New("Threads API returned no post media")
	}
	items := media.CarouselMedia
	if len(items) == 0 {
		items = []threadsMedia{*media}
	}
	var video *threadsMedia
	for i := range items {
		if len(items[i].VideoVersions) > 0 {
			video = &items[i]
			break
		}
	}
	if video == nil {
		return nil, errors.New("This Threads post contains no downloadable video")
	}
	uploader := media.User.Username
	caption := media.Caption.Text
	title := caption
	if title == "" {
		title = "Threads video by " + cmp(uploader, "unknown user")
	}
	var formats []map[string]any
	for _, v := range video.VideoVersions {
		if v.URL == "" {
			continue
		}
		fm := map[string]any{"url": v.URL, "width": video.OriginalWidth, "height": video.OriginalHeight,
			"http_headers": map[string]string{"Referer": "https://www.threads.com/"}}
		if v.Type != nil {
			fm["format_id"] = strconv.Itoa(*v.Type)
		}
		formats = append(formats, fm)
	}
	if len(formats) == 0 {
		return nil, errors.New("Threads API returned no downloadable video formats")
	}
	info := map[string]any{
		"id": code, "title": title, "description": caption, "webpage_url": canonical,
		"extractor": "Threads", "extractor_key": "Threads", "formats": formats,
		"uploader": uploader, "timestamp": media.TakenAt,
	}
	if len(video.ImageVersions2.Candidates) > 0 {
		info["thumbnail"] = video.ImageVersions2.Candidates[0].URL
	}
	slog.Info("downloading Threads post through the Threads web API", "code", code)
	return f.runInfoJSON(ctx, canonical, info, audio, progress)
}

type threadsMedia struct {
	VideoVersions []struct {
		URL  string `json:"url"`
		Type *int   `json:"type"`
	} `json:"video_versions"`
	CarouselMedia  []threadsMedia `json:"carousel_media"`
	OriginalWidth  int            `json:"original_width"`
	OriginalHeight int            `json:"original_height"`
	ImageVersions2 struct {
		Candidates []struct {
			URL string `json:"url"`
		} `json:"candidates"`
	} `json:"image_versions2"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Caption struct {
		Text string `json:"text"`
	} `json:"caption"`
	TakenAt int64 `json:"taken_at"`
}

// ---------------------------------------------------------------- TikTok ---

var tiktokPostRe = regexp.MustCompile(`/(video|photo)/(\d+)`)

var errNoFallback = errors.New("no TikTok fallback applies")

func (f *Fetcher) resolveTikTok(ctx context.Context, rawURL string) (kind, id, canonical string, err error) {
	if m := tiktokPostRe.FindStringSubmatch(rawURL); m != nil {
		return m[1], m[2], rawURL, nil
	}
	var seen []string
	client := *f.HTTP
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		seen = append(seen, req.URL.String())
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	for _, h := range defaultHeaders {
		if h[0] != "Accept-Encoding" {
			req.Header.Set(h[0], h[1])
		}
	}
	resp, err := client.Do(req)
	if err == nil {
		seen = append(seen, resp.Request.URL.String())
		resp.Body.Close()
	}
	for _, u := range seen {
		if m := tiktokPostRe.FindStringSubmatch(u); m != nil {
			return m[1], m[2], u, nil
		}
	}
	if err != nil {
		return "", "", "", err
	}
	return "", "", "", errors.New("Unable to resolve TikTok post ID from URL")
}

func (f *Fetcher) tiktokFallback(ctx context.Context, rawURL string, audio bool, original error, progress func(Progress)) (*Result, error) {
	kind, id, canonical, err := f.resolveTikTok(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if kind == "photo" {
		return f.tiktokSlideshow(ctx, id, canonical, audio)
	}
	lower := strings.ToLower(original.Error())
	if !strings.Contains(lower, "tiktok is requiring login") && !strings.Contains(lower, "unable to extract universal data") &&
		!strings.Contains(lower, "video not available, status code -1") {
		return nil, errNoFallback
	}
	slog.Info("TikTok webpage requires login; retrying through the official embed endpoint", "id", id)
	meta := f.tiktokOEmbed(ctx, canonical)
	args := f.baseArgs(rawURL, audio, false)
	for i, a := range args {
		if a == "-o" {
			args[i+1] = filepath.Join(f.VideosDir, id+".%(ext)s")
		}
	}
	res, err := f.ytdlp(ctx, args, "https://www.tiktok.com/embed/v2/"+id, progress)
	if err != nil {
		return nil, err
	}
	res.VideoID = id
	res.Extractor = "TikTok"
	if t, _ := meta["title"].(string); t != "" {
		res.Title, res.Description = t, t
	} else if res.Title == "" {
		res.Title = "TikTok " + id
	}
	return res, nil
}

func (f *Fetcher) tiktokOEmbed(ctx context.Context, canonical string) map[string]any {
	resp, err := f.get(ctx, "https://www.tiktok.com/oembed?url="+url.QueryEscape(canonical), map[string]string{"User-Agent": desktopUA})
	if err != nil {
		slog.Warn("TikTok oEmbed metadata unavailable", "err", err)
		return nil
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

var frontityRe = regexp.MustCompile(`(?s)<script[^>]+id=["']__FRONTITY_CONNECT_STATE__["'][^>]*>(.*?)</script>`)

type tiktokVideoData struct {
	ImagePostInfo struct {
		DisplayImages []struct {
			URLList []string `json:"urlList"`
			Width   int      `json:"width"`
			Height  int      `json:"height"`
		} `json:"displayImages"`
	} `json:"imagePostInfo"`
	ItemInfos struct {
		Text string `json:"text"`
	} `json:"itemInfos"`
	AuthorInfos struct {
		UniqueID string `json:"uniqueId"`
	} `json:"authorInfos"`
	MusicInfos struct {
		PlayURL []string `json:"playUrl"`
	} `json:"musicInfos"`
}

func (f *Fetcher) tiktokEmbedData(ctx context.Context, id string) (*tiktokVideoData, error) {
	resp, err := f.get(ctx, "https://www.tiktok.com/embed/v2/"+id, map[string]string{"User-Agent": desktopUA, "Accept-Language": "en-US,en;q=0.9"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	m := frontityRe.FindSubmatch(body)
	if m == nil {
		return nil, errors.New("TikTok embed page returned no post data")
	}
	var state struct {
		Source struct {
			Data map[string]struct {
				VideoData *tiktokVideoData `json:"videoData"`
			} `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &state); err != nil {
		return nil, err
	}
	vd := state.Source.Data["/embed/v2/"+id].VideoData
	if vd == nil {
		return nil, errors.New("TikTok embed page returned no video data")
	}
	return vd, nil
}

// tiktokSlideshow renders a photo post to MP4 (or its soundtrack to M4A).
func (f *Fetcher) tiktokSlideshow(ctx context.Context, id, canonical string, audio bool) (*Result, error) {
	vd, err := f.tiktokEmbedData(ctx, id)
	if err != nil {
		return nil, err
	}
	images := vd.ImagePostInfo.DisplayImages
	if len(images) == 0 {
		return nil, errors.New("TikTok post does not contain a downloadable slideshow")
	}
	referer := "https://www.tiktok.com/embed/v2/" + id
	title := vd.ItemInfos.Text
	if title == "" {
		title = "TikTok photo post by " + cmp(vd.AuthorInfos.UniqueID, "unknown user")
	}
	tmp, err := os.MkdirTemp(f.VideosDir, "tiktok-"+id+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	slog.Info("downloading TikTok photo post through the official embed endpoint", "id", id)

	res := &Result{VideoID: id, Title: title, Description: vd.ItemInfos.Text, Extractor: "TikTok", Ready: !audio}
	var soundtrack string
	if len(vd.MusicInfos.PlayURL) > 0 {
		soundtrack = filepath.Join(tmp, "soundtrack")
		if err := f.downloadFirst(ctx, vd.MusicInfos.PlayURL, soundtrack, referer); err != nil {
			if audio {
				return nil, err
			}
			slog.Warn("TikTok soundtrack unavailable; rendering silently", "err", err)
			soundtrack = ""
		}
	}
	if audio {
		if soundtrack == "" {
			return nil, errors.New("TikTok photo post has no downloadable soundtrack")
		}
		res.FilePath = filepath.Join(f.VideosDir, id+".m4a")
		return res, f.Media.ExtractAudio(ctx, soundtrack, res.FilePath)
	}

	portrait, landscape := 0, 0
	var slides []string
	for i, img := range images {
		if img.Height > img.Width {
			portrait++
		} else if img.Width > img.Height {
			landscape++
		}
		p := filepath.Join(tmp, fmt.Sprintf("slide-%03d.jpg", i))
		if err := f.downloadFirst(ctx, img.URLList, p, referer); err != nil {
			slog.Warn("TikTok slide could not be downloaded", "slide", i+1, "err", err)
			continue
		}
		slides = append(slides, p)
	}
	if len(slides) == 0 {
		return nil, errors.New("TikTok photo post has no downloadable images")
	}
	res.Width, res.Height = 720, 720
	if portrait > landscape {
		res.Width, res.Height = 720, 1280
	} else if landscape > portrait {
		res.Width, res.Height = 1280, 720
	}
	res.ThumbSource = filepath.Join(f.VideosDir, id+".thumbsrc.jpg")
	if err := copyFile(slides[0], res.ThumbSource); err != nil {
		res.ThumbSource = ""
	}
	res.FilePath = filepath.Join(f.VideosDir, id+".mp4")
	res.VCodec = "h264"
	if err := f.Media.Slideshow(ctx, slides, soundtrack, res.Width, res.Height, res.FilePath); err != nil {
		return nil, fmt.Errorf("Could not render TikTok slideshow: %w", err)
	}
	return res, nil
}

// --------------------------------------------------------------- helpers ---

func cmp(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func runQuiet(ctx context.Context, name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasPrefix(lines[i], "ERROR:") {
				return nil, errors.New(lines[i])
			}
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("yt-dlp timed out after %s", timeout)
		}
		return nil, fmt.Errorf("yt-dlp: %w: %s", err, strings.Join(lines[max(0, len(lines)-5):], "\n"))
	}
	return stdout.Bytes(), nil
}
