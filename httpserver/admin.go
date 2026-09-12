package httpserver

import "net/http"

// AdminMux returns a mux serving the three probes.
//
// The paths are fixed here rather than left to convention because a probe path
// is part of a deployment's contract with Kubernetes:
//
//	GET /healthz   -> Live
//	GET /readyz    -> Ready
//	GET /startupz  -> Started
//
// A plain *http.ServeMux is returned so adding an endpoint needs no API from
// this package:
//
//	admin := httpserver.AdminMux(h)
//	admin.Handle("/debug/pprof/", pprof.Handler())   // svcrt/httpserver/pprof
//
// Nothing is served at a fixed path unless you call this: the gates remain
// mountable individually wherever you like.
//
// An admin surface belongs on a port separate from application traffic, so it
// is not publicly routable, not behind the application's auth middleware, and
// not sharing timeouts tuned for real requests.
func AdminMux(h *Health) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", h.Live)
	mux.Handle("GET /readyz", h.Ready)
	mux.Handle("GET /startupz", h.Started)
	return mux
}
