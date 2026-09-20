package telemetry

import (
	"context"
	"log/slog"
	"slices"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// LogExtractor returns an extractor that adds the current trace and span ids
// to every log record, plus any allowlisted baggage members.
//
// The return type is the bare func type rather than svcrt/logging's named
// Extractor because this module does not import svcrt/logging -- and could not
// use the named type even if it did: Go permits assignment between a named and
// an unnamed type with the same underlying type, but not between two different
// named types.
//
// With no arguments it emits trace_id and span_id only. Baggage arrives from
// an untrusted upstream, so members are opted into by name rather than
// forwarded wholesale: anything a caller puts in a baggage header would
// otherwise become a log attribute of unbounded name and cardinality.
//
// It returns nil when there is no valid span, which svcrt/logging's contract
// explicitly permits. That check is not cosmetic: an absent span context
// reports all-zero ids rather than failing, so skipping it would stamp a
// well-formed but false trace_id on every log line emitted outside a request.
func LogExtractor(baggageKeys ...string) func(context.Context) []slog.Attr {
	// Clone so a caller mutating their slice afterwards cannot change which
	// baggage members this extractor trusts.
	keys := slices.Clone(baggageKeys)

	return func(ctx context.Context) []slog.Attr {
		var attrs []slog.Attr

		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			attrs = append(attrs,
				slog.String(KeyTraceID, sc.TraceID().String()),
				slog.String(KeySpanID, sc.SpanID().String()),
			)
		}

		// Looked up per record rather than cached: baggage is per-request,
		// and this extractor outlives any one request.
		if len(keys) > 0 {
			b := baggage.FromContext(ctx)
			for _, k := range keys {
				if v := b.Member(k).Value(); v != "" {
					attrs = append(attrs, slog.String(k, v))
				}
			}
		}

		return attrs
	}
}
