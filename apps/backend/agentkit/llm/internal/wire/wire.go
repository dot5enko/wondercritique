// Package wire holds HTTP/SSE plumbing shared by the llm provider adapters:
// error parsing, retry-with-backoff before the first stream byte, and SSE
// event splitting.
package wire

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const retryBase = 500 * time.Millisecond

// APIError is a non-200 HTTP response from a provider.
type APIError struct {
	Status     int
	Type       string // e.g. invalid_request_error, authentication_error, rate_limit_error, server_error
	Code       string // e.g. invalid_api_key, model_not_found, rate_limit_exceeded; often empty
	Param      string
	Message    string
	RetryAfter time.Duration // from the Retry-After header, 0 if absent
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d", e.Status)
	for _, s := range []string{e.Type, e.Code} {
		if s != "" {
			b.WriteString(" " + s)
		}
	}
	if e.Param != "" {
		fmt.Fprintf(&b, " (param %s)", e.Param)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// Temporary reports whether retrying the same request may succeed.
func (e *APIError) Temporary() bool { return RetryableStatus(e.Status) }

// RetryableStatus reports whether an HTTP status may be retried. 504 is
// excluded: the same request will usually time out again.
func RetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code != http.StatusGatewayTimeout)
}

// Backoff returns the exponential delay for attempt (0-based) with jitter in [d/2, d).
func Backoff(attempt int) time.Duration {
	d := retryBase << min(attempt, 10)
	return d/2 + rand.N(d/2)
}

// SleepCtx waits for d or until ctx is cancelled.
func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ParseRetryAfter parses a Retry-After header value (delay-seconds or HTTP date).
func ParseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// ParseAPIError builds an APIError from a non-200 response. The error message
// is prefixed with pkg (e.g. "chat").
func ParseAPIError(resp *http.Response, pkg string) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{Status: resp.StatusCode, RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"))}
	var env struct {
		Error *struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    any     `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil {
		e.Message, e.Type = env.Error.Message, env.Error.Type
		if env.Error.Param != nil {
			e.Param = *env.Error.Param
		}
		switch c := env.Error.Code.(type) {
		case string:
			e.Code = c
		case float64:
			e.Code = strconv.FormatFloat(c, 'f', -1, 64)
		}
		return e
	}
	// Anthropic nests errors as {"type":"error","error":{...}} — same shape, but
	// also accept a bare {"error":"..."} string form.
	var str struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &str) == nil && str.Error != "" {
		e.Message = str.Error
		return e
	}
	if msg := strings.TrimSpace(string(raw)); msg != "" {
		e.Message = msg
	} else {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}

// Errorf prefixes err with "pkg: ".
func Errorf(pkg string, err error) error { return fmt.Errorf("%s: %w", pkg, err) }

// Open POSTs via do, retrying retryable failures (per RetryableStatus and
// transport errors) before any response byte is consumed. sleep must respect
// context cancellation; pkg prefixes error messages.
func Open(ctx context.Context, pkg string, maxRetries int, do func() (*http.Response, error), sleep func(context.Context, time.Duration) error) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := do()
		var wait time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			err = fmt.Errorf("%s: %w", pkg, err)
		case resp.StatusCode == http.StatusOK:
			return resp, nil
		default:
			apiErr := ParseAPIError(resp, pkg)
			if !RetryableStatus(resp.StatusCode) {
				return nil, fmt.Errorf("%s: %w", pkg, apiErr)
			}
			err, wait = apiErr, apiErr.RetryAfter
		}
		if attempt >= maxRetries {
			return nil, err
		}
		if wait <= 0 {
			wait = Backoff(attempt)
		}
		if serr := sleep(ctx, wait); serr != nil {
			return nil, serr
		}
	}
}

// Event is one parsed SSE event.
type Event struct {
	Name string // from "event: ..." ("" when absent)
	Data string // joined "data: ..." lines
}

// Done reports whether the event is the OpenAI-style "[DONE]" terminator.
func (e Event) Done() bool { return e.Name == "" && e.Data == "[DONE]" }

// DrainEvents splits an SSE stream into events and hands each to handle.
// It stops (returning nil) when handle returns ErrStop; other handle errors
// abort the stream. Keep-alive comments are skipped.
func DrainEvents(r io.Reader, handle func(Event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20) // data lines can be long
	var eventName string
	var data []string
	flush := func() error {
		if len(data) == 0 {
			eventName = ""
			return nil
		}
		ev := Event{Name: eventName, Data: strings.Join(data, "\n")}
		eventName, data = "", nil
		return handle(ev)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// comment/keepalive
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// ignore unknown lines
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

// ErrStop is returned by a DrainEvents handler to stop consuming silently
// (the consumer stopped iterating or the stream ended cleanly).
var ErrStop = fmt.Errorf("sse consumer stopped")
