package logging

import (
	"log/slog"
	"net/http"
	"time"
)

// statusWriter records the response status without hiding any of the optional
// interfaces the wrapped writer implements.
//
// It deliberately implements only Unwrap rather than forwarding Flusher,
// Hijacker, and ReaderFrom individually: http.ResponseController walks the
// Unwrap chain, so one method keeps every capability reachable and cannot
// fall out of date as new optional interfaces appear.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.status = http.StatusOK
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware logs one line per request at completion.
//
// The route is taken from the matched ServeMux pattern, not the request path,
// so the key holds "GET /orders/{id}" rather than a distinct value per order
// id. When no pattern matched -- an unmounted handler, or a 404 -- the route
// key is omitted entirely rather than falling back to the raw path, which
// would reintroduce unbounded cardinality on exactly the URLs an unauthorized
// scanner can generate at will.
func Middleware(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sw, r)

			attrs := make([]slog.Attr, 0, 4)
			attrs = append(attrs, slog.String(KeyMethod, r.Method))
			if r.Pattern != "" {
				attrs = append(attrs, slog.String(KeyRoute, r.Pattern))
			}
			attrs = append(attrs,
				slog.Int(KeyStatus, sw.status),
				slog.Int64(KeyDurMS, time.Since(start).Milliseconds()),
			)

			l.LogAttrs(r.Context(), slog.LevelInfo, "http request", attrs...)
		})
	}
}
