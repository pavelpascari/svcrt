package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/slogtest"
	"time"

	"github.com/pavelpascari/svcrt/logging"
)

type ctxKey struct{}

// traceExtractor stands in for the one svcrt/telemetry will ship at R3.
func traceExtractor() logging.Extractor {
	return func(ctx context.Context) []slog.Attr {
		id, ok := ctx.Value(ctxKey{}).(string)
		if !ok {
			return nil
		}
		return []slog.Attr{slog.String(logging.KeyTraceID, id)}
	}
}

// capture returns a logger writing JSON to buf, plus a decoder for the lines.
func capture(t *testing.T, ex ...logging.Extractor) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelDebug}, ex...)
	return log, func() []map[string]any {
		var out []map[string]any
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		for dec.More() {
			m := map[string]any{}
			if err := dec.Decode(&m); err != nil {
				t.Fatalf("decode log line: %v", err)
			}
			out = append(out, m)
		}
		return out
	}
}

func TestNewWritesJSON(t *testing.T) {
	t.Parallel()

	log, lines := capture(t)
	log.Info("hello", "n", 1)

	got := lines()
	if len(got) != 1 {
		t.Fatalf("%d lines, want 1", len(got))
	}
	if got[0]["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", got[0]["msg"])
	}
	if got[0]["n"] != float64(1) {
		t.Errorf("n = %v, want 1", got[0]["n"])
	}
}

func TestExtractorAddsAttrsFromContext(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	log.InfoContext(ctx, "hello")

	got := lines()
	if got[0][logging.KeyTraceID] != "abc123" {
		t.Errorf("%s = %v, want abc123", logging.KeyTraceID, got[0][logging.KeyTraceID])
	}
}

func TestExtractorContributesNothingWhenContextIsBare(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	log.Info("hello")

	if _, ok := lines()[0][logging.KeyTraceID]; ok {
		t.Errorf("%s present with no value in context", logging.KeyTraceID)
	}
}

func TestExtractorRunsPerRecordNotOnce(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	log.InfoContext(context.WithValue(context.Background(), ctxKey{}, "one"), "a")
	log.InfoContext(context.WithValue(context.Background(), ctxKey{}, "two"), "b")

	got := lines()
	if got[0][logging.KeyTraceID] != "one" || got[1][logging.KeyTraceID] != "two" {
		t.Errorf("trace ids = %v, %v; want one, two",
			got[0][logging.KeyTraceID], got[1][logging.KeyTraceID])
	}
}

func TestLevelIsRespected(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelWarn})
	log.Info("suppressed")

	if buf.Len() != 0 {
		t.Errorf("info line emitted at warn level: %s", buf.String())
	}
}

func TestDefaultLevelIsInfo(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{})
	log.Debug("suppressed")
	if buf.Len() != 0 {
		t.Errorf("debug line emitted at the default level: %s", buf.String())
	}
	log.Info("emitted")
	if buf.Len() == 0 {
		t.Error("info line suppressed at the default level")
	}
}

func TestLevelVarAllowsRuntimeFlipping(t *testing.T) {
	t.Parallel()

	var lvl slog.LevelVar
	lvl.Set(slog.LevelWarn)

	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: &lvl})

	log.Info("suppressed")
	if buf.Len() != 0 {
		t.Fatal("info emitted while level was warn")
	}

	lvl.Set(slog.LevelInfo)
	log.Info("emitted")
	if buf.Len() == 0 {
		t.Error("info suppressed after lowering the level")
	}
}

func TestWithAttrsDoesNotMutateReceiver(t *testing.T) {
	t.Parallel()

	// With an extractor, so this exercises the recorded-ops path rather than
	// slog's own WithAttrs.
	log, lines := capture(t, traceExtractor())
	base := log.With("a", 1)
	_ = base.With("b", 2)

	base.Info("only-a")

	got := lines()
	if _, ok := got[0]["b"]; ok {
		t.Error("attr from a derived logger leaked into its parent")
	}
	if got[0]["a"] != float64(1) {
		t.Errorf("a = %v, want 1", got[0]["a"])
	}
}

func TestWithGroupEmptyNameIsNoop(t *testing.T) {
	t.Parallel()

	h := logging.NewHandler(slog.NewJSONHandler(io.Discard, nil))
	if got := h.WithGroup(""); got != h {
		t.Error("WithGroup(\"\") returned a different handler than the receiver")
	}
}

