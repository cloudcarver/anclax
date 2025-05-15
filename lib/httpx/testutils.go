package httpx

import (
	"io"
	"net/http"
	"testing"
)

type NoopHTTPDelegate struct {
	req *http.Request
	Res *http.Response
}

func (n *NoopHTTPDelegate) Do(req *http.Request) (*http.Response, error) {
	n.req = req
	return n.Res, nil
}

func (n *NoopHTTPDelegate) GetHeader(k string) string {
	return n.req.Header.Get(k)
}

func (n *NoopHTTPDelegate) GetQuery(k string) string {
	return n.req.URL.Query().Get(k)
}

func (n *NoopHTTPDelegate) GetRequestBody() ([]byte, error) {
	return io.ReadAll(n.req.Body)
}

func (n *NoopHTTPDelegate) AssertHeader(t *testing.T, k string, v string) string {
	t.Helper()
	actual := n.GetHeader(k)
	if actual != v {
		t.Errorf("expected header %s: %s, got: %s", k, v, actual)
	}
	return actual
}

func (n *NoopHTTPDelegate) AssertQuery(t *testing.T, k string, v string) string {
	t.Helper()
	actual := n.GetQuery(k)
	if actual != v {
		t.Errorf("expected query %s: %s, got: %s", k, v, actual)
	}
	return actual
}

func (n *NoopHTTPDelegate) AssertRequestBody(t *testing.T, expected []byte) []byte {
	t.Helper()
	actual, err := n.GetRequestBody()
	if err != nil {
		t.Errorf("failed to get request body: %v", err)
	}
	if string(actual) != string(expected) {
		t.Errorf("expected request body: %s, got: %s", string(expected), string(actual))
	}
	return actual
}

func (n *NoopHTTPDelegate) AssertMethod(t *testing.T, method string) string {
	t.Helper()
	actual := n.req.Method
	if actual != method {
		t.Errorf("expected method: %s, got: %s", method, actual)
	}
	return actual
}

func (n *NoopHTTPDelegate) AssertURL(t *testing.T, url string) string {
	t.Helper()
	actual := n.req.URL.String()
	if actual != url {
		t.Errorf("expected URL: %s, got: %s", url, actual)
	}
	return actual
}
