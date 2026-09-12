// Package httpserver provides an http.Server with timeout defaults that are
// hard to get wrong, health gates, and an admin mux.
//
// Server.Start returns once the listener is accepting, which is exactly the
// shape svcrt/lifecycle expects of a component's start function — so the two
// compose with no import between them.
package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// defaultReadHeaderTimeout is applied when Options leaves it zero. Go's own
// default is "wait forever", which is a Slowloris hole: a handful of
// connections dribbling headers one byte at a time pin server goroutines
// indefinitely.
const defaultReadHeaderTimeout = 10 * time.Second

// Options mirrors the knobs of http.Server field for field, so a reader who
// knows one knows the other.
type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration // zero means defaultReadHeaderTimeout
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	// OnServeError is called if Serve fails after Start returned. It is never
	// called for the graceful http.ErrServerClosed. Wire it to
	// lifecycle.Fatal to make a listener dying trigger an ordered shutdown.
	OnServeError func(error)
}

// Server wraps an http.Server with a lifecycle-shaped Start and Shutdown.
type Server struct {
	srv  *http.Server
	opts Options

	mu   sync.Mutex
	ln   net.Listener
	addr string
}

// New returns a Server that will serve h.
func New(h http.Handler, opts Options) *Server {
	rht := opts.ReadHeaderTimeout
	if rht == 0 {
		rht = defaultReadHeaderTimeout
	}
	return &Server{
		opts: opts,
		addr: opts.Addr,
		srv: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: rht,
			ReadTimeout:       opts.ReadTimeout,
			WriteTimeout:      opts.WriteTimeout,
			IdleTimeout:       opts.IdleTimeout,
			MaxHeaderBytes:    opts.MaxHeaderBytes,
		},
	}
}

// Start binds the listener and returns once it is accepting. Serving happens
// in a goroutine this Server owns.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.opts.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.ln = ln
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	go func() {
		err := s.srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return // the normal result of Shutdown
		}
		if s.opts.OnServeError != nil {
			s.opts.OnServeError(err)
		}
	}()
	return nil
}

// Shutdown stops accepting and waits for in-flight requests, bounded by ctx.
// It is a no-op if Start was never called.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	started := s.ln != nil
	s.mu.Unlock()
	if !started {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// Addr returns the resolved listen address once Start has run, and the
// configured address before that. Bind ":0" and ask, so tests never race on a
// fixed port.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
