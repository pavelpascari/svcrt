// Package testkit provides test helpers for services built on svcrt: log
// capture, a scripted stub upstream, and assertions on contract errors.
//
// It is for CONSUMERS of svcrt. svcrt's own core modules cannot use it: a
// test-only dependency still lands in go.mod, and those modules are gated at
// zero requires so that adopting one costs nothing. A core module that
// imported testkit for its tests would fail that gate.
//
// Nothing here imports testing. TB below is satisfied by *testing.T, and
// importing testing from a non-test package would register test flags in any
// consumer that referenced this one.
package testkit

// TB is the subset of *testing.T these helpers need.
//
// It is not testing.TB. That interface carries an unexported private() method
// specifically to prevent outside implementations, and this package's own
// tests must be able to fake it -- an assertion that cannot be shown to fail
// is worse than no assertion at all.
//
// *testing.T and *testing.B both satisfy this.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}
