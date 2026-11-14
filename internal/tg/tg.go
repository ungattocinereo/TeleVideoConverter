// Package tg is a small Telegram Bot API client: just the calls this bot
// makes, streaming multipart uploads, retries on 429 and network errors, and
// errors that never contain the bot token.
package tg

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	token     string
	base      string
	api       *http.Client // regular calls
	poll      *http.Client // long polling
	upload    *http.Client // file uploads
	attempts  int
	baseDelay time.Duration
	maxDelay  time.Duration
}

// transport returns a dedicated HTTP/1.1 transport. Polling, regular calls
// and uploads each get their own connections: sharing one HTTP/2
// connection made small uploads wait minutes behind the long poll.
func transport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: time.Second,
	}
}

func New(token string, attempts int, baseDelay, maxDelay time.Duration) *Client {
	return &Client{
		token:     token,
		base:      "https://api.telegram.org/bot" + token + "/",
		api:       &http.Client{Timeout: 30 * time.Second, Transport: transport()},
		poll:      &http.Client{Timeout: 45 * time.Second, Transport: transport()},
		upload:    &http.Client{Timeout: 10 * time.Minute, Transport: transport()},
		attempts:  max(1, attempts),
		baseDelay: baseDelay,
		maxDelay:  maxDelay,
	}
}

// APIError is an error answer from Telegram (ok=false).
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type response struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// sanitize removes the token from errors (net/url errors include the URL).
func (c *Client) sanitize(err error) error {
	if err == nil || c.token == "" || !strings.Contains(err.Error(), c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "<token>"))
}

// slowCall is the duration above which a call is logged with its timing
// breakdown (how long the upload took vs. how long Telegram thought).
const slowCall = 3 * time.Second

// bodyTimer records when the request body has been fully read (sent).
type bodyTimer struct {
	r    io.Reader
	n    int64
	done time.Time
}

func (b *bodyTimer) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if err == io.EOF && b.done.IsZero() {
		b.done = time.Now()
	}
	return n, err
}

// body produces a request body, its content type and its exact length.
type body func() (io.Reader, string, int64, error)

