// Package apiclient owns the application's outbound API client policy.
// This is scaffold source: edit it directly to customize client behavior.
package apiclient

import (
	"errors"
	"io"
	"net/http"
	"time"

	"myexampleapp/pkg/zgen/apigen"
)

// Config controls one client's request timeout and response size limit.
type Config struct {
	// Timeout covers the whole request, including reading the response body.
	// Zero or a negative value disables the timeout, as with http.Client.
	Timeout time.Duration
	// MaxResponseBodyBytes limits data read from each response, including raw
	// streaming responses. Zero or a negative value disables the size limit.
	MaxResponseBodyBytes int64
}

// DefaultConfig defines the application's defaults. Change these values here;
// anclax gen preserves this file.
func DefaultConfig() Config {
	return Config{
		Timeout:              30 * time.Second,
		MaxResponseBodyBytes: 10 << 20,
	}
}

// ErrResponseBodyTooLarge is returned when the configured size limit is exceeded.
var ErrResponseBodyTooLarge = errors.New("response body exceeds maximum size")

// New creates an API client using application-owned policy. Transport settings
// and response handling can be customized in this ordinary application code.
func New(server string, cfg Config) (*apigen.ClientWithResponses, error) {
	client := &responseLimitedClient{
		client:               &http.Client{Timeout: cfg.Timeout},
		maxResponseBodyBytes: cfg.MaxResponseBodyBytes,
	}
	return apigen.NewClientWithResponses(server, apigen.WithHTTPClient(client))
}

type responseLimitedClient struct {
	client               apigen.HttpRequestDoer
	maxResponseBodyBytes int64
}

func (c *responseLimitedClient) Do(req *http.Request) (*http.Response, error) {
	rsp, err := c.client.Do(req)
	if err != nil {
		return rsp, err
	}
	if c.maxResponseBodyBytes > 0 {
		rsp.Body = &limitedResponseBody{ReadCloser: rsp.Body, remaining: c.maxResponseBodyBytes}
	}
	return rsp, nil
}

type limitedResponseBody struct {
	io.ReadCloser
	remaining int64
	overflow  error
}

func (b *limitedResponseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.overflow != nil {
		return 0, b.overflow
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			b.overflow = ErrResponseBodyTooLarge
			if err != nil && err != io.EOF {
				b.overflow = errors.Join(b.overflow, err)
			}
			return 0, b.overflow
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}
