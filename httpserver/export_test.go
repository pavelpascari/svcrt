package httpserver

import "time"

// ReadHeaderTimeoutForTest exposes the resolved timeout for assertions.
func (s *Server) ReadHeaderTimeoutForTest() time.Duration { return s.srv.ReadHeaderTimeout }

// CheckTimeoutForTest exposes the per-readiness-check timeout for assertions.
func CheckTimeoutForTest() time.Duration { return checkTimeout }