func TestWithAttrsEmptyIsNoop(t *testing.T) {
	t.Parallel()

	h := logging.NewHandler(slog.NewJSONHandler(io.Discard, nil))
	if got := h.WithAttrs(nil); got != h {
		t.Error("WithAttrs(nil) returned a different handler than the receiver")
	}
}

// TestExtractorAttrsStayTopLevelAboveGroup is the minimal proof of this
// package's central design point: a group must not swallow correlation
// attrs. Task 10 adds the exhaustive slogtest conformance and
// group-placement suite on top of this.
func TestExtractorAttrsStayTopLevelAboveGroup(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	grouped := log.WithGroup("req")
	grouped.InfoContext(ctx, "hello")

	got := lines()[0]
	if got[logging.KeyTraceID] != "abc123" {
		t.Errorf("%s = %v, want abc123 at top level", logging.KeyTraceID, got[logging.KeyTraceID])
	}
	if req, ok := got["req"].(map[string]any); ok {
		if _, nested := req[logging.KeyTraceID]; nested {
			t.Errorf("%s leaked into the req group: %v", logging.KeyTraceID, req)
		}
	}
}

// buildRecordWithSpareBackCapacity returns a Record whose internal back
// slice (the overflow storage slog.Record uses once more than 5 attrs have
// been added) has spare capacity. slog.Record.AddAttrs self-checks for
// exactly this: if it detects that a slot past its own length is already
// occupied, it means some other copy of the Record wrote there without
// cloning first, and it appends a "!BUG" attr saying so. That self-check is
// used below as a deterministic oracle for whether Handle cloned the record
// before mutating it, instead of relying on go-mutesting's own detection or
// on timing-sensitive data races.
func buildRecordWithSpareBackCapacity() slog.Record {
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	for i := 0; i < 5; i++ {
		r.AddAttrs(slog.Int(fmt.Sprintf("f%d", i), i)) // fills the front array
	}
	for i := 0; i < 3; i++ {
		r.AddAttrs(slog.Int(fmt.Sprintf("k%d", i), i)) // grows back with slack
	}
	return r
}

func dumpRecord(r slog.Record) string {
	var buf bytes.Buffer
	_ = slog.NewJSONHandler(&buf, nil).Handle(context.Background(), r)
	return buf.String()
}

func TestHandleClonesRecordBeforeMutatingIt(t *testing.T) {
	t.Parallel()

	base := buildRecordWithSpareBackCapacity()
	sibling := base // shares base's backing array while it has spare capacity

	var buf bytes.Buffer
	h := logging.NewHandler(slog.NewJSONHandler(&buf, nil), traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	if err := h.Handle(ctx, base); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// If Handle mutated base's shared array instead of a private clone,
	// sibling's own next AddAttrs call will trip slog's own guard and emit
	// a "!BUG" attr -- proof the record was not cloned first.
	sibling.AddAttrs(slog.String("marker", "m"))
	got := dumpRecord(sibling)
	if strings.Contains(got, "!BUG") {
		t.Errorf("Handle mutated a record it did not own instead of cloning it: %s", got)
	}
}

// TestWithGroupActuallyNestsSubsequentAttrs proves WithGroup's effect is
// real: an attribute attached after the group must be nested under it in
// the JSON output. TestExtractorAttrsStayTopLevelAboveGroup above only
// proves the opposite (extractor attrs are NOT nested); this test is needed
// so a broken WithGroup that silently drops grouping cannot pass by having
// nothing groupable to observe.
func TestWithGroupActuallyNestsSubsequentAttrs(t *testing.T) {
	t.Parallel()

	log, lines := capture(t)
	log.WithGroup("req").With("id", 5).Info("hello")

	got := lines()[0]
	req, ok := got["req"].(map[string]any)
	if !ok {
		t.Fatalf(`"req" group missing or wrong type in %v`, got)
	}
	if req["id"] != float64(5) {
		t.Errorf("req.id = %v, want 5", req["id"])
	}
}

// TestFastPathAddsExtractorAttrsAfterRecordOwnAttrs distinguishes the fast
// path (Handle adds extractor attrs to the record itself, so they land
// after the call's own args in JSON output) from the slow path (which would
// instead pre-attach them via the next handler's WithAttrs, landing them
// before). Decoding into a map can't see this -- map key order isn't
// preserved -- so this checks the raw encoded line.
func TestFastPathAddsExtractorAttrsAfterRecordOwnAttrs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Level: slog.LevelDebug}, traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	log.InfoContext(ctx, "hello", "x", 1)

	line := buf.String()
	xi := strings.Index(line, `"x"`)
	ti := strings.Index(line, `"`+logging.KeyTraceID+`"`)
	if xi < 0 || ti < 0 {
		t.Fatalf(`expected both "x" and %q in output: %s`, logging.KeyTraceID, line)
	}
	if ti < xi {
		t.Errorf("%s appeared before the call's own attrs on the fast path (no groups, no With); got: %s",
			logging.KeyTraceID, line)
	}
}

