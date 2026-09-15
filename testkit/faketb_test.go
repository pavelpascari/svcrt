package testkit

import "fmt"

// fakeTB records failures instead of reporting them, so a test can assert that
// an assertion FAILED.
//
// It implements testkit.TB rather than testing.TB, which is the reason TB
// exists at all: testing.TB carries an unexported private() method precisely
// to prevent outside implementations, so a fake of it cannot be written.
type fakeTB struct {
	helpers  int
	failed   bool
	fatal    bool
	messages []string
	cleanups []func()
}

func (f *fakeTB) Helper() { f.helpers++ }

func (f *fakeTB) Errorf(format string, args ...any) {
	f.failed = true
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

// Fatalf records and returns. The real Fatalf does not return, so a helper
// written to rely on that will misbehave here -- which is itself worth
// knowing, and is why assertions in this package return immediately after
// calling Fatalf rather than falling through.
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = true
	f.fatal = true
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }

func (f *fakeTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
	f.cleanups = nil
}
