package apigen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudcarver/anclax/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type endlessZeroReader struct{}

func (endlessZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type trackedResponseBody struct {
	io.Reader
	bytesRead int64
	closes    int
	closeErr  error
}

func (b *trackedResponseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytesRead += int64(n)
	return n, err
}

func (b *trackedResponseBody) Close() error {
	b.closes++
	return b.closeErr
}

func TestGeneratedResponseParserLeavesSizePolicyToApplication(t *testing.T) {
	const bodySize = 10<<20 + 1
	body := &trackedResponseBody{Reader: io.LimitReader(endlessZeroReader{}, bodySize)}
	rsp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       body,
	}

	parsed, err := ParseListTasksResponse(rsp)
	require.NoError(t, err)
	require.Len(t, parsed.Body, bodySize)
	require.EqualValues(t, bodySize, body.bytesRead)
	require.Equal(t, 1, body.closes)
}

type failingResponseReader struct{ err error }

func (r failingResponseReader) Read(p []byte) (int, error) {
	return copy(p, "partial response"), r.err
}

func TestGeneratedResponseParserClosesBodyOnReadAndDecodeErrors(t *testing.T) {
	readErr := errors.New("response read failed")
	for _, tc := range []struct {
		name        string
		reader      io.Reader
		contentType string
		wantErr     error
	}{
		{"read failure", failingResponseReader{readErr}, "text/plain", readErr},
		{"invalid JSON", strings.NewReader("{"), "application/json", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedResponseBody{Reader: tc.reader}
			parsed, err := ParseListTasksResponse(&http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{tc.contentType}},
				Body:       body,
			})
			require.Nil(t, parsed)
			require.Error(t, err)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.Equal(t, 1, body.closes)
		})
	}
}

func TestGeneratedResponseParserLogsCloseErrorsWithoutReplacingReadResult(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	previousLog := xClientLog
	xClientLog = logger.NewLogAgentWithLogger("api-client", zap.New(core))
	t.Cleanup(func() { xClientLog = previousLog })

	readErr := errors.New("response read failed")
	for _, tc := range []struct {
		name    string
		reader  io.Reader
		wantErr error
	}{
		{"success", strings.NewReader("response"), nil},
		{"read failure", failingResponseReader{readErr}, readErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedResponseBody{Reader: tc.reader, closeErr: errors.New("private-close-canary")}
			parsed, err := ParseListTasksResponse(&http.Response{StatusCode: http.StatusInternalServerError, Body: body})
			if tc.wantErr != nil {
				require.Nil(t, parsed)
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, "response", string(parsed.Body))
			}
			require.Equal(t, 1, body.closes)
			entries := logs.TakeAll()
			require.Len(t, entries, 1)
			require.Equal(t, zapcore.WarnLevel, entries[0].Level)
			require.Equal(t, "failed to close response body", entries[0].Message)
			require.Equal(t, map[string]interface{}{"module": "api-client"}, entries[0].ContextMap())
		})
	}
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

type cancelOnReadBody struct {
	io.ReadCloser
	cancel    context.CancelFunc
	bytesRead int
}

func (b *cancelOnReadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.bytesRead += n
		b.cancel()
	}
	return n, err
}

func TestGeneratedClientPreservesContextCancellationWhileReading(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		if _, err := w.Write([]byte("stream-start")); err != nil {
			t.Errorf("write response: %v", err)
			return
		}
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var body *cancelOnReadBody
	client, err := NewClientWithResponses(server.URL, WithHTTPClient(httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		rsp, err := server.Client().Do(req)
		if err != nil {
			return nil, err
		}
		body = &cancelOnReadBody{ReadCloser: rsp.Body, cancel: cancel}
		rsp.Body = body
		return rsp, nil
	})))
	require.NoError(t, err)

	parsed, err := client.ListTasksWithResponse(ctx)
	require.Nil(t, parsed)
	require.NotNil(t, body)
	require.Positive(t, body.bytesRead)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGeneratedRawClientKeepsLargeResponsesStreaming(t *testing.T) {
	const bodySize = 10<<20 + 1
	body := &trackedResponseBody{Reader: io.LimitReader(endlessZeroReader{}, bodySize)}
	client, err := NewClient("http://example.test", WithHTTPClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})))
	require.NoError(t, err)
	rsp, err := client.ListTasks(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rsp.Body.Close()) })
	require.Zero(t, body.bytesRead)
	require.Zero(t, body.closes)
	n, err := io.Copy(io.Discard, rsp.Body)
	require.NoError(t, err)
	require.EqualValues(t, bodySize, n)
}

func TestGeneratedClientLeavesTimeoutPolicyToApplication(t *testing.T) {
	client, err := NewClient("http://example.test")
	require.NoError(t, err)
	defaultClient, ok := client.Client.(*http.Client)
	require.True(t, ok)
	require.Zero(t, defaultClient.Timeout)

	override := &http.Client{Timeout: time.Minute}
	client, err = NewClient("http://example.test", WithHTTPClient(override))
	require.NoError(t, err)
	require.Same(t, override, client.Client)
	require.Equal(t, time.Minute, override.Timeout)
}