// spyHandler wraps a real handler and counts WithAttrs calls, so a test can
// tell whether the wrapper called next.WithAttrs at all -- something a
// decoded JSON line can't reveal, since the stdlib handler already treats a
// zero-length WithAttrs call as a no-op and returns unchanged.
type spyHandler struct {
	next           slog.Handler
	withAttrsCalls *int
}

func (s *spyHandler) Enabled(ctx context.Context, l slog.Level) bool { return s.next.Enabled(ctx, l) }
func (s *spyHandler) Handle(ctx context.Context, r slog.Record) error {
	return s.next.Handle(ctx, r)
}
func (s *spyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	*s.withAttrsCalls++
	return &spyHandler{next: s.next.WithAttrs(attrs), withAttrsCalls: s.withAttrsCalls}
}
func (s *spyHandler) WithGroup(name string) slog.Handler {
	return &spyHandler{next: s.next.WithGroup(name), withAttrsCalls: s.withAttrsCalls}
}

// TestSlowPathSkipsWithAttrsWhenExtractorContributesNothing exercises the
// slow path (reached once a group or With has been recorded) and checks
// that an extractor contributing nothing never causes a call to
// next.WithAttrs -- not just that the eventual JSON output is unaffected.
func TestSlowPathSkipsWithAttrsWhenExtractorContributesNothing(t *testing.T) {
	t.Parallel()

	calls := 0
	spy := &spyHandler{next: slog.NewJSONHandler(io.Discard, nil), withAttrsCalls: &calls}

	h := logging.NewHandler(spy, traceExtractor())
	grouped := h.WithGroup("req") // forces the slow path: ops is now non-empty

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	if err := grouped.Handle(context.Background(), r); err != nil { // bare context: extractor contributes nothing
		t.Fatalf("Handle: %v", err)
	}

	if calls != 0 {
		t.Errorf("next.WithAttrs called %d times for an extractor that contributed nothing", calls)
	}
}

// TestWithAttrsClipPreventsSiblingOpsCorruption proves slices.Clip in
// WithAttrs is load-bearing, not defensive dead weight. Chaining WithAttrs
// grows h.ops's backing array past exact-fit capacity (Go's own append
// growth leaves slack well before 18 single-op appends -- empirically
// confirmed: len 3 already has cap 4). Once a handler's ops slice has that
// slack, branching two children off it with append(h.ops, ...) (no Clip)
// makes both children's appends target the SAME index in the shared array;
// whichever branch is taken second silently overwrites the first branch's
// op. Clip forces each branch to allocate its own array instead.
func TestWithAttrsClipPreventsSiblingOpsCorruption(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// An extractor is required for this test to mean anything: with none
	// registered, WithAttrs delegates eagerly and no ops slice is built, so
	// there is no shared backing array to corrupt. traceExtractor contributes
	// nothing to a bare context, so the expected output is unchanged.
	mid := logging.NewHandler(slog.NewJSONHandler(&buf, nil), traceExtractor())
	for i := 0; i < 18; i++ {
		mid = mid.WithAttrs([]slog.Attr{slog.Int(fmt.Sprintf("p%d", i), i)})
	}

	// Two siblings derived from the SAME parent, with different attrs.
	// Both derivations happen before either is used, so a shared,
	// unclipped backing array would let the second overwrite the first.
	left := mid.WithAttrs([]slog.Attr{slog.String("branch", "left")})
	right := mid.WithAttrs([]slog.Attr{slog.String("branch", "right")})

	if err := left.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)); err != nil {
		t.Fatalf("left.Handle: %v", err)
	}
	if err := right.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)); err != nil {
		t.Fatalf("right.Handle: %v", err)
	}

	dec := json.NewDecoder(&buf)
	var lineLeft, lineRight map[string]any
	if err := dec.Decode(&lineLeft); err != nil {
		t.Fatalf("decode left line: %v", err)
	}
	if err := dec.Decode(&lineRight); err != nil {
		t.Fatalf("decode right line: %v", err)
	}

	if lineLeft["branch"] != "left" {
		t.Errorf("left branch = %v, want left (sibling ops slice was overwritten)", lineLeft["branch"])
	}
	if lineRight["branch"] != "right" {
		t.Errorf("right branch = %v, want right", lineRight["branch"])
	}
}

