package lifecycle_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/lifecycle"
)

// TestRunLogsStartingAndStoppingComponents pins the two per-component log
// lines directly, by name. Nothing about Run's return value or the recorder's
// op order depends on these lines existing -- they could be deleted outright
// and every other test in this package would still pass -- so a regression
// that silently drops one of them needs a test that reads the log output
// itself.
func TestRunLogsStartingAndStoppingComponents(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lc := lifecycle.New(lifecycle.Config{Logger: log})
	lc.Add("a",
		func(context.Context) error { return nil },
		func(context.Context) error { return nil })

	if err := runWithTimeout(t, lc, nil); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "starting component") {
		t.Errorf("log missing \"starting component\": %s", out)
	}
	if !strings.Contains(out, "stopping component") {
		t.Errorf("log missing \"stopping component\": %s", out)
	}
}
