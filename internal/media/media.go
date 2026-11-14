// Package media wraps ffprobe/ffmpeg: it decides whether a download can be
// remuxed as-is or must be re-encoded, makes thumbnails, and prepares
// Telegram-sized copies of large videos.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Tools struct {
	FFmpeg  string
	FFprobe string
	Profile string // H.264 profile for re-encodes: baseline|main|high
}

type Probe struct {
	Duration   float64
	Width      int
	Height     int
	HasVideo   bool
	HasAudio   bool
	VideoCodec string
	Profile    string
	PixFmt     string
	AudioCodec string
	Container  string
	FPS        float64 // average frame rate
}

func (t Tools) Probe(ctx context.Context, path string) (*Probe, error) {
	out, err := run(ctx, 2*time.Minute, t.FFprobe, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path)
	if err != nil {
		return nil, err
	}
	var data struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Profile   string `json:"profile"`
			PixFmt    string `json:"pix_fmt"`
			AvgFPS    string `json:"avg_frame_rate"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Duration  string `json:"duration"`
			Disp      struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration   string `json:"duration"`
			FormatName string `json:"format_name"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	p := &Probe{Container: data.Format.FormatName}
	p.Duration, _ = strconv.ParseFloat(data.Format.Duration, 64)
	for _, s := range data.Streams {
		switch {
		case s.CodecType == "video" && s.Disp.AttachedPic == 0 && !p.HasVideo:
			p.HasVideo = true
			p.VideoCodec, p.Profile, p.PixFmt = s.CodecName, s.Profile, s.PixFmt
			p.Width, p.Height = s.Width, s.Height
			p.FPS = parseRate(s.AvgFPS)
			if p.Duration <= 0 {
				p.Duration, _ = strconv.ParseFloat(s.Duration, 64)
			}
		case s.CodecType == "audio" && !p.HasAudio:
			p.HasAudio = true
			p.AudioCodec = s.CodecName
		}
	}
	return p, nil
}

// videoPlaysEverywhere is the copy-instead-of-encode rule: 8-bit 4:2:0 H.264
// with even dimensions. (High 10 / 4:2:2 / 4:4:4 and HEVC/VP9/AV1 are not.)
func (p *Probe) videoPlaysEverywhere() bool {
	if p.VideoCodec != "h264" {
		return false
	}
	if p.PixFmt != "yuv420p" && p.PixFmt != "yuvj420p" {
		return false
	}
	if strings.Contains(p.Profile, "10") || strings.Contains(p.Profile, "4:2:2") || strings.Contains(p.Profile, "4:4:4") {
		return false
	}
	return p.Width%2 == 0 && p.Height%2 == 0
}

func (p *Probe) audioPlaysEverywhere() bool {
	return !p.HasAudio || p.AudioCodec == "aac" || p.AudioCodec == "mp3"
}

func parseRate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	if !ok {
		return n
	}
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// StandardFPS picks the nearest common frame rate for a constant-rate
// encode (Threads' 28.79 and X's 30.006 become 30).
func StandardFPS(fps float64) int {
	best := 30
	for _, r := range []int{24, 25, 30, 50, 60} {
		if fps > 0 && math.Abs(fps-float64(r)) < math.Abs(fps-float64(best)) {
			best = r
		}
	}
	return best
}

// WillEncode reports whether Normalize re-encodes the video. Telegram
// re-processes remuxed social-media files (irregular timestamps) for 15 s
// to several minutes, but takes a constant-frame-rate H.264 encode
// instantly, so everything that is sent as-is gets encoded. Files above
// remuxAbove are only remuxed: Telegram receives a compressed CFR copy
// of those anyway (PrepareForTelegram).
func (p *Probe) WillEncode(size, remuxAbove int64) bool {
	if !p.HasVideo {
		return false
	}
	return !p.videoPlaysEverywhere() || size <= remuxAbove
}

type NormalizeResult struct {
	Path    string
	Probe   *Probe
	Action  string // "copy", "remux", "audio", "encode"
	Elapsed time.Duration
}

