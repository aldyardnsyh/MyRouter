package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxErrorBody bounds how much of a non-2xx upstream response is buffered. Error
// bodies only feed error classification and a 300-char message, so 64KB is
// plenty while keeping memory predictable under a hostile upstream.
const maxErrorBody = 64 << 10

// httpClient centralizes the shared HTTP client with sane timeouts.
var DefaultTimeout = 60 * time.Second

// NewHTTPClient returns the client used by all adapters.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 0, // per-request deadlines come from the routing engine
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}

// doJSON performs a JSON HTTP request and decodes 2xx responses into out.
// Non-2xx statuses are returned via the error with the raw body preserved in
// a statusError so adapters can map them via MapError.
func doJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any, out any) (int, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	// Cap the body we buffer to keep memory bounded. Success payloads need the
	// full budget, but an error body is only used for classification and a short
	// message, so a hostile or misbehaving upstream must not be able to force us
	// to buffer megabytes just to build an error.
	limit := int64(8 << 20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limit = maxErrorBody
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, raw, &statusError{Status: resp.StatusCode, Body: raw, Header: resp.Header}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, raw, err
		}
	}
	return resp.StatusCode, raw, nil
}

// MapHTTPError converts a doJSON statusError into an adapter-specific *Error
// using the supplied mapper. Non-status errors are classified as transport.
func MapHTTPError(err error, mapper func(int, []byte) *Error) error {
	var se *statusError
	if errors.As(err, &se) {
		return mapper(se.Status, se.Body)
	}
	return classifyTransport(err)
}

// sseReader iterates Server-Sent-Events `data:` payloads.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64<<10), 4<<20)
	return &sseReader{scanner: s}
}

// next returns the next data payload (empty string = ignore, io.EOF = done).
func (s *sseReader) next() (string, error) {
	for s.scanner.Scan() {
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			return payload, nil
		}
		// Some providers emit bare JSON lines; treat them as data.
		if strings.HasPrefix(line, "{") {
			return line, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

// retryAfterSeconds parses a Retry-After header.
func retryAfterSeconds(h http.Header) int {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return int(d.Seconds())
		}
	}
	return 0
}

// postStream opens a streaming request and returns the response body reader.
func postStream(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) (io.ReadCloser, http.Header, int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, resp.Header, resp.StatusCode, &statusError{Status: resp.StatusCode, Body: raw, Header: resp.Header}
	}
	return resp.Body, resp.Header, resp.StatusCode, nil
}

// statusError carries a non-2xx response from a streaming call.
type statusError struct {
	Status int
	Body   []byte
	Header http.Header
}

func (e *statusError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.Status, truncate(string(e.Body), 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
