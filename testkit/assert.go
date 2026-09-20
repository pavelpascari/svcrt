package testkit

import (
	"errors"
	"fmt"
	"maps"

	"github.com/pavelpascari/svcrt/contract"
)

// Code returns the error's contract code, unwrapping as it goes.
//
// The non-asserting accessor, for a test that wants to branch on the code
// rather than fail on it.
func Code(err error) (string, bool) {
	var coded contract.Coded
	if !errors.As(err, &coded) {
		return "", false
	}
	return coded.ErrorCode(), true
}

// AssertCode fails unless err carries want as its contract code.
//
// This exists because svcrt's oldest cross-cutting rule -- the wire carries
// codes and language-neutral scalars, never server-authored prose -- is
// otherwise enforced by nothing but review. Asserting on the code rather than
// on a message is the behaviour this makes easiest.
func AssertCode(tb TB, err error, want string) {
	tb.Helper()

	got, ok := Code(err)
	if !ok {
		tb.Fatalf("error does not carry a contract code: %v", err)
		return
	}
	if got != want {
		tb.Fatalf("error code = %q, want %q", got, want)
	}
}

// AssertParams fails unless err carries exactly want as its contract params.
//
// Exactly: a param present in the error but absent from want is a failure too.
// Error params are the data a client renders its own message from, so an
// unexpected one is a wire change, not a detail.
func AssertParams(tb TB, err error, want map[string]any) {
	tb.Helper()

	var detailed contract.Detailed
	if !errors.As(err, &detailed) {
		tb.Fatalf("error does not carry contract params: %v", err)
		return
	}
	got := detailed.ErrorParams()
	if !maps.Equal(toComparable(got), toComparable(want)) {
		tb.Fatalf("error params = %v, want %v", got, want)
	}
}

// toComparable renders values as strings so maps.Equal can compare a
// map[string]any whose values are not themselves comparable, and so an int 8
// and an int64 8 -- which a JSON round trip turns into each other -- do not
// read as different params.
func toComparable(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}