// Normalize turns a downloaded file into <dir>/<id>.mp4 that Telegram plays
// on every client. Compatible streams are only remuxed (with faststart);
// everything else is re-encoded once with libx264/AAC.
func (t Tools) Normalize(ctx context.Context, in, out string, remuxAbove int64) (*NormalizeResult, error) {
	start := time.Now()
	p, err := t.Probe(ctx, in)
	if err != nil {
		return nil, err
	}
	if !p.HasVideo {
		return nil, fmt.Errorf("downloaded file has no video stream")
	}
	st, err := os.Stat(in)
	if err != nil {
		return nil, err
	}
	copyVideo, copyAudio := !p.WillEncode(st.Size(), remuxAbove), p.audioPlaysEverywhere()
	action := "remux"
	switch {
	case !copyVideo:
		action = "encode"
	case !copyAudio:
		action = "audio"
	}

	tmp := out + ".tmp.mp4"
	args := []string{"-hide_banner", "-nostdin", "-y", "-i", in, "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn"}
	if copyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, t.x264Args()...)
		args = append(args, "-crf", "23", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p",
			"-fps_mode", "cfr", "-r", strconv.Itoa(StandardFPS(p.FPS)))
	}
	if copyAudio {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ar", "44100")
	}
	args = append(args, "-movflags", "+faststart", "-max_muxing_queue_size", "9999", tmp)

	timeout := 10 * time.Minute
	if action == "encode" {
		timeout = max(5*time.Minute, time.Duration(p.Duration*6)*time.Second)
	}
	if _, err := run(ctx, timeout, t.FFmpeg, args...); err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("ffmpeg %s: %w", action, err)
	}
	if err := os.Rename(tmp, out); err != nil {
		return nil, err
	}
	if in != out {
		os.Remove(in)
	}
	np, err := t.Probe(ctx, out)
	if err != nil {
		return nil, err
	}
	return &NormalizeResult{Path: out, Probe: np, Action: action, Elapsed: time.Since(start)}, nil
}

func (t Tools) x264Args() []string {
	level := "4.1"
	if t.Profile == "baseline" {
		level = "3.1"
	}
	return []string{"-c:v", "libx264", "-preset", "veryfast", "-profile:v", t.Profile, "-level:v", level,
		"-pix_fmt", "yuv420p", "-g", "50", "-keyint_min", "25"}
}

// Thumbnail converts src (any image ffmpeg reads) into a JPEG whose longest
// side is 320px — Telegram's thumbnail limit — and removes src.
func (t Tools) Thumbnail(ctx context.Context, src, dst string) error {
	_, err := run(ctx, time.Minute, t.FFmpeg, "-hide_banner", "-nostdin", "-y", "-i", src, "-frames:v", "1",
		"-vf", "scale='if(gte(iw,ih),320,-2)':'if(gte(iw,ih),-2,320)',format=yuvj420p", "-q:v", "4", dst)
	if err == nil && src != dst {
		os.Remove(src)
	}
	return err
}

// FrameThumbnail grabs a frame from a video when the site gave no thumbnail.
func (t Tools) FrameThumbnail(ctx context.Context, video, dst string, duration float64) error {
	at := "0"
	if duration > 2 {
		at = "1"
	}
	_, err := run(ctx, time.Minute, t.FFmpeg, "-hide_banner", "-nostdin", "-y", "-ss", at, "-i", video, "-frames:v", "1",
		"-vf", "scale='if(gte(iw,ih),320,-2)':'if(gte(iw,ih),-2,320)',format=yuvj420p", "-q:v", "4", dst)
	return err
}

