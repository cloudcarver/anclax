package apigen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type endlessZeroReader struct{}

func (endlessZeroReader) Read(p []byte) (int, error) {
	return len(p), nil
}

func TestGeneratedResponseParserAcceptsExactSizeLimit(t *testing.T) {
	rsp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(io.LimitReader(endlessZeroReader{}, MaxResponseBodyBytes)),
	}

	parsed, err := ParseListTasksResponse(rsp)
	require.NoError(t, err)
	require.Len(t, parsed.Body, int(MaxResponseBodyBytes))
}

func TestGeneratedResponseParserRejectsStreamingBodyOverLimit(t *testing.T) {
	rsp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(endlessZeroReader{}),
	}

	parsed, err := ParseListTasksResponse(rsp)
	require.Nil(t, parsed)
	require.ErrorIs(t, err, ErrResponseBodyTooLarge)
	require.Equal(t, "response body exceeds maximum size", err.Error())
}

func TestGeneratedClientPreservesContextCancellationWhileReading(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		_, _ = w.Write([]byte("stream-start"))
		flusher.Flush()

		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		payload := make([]byte, 1024)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write(payload); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	client, err := NewClientWithResponses(server.URL, WithHTTPClient(&http.Client{}))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	parsed, err := client.ListTasksWithResponse(ctx)
	require.Nil(t, parsed)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "error = %v", err)
	require.NotErrorIs(t, err, ErrResponseBodyTooLarge)
}

func TestGeneratedClientUsesTimeoutUnlessOverridden(t *testing.T) {
	client, err := NewClient("http://example.test")
	require.NoError(t, err)
	defaultClient, ok := client.Client.(*http.Client)
	require.True(t, ok)
	require.Equal(t, DefaultHTTPClientTimeout, defaultClient.Timeout)

	override := &http.Client{}
	client, err = NewClient("http://example.test", WithHTTPClient(override))
	require.NoError(t, err)
	require.Same(t, override, client.Client)
	require.Zero(t, override.Timeout)
}
