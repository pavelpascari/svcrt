package telemetry

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

// statusWriter records the response status without hiding any of the optional
// interfaces the wrapped writer implements.
//
// It implements only Unwrap rather than forwarding Flusher, Hijacker and
// ReaderFrom individually: http.ResponseController walks the Unwrap chain, so
// one method keeps every capability reachable and cannot fall out of date as
// new optional interfaces appear.
//
// This duplicates svcrt/httpserver's unexported writer of the same name. The
// duplication is forced by the module boundary -- telemetry imports no svcrt
// module -- and is preferable to the dependency that would remove it.
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

// Server returns middleware that extracts inbound trace context, records a
// server span, and reports request duration.
//
// The return type is the bare func type rather than a named one declared here,
// so the result is assignable to svcrt/httpserver.Middleware without this
// module importing it. See conventions.md S2.
//
// Compose it OUTSIDE svcrt/httpserver.AccessLog. The access-log line is
// emitted by the handler AccessLog wraps, so the span has to already be in the
// context by then; inverted, the line simply carries no trace_id and nothing
// complains.
func Server(o Options) func(http.Handler) http.Handler {
	prop := o.propagator()
	tracer := o.tracer()
	hist := durationHistogram(o.meter(),
		"http.server.request.duration",
		"Duration of inbound HTTP requests.",
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			// Named for the method alone at this point: the route comes from
			// the matched ServeMux pattern, which is not set until the mux has
			// run. The name is corrected on the way out.
			ctx, span := tracer.Start(ctx, r.Method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(semconv.HTTPRequestMethodKey.String(r.Method)),
			)
			defer span.End()

			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			r = r.WithContext(ctx)

			// Deferred so a panicking handler still produces a finished span
			// and a recorded duration. The request that crashed is the one
			// most worth having in a trace and in the latency histogram, and
			// an undeferred call skips exactly that case.
			//
			// The panic is deliberately NOT recovered: whether it becomes a
			// 500 or takes the process down is the service author's decision.
			// This only makes sure it is not also invisible.
			defer func() {
				attrs := make([]attribute.KeyValue, 0, 3)
				attrs = append(attrs, semconv.HTTPRequestMethodKey.String(r.Method))

				// r.Pattern is the verbatim registered ServeMux pattern, which
				// already carries the method when registered as "GET /x" --
				// matching svcrt/httpserver.AccessLog's KeyRoute convention.
				// Prepending r.Method here would double it up.
				if r.Pattern != "" {
					span.SetName(r.Pattern)
					attrs = append(attrs, semconv.HTTPRoute(r.Pattern))
				}
				// written is false when the handler panicked before writing:
				// net/http sends no response at all, so the optimistic 200
				// default is not what happened. Omit rather than invent.
				if sw.written {
					attrs = append(attrs, semconv.HTTPResponseStatusCode(sw.status))
					// 4xx is the caller's fault and leaves the span Unset;
					// only 5xx marks this server's span as failed.
					if sw.status >= http.StatusInternalServerError {
						span.SetStatus(codes.Error, http.StatusText(sw.status))
					}
				}

				// attrs[1:] skips the method, which the span already carries
				// from WithAttributes at Start; the metric needs it in its
				// own attribute set.
				span.SetAttributes(attrs[1:]...)
				hist.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
			}()

			next.ServeHTTP(sw, r)
		})
	}
}