// ExtractAudio converts any input to an AAC .m4a file.
func (t Tools) ExtractAudio(ctx context.Context, in, out string) error {
	_, err := run(ctx, 10*time.Minute, t.FFmpeg, "-hide_banner", "-nostdin", "-y", "-i", in, "-vn",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", out)
	return err
}

// Slideshow renders images (shown slideSeconds each) and an optional
// soundtrack into an H.264/AAC MP4 of the given size, like the old TikTok
// photo-post support.
func (t Tools) Slideshow(ctx context.Context, images []string, soundtrack string, width, height int, out string) error {
	const slideSeconds = 2.5
	dir := filepath.Dir(images[0])
	list := filepath.Join(dir, "slides.txt")
	var b strings.Builder
	for _, img := range images {
		fmt.Fprintf(&b, "file '%s'\nduration %.1f\n", strings.ReplaceAll(img, "'", `'\''`), slideSeconds)
	}
	fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(images[len(images)-1], "'", `'\''`))
	if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
		return err
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-f", "concat", "-safe", "0", "-i", list}
	if soundtrack != "" {
		args = append(args, "-i", soundtrack)
	}
	args = append(args, "-map", "0:v:0")
	if soundtrack != "" {
		args = append(args, "-map", "1:a:0?")
	}
	args = append(args, "-vf", fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=30,format=yuv420p",
		width, height, width, height))
	args = append(args, t.x264Args()...)
	args = append(args, "-crf", "23")
	if soundtrack != "" {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ar", "44100")
	}
	args = append(args, "-t", fmt.Sprintf("%.1f", float64(len(images))*slideSeconds), "-movflags", "+faststart", out)
	_, err := run(ctx, 10*time.Minute, t.FFmpeg, args...)
	return err
}

// ------------------------------------------------- Telegram size limits ---

type Limits struct {
	SendLimitBytes int64
	TargetBytes    int64
	MinVideoKbps   int
	AudioKbps      int
	Policy         string
}

type Part struct {
	Path          string
	Width, Height int
	Duration      float64
	Index, Count  int
	Temporary     bool
}

var scaleLadder = [...]int{720, 540, 480, 360}

func (l Limits) rawBitrates(duration float64, hasAudio bool) (video, audio int) {
	usableBits := float64(l.TargetBytes) * 8 * 0.92
	total := max(1, int(usableBits/math.Max(duration, 1)/1000))
	if hasAudio {
		audio = l.AudioKbps
	}
	return total - audio, audio
}

func (l Limits) Bitrates(duration float64, hasAudio bool) (video, audio int) {
	video, audio = l.rawBitrates(duration, hasAudio)
	return max(l.MinVideoKbps, video), audio
}

func (l Limits) PartCount(duration float64, hasAudio bool) int {
	video, _ := l.rawBitrates(duration, hasAudio)
	if video >= l.MinVideoKbps {
		return 1
	}
	audio := 0
	if hasAudio {
		audio = l.AudioKbps
	}
	usableBits := float64(l.TargetBytes) * 8 * 0.92
	maxPerPart := math.Max(1, usableBits/float64((l.MinVideoKbps+audio)*1000))
	return max(1, int(math.Ceil(duration/maxPerPart)))
}

func MaxSide(videoKbps int) int {
	switch {
	case videoKbps >= 1000:
		return scaleLadder[0]
	case videoKbps >= 700:
		return scaleLadder[1]
	case videoKbps >= 450:
		return scaleLadder[2]
	}
	return scaleLadder[3]
}