// TestWithGroupClipPreventsSiblingOpsCorruption is
// TestWithAttrsClipPreventsSiblingOpsCorruption's counterpart for
// WithGroup, which appends to the same h.ops slice and needs the same
// slices.Clip protection.
func TestWithGroupClipPreventsSiblingOpsCorruption(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// See the WithAttrs counterpart: without an extractor there is no ops
	// slice to share, so the extractor is what keeps this test load-bearing.
	mid := logging.NewHandler(slog.NewJSONHandler(&buf, nil), traceExtractor())
	for i := 0; i < 18; i++ {
		mid = mid.WithGroup(fmt.Sprintf("g%d", i))
	}

	// Deliberately do NOT chain a further WithAttrs/WithGroup call onto
	// left/right: that call's own (correctly-clipped) append would copy
	// the vulnerable op out of the shared array before the sibling gets
	// a chance to corrupt it, masking exactly the bug this test targets.
	// Instead, give each its own attr via the Record itself at Handle
	// time -- a group nests a record's own attrs the same way it nests
	// WithAttrs-attached ones.
	left := mid.WithGroup("left")
	right := mid.WithGroup("right")

	recLeft := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	recLeft.AddAttrs(slog.String("k", "v"))
	if err := left.Handle(context.Background(), recLeft); err != nil {
		t.Fatalf("left.Handle: %v", err)
	}

	recRight := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	recRight.AddAttrs(slog.String("k", "v"))
	if err := right.Handle(context.Background(), recRight); err != nil {
		t.Fatalf("right.Handle: %v", err)
	}

	// left/right nest 18 levels deep (WithGroup nests each subsequent
	// group inside the last), so check by substring rather than by
	// decoding into a shallow map -- what matters here is which group
	// name shows up in which line, not its exact nesting depth.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	lineLeft, lineRight := lines[0], lines[1]

	if !strings.Contains(lineLeft, `"left":`) || strings.Contains(lineLeft, `"right":`) {
		t.Errorf(`left line missing its own "left" group or contains "right" (sibling ops slice was overwritten): %s`, lineLeft)
	}
	if !strings.Contains(lineRight, `"right":`) || strings.Contains(lineRight, `"left":`) {
		t.Errorf(`right line missing its own "right" group or contains "left": %s`, lineRight)
	}
}

// --- group correctness and stdlib conformance ---

func TestExtractorAttrsStayTopLevelUnderWithGroup(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	log.WithGroup("req").InfoContext(ctx, "hello", "path", "/x")

	got := lines()[0]

	// The correlation key must be findable by name at the top level.
	if got[logging.KeyTraceID] != "abc123" {
		t.Errorf("top-level %s = %v, want abc123", logging.KeyTraceID, got[logging.KeyTraceID])
	}
	group, ok := got["req"].(map[string]any)
	if !ok {
		t.Fatalf("req = %#v, want a group object", got["req"])
	}
	if _, nested := group[logging.KeyTraceID]; nested {
		t.Errorf("%s was nested inside the group: %#v", logging.KeyTraceID, group)
	}
	if group["path"] != "/x" {
		t.Errorf("req.path = %v, want /x", group["path"])
	}
}

func TestExtractorAttrsStayTopLevelUnderNestedGroups(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	log.WithGroup("a").WithGroup("b").InfoContext(ctx, "hello", "k", "v")

	got := lines()[0]
	if got[logging.KeyTraceID] != "abc123" {
		t.Errorf("top-level %s missing: %#v", logging.KeyTraceID, got)
	}
	a, ok := got["a"].(map[string]any)
	if !ok {
		t.Fatalf("a = %#v, want a group", got["a"])
	}
	b, ok := a["b"].(map[string]any)
	if !ok {
		t.Fatalf("a.b = %#v, want a group", a["b"])
	}
	if b["k"] != "v" {
		t.Errorf("a.b.k = %v, want v", b["k"])
	}
	if _, nested := a[logging.KeyTraceID]; nested {
		t.Errorf("%s leaked into the a group: %#v", logging.KeyTraceID, a)
	}
	if _, nested := b[logging.KeyTraceID]; nested {
		t.Errorf("%s leaked into the a.b group: %#v", logging.KeyTraceID, b)
	}
}

