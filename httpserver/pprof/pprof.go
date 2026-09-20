// Package pprof exposes Go's profiling endpoints as a single http.Handler.
//
// # Import side effect
//
// Importing this package also imports net/http/pprof, whose init() registers
// the profiling handlers on http.DefaultServeMux. That happens on any import,
// blank or not, and cannot be prevented. It is inert unless something serves
// DefaultServeMux — nothing in svcrt ever does, and a service should build its
// own mux — but it is the reason this lives in its own package rather than in
// svcrt/httpserver: a consumer of httpserver must not silently acquire pprof
// endpoints on a mux it did not ask about.
//
// Mount it only on an admin port that is not publicly routable. Profiling
// endpoints expose heap dumps and goroutine traces.
package pprof

import (
	"net/http"
	netpprof "net/http/pprof"
)

// Handler returns an http.Handler routing the whole /debug/pprof/ subtree.
//
// It is one handler rather than five so a caller mounts it once at that prefix;
// net/http/pprof exposes five separate handlers and requiring each to be
// mounted is how one gets forgotten.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", netpprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", netpprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", netpprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", netpprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", netpprof.Trace)
	return mux
}