func (c *Client) do(ctx context.Context, hc *http.Client, method string, mk body, out any) error {
	reader, contentType, length, err := mk()
	if err != nil {
		return err
	}
	bt := &bodyTimer{r: reader}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bt)
	if err != nil {
		return c.sanitize(err)
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", contentType)
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		return c.sanitize(err)
	}
	defer resp.Body.Close()
	if took := time.Since(start); took > slowCall && method != "getUpdates" {
		attrs := []any{"method", method, "took", took.Round(time.Millisecond), "bytes", bt.n}
		if !bt.done.IsZero() {
			attrs = append(attrs, "upload", bt.done.Sub(start).Round(time.Millisecond),
				"telegram_processing", time.Since(bt.done).Round(time.Millisecond))
		}
		slog.Info("slow telegram call", attrs...)
	}
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: bad response (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if !r.OK {
		return &APIError{Method: method, Code: r.ErrorCode, Description: r.Description, RetryAfter: r.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// retry runs fn, retrying rate limits and network failures like the Python
// bot did. Other API errors (bad request, forbidden) are returned at once.
func (c *Client) retry(ctx context.Context, op string, fn func() error) error {
	var err error
	for attempt := 1; attempt <= c.attempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || attempt == c.attempts {
			return err
		}
		var delay time.Duration
		var apiErr *APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Code == 429:
			delay = min(c.maxDelay, max(time.Second, time.Duration(apiErr.RetryAfter)*time.Second))
			slog.Warn("telegram rate-limited", "op", op, "attempt", attempt, "retry_in", delay)
		case errors.As(err, &apiErr) && apiErr.Code >= 500:
			delay = min(c.maxDelay, c.baseDelay<<(attempt-1))
			slog.Warn("telegram server error", "op", op, "attempt", attempt, "err", err, "retry_in", delay)
		case errors.As(err, &apiErr):
			return err
		default:
			// Network trouble: drop pooled keep-alive connections so the
			// retry dials fresh ones instead of reusing a dead socket.
			c.ResetConnections()
			delay = min(c.maxDelay, c.baseDelay<<(attempt-1))
			slog.Warn("telegram request failed", "op", op, "attempt", attempt, "err", err, "retry_in", delay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

// ResetConnections closes idle connections of all three clients.
func (c *Client) ResetConnections() {
	for _, hc := range []*http.Client{c.api, c.poll, c.upload} {
		hc.CloseIdleConnections()
	}
}

func jsonBody(params any) body {
	return func() (io.Reader, string, int64, error) {
		b, err := json.Marshal(params)
		return bytes.NewReader(b), "application/json", int64(len(b)), err
	}
}

// Call performs a JSON API call with retries.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return c.retry(ctx, method, func() error { return c.do(ctx, c.api, method, jsonBody(params), out) })
}

// CallOnce performs a JSON API call without retries (status animation edits).
func (c *Client) CallOnce(ctx context.Context, method string, params, out any) error {
	return c.do(ctx, c.api, method, jsonBody(params), out)
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var ups []Update
	err := c.do(ctx, c.poll, "getUpdates", jsonBody(map[string]any{
		"offset": offset, "timeout": timeout, "allowed_updates": []string{"message"},
	}), &ups)
	return ups, err
}

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	return &u, c.Call(ctx, "getMe", map[string]any{}, &u)
}

type SendOpts struct {
	ParseMode             string
	DisableWebPagePreview bool
	ReplyTo               int64
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, o SendOpts) (*Message, error) {
	p := map[string]any{"chat_id": chatID, "text": text}
	if o.ParseMode != "" {
		p["parse_mode"] = o.ParseMode
	}
	if o.DisableWebPagePreview {
		p["link_preview_options"] = map[string]bool{"is_disabled": true}
	}
	if o.ReplyTo != 0 {
		p["reply_parameters"] = map[string]any{"message_id": o.ReplyTo, "allow_sending_without_reply": true}
	}
	var m Message
	return &m, c.Call(ctx, "sendMessage", p, &m)
}

func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, retry bool) error {
	p := map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}
	var err error
	if retry {
		err = c.Call(ctx, "editMessageText", p, nil)
	} else {
		err = c.CallOnce(ctx, "editMessageText", p, nil)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

// countWriter counts bytes, to size a multipart body before sending it.
type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

// writeMultipart writes fields and files; with dry=true file contents are
// skipped (only their sizes are counted) to compute Content-Length.
func writeMultipart(w io.Writer, boundary string, fields map[string]string, files map[string]string, dry bool) (string, int64, error) {
	cw := &countWriter{}
	mw := multipart.NewWriter(io.MultiWriter(w, cw))
	if err := mw.SetBoundary(boundary); err != nil {
		return "", 0, err
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return "", 0, err
		}
	}
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	var fileBytes int64
	for _, field := range names {
		path := files[field]
		part, err := mw.CreateFormFile(field, filepath.Base(path))
		if err != nil {
			return "", 0, err
		}
		if dry {
			st, err := os.Stat(path)
			if err != nil {
				return "", 0, err
			}
			fileBytes += st.Size()
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return "", 0, err
		}
		_, err = io.Copy(part, f)
		f.Close()
		if err != nil {
			return "", 0, err
		}
	}
	if err := mw.Close(); err != nil {
		return "", 0, err
	}
	return mw.FormDataContentType(), cw.n + fileBytes, nil
}

// Upload sends a multipart request with an exact Content-Length. files maps
// form field -> local path; files are streamed from disk (reopened on every
// retry), never buffered in memory.
func (c *Client) Upload(ctx context.Context, method string, fields map[string]string, files map[string]string, out any) error {
	boundary := multipart.NewWriter(io.Discard).Boundary()
	mk := func() (io.Reader, string, int64, error) {
		contentType, length, err := writeMultipart(io.Discard, boundary, fields, files, true)
		if err != nil {
			return nil, "", 0, err
		}
		pr, pw := io.Pipe()
		go func() {
			_, _, err := writeMultipart(pw, boundary, fields, files, false)
			pw.CloseWithError(err)
		}()
		return pr, contentType, length, nil
	}
	return c.retry(ctx, method, func() error { return c.do(ctx, c.upload, method, mk, out) })
}

type VideoParams struct {
	Width, Height int
	Duration      int
	Caption       string
	Thumbnail     string // local JPEG path, optional
}

// sentMedia is the part of a sent Message we care about: the file_id of
// whatever Telegram turned the upload into (short silent clips can come
// back as an animation).
type sentMedia struct {
	Video *struct {
		FileID string `json:"file_id"`
	} `json:"video"`
	Animation *struct {
		FileID string `json:"file_id"`
	} `json:"animation"`
	Document *struct {
		FileID string `json:"file_id"`
	} `json:"document"`
	Audio *struct {
		FileID string `json:"file_id"`
	} `json:"audio"`
}

func (m *sentMedia) fileID() string {
	switch {
	case m.Video != nil:
		return m.Video.FileID
	case m.Animation != nil:
		return m.Animation.FileID
	case m.Document != nil:
		return m.Document.FileID
	case m.Audio != nil:
		return m.Audio.FileID
	}
	return ""
}

// SendVideo uploads a video and returns Telegram's file_id for it, which
// SendVideoByID can reuse without uploading again.
func (c *Client) SendVideo(ctx context.Context, chatID int64, path string, p VideoParams) (string, error) {
	fields := map[string]string{
		"chat_id":            strconv.FormatInt(chatID, 10),
		"supports_streaming": "true",
	}
	if p.Width > 0 && p.Height > 0 {
		fields["width"] = strconv.Itoa(p.Width)
		fields["height"] = strconv.Itoa(p.Height)
	}
	if p.Duration > 0 {
		fields["duration"] = strconv.Itoa(p.Duration)
	}
	if p.Caption != "" {
		fields["caption"] = p.Caption
	}
	files := map[string]string{"video": path}
	if p.Thumbnail != "" {
		if _, err := os.Stat(p.Thumbnail); err == nil {
			files["thumb_file"] = p.Thumbnail
			fields["thumbnail"] = "attach://thumb_file"
		}
	}
	var m sentMedia
	err := c.Upload(ctx, "sendVideo", fields, files, &m)
	return m.fileID(), err
}

// SendVideoByID re-sends a video Telegram already has.
func (c *Client) SendVideoByID(ctx context.Context, chatID int64, fileID string, p VideoParams) error {
	params := map[string]any{"chat_id": chatID, "video": fileID, "supports_streaming": true}
	if p.Caption != "" {
		params["caption"] = p.Caption
	}
	return c.Call(ctx, "sendVideo", params, nil)
}

func (c *Client) SendAudio(ctx context.Context, chatID int64, path, title string) (string, error) {
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if title != "" {
		fields["title"] = title
	}
	var m sentMedia
	err := c.Upload(ctx, "sendAudio", fields, map[string]string{"audio": path}, &m)
	return m.fileID(), err
}
