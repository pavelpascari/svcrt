package kit

import (
	"io"
	"log/slog"

	"github.com/pavelpascari/svcrt/logging"
	"github.com/pavelpascari/svcrt/telemetry"
)

// LoggerOptions configures NewLogger. The zero value emits JSON at info level
// with trace_id and span_id on every record made inside a span.
type LoggerOptions struct {
	// Logging configures the handler.
	Logging logging.Options

	// Baggage names the baggage members to copy onto every record. Empty --
	// the default -- copies none. Baggage arrives from an untrusted upstream,
	// so members are opted into by name rather than forwarded wholesale.
	Baggage []string
}

// NewLogger returns a logger whose records carry the current trace and span
// ids, and any allowlisted baggage.
//
// This exists because omitting the extractor is undetectable. svcrt/logging
// deliberately knows nothing about spans, so a logger built the ordinary way
// produces correct output, alongside correct spans and correct metrics, and
// simply cannot be pivoted between them. Nothing errors and no test a service
// owns would notice. It is also the omission that makes the other two useless:
// a trace you cannot reach from a log line is one you will not find mid-incident.
func NewLogger(w io.Writer, o LoggerOptions) *slog.Logger {
	return logging.New(w, o.Logging, telemetry.LogExtractor(o.Baggage...))
}
