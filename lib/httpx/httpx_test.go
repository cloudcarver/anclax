package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPClient(t *testing.T) {
	base := "http://test.example"
	c := NewHTTPClient(base)
	assert.Equal(t, base, c.base)

	baseWithTrailingSlash := fmt.Sprintf("%s/", base)
	c = NewHTTPClient(baseWithTrailingSlash)
	assert.Equal(t, base, c.base)
}

func TestStartRequest(t *testing.T) {
	base := "http://test.example"
	path := "/test"
	c := NewHTTPClient(base)
	ctx := context.Background()

	rc := c.Get(ctx, path)
	assert.Equal(t, path, rc.path)
	assert.Equal(t, "GET", rc.method)

	rc = c.Post(ctx, path)
	assert.Equal(t, path, rc.path)
	assert.Equal(t, "POST", rc.method)

	rc = c.Put(ctx, path)
	assert.Equal(t, path, rc.path)
	assert.Equal(t, "PUT", rc.method)

	rc = c.Delete(ctx, path)
	assert.Equal(t, path, rc.path)
	assert.Equal(t, "DELETE", rc.method)
}

func TestHandleError(t *testing.T) {
	c := NewHTTPClient("http://test.example", &NoopHTTPDelegate{})
	rc := c.Get(context.Background(), "/test")
	testErr := errors.New("error for test")

	rc.handleErr(testErr)
	_, err := rc.Do()
	assert.True(t, strings.Contains(err.Error(), testErr.Error()))
}

func TestHandleError_append_nil(t *testing.T) {
	c := NewHTTPClient("http://test.example", &NoopHTTPDelegate{})
	rc := c.Get(context.Background(), "/test")

	rc.handleErr(nil)
	_, err := rc.Do()
	assert.NoError(t, err)
}

func TestSetHeader(t *testing.T) {
	c := NewHTTPClient("http://test.example")
	var (
		k = "test"
		v = "test"
	)
	c.SetHeader(k, v)
	assert.Equal(t, c.headers.Get(k), v)
}

func TestUnsetHeader(t *testing.T) {
	var (
		k = "test"
	)
	c := NewHTTPClient("http://test.example")
	c.headers.Add(k, "val")
	c.UnsetHeader(k)
	assert.Equal(t, 0, len(c.headers.Get(k)))
}

func TestWithQuery(t *testing.T) {
	var (
		base = "http://test.example"
		path = "/test"
		k    = "上升波"
		v    = "value-/\\%$#?&=+"
	)
	d := &NoopHTTPDelegate{}
	c := NewHTTPClient(base, d)
	rc := c.startRequest(context.Background(), "GET", path).WithQuery(k, v)
	assert.Equal(t, rc.query[k], v)

	_, err := rc.Do()
	require.NoError(t, err)
	assert.Equal(t, v, d.GetQuery(k))
	assert.Equal(t, fmt.Sprintf("%s%s?%s=%s", base, path, neturl.QueryEscape(k), neturl.QueryEscape(v)), d.req.URL.String())
}

func TestWithJSON(t *testing.T) {
	d := &NoopHTTPDelegate{}
	c := NewHTTPClient("http://test.example", d)
	jsonRaw := []byte(`{"test":"test"}`)
	data := make(map[string]string)
	err := json.Unmarshal(jsonRaw, &data)
	require.NoError(t, err)
	rc := c.startRequest(context.Background(), "GET", "/test").WithJSON(data)
	_, err = rc.Do()
	require.NoError(t, err)

	bodyRaw, err := d.GetRequestBody()
	require.NoError(t, err)

	assert.Equal(t, jsonRaw, bodyRaw)
}

func TestWithHeader(t *testing.T) {
	var (
		k = "test"
		v = "test"
	)
	d := &NoopHTTPDelegate{}
	c := NewHTTPClient("http://test.example", d)
	rc := c.startRequest(context.Background(), "GET", "/test").WithHeader(k, v)
	_, err := rc.Do()
	require.NoError(t, err)

	assert.Equal(t, v, d.req.Header.Get(k))
}

func TestExpectStatus(t *testing.T) {
	rh := &ResponseHelper{Response: &http.Response{
		Status:     "200 OK",
		StatusCode: 200,
	}}
	err := rh.ExpectStatus(200)
	assert.NoError(t, err)

	err = rh.ExpectStatus(202)
	assert.Error(t, err)
}

func TestExpectStatusWithMessage(t *testing.T) {
	rh := &ResponseHelper{Response: &http.Response{
		Status:     "200 OK",
		StatusCode: 200,
	}}
	err := rh.ExpectStatusWithMessage("test msg", 202)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test msg")
}

func TestResponseHelperJSON(t *testing.T) {
	rh := &ResponseHelper{Response: &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"test":"test"}`)),
	}}
	data := make(map[string]string)
	err := rh.JSON(&data)
	require.NoError(t, err)
	assert.Equal(t, "test", data["test"])
}

func TestPoll(t *testing.T) {
	var (
		interval = 50 * time.Millisecond
		timeout  = 200 * time.Millisecond
	)
	d := &NoopHTTPDelegate{}
	c := NewHTTPClient("http://test.example", d)
	rc := c.startRequest(context.Background(), "GET", "/test")

	// success immediately
	err := rc.Poll(
		func(rh *ResponseHelper) (bool, error) {
			return true, nil
		},
		interval,
		timeout,
	)
	require.NoError(t, err)

	// success later
	cnt := 0
	err = rc.Poll(
		func(rh *ResponseHelper) (bool, error) {
			cnt += 1
			if cnt == 2 {
				return true, nil
			}
			return false, nil
		},
		interval,
		timeout,
	)
	require.NoError(t, err)

	// fail
	testErr := errors.New("polling test error")
	err = rc.Poll(
		func(rh *ResponseHelper) (bool, error) {
			return false, testErr
		},
		interval,
		timeout,
	)
	assert.True(t, errors.Is(err, testErr))
}

func TestGlobalHeaders(t *testing.T) {
	d := &NoopHTTPDelegate{}
	c := NewHTTPClient("http://test.example", d)
	var (
		k = "test"
		v = "test"
	)
	c.SetHeader(k, v)

	_, err := c.startRequest(context.Background(), "GET", "/test").Do()
	require.NoError(t, err)
	assert.Equal(t, v, d.req.Header.Get(k))
}
