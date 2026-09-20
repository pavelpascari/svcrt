package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Attribute keys emitted by Recover.
//
// They live here rather than in a logging package because Recover is what
// emits them -- a module that does not produce an attribute should not be
// naming it.
const (
	KeyPanic = "panic"
	KeyStack = "stack"
)

// Recover turns a panicking handler into a 500 and a logged stack trace.
//
// Nothing else in svcrt recovers: AccessLog and svcrt/telemetry both say a
// panic becoming a 500 is the service author's decision, made in their own
// middleware. This is that middleware, shipped rather than rewritten by every
// author -- and still opt-in, because nothing composes it for you.
//
// # Compose it INNERMOST
//
//	Chain(telemetry.Server(...), AccessLog(log), Recover(log))(mux)
//
// This is the opposite of the usual instinct, and the reason is mechanical. A
// panic unwinds deferred calls in inner frames first. With Recover outside
// AccessLog, the access-log line is emitted BEFORE the 500 is written, so it
// records no status (it omits rather than invents one), and a telemetry span
// outside it records none either -- the client receives a 500 that your own
// observability never saw. Innermost, every middleware outside observes the
// real response.
//
// The trade is that a panic inside AccessLog or telemetry.Server is not
// caught. Those are svcrt's code and are tested; the handler is not.
//
// # What it does not do
//
// http.ErrAbortHandler is re-panicked unchanged. net/http documents it as the
// way to abort a response without logging, and swallowing it converts a
// deliberate silent abort into a spurious 500 and a spurious error line.
//
// The panic value never reaches the client: the response is a bare 500 with no
// body, and the value and stack go to the log. A panic message is internal
// state that routinely carries paths, query fragments and occasionally
// credentials.
func Recover(l *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				v := recover()
				if v == nil {
					return
				}
				// net/http's own sentinel: the handler chose to abort. Let it
				// through untouched rather than reporting a server error
				// nobody had.
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}

				l.LogAttrs(r.Context(), slog.LevelError, "panic serving request",
					slog.Any(KeyPanic, v),
					slog.String(KeyStack, string(debug.Stack())),
				)

				// Only if the handler had not already responded: writing a
				// second header is a no-op net/http warns about, and the
				// status the client actually received is the first one.
				if !sw.written {
					sw.WriteHeader(http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(sw, r)
		})
	}
}
