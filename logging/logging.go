// Package logging provides a slog.Handler that enriches records from their
// context, plus the attribute-key conventions svcrt services share.
//
// It is not a logging framework and not a wrapper: the logger it returns is a
// plain *slog.Logger, and everything it produces is ordinary slog output.
package logging

import (
	"io"
	"log/slog"
)

// Well-known attribute keys.
//
// Log output is consumed by a node agent with a schema contract, so these
// names are part of the interface rather than free-form text (12-factor XI,
// amended).
const (
	KeyMethod = "method"
	KeyRoute  = "route"
	KeyStatus = "status"
	KeyDurMS  = "duration_ms"
)

// Options configures the logger returned by New.
type Options struct {
	// Level is the minimum level to emit. Nil means slog.LevelInfo.
	//
	// It is a Leveler rather than a Level so a service that wants runtime
	// level changes can pass a *slog.LevelVar. Nothing here wires one up.
	Level slog.Leveler

	// AddSource records the source position of the log call.
	AddSource bool

	// ReplaceAttr is passed through to the underlying JSON handler.
	ReplaceAttr func(groups []string, a slog.Attr) slog.Attr
}

// New returns a logger that writes JSON to w and enriches every record using
// ex.
//
// Write to os.Stdout in production: the process emits an event stream and
// takes no position on where it is stored (12-factor XI).
func New(w io.Writer, opts Options, ex ...Extractor) *slog.Logger {
	level := opts.Level
	if level == nil {
		level = slog.LevelInfo
	}
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		AddSource:   opts.AddSource,
		ReplaceAttr: opts.ReplaceAttr,
	})
	return slog.New(NewHandler(base, ex...))
}