func TestWithAttrsBeforeGroupStillPlacesExtractorAtTopLevel(t *testing.T) {
	t.Parallel()

	log, lines := capture(t, traceExtractor())
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")

	log.With("service", "orders").WithGroup("req").InfoContext(ctx, "hello", "path", "/x")

	got := lines()[0]
	if got[logging.KeyTraceID] != "abc123" {
		t.Errorf("top-level %s missing: %#v", logging.KeyTraceID, got)
	}
	if got["service"] != "orders" {
		t.Errorf("service = %v, want orders", got["service"])
	}
	req, ok := got["req"].(map[string]any)
	if !ok {
		t.Fatalf("req = %#v, want a group", got["req"])
	}
	if _, nested := req[logging.KeyTraceID]; nested {
		t.Errorf("%s leaked into the req group: %#v", logging.KeyTraceID, req)
	}
}

// TestHandler is the stdlib's own conformance suite for slog.Handler. It is
// the reason hand-writing a Handler is acceptable here at all.
func TestHandlerSatisfiesSlogtest(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	h := logging.NewHandler(slog.NewJSONHandler(&buf, nil))

	results := func() []map[string]any {
		var out []map[string]any
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		for dec.More() {
			m := map[string]any{}
			if err := dec.Decode(&m); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out = append(out, m)
		}
		return out
	}

	if err := slogtest.TestHandler(h, results); err != nil {
		t.Errorf("slogtest.TestHandler: %v", err)
	}
}

// The same suite with extractors attached, so the slow path is covered too.
func TestHandlerWithExtractorsSatisfiesSlogtest(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	h := logging.NewHandler(slog.NewJSONHandler(&buf, nil), traceExtractor())

	results := func() []map[string]any {
		var out []map[string]any
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		for dec.More() {
			m := map[string]any{}
			if err := dec.Decode(&m); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out = append(out, m)
		}
		return out
	}

	if err := slogtest.TestHandler(h, results); err != nil {
		t.Errorf("slogtest.TestHandler with extractors: %v", err)
	}
}

// --- allocation behaviour ---
//
// `log := base.With("service", "orders")` is the first thing most services do,
// and it used to move every subsequent record onto the ops-replay slow path
// permanently -- paying a WithAttrs clone per record to solve an
// extractor-placement problem in a logger that has no extractors. These
// benchmarks pin the fix: with no extractors registered, svcrt must cost
// exactly what plain slog costs, whether or not .With() was called.

func benchRecord() slog.Record {
	return slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
}

func BenchmarkPlainSlogWithAttrs(b *testing.B) {
	h := slog.Handler(slog.NewJSONHandler(io.Discard, nil)).
		WithAttrs([]slog.Attr{slog.String("service", "orders")})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, benchRecord())
	}
}

func BenchmarkNoExtractorsWithoutWith(b *testing.B) {
	h := logging.NewHandler(slog.NewJSONHandler(io.Discard, nil))
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, benchRecord())
	}
}

// The regression guard. This must report 0 allocs/op.
func BenchmarkNoExtractorsWithWith(b *testing.B) {
	h := logging.NewHandler(slog.NewJSONHandler(io.Discard, nil)).
		WithAttrs([]slog.Attr{slog.String("service", "orders")})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, benchRecord())
	}
}

func BenchmarkWithExtractorAndWith(b *testing.B) {
	h := logging.NewHandler(slog.NewJSONHandler(io.Discard, nil), traceExtractor()).
		WithAttrs([]slog.Attr{slog.String("service", "orders")})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = h.Handle(ctx, benchRecord())
	}
}

// The benchmarks above report the cost; this asserts it, so a revert is
// caught by `go test` rather than by someone remembering to read a
// benchmark. It compares against plain slog rather than against a literal 0
// because the race detector allocates on its own -- the claim is "svcrt costs
// nothing extra", and that is what is measured.
func TestNoExtractorWithAttrsCostsNothingExtraPerRecord(t *testing.T) {
	ctx := context.Background()
	measure := func(h slog.Handler) float64 {
		return testing.AllocsPerRun(100, func() {
			_ = h.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0))
		})
	}

	attrs := []slog.Attr{slog.String("service", "orders")}
	plain := measure(slog.Handler(slog.NewJSONHandler(io.Discard, nil)).WithAttrs(attrs))
	ours := measure(logging.NewHandler(slog.NewJSONHandler(io.Discard, nil)).WithAttrs(attrs))

	if ours > plain {
		t.Errorf("with no extractors, .With() then Handle allocates %.0f/op against plain slog's %.0f/op; "+
			"WithAttrs is recording an op again instead of delegating", ours, plain)
	}
}
