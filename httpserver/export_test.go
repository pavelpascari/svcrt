package httpserver

import (
	"net/http"
	"time"
)

// ReadHeaderTimeoutForTest exposes the resolved timeout for assertions.
func (s *Server) ReadHeaderTimeoutForTest() time.Duration { return s.srv.ReadHeaderTimeout }

// CheckTimeoutForTest exposes the per-readiness-check timeout for assertions.
func CheckTimeoutForTest() time.Duration { return checkTimeout }

// HTTPServerForTest exposes the wrapped http.Server. Options mirrors
// http.Server field for field and New copies six of them across; without a
// way to read the result, a dropped or swapped copy is invisible --
// go-mutesting does not mutate struct-literal field assignments, so the
// mutation gate cannot see it either.
func (s *Server) HTTPServerForTest() *http.Server { return s.srv }
