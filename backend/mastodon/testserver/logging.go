package testserver

import (
	"fmt"
	"net/http"
	"sync"
)

// LoggingHandler is an intercept http handler which writes http
// traffic on the provided testing construct.
type LoggingHandler struct {
	Logf    func(format string, args ...any)
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
	h.Logf("HTTP Request %d: %s %s [cookie:%s]", idx, req.Host, req.URL, cookie)

	// Do the actual request.
	h.Handler.ServeHTTP(writer, req)

	msg := fmt.Sprintf("HTTP Response %d:", idx)

	// See the cookies that were sent back.
	header := http.Header{}
	header.Add("Cookie", writer.Header().Get("Set-Cookie"))
	respCookie, err := (&http.Request{Header: header}).Cookie("mastopoof")
	if err == nil {
		msg += fmt.Sprintf(" Set-Cookie:%v", respCookie)
	}

	if link := writer.Header().Get("Link"); link != "" {
		msg += fmt.Sprintf(" Link: %v", link)
	}
	h.Logf("%s", msg)
}
