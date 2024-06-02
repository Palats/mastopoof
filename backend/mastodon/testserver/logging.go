package testserver

import (
	"net/http"
	"sync"
	"testing"
)

// LoggingHandler is an intercept http handler which writes http
// traffic on the provided testing construct.
type LoggingHandler struct {
	T       testing.TB
	Handler http.Handler

	m   sync.Mutex
	idx int64
}

func (h *LoggingHandler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	h.m.Lock()
	idx := h.idx
	h.idx++
	h.m.Unlock()

	cookie, _ := req.Cookie("mastopoof")
	h.T.Logf("HTTP Request %d: %s %s [cookie:%s]", idx, req.Host, req.URL, cookie)

	// Do the actual request.
	h.Handler.ServeHTTP(writer, req)

	// And see the cookies that were sent back.
	header := http.Header{}
	header.Add("Cookie", writer.Header().Get("Set-Cookie"))
	respCookie, err := (&http.Request{Header: header}).Cookie("mastopoof")
	if err == nil {
		h.T.Logf("HTTP Response %d: Set-Cookie:%v", idx, respCookie)
	}

	if link := writer.Header().Get("Link"); link != "" {
		h.T.Logf("HTTP Response %d: Link: %v", idx, link)
	}
}
