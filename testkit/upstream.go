package testkit

import (
	"net/http"
	"net/http/httptest"
	"sync"
)

// Response is one scripted answer from an Upstream.
type Response struct {
	Status int
	Body   string
	Header http.Header
}

// Status returns a Response with only a status code.
func Status(code int) Response { return Response{Status: code} }

// JSON returns a Response with a JSON content type and body.
func JSON(code int, body string) Response {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return Response{Status: code, Body: body, Header: h}
}

// Server is a stub upstream serving a fixed script.
type Server struct {
	srv    *httptest.Server
	script []Response

	mu sync.Mutex
	n  int
}

// Upstream starts a stub HTTP server answering script in order, repeating the
// LAST entry for every request after the script is exhausted.
//
// Repeat-last rather than exhaust-and-fail because both shapes a test needs
// fall out of it: Upstream(tb, Status(503), Status(503), JSON(200, "{}"))
// fails twice and then recovers, while Upstream(tb, Status(500)) is simply
// down. An empty script answers 200 with an empty body.
//
// The server registers its own shutdown with tb.Cleanup, so a test never
// closes it.
func Upstream(tb TB, script ...Response) *Server {
	tb.Helper()

	s := &Server{script: script}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := s.next()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		status := resp.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp.Body))
	}))
	tb.Cleanup(s.srv.Close)
	return s
}

// next returns the response for this request and advances the counter.
func (s *Server) next() Response {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.n
	s.n++
	if len(s.script) == 0 {
		return Response{Status: http.StatusOK}
	}
	if i >= len(s.script) {
		i = len(s.script) - 1
	}
	return s.script[i]
}

// URL is the base URL to point a client at.
func (s *Server) URL() string { return s.srv.URL }

// Requests is how many requests the upstream has served.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
