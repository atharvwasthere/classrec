// Package sarvam is a small client for Sarvam's batch speech-to-text API.
// Endpoints and fields follow https://docs.sarvam.ai/api-reference/speech-to-text/stt/job/*;
// the presigned-upload headers follow the official Python SDK (sarvamai 0.1.49).
package sarvam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.sarvam.ai"

type JobParams struct {
	Model          string   `json:"model"`
	Mode           string   `json:"mode"`
	LanguageCode   string   `json:"language_code,omitempty"`
	WithTimestamps bool     `json:"with_timestamps"`
	Keyterms       []string `json:"keyterms,omitempty"`
}

// Fingerprint identifies these parameters, so results and jobs made with other ones are never reused.
func (p JobParams) Fingerprint() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

type RetryPolicy struct {
	Max      int
	Base     time.Duration
	MaxDelay time.Duration
	Sleep    func(ctx context.Context, d time.Duration) error
}

type Client struct {
	BaseURL   string
	HTTP      *http.Client
	Retry     RetryPolicy
	PollEvery time.Duration

	key string
}

func New(apiKey string) *Client {
	return &Client{
		BaseURL:   defaultBaseURL,
		HTTP:      &http.Client{Timeout: 10 * time.Minute},
		Retry:     RetryPolicy{Max: 5, Base: time.Second, MaxDelay: 30 * time.Second, Sleep: sleep},
		PollEvery: 5 * time.Second,
		key:       apiKey,
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// apiError carries Sarvam's error body. It never includes request headers or URLs.
type apiError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("sarvam: HTTP %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.RequestID != "" {
		s += " (request " + e.RequestID + ")"
	}
	return s
}

func parseAPIError(status int, body []byte) *apiError {
	var wrapper struct {
		Error struct {
			Message   string `json:"message"`
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	e := &apiError{Status: status}
	if json.Unmarshal(body, &wrapper) == nil && wrapper.Error.Message != "" {
		e.Code, e.Message, e.RequestID = wrapper.Error.Code, wrapper.Error.Message, wrapper.Error.RequestID
		return e
	}
	e.Message = strings.TrimSpace(string(body))
	if len(e.Message) > 200 {
		e.Message = e.Message[:200]
	}
	return e
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// scrub drops the URL from transport errors: presigned URLs carry a signature.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

func (c *Client) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	d := c.Retry.Base << attempt
	if d > c.Retry.MaxDelay || d <= 0 {
		d = c.Retry.MaxDelay
	}
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// do sends the request built by build, retrying network errors and 408/429/5xx with
// exponential backoff. It returns the body of the first non-retryable response.
func (c *Client) do(ctx context.Context, build func() (*http.Request, error)) (int, []byte, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return 0, nil, err
		}
		req = req.WithContext(ctx)
		resp, err := c.HTTP.Do(req)
		var retryAfter time.Duration
		if err != nil {
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			lastErr = scrub(err)
		} else {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case readErr != nil:
				lastErr = scrub(readErr)
			case !retryableStatus(resp.StatusCode):
				return resp.StatusCode, body, nil
			default:
				apiErr := parseAPIError(resp.StatusCode, body)
				if apiErr.Code == "insufficient_quota_error" {
					return resp.StatusCode, body, nil
				}
				lastErr = apiErr
				if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
					retryAfter = time.Duration(s) * time.Second
				}
			}
		}
		if attempt >= c.Retry.Max {
			return 0, nil, fmt.Errorf("giving up after %d attempts: %w", attempt+1, lastErr)
		}
		if err := c.Retry.Sleep(ctx, c.delay(attempt, retryAfter)); err != nil {
			return 0, nil, err
		}
	}
}

func (c *Client) apiJSON(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
	}
	status, body, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(method, c.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("api-subscription-key", c.key)
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return parseAPIError(status, body)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) initJob(ctx context.Context, p JobParams) (string, error) {
	var resp struct {
		JobID string `json:"job_id"`
	}
	err := c.apiJSON(ctx, http.MethodPost, "/speech-to-text/job/v1", map[string]any{"job_parameters": p}, &resp)
	if err == nil && resp.JobID == "" {
		err = errors.New("sarvam: init job returned no job_id")
	}
	return resp.JobID, err
}

type signedURLs map[string]struct {
	FileURL string `json:"file_url"`
}

func (c *Client) links(ctx context.Context, path, field, jobID string, names []string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := c.apiJSON(ctx, http.MethodPost, path, map[string]any{"job_id": jobID, "files": names}, &raw); err != nil {
		return nil, err
	}
	var urls signedURLs
	if err := json.Unmarshal(raw[field], &urls); err != nil {
		return nil, fmt.Errorf("sarvam: no %s in response", field)
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		u, ok := urls[n]
		if !ok || u.FileURL == "" {
			return nil, fmt.Errorf("sarvam: no %s link for %s", field, n)
		}
		out[n] = u.FileURL
	}
	return out, nil
}

func (c *Client) uploadLinks(ctx context.Context, jobID string, names []string) (map[string]string, error) {
	return c.links(ctx, "/speech-to-text/job/v1/upload-files", "upload_urls", jobID, names)
}

func (c *Client) downloadLinks(ctx context.Context, jobID string, names []string) (map[string]string, error) {
	return c.links(ctx, "/speech-to-text/job/v1/download-files", "download_urls", jobID, names)
}

// putFile uploads to a presigned Azure blob URL.
func (c *Client) putFile(ctx context.Context, signedURL, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	status, body, err := c.do(ctx, func() (*http.Request, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPut, signedURL, f)
		if err != nil {
			f.Close()
			return nil, scrub(err)
		}
		req.ContentLength = fi.Size()
		req.Header.Set("x-ms-blob-type", "BlockBlob")
		req.Header.Set("Content-Type", "audio/ogg")
		return req, nil
	})
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("upload failed: HTTP %d: %s", status, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	return nil
}

func (c *Client) getBytes(ctx context.Context, signedURL string) ([]byte, error) {
	status, body, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, signedURL, nil)
		return req, scrub(err)
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("download failed: HTTP %d", status)
	}
	return body, nil
}

func (c *Client) start(ctx context.Context, jobID string) error {
	return c.apiJSON(ctx, http.MethodPost, "/speech-to-text/job/v1/"+url.PathEscape(jobID)+"/start", nil, nil)
}

type fileRef struct {
	FileName string `json:"file_name"`
}

type fileDetail struct {
	Inputs       []fileRef `json:"inputs"`
	Outputs      []fileRef `json:"outputs"`
	State        string    `json:"state"`
	ErrorMessage *string   `json:"error_message"`
}

type jobStatus struct {
	JobState     string       `json:"job_state"`
	ErrorMessage string       `json:"error_message"`
	JobDetails   []fileDetail `json:"job_details"`
}

func (c *Client) status(ctx context.Context, jobID string) (*jobStatus, error) {
	var s jobStatus
	if err := c.apiJSON(ctx, http.MethodGet, "/speech-to-text/job/v1/"+url.PathEscape(jobID)+"/status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
