package logging_test

import (
	"context"
	"log/slog"
	"os"

	"github.com/pavelpascari/svcrt/logging"
)

// dropTime removes the timestamp so these examples have stable output. A real
// service keeps it -- this is the one thing an example has to take out.
func dropTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// ExampleNew builds the logger a service writes to stdout. What comes back is
// a plain *slog.Logger: there is no wrapper type and no framework.
func ExampleNew() {
	log := logging.New(os.Stdout, logging.Options{
		// Drop the timestamp so this example's output is stable. A real
		// service keeps it.
		ReplaceAttr: dropTime,
	})

	log.Info("started", "addr", ":8080")

	// Output: {"level":"INFO","msg":"started","addr":":8080"}
}

// requestIDKey is the context key the Extractor below reads. A real service
// would set it in middleware; svcrt/telemetry supplies an Extractor of exactly
// this shape that reads span context instead.
type requestIDKey struct{}

// ExampleNew_extractor adds an Extractor, which is how a correlation id
// reaches every record without being passed down through every call.
func ExampleNew_extractor() {
	requestID := func(ctx context.Context) []slog.Attr {
		// Type-assert with the two-result form: an Extractor that panics
		// takes the request down with it, because nothing here recovers.
		if id, ok := ctx.Value(requestIDKey{}).(string); ok {
			return []slog.Attr{slog.String("request_id", id)}
		}
		return nil // Returning nil is fine and contributes nothing.
	}

	log := logging.New(os.Stdout, logging.Options{ReplaceAttr: dropTime}, requestID)

	ctx := context.WithValue(context.Background(), requestIDKey{}, "req-7")
	log.InfoContext(ctx, "handled")

	// Without a request id in the context the extractor contributes nothing
	// and the line is still emitted.
	log.InfoContext(context.Background(), "background sweep")

	// Output:
	// {"level":"INFO","msg":"handled","request_id":"req-7"}
	// {"level":"INFO","msg":"background sweep"}
}
