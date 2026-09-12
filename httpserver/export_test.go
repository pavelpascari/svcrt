package httpserver

import "time"

// ReadHeaderTimeoutForTest exposes the resolved timeout for assertions.
func (s *Server) ReadHeaderTimeoutForTest() time.Duration { return s.srv.ReadHeaderTimeout }

// CloseListenerForTest closes the listener out from under Serve, simulating a
// listener dying after a successful start.
func (s *Server) CloseListenerForTest() {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}
