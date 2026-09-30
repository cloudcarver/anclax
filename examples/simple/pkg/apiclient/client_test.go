package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"myexampleapp/pkg/zgen/apigen"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type trackedBody struct {
	io.Reader
	bytesRead int64
	closes    int
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytesRead += int64(n)
	return n, err
}

func (b *trackedBody) Close() error {
	b.closes++
	return nil
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestConfiguredResponseLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int64
		size      int64 // -1 produces a stream without EOF.
		overflow  bool
		bytesRead int64
	}{
		{"below limit", 8, 7, false, 7},
		{"exact limit", 8, 8, false, 8},
		{"over limit", 8, 9, true, 9},
		{"endless stream", 8, -1, true, 9},
		{"larger custom limit", 16, 12, false, 12},
		{"limit disabled", 0, 10<<20 + 1, false, 10<<20 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = zeroReader{}
			if tc.size >= 0 {
				reader = io.LimitReader(reader, tc.size)
			}
			body := &trackedBody{Reader: reader}
			doer := &responseLimitedClient{
				client: doerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusInternalServerError, Body: body}, nil
				}),
				maxResponseBodyBytes: tc.limit,
			}
			client, err := apigen.NewClientWithResponses("http://example.test", apigen.WithHTTPClient(doer))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := client.GetCounterWithResponse(t.Context())
			if tc.overflow {
				if parsed != nil || !errors.Is(err, ErrResponseBodyTooLarge) {
					t.Fatalf("response = %v, error = %v; want overflow", parsed, err)
				}
			} else if err != nil || parsed == nil || int64(len(parsed.Body)) != tc.size {
				t.Fatalf("response = %v, error = %v; want %d bytes", parsed, err, tc.size)
			}
			if body.bytesRead != tc.bytesRead || body.closes != 1 {
				t.Fatalf("read %d bytes, closed %d times; want %d bytes and one close", body.bytesRead, body.closes, tc.bytesRead)
			}
		})
	}
}

func TestRawResponsesKeepOverflowErrorWithoutReadingMore(t *testing.T) {
	body := &trackedBody{Reader: zeroReader{}}
	doer := &responseLimitedClient{
		client: doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		}),
		maxResponseBodyBytes: 8,
	}
	client, err := apigen.NewClient("http://example.test", apigen.WithHTTPClient(doer))
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := client.GetCounter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rsp.Body.Close(); err != nil {
			t.Errorf("close response: %v", err)
		}
	})
	if body.bytesRead != 0 || body.closes != 0 {
		t.Fatal("raw response was consumed before the caller read it")
	}
	if _, err := io.Copy(io.Discard, rsp.Body); !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("copy error = %v, want overflow", err)
	}
	if n, err := rsp.Body.Read(make([]byte, 32)); n != 0 || !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("read after overflow = (%d, %v)", n, err)
	}
	if body.bytesRead != 9 {
		t.Fatalf("read %d bytes, want at most limit + 1", body.bytesRead)
	}
}

type finalErrorReader struct {
	*strings.Reader
	err error
}

func (r finalErrorReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil {
		return n, err
	}
	if r.Len() == 0 {
		return n, r.err
	}
	return n, nil
}

func TestResponseReadErrorsArePreserved(t *testing.T) {
	readErr := errors.New("response read failed")
	for _, payload := range []string{"1234", "123456789"} {
		t.Run(payload, func(t *testing.T) {
			body := &trackedBody{Reader: finalErrorReader{strings.NewReader(payload), readErr}}
			doer := &responseLimitedClient{
				client: doerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
				}),
				maxResponseBodyBytes: 8,
			}
			client, err := apigen.NewClientWithResponses("http://example.test", apigen.WithHTTPClient(doer))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := client.GetCounterWithResponse(t.Context())
			if parsed != nil || !errors.Is(err, readErr) {
				t.Fatalf("response = %v, error = %v; want original read error", parsed, err)
			}
			if len(payload) > 8 && !errors.Is(err, ErrResponseBodyTooLarge) {
				t.Fatalf("error = %v, want overflow as well", err)
			}
			if body.closes != 1 {
				t.Fatalf("body closed %d times, want once", body.closes)
			}
		})
	}
}

func TestNewUsesApplicationConfiguration(t *testing.T) {
	const payload = "response larger than eight bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		w.(http.Flusher).Flush() // Exercise a response with no Content-Length.
		if _, err := io.WriteString(w, payload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name     string
		cfg      Config
		overflow bool
	}{
		{"defaults", DefaultConfig(), false},
		{"smaller limit", Config{Timeout: time.Second, MaxResponseBodyBytes: 8}, true},
		{"larger limit", Config{Timeout: time.Second, MaxResponseBodyBytes: 64}, false},
		{"disabled limits", Config{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(server.URL, tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := client.GetCounterWithResponse(t.Context())
			if tc.overflow {
				if parsed != nil || !errors.Is(err, ErrResponseBodyTooLarge) {
					t.Fatalf("response = %v, error = %v; want overflow", parsed, err)
				}
			} else if err != nil || parsed == nil || string(parsed.Body) != payload {
				t.Fatalf("response = %v, error = %v; want complete response", parsed, err)
			}
		})
	}
}

func TestNewAppliesConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := New(server.URL, Config{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := client.GetCounterWithResponse(ctx)
	if parsed != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("response = %v, error = %v; want timeout", parsed, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("request was stopped by the outer context: %v", ctx.Err())
	}
}
