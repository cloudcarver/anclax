package httpx

import (
	"net/http"
)

type NoopHTTPDelegate struct {
	req *http.Request
	Res *http.Response
}

func (n *NoopHTTPDelegate) Do(req *http.Request) (*http.Response, error) {
	n.req = req
	return n.Res, nil
}

func (n *NoopHTTPDelegate) GetRequest() *http.Request {
	return n.req
}
