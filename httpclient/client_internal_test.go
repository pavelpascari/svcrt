package httpclient

import (
	"testing"
	"time"
)

// DialTimeout is a public option with a safety rationale (Deviation 1) whose
// value is only observable by dialing, so no black-box test could read it
// back. The R2 review confirmed the gap by running the mutants: zeroing
// net.Dialer.Timeout, and swapping Timeout with KeepAlive, both survived the
// full suite. newDialer exists so this test can exist -- the same reason
// httpserver has HTTPServerForTest and lifecycle has its internal tests.
//
// Every value here is DISTINCT for the same reason
// TestEveryOptionLandsOnItsOwnDestination uses distinct values: with
// Timeout and KeepAlive equal, a test would pass with the two wired to each
// other's destination, which is precisely one of the two confirmed mutants.
func TestDialerCarriesTheDialTimeoutAndKeepAlive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		opts          Options
		wantTimeout   time.Duration
		wantKeepAlive time.Duration
	}{
		{
			name:          "explicit DialTimeout",
			opts:          Options{DialTimeout: 7 * time.Second},
			wantTimeout:   7 * time.Second,
			wantKeepAlive: 30 * time.Second,
		},
		{
			name:          "zero DialTimeout takes Deviation 1's default",
			opts:          Options{},
			wantTimeout:   5 * time.Second,
			wantKeepAlive: 30 * time.Second,
		},
		{
			// A negative Timeout means "no deadline at all" to net.Dialer --
			// the exact opposite of Deviation 1's fail-fast intent -- so it
			// must take the default exactly as zero does.
			name:          "negative DialTimeout takes Deviation 1's default",
			opts:          Options{DialTimeout: -1 * time.Second},
			wantTimeout:   5 * time.Second,
			wantKeepAlive: 30 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDialer(tc.opts)
			if got := d.Timeout; got != tc.wantTimeout {
				t.Errorf("Dialer.Timeout = %v, want %v", got, tc.wantTimeout)
			}
			if got := d.KeepAlive; got != tc.wantKeepAlive {
				t.Errorf("Dialer.KeepAlive = %v, want %v", got, tc.wantKeepAlive)
			}
		})
	}
}
