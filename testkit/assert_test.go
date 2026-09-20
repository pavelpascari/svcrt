package testkit

import (
	"errors"
	"fmt"
	"testing"
)

// codedErr is a minimal contract.Coded. testkit's tests build their own rather
// than reaching for a sibling's fixture.
type codedErr struct {
	code   string
	params map[string]any
}

func (e codedErr) Error() string               { return "boom: " + e.code }
func (e codedErr) ErrorCode() string           { return e.code }
func (e codedErr) ErrorParams() map[string]any { return e.params }

// codedOnly satisfies Coded but NOT Detailed.
type codedOnly struct{ code string }

func (e codedOnly) Error() string     { return "boom" }
func (e codedOnly) ErrorCode() string { return e.code }

func TestAssertCodePassesOnAMatch(t *testing.T) {
	f := &fakeTB{}
	AssertCode(f, codedOnly{code: "order_not_found"}, "order_not_found")
	if f.failed {
		t.Fatalf("AssertCode failed on a matching code: %v", f.messages)
	}
	if f.helpers == 0 {
		t.Error("AssertCode did not call Helper, so failures report the caller's line wrongly")
	}
}

// TestAssertCodeFailsWhenItShould is the test that matters. An assertion that
// never fails is worse than no assertion, and only a fake TB can prove this.
func TestAssertCodeFailsWhenItShould(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"different code": {codedOnly{code: "id_too_long"}, "order_not_found"},
		"nil error":      {nil, "order_not_found"},
		"plain error":    {errors.New("not coded"), "order_not_found"},
		"empty want":     {codedOnly{code: "x"}, ""},

		// The two below pin the no-code branch INDEPENDENTLY of the
		// comparison that follows it. Without them, deleting the branch
		// entirely is invisible: Code returns "" when it finds no code, so
		// `got != want` fails on its own for every want above, with a
		// different message and the same verdict. It stops doing that the
		// moment want is "" -- and then AssertCode(t, nil, "") passes,
		// silently, against an error carrying no code at all. A mutation run
		// found exactly that (docs/mutation-survivors.md, R8).
		"no code, empty want":   {errors.New("not coded"), ""},
		"nil error, empty want": {nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeTB{}
			AssertCode(f, tc.err, tc.want)
			if !f.failed {
				t.Fatalf("AssertCode(%v, %q) did not fail", tc.err, tc.want)
			}
		})
	}
}

// TestAssertCodeUnwraps: a handler wrapping its error with %w must still be
// assertable, which is how errors actually reach a test.
func TestAssertCodeUnwraps(t *testing.T) {
	f := &fakeTB{}
	wrapped := fmt.Errorf("serving request: %w", codedOnly{code: "order_not_found"})
	AssertCode(f, wrapped, "order_not_found")
	if f.failed {
		t.Fatalf("AssertCode did not unwrap: %v", f.messages)
	}
}

func TestAssertParamsPassesOnAMatch(t *testing.T) {
	f := &fakeTB{}
	AssertParams(f, codedErr{code: "id_too_long", params: map[string]any{"max": 8}},
		map[string]any{"max": 8})
	if f.failed {
		t.Fatalf("AssertParams failed on matching params: %v", f.messages)
	}
	if f.helpers == 0 {
		t.Error("AssertParams did not call Helper, so failures report the caller's line wrongly")
	}
}

// TestAssertParamsIgnoresNumericWidth: an int 8 and an int64 8 are the same
// param. A JSON round trip turns one into the other, so a test asserting on an
// error that crossed the wire must not have to know which side it is on.
func TestAssertParamsIgnoresNumericWidth(t *testing.T) {
	f := &fakeTB{}
	AssertParams(f, codedErr{code: "id_too_long", params: map[string]any{"max": int64(8)}},
		map[string]any{"max": 8})
	if f.failed {
		t.Fatalf("AssertParams distinguished int from int64: %v", f.messages)
	}
}

func TestAssertParamsFailsWhenItShould(t *testing.T) {
	detailed := codedErr{code: "id_too_long", params: map[string]any{"max": 8}}
	cases := map[string]struct {
		err  error
		want map[string]any
	}{
		"missing key":     {detailed, map[string]any{"min": 1}},
		"different value": {detailed, map[string]any{"max": 9}},
		// want asks for a param the error does not carry.
		"extra key wanted": {detailed, map[string]any{"max": 8, "other": 1}},
		// The other direction, which is what "exactly" in the doc comment
		// claims: the error carries a param nobody asked for. An unexpected
		// param is a wire change, so it is a failure, not a detail.
		"extra key in error": {
			codedErr{code: "id_too_long", params: map[string]any{"max": 8, "sneaked": "in"}},
			map[string]any{"max": 8},
		},
		// The degenerate form of the same direction: want is empty, the error
		// still carries params.
		"empty want": {detailed, map[string]any{}},
		"nil want":   {detailed, nil},
		// want asks for params from an error that carries none at all.
		"nil params in error": {codedErr{code: "id_too_long"}, map[string]any{"max": 8}},
		"coded not detailed":  {codedOnly{code: "id_too_long"}, map[string]any{"max": 8}},
		"nil error":           {nil, map[string]any{"max": 8}},
		"plain error":         {errors.New("not coded"), map[string]any{"max": 8}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeTB{}
			AssertParams(f, tc.err, tc.want)
			if !f.failed {
				t.Fatalf("AssertParams(%v, %v) did not fail", tc.err, tc.want)
			}
		})
	}
}

// TestAssertParamsUnwraps: params must survive a %w wrap for the same reason
// codes must.
func TestAssertParamsUnwraps(t *testing.T) {
	f := &fakeTB{}
	wrapped := fmt.Errorf("serving request: %w",
		codedErr{code: "id_too_long", params: map[string]any{"max": 8}})
	AssertParams(f, wrapped, map[string]any{"max": 8})
	if f.failed {
		t.Fatalf("AssertParams did not unwrap: %v", f.messages)
	}
}

func TestCodeReportsPresence(t *testing.T) {
	if got, ok := Code(codedOnly{code: "x"}); !ok || got != "x" {
		t.Errorf("Code = %q, %v; want \"x\", true", got, ok)
	}
	if _, ok := Code(errors.New("plain")); ok {
		t.Error("Code reported a code for a plain error")
	}
	if _, ok := Code(nil); ok {
		t.Error("Code reported a code for a nil error")
	}
}
