package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/httpserver"
)

func probe(t *testing.T, h http.Handler) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	return rec.Code, rec.Body.String()
}

func TestGatesStartClosed(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	for name, g := range map[string]*httpserver.Gate{
		"Started": h.Started, "Live": h.Live, "Ready": h.Ready,
	} {
		if code, _ := probe(t, g); code != http.StatusServiceUnavailable {
			t.Errorf("%s before Set(true) = %d, want 503", name, code)
		}
	}
}

func TestGateSetTrueReturns200(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Live.Set(true)
	if code, _ := probe(t, h.Live); code != http.StatusOK {
		t.Errorf("Live = %d, want 200", code)
	}
}

func TestDrainFlipsReadyOnlyAndLeavesLiveUp(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Started.Set(true)
	h.Live.Set(true)
	h.Ready.Set(true)

	h.Drain()

	if code, _ := probe(t, h.Ready); code != http.StatusServiceUnavailable {
		t.Errorf("Ready after Drain = %d, want 503", code)
	}
	// A liveness failure during drain invites a restart mid-shutdown.
	if code, _ := probe(t, h.Live); code != http.StatusOK {
		t.Errorf("Live after Drain = %d, want 200", code)
	}
	if code, _ := probe(t, h.Started); code != http.StatusOK {
		t.Errorf("Started after Drain = %d, want 200", code)
	}
}

func TestReadyCheckFailureReturns503WithTheCheckName(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Ready.Set(true)
	h.AddReadyCheck("db", func(context.Context) error { return errors.New("dial tcp 10.0.0.5: refused") })

	code, body := probe(t, h.Ready)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}

	var got struct {
		Failed []string `json:"failed"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	if !slices.Equal(got.Failed, []string{"db"}) {
		t.Errorf("failed = %v, want [db]", got.Failed)
	}
	// Codes, not prose: the cause never reaches the wire.
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "refused") {
		t.Errorf("body leaked the check's error text: %s", body)
	}
}

func TestOnCheckErrorReceivesTheNameAndError(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Ready.Set(true)
	wantErr := errors.New("dial tcp 10.0.0.5: refused")
	h.AddReadyCheck("db", func(context.Context) error { return wantErr })

	var gotName string
	var gotErr error
	h.Ready.OnCheckError = func(name string, err error) {
		gotName = name
		gotErr = err
	}

	code, body := probe(t, h.Ready)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if gotName != "db" {
		t.Errorf("OnCheckError name = %q, want %q", gotName, "db")
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnCheckError err = %v, want %v", gotErr, wantErr)
	}
	// The callback is where the cause goes; the wire still only gets the name.
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "refused") {
		t.Errorf("body leaked the check's error text: %s", body)
	}
}

func TestNilOnCheckErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Ready.Set(true)
	h.AddReadyCheck("db", func(context.Context) error { return errors.New("boom") })

	// h.Ready.OnCheckError is left nil: ServeHTTP must not dereference it.
	code, _ := probe(t, h.Ready)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
}

func TestReadyPassesWhenAllChecksPass(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Ready.Set(true)
	h.AddReadyCheck("db", func(context.Context) error { return nil })
	h.AddReadyCheck("cache", func(context.Context) error { return nil })

	if code, _ := probe(t, h.Ready); code != http.StatusOK {
		t.Errorf("Ready = %d, want 200", code)
	}
}

func TestReadyReportsEveryFailingCheck(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Ready.Set(true)
	h.AddReadyCheck("db", func(context.Context) error { return errors.New("x") })
	h.AddReadyCheck("ok", func(context.Context) error { return nil })
	h.AddReadyCheck("cache", func(context.Context) error { return errors.New("y") })

	_, body := probe(t, h.Ready)
	var got struct {
		Failed []string `json:"failed"`
	}
	_ = json.Unmarshal([]byte(body), &got)
	slices.Sort(got.Failed)
	if !slices.Equal(got.Failed, []string{"cache", "db"}) {
		t.Errorf("failed = %v, want [cache db]", got.Failed)
	}
}

func TestChecksAreSkippedWhenTheGateIsClosed(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	ran := false
	h.AddReadyCheck("db", func(context.Context) error { ran = true; return nil })

	if code, _ := probe(t, h.Ready); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if ran {
		t.Error("a readiness check ran while the gate was closed")
	}
}

// A hung check must not hang the probe.
func TestAHungCheckTimesOutAndCountsAsFailure(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	h.Ready.Set(true)
	h.AddReadyCheck("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	done := make(chan int, 1)
	go func() { c, _ := probe(t, h.Ready); done <- c }()

	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a hung readiness check hung the probe")
	}
}

func TestGateIsSafeUnderConcurrentSetAndServe(t *testing.T) {
	t.Parallel()
	h := httpserver.NewHealth()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); h.Ready.Set(i%2 == 0) }()
		wg.Add(1)
		go func() { defer wg.Done(); probe(t, h.Ready) }()
	}
	wg.Wait()
}