// PrepareForTelegram returns the file itself when it fits, otherwise
// temporary compressed copies (split into parts for long videos). Each part
// is encoded in a single pass; if it overshoots, it is redone 15% smaller.
func (t Tools) PrepareForTelegram(ctx context.Context, l Limits, path, tmpDir string) ([]Part, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	p, err := t.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	if st.Size() <= l.TargetBytes || !p.HasVideo {
		return []Part{{Path: path, Width: p.Width, Height: p.Height, Duration: p.Duration, Index: 1, Count: 1}}, nil
	}
	if p.Duration <= 0 {
		return nil, fmt.Errorf("cannot compress video for Telegram: duration is unknown")
	}
	count := l.PartCount(p.Duration, p.HasAudio)
	if count > 1 && l.Policy != "compress_then_split" {
		return nil, fmt.Errorf("video is too long to fit Telegram limit without splitting")
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	partLen := p.Duration / float64(count)
	var parts []Part
	cleanup := func() { Cleanup(parts) }
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for i := range count {
		startAt := float64(i) * partLen
		dur := partLen
		if i == count-1 {
			dur = p.Duration - startAt
		}
		vk, ak := l.Bitrates(dur, p.HasAudio)
		suffix := "compressed"
		if count > 1 {
			suffix = fmt.Sprintf("part%02d-of-%02d", i+1, count)
		}
		out := filepath.Join(tmpDir, fmt.Sprintf("%s-%s-%d.mp4", base, suffix, time.Now().UnixNano()%1e8))
		var size int64
		for attempt := range 2 {
			kbps := vk
			if attempt == 1 {
				kbps = int(float64(vk) * 0.85)
			}
			if err := t.compressPart(ctx, path, out, startAt, dur, p.FPS, count > 1, p.HasAudio, kbps, ak, MaxSide(kbps)); err != nil {
				os.Remove(out)
				cleanup()
				return nil, err
			}
			fi, err := os.Stat(out)
			if err != nil {
				cleanup()
				return nil, err
			}
			size = fi.Size()
			if size <= l.SendLimitBytes {
				break
			}
			slog.Warn("compressed part over limit, retrying smaller", "size", size, "kbps", kbps)
		}
		if size > l.SendLimitBytes {
			os.Remove(out)
			cleanup()
			return nil, fmt.Errorf("compressed part is still too large for Telegram: %.1f MB", float64(size)/(1024*1024))
		}
		op, err := t.Probe(ctx, out)
		if err != nil {
			os.Remove(out)
			cleanup()
			return nil, err
		}
		parts = append(parts, Part{Path: out, Width: op.Width, Height: op.Height, Duration: op.Duration,
			Index: i + 1, Count: count, Temporary: true})
	}
	return parts, nil
}

func (t Tools) compressPart(ctx context.Context, in, out string, startAt, dur, fps float64, split, hasAudio bool, vk, ak, maxSide int) error {
	args := []string{"-hide_banner", "-nostdin", "-y"}
	if split {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startAt), "-t", fmt.Sprintf("%.3f", dur))
	}
	args = append(args, "-i", in, "-map", "0:v:0")
	args = append(args, t.x264Args()...)
	args = append(args,
		"-b:v", fmt.Sprintf("%dk", vk), "-maxrate", fmt.Sprintf("%dk", vk*5/4), "-bufsize", fmt.Sprintf("%dk", vk*2),
		"-vf", fmt.Sprintf("scale='if(gte(iw,ih),min(%[1]d,iw),-2)':'if(gte(iw,ih),-2,min(%[1]d,ih))',scale=trunc(iw/2)*2:trunc(ih/2)*2", maxSide))
	args = append(args, "-fps_mode", "cfr", "-r", strconv.Itoa(StandardFPS(fps)))
	if hasAudio {
		args = append(args, "-map", "0:a:0?", "-c:a", "aac", "-b:a", fmt.Sprintf("%dk", ak), "-ar", "44100")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-movflags", "+faststart", "-max_muxing_queue_size", "9999", out)
	timeout := max(3*time.Minute, min(3*time.Hour, time.Duration(dur*30)*time.Second))
	_, err := run(ctx, timeout, t.FFmpeg, args...)
	return err
}

func Cleanup(parts []Part) {
	for _, p := range parts {
		if p.Temporary {
			os.Remove(p.Path)
		}
	}
}

// ------------------------------------------------------------- helpers ---

// run executes a tool in its own process group, killing the whole group on
// timeout or cancellation, and returns stdout. Errors carry stderr's tail.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s timed out after %s", filepath.Base(name), timeout)
		}
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, Tail(stderr.String(), 800))
	}
	return stdout.Bytes(), nil
}

// Tail returns the last n bytes of s, trimmed.
func Tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}
