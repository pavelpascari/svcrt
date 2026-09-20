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

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Client returns middleware that records a client span, injects trace context
// into the outbound request, and reports request duration.
//
// The return type is the bare func type rather than a named one declared here,
// so the result is assignable to svcrt/httpclient.Middleware without this
// module importing it. Go assigns an unnamed func type to any named type with
// the same underlying type, but never one named type to another -- so a
// telemetry.Middleware declared here for tidiness would compile and then
// refuse to go into httpclient.Chain.
//
// Compose it INSIDE svcrt/resilience.Retry, which gives one span per attempt:
// a retried call then shows three spans and the two that failed are visible.
// Outside, three attempts collapse into one span and the retries become
// invisible -- the opposite of what you instrumented for.
func Client(o Options) func(http.RoundTripper) http.RoundTripper {
	prop := o.propagator()
	tracer := o.tracer()
	hist := durationHistogram(o.meter(),
		"http.client.request.duration",
		"Duration of outbound HTTP requests.",
	)

	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// Host alone, never the full URL: a per-path or per-query label is
			// an unbounded metric label set and an unbounded span attribute.
			addr := req.URL.Hostname()

			ctx, span := tracer.Start(req.Context(), req.Method,
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(
					semconv.HTTPRequestMethodKey.String(req.Method),
					semconv.ServerAddress(addr),
				),
			)
			defer span.End()

			// http.RoundTripper's contract says RoundTrip must not modify the
			// request it is given, and injecting traceparent is a
			// modification. With resilience.Retry in the chain the same
			// request object is replayed, so mutating it corrupts every later
			// attempt. Clone rebinds to the span context in the same step,
			// and -- unlike WithContext -- gives the header map its own
			// backing storage, so writing into it here cannot reach the
			// caller's own Header map.
			req = req.Clone(ctx)
			prop.Inject(ctx, propagation.HeaderCarrier(req.Header))

			start := time.Now()
			resp, err := next.RoundTrip(req)

			attrs := make([]attribute.KeyValue, 0, 3)
			attrs = append(attrs,
				semconv.HTTPRequestMethodKey.String(req.Method),
				semconv.ServerAddress(addr),
			)
			switch {
			case err != nil:
				span.SetStatus(codes.Error, err.Error())
			default:
				attrs = append(attrs, semconv.HTTPResponseStatusCode(resp.StatusCode))
				span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))
				// Unlike the server side, a 4xx here IS this caller's problem:
				// it asked the upstream for something wrong.
				if resp.StatusCode >= http.StatusBadRequest {
					span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
				}
			}
			hist.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))

			return resp, err
		})
	}
}
