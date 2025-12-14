package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	neturl "net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
)

type H = map[string]any

type FileData struct {
	Key      string
	Filename string
	Content  io.ReadCloser
}

type HTTPDelegate interface {
	Do(req *http.Request) (*http.Response, error)
}

type HTTPClient struct {
	base    string
	m       sync.RWMutex
	headers http.Header
	client  HTTPDelegate
}

func NewHTTPClient(base string, httpDelegate ...HTTPDelegate) *HTTPClient {
	pathBase := base
	if strings.HasSuffix(base, "/") {
		pathBase = strings.TrimRight(base, "/")
	}
	var delegate HTTPDelegate
	if len(httpDelegate) != 0 {
		delegate = httpDelegate[0]
	} else {
		delegate = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}
	}
	return &HTTPClient{
		base:    pathBase,
		headers: http.Header{},
		client:  delegate,
	}
}

func (c *HTTPClient) SetProxy(proxy string) {
	c.m.Lock()
	defer c.m.Unlock()
	c.client = &http.Client{
		Transport: &http.Transport{
			Proxy: func(req *http.Request) (*neturl.URL, error) {
				return neturl.Parse(proxy)
			},
		},
	}
}

func (c *HTTPClient) UnsetProxy() {
	c.m.Lock()
	defer c.m.Unlock()
	c.client = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}

func (c *HTTPClient) SetHeader(key, val string) {
	c.m.Lock()
	defer c.m.Unlock()
	c.headers.Add(key, val)
}

func (c *HTTPClient) UnsetHeader(key string) {
	c.m.Lock()
	defer c.m.Unlock()
	c.headers.Del(key)
}

type RequestContext struct {
	c       *HTTPClient
	ctx     context.Context
	method  string
	path    string
	body    io.Reader
	headers http.Header
	query   map[string][]string
	errors  []error
}

type ResponseHelper struct {
	*http.Response
}

func (c *HTTPClient) startRequest(ctx context.Context, method string, path string) *RequestContext {
	headers := http.Header{}
	for k, l := range c.headers {
		for _, v := range l {
			headers.Add(k, v)
		}
	}
	return &RequestContext{
		c:       c,
		ctx:     ctx,
		method:  method,
		query:   map[string][]string{},
		headers: headers,
		path:    path,
	}
}

func (c *HTTPClient) Get(ctx context.Context, path string) *RequestContext {
	return c.startRequest(ctx, "GET", path)
}

func (c *HTTPClient) Post(ctx context.Context, path string) *RequestContext {
	return c.startRequest(ctx, "POST", path)
}

func (c *HTTPClient) Put(ctx context.Context, path string) *RequestContext {
	return c.startRequest(ctx, "PUT", path)
}

func (c *HTTPClient) Delete(ctx context.Context, path string) *RequestContext {
	return c.startRequest(ctx, "DELETE", path)
}

func (rc *RequestContext) handleErr(err error) {
	if err == nil {
		return
	}
	rc.errors = append(rc.errors, err)
}

func (rc *RequestContext) WithQuery(key string, vals ...string) *RequestContext {
	rc.query[key] = append(rc.query[key], vals...)
	return rc
}

func (rc *RequestContext) WithJSON(data any) *RequestContext {
	raw, err := json.Marshal(data)
	rc.handleErr(err)
	if err == nil {
		rc.body = bytes.NewReader(raw)
	}
	rc.headers.Add("Content-Type", "application/json")
	return rc
}

func (rc *RequestContext) WithMultipartWriter(fn func(w *multipart.Writer) error) *RequestContext {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := fn(writer); err != nil {
		rc.handleErr(err)
	}
	if err := writer.Close(); err != nil {
		rc.handleErr(err)
	}
	rc.body = body
	rc.headers.Add("Content-Type", writer.FormDataContentType())
	return rc
}

func getContentTypeFromFilename(filename string) string {
	ext := filepath.Ext(filename)
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	}
	return "application/octet-stream"
}

func createFormFile(w *multipart.Writer, fieldname, filename string) (io.Writer, error) {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldname, filename))
	h.Set("Content-Type", getContentTypeFromFilename(filename))
	return w.CreatePart(h)
}

func (rc *RequestContext) WithMultipartForm(fileds map[string]string, files []FileData) *RequestContext {
	return rc.WithMultipartWriter(func(w *multipart.Writer) error {
		for _, file := range files {
			part, err := createFormFile(w, file.Key, file.Filename)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, file.Content); err != nil {
				return err
			}
		}
		for k, v := range fileds {
			if err := w.WriteField(k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (rc *RequestContext) WithHeader(key, val string) *RequestContext {
	rc.headers.Add(key, val)
	return rc
}

func (rc *RequestContext) Poll(onResponse func(*ResponseHelper) (bool, error), pollingInterval time.Duration, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(rc.ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			res, err := rc.Do()
			if err != nil {
				return err
			}
			ok, err := onResponse(res)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
}

func (rc *RequestContext) Do() (*ResponseHelper, error) {
	// handle previous errors
	if len(rc.errors) != 0 {
		msg := ""
		for _, e := range rc.errors {
			msg += fmt.Sprintf("%v;", e)
		}
		return nil, fmt.Errorf("failed to construct request: %s", msg)
	}

	// path
	urlStr, err := neturl.JoinPath(rc.c.base, strings.Split(rc.path, "/")...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to construct URL, base: %s, path: %s", rc.c.base, rc.path)
	}

	// new request
	req, err := http.NewRequest(rc.method, urlStr, rc.body)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to construct request, method: %s, url: %s", rc.method, urlStr)
	}

	// query
	query := req.URL.Query()
	for k, v := range rc.query {
		for _, vv := range v {
			query.Add(k, vv)
		}
	}
	req.URL.RawQuery = query.Encode()

	// headers
	req.Header = rc.headers

	// send request
	res, err := rc.c.client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to send request, method: %s, path: %s, query: %v, headers: %v", rc.method, rc.path, rc.query, rc.headers)
	}
	return NewResponseHelper(res), nil
}

func NewResponseHelper(res *http.Response) *ResponseHelper {
	return &ResponseHelper{res}
}

func (rh *ResponseHelper) Bytes() ([]byte, error) {
	return io.ReadAll(rh.Body)
}

func (rh *ResponseHelper) Text() string {
	raw, err := io.ReadAll(rh.Body)
	if err != nil {
		return err.Error()
	}
	return string(raw)
}

func (rh *ResponseHelper) JSON(data any) error {
	raw, err := io.ReadAll(rh.Body)
	if err != nil {
		return errors.Wrap(err, "failed to read body from HTTP response")
	}
	if err := json.Unmarshal(raw, data); err != nil {
		return errors.Wrapf(err, "failed to unmarshal JSON, body: %s", string(raw))
	}
	return nil
}

func (rh *ResponseHelper) ExpectStatusWithMessage(msg string, statusCodes ...int) error {
	for _, c := range statusCodes {
		if rh.StatusCode == c {
			return nil
		}
	}
	if len(msg) == 0 {
		return fmt.Errorf("unexpected status code: %d, expecting: %v", rh.StatusCode, statusCodes)
	}
	return fmt.Errorf("%s, unexpected status code: %d, expecting: %v", msg, rh.StatusCode, statusCodes)
}

func (rh *ResponseHelper) ExpectStatus(statusCodes ...int) error {
	return rh.ExpectStatusWithMessage("", statusCodes...)
}
