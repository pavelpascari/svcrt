package logging

import (
	"context"
	"log/slog"
	"slices"
)

// Extractor derives attributes from a request context.
//
// It exists so correlation works without logging depending on a tracing
// library: svcrt/telemetry supplies an Extractor that reads span context, and
// this package never learns what a span is.
//
// An Extractor runs on every record, so it must be cheap and must not block.
// Returning nil is fine and contributes nothing.
//
// It must also not panic. Nothing here recovers: a panicking Extractor
// propagates out of the log call and into whatever was being logged, which
// for the HTTP middleware means the request. An Extractor that reads a value
// of uncertain shape out of a context should type-assert with the two-result
// form and return nil, not assume a sandbox that does not exist.
type Extractor func(context.Context) []slog.Attr

// op is a deferred WithAttrs or WithGroup call.
type op struct {
	group string
	attrs []slog.Attr
}

func (o op) apply(h slog.Handler) slog.Handler {
	if o.group != "" {
		return h.WithGroup(o.group)
	}
	return h.WithAttrs(o.attrs)
}

type handler struct {
	next slog.Handler
	ex   []Extractor
	ops  []op
}

// NewHandler wraps next so that every record is enriched with the attributes
// ex derives from its context.
//
// Extractor attributes are always emitted at the top level, even when the
// logger has been grouped. Correlation keys nested inside a group are
// invisible to log pipelines that look for them by name.
func NewHandler(next slog.Handler, ex ...Extractor) slog.Handler {
	return &handler{next: next, ex: ex}
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

// WithAttrs records the call instead of applying it, so Handle can place
// extractor attributes beneath it. slices.Clip prevents a shared backing array
// from letting one derived handler overwrite another's ops.
//
// With no extractors registered there is nothing to place, so the call is
// delegated eagerly instead. That matters: recording an op moves every later
// record onto Handle's ops-replay slow path permanently, which costs a
// WithAttrs clone per record. `log := base.With("service", "orders")` is the
// first thing most services do, and it should not buy an ongoing cost to
// solve a problem the service does not have. Delegating keeps the
// no-extractor logger at plain-slog cost -- zero allocations per record.
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	if len(h.ex) == 0 {
		return &handler{next: h.next.WithAttrs(attrs)}
	}
	return &handler{
		next: h.next,
		ex:   h.ex,
		ops:  append(slices.Clip(h.ops), op{attrs: attrs}),
	}
}

// WithGroup defers for the same reason WithAttrs does, and delegates eagerly
// under the same condition. See WithAttrs.
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	if len(h.ex) == 0 {
		return &handler{next: h.next.WithGroup(name)}
	}
	return &handler{
		next: h.next,
		ex:   h.ex,
		ops:  append(slices.Clip(h.ops), op{group: name}),
	}
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	// Fast path: nothing was grouped or pre-attached, so extractor attributes
	// are already top level and can go straight onto the record.
	if len(h.ops) == 0 {
		if len(h.ex) == 0 {
			return h.next.Handle(ctx, r)
		}
		r = r.Clone() // required: a handler must not mutate a record it did not create
		for _, e := range h.ex {
			if attrs := e(ctx); len(attrs) > 0 {
				r.AddAttrs(attrs...)
			}
		}
		return h.next.Handle(ctx, r)
	}

	// Slow path: apply extractor attributes to the ungrouped root handler,
	// then replay the recorded WithAttrs/WithGroup calls on top.
	n := h.next
	for _, e := range h.ex {
		if attrs := e(ctx); len(attrs) > 0 {
			n = n.WithAttrs(attrs)
		}
	}
	for _, o := range h.ops {
		n = o.apply(n)
	}
	return n.Handle(ctx, r)
}
