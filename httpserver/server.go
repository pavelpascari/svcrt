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

// errAlreadyShutDown is returned by Start once a prior Shutdown has actually
// stopped a running server. Its text says what to do, not just what
// happened: there is no way to make this Server serve again, so the fix is
// always "construct a new one," never "call Start again."
var errAlreadyShutDown = errors.New(
	"httpserver: Server already shut down; construct a new Server instead of calling Start again",
)

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
//
// A Server is single-use: one Start/Shutdown pair, never reused. Go's
// http.Server cannot be restarted once Shutdown has stopped it -- a later
// Serve on it returns http.ErrServerClosed immediately, closing whatever
// listener it was given without ever accepting a connection -- so a Server
// wrapping it can't be restarted either. Calling Start again after such a
// Shutdown returns an error rather than binding a listener that will never
// serve anything; construct a new Server instead.
type Server struct {
	srv  *http.Server
	opts Options

	mu       sync.Mutex
	ln       net.Listener
	addr     string
	shutDown bool // set once Shutdown has actually stopped a started server
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
// in a goroutine this Server owns, and that goroutine runs until process exit
// if Shutdown is never called — pair Start with Shutdown (for example
// lc.Add("api", srv.Start, srv.Shutdown)) so the goroutine is always
// discharged.
//
// Start returns immediately, before binding, if ctx is already cancelled. A
// cancellation that arrives during the bind itself is not aborted: the bind
// window is tiny, and a lifecycle's unwind stops anything that did bind, so
// the cost of that residual is a little wasted work, not a leaked listener.
//
// Start returns an error, and binds nothing, if a prior Shutdown already
// stopped this Server: see the Server doc for why reuse is refused rather
// than attempted. Without this check, the underlying http.Server's Serve
// would return http.ErrServerClosed the instant it ran, which the serving
// goroutine treats as the ordinary result of a graceful Shutdown and
// swallows without calling OnServeError -- so Start would report success,
// Addr would report a real address, and every request to it would be
// connection-refused, silently.
func (s *Server) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	shutDown := s.shutDown
	s.mu.Unlock()
	if shutDown {
		return errAlreadyShutDown
	}

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
// It is a no-op if Start was never called -- but if Start did run, this
// permanently seals the Server: the underlying http.Server cannot be
// restarted, so this Server cannot be either. A later Start returns
// errAlreadyShutDown instead of silently reusing it.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	started := s.ln != nil
	if started {
		s.shutDown = true
	}
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
