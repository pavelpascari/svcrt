package httpserver_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	stdlog "log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pavelpascari/svcrt/httpserver"
)

// recoverLogger builds a logger from STDLIB slog, not svcrt/logging.
//
// httpserver has zero require directives and must keep them: a test-only
// import still lands in go.mod, and scripts/ci.sh fails any non-exempt module
// whose `go list -m all` returns more than one line. The existing tests in
// accesslog_test.go build their logger the same way for the same reason.
func recoverLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestRecoverTurnsAPanicIntoA500(t *testing.T) {
	log, logs := recoverLogger()
	h := httpserver.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler exploded")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "handler exploded") {
		t.Errorf("panic value not logged:\n%s", logs.String())
	}
}

// TestRecoverLeaksNeitherPanicNorStackToTheClient is a leak check, and it is
// why the status assertion above is not enough: "it returned 500" is equally
// true of an implementation that writes the panic message into the body.
func TestRecoverLeaksNeitherPanicNorStackToTheClient(t *testing.T) {
	log, _ := recoverLogger()
	h := httpserver.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret-token-abc123")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	body := rec.Body.String()
	if strings.Contains(body, "secret-token-abc123") {
		t.Fatalf("panic value reached the client: %q", body)
	}
	// A stack frame always names this test file.
	if strings.Contains(body, "recover_test.go") || strings.Contains(body, "goroutine") {
		t.Fatalf("stack trace reached the client: %q", body)
	}
}

func TestRecoverLogsTheStackUnderItsOwnKey(t *testing.T) {
	log, logs := recoverLogger()
	h := httpserver.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &line); err != nil {
		t.Fatalf("decode log %q: %v", logs.String(), err)
	}
	if line["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", line["level"])
	}
	if line[httpserver.KeyPanic] != "boom" {
		t.Errorf("%s = %v, want boom", httpserver.KeyPanic, line[httpserver.KeyPanic])
	}
	stack, _ := line[httpserver.KeyStack].(string)
	if !strings.Contains(stack, "recover_test.go") {
		t.Errorf("%s does not contain a stack: %q", httpserver.KeyStack, stack)
	}
}

// TestRecoverRePanicsErrAbortHandler: net/http documents
// panic(http.ErrAbortHandler) as the way to abort a response silently.
// Swallowing it converts a deliberate abort into a spurious 500 AND a spurious
// error log, so both halves are asserted -- a test checking only "no 500"
// would pass against a middleware that swallowed it and wrote nothing.
func TestRecoverRePanicsErrAbortHandler(t *testing.T) {
	log, logs := recoverLogger()
	h := httpserver.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	rec := httptest.NewRecorder()
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("ErrAbortHandler was swallowed; it must propagate to net/http")
			}
			if r != http.ErrAbortHandler {
				t.Fatalf("re-panicked %v, want http.ErrAbortHandler unchanged", r)
			}
		}()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}()

	if logs.Len() != 0 {
		t.Errorf("ErrAbortHandler was logged as an error:\n%s", logs.String())
	}
}

// TestRecoverRePanicsWrappedErrAbortHandler pins the errors.Is in the sentinel
// check, which a plain `==` would pass every other test in this file.
//
// net/http's own recovery compares with `!=`, so a wrapped value still gets
// its stack logged by the server -- but the handler's intent was an abort, and
// Recover's job is not to convert that into a 500 nobody had. The value is
// re-panicked exactly as given, wrapper included.
func TestRecoverRePanicsWrappedErrAbortHandler(t *testing.T) {
	log, logs := recoverLogger()
	wrapped := fmt.Errorf("streaming to client: %w", http.ErrAbortHandler)
	h := httpserver.Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(wrapped)
	}))

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("a wrapped ErrAbortHandler was swallowed")
			}
			if r != any(wrapped) {
				t.Fatalf("re-panicked %v, want the wrapped value unchanged", r)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}()

	if logs.Len() != 0 {
		t.Errorf("a wrapped ErrAbortHandler was logged as an error:\n%s", logs.String())
	}
}

// TestRecoverDoesNotWriteTwice: a handler that already responded and then
// panics must keep its status, and must not trigger net/http's "superfluous
// WriteHeader" warning.
func TestRecoverDoesNotWriteTwice(t *testing.T) {
	log, _ := recoverLogger()
	h := httpserver.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("partial"))
		panic("after writing")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the 418 the handler already sent", rec.Code)
	}
}

// syncBuffer is an io.Writer safe for the concurrent write net/http performs
// from the serving goroutine alongside the test goroutine's read.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRecoverDoesNotLogSuperfluousWriteHeader is the half of "does not write
// twice" that a ResponseRecorder cannot prove.
//
// httptest.ResponseRecorder keeps the first status regardless of how many
// times WriteHeader is called, so TestRecoverDoesNotWriteTwice stays green
// even with the `if !sw.written` guard deleted -- break-it experiment 3
// survived it. The second write is only observable where net/http itself
// objects: a real server, whose ErrorLog receives "http: superfluous
// response.WriteHeader call". That warning is what this test captures, and
// deleting the guard makes it appear.
func TestRecoverDoesNotLogSuperfluousWriteHeader(t *testing.T) {
	log, _ := recoverLogger()

	var srvLog syncBuffer
	srv := httptest.NewUnstartedServer(httpserver.Recover(log)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("partial"))
			panic("after writing")
		})))
	srv.Config.ErrorLog = stdlog.New(&srvLog, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	// Close waits for the handler to return, so every line net/http logs for
	// this request is in the buffer by the time it comes back.
	srv.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want the 418 the handler already sent", resp.StatusCode)
	}
	if string(body) != "partial" {
		t.Errorf("body = %q, want the bytes the handler already wrote", body)
	}
	if got := srvLog.String(); strings.Contains(got, "superfluous") {
		t.Errorf("Recover wrote a second header after the handler responded:\n%s", got)
	}
}

func TestRecoverPassesThroughWhenNothingPanics(t *testing.T) {
	log, logs := recoverLogger()
	h := httpserver.Recover(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("fine"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusCreated || rec.Body.String() != "fine" {
		t.Fatalf("status = %d body = %q; want 201 and \"fine\"", rec.Code, rec.Body)
	}
	if logs.Len() != 0 {
		t.Errorf("logged on a request that did not panic:\n%s", logs.String())
	}
}

// TestRecoverInnermostLetsAccessLogSeeThe500 is the ordering assertion, and it
// is the opposite of the usual instinct. A panic unwinds inner defers first,
// so with Recover OUTSIDE AccessLog the access line runs before the 500 exists
// and records no status at all.
func TestRecoverInnermostLetsAccessLogSeeThe500(t *testing.T) {
	log, logs := recoverLogger()

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := httpserver.Chain(
		httpserver.AccessLog(log),
		httpserver.Recover(log),
	)(panicking)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	var sawStatus bool
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) != nil {
			continue
		}
		if line["msg"] == "http request" && line[httpserver.KeyStatus] == float64(500) {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatalf("access log did not record status 500; Recover is not innermost:\n%s", logs.String())
	}
}
