package httpserver_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/pavelpascari/svcrt/httpserver"
)

// exampleLogger builds a logger from STDLIB slog, not svcrt/logging.
//
// httpserver has zero require directives and must keep them: a test-only
// import still lands in go.mod. Duration and stack are dropped so the output
// is stable; a real service keeps both.
func exampleLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey, httpserver.KeyDurMS, httpserver.KeyStack:
				return slog.Attr{}
			}
			return a
		},
	}))
}

// ExampleChain folds middleware in the order a service wants it, and shows why
// Recover goes INNERMOST: a panic unwinds inner frames first, so only from
// there does the access-log line outside it record the real 500.
func ExampleChain() {
	log := exampleLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(http.ResponseWriter, *http.Request) {
		panic("upstream returned nil")
	})

	// In a full service telemetry.Server(...) goes outside AccessLog, so the
	// span is in the context by the time the line is emitted.
	h := httpserver.Chain(
		httpserver.AccessLog(log),
		httpserver.Recover(log),
	)(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/42", nil))

	// The panic value never reaches the client: the response is a bare 500.
	fmt.Println("client saw:", rec.Code, rec.Body.Len(), "bytes")

	// Output:
	// {"level":"ERROR","msg":"panic serving request","panic":"upstream returned nil"}
	// {"level":"INFO","msg":"http request","method":"GET","route":"GET /orders/{id}","status":500}
	// client saw: 500 0 bytes
}

// ExampleNewHealth mounts the three probes Kubernetes distinguishes and opens
// them from the lifecycle hooks rather than by hand at construction time --
// which is what makes readiness honest.
func ExampleNewHealth() {
	h := httpserver.NewHealth()

	admin := httptest.NewServer(httpserver.AdminMux(h))
	defer admin.Close()

	probe := func(path string) {
		resp, err := admin.Client().Get(admin.URL + path)
		if err != nil {
			fmt.Println(path, "error:", err)
			return
		}
		defer resp.Body.Close()
		fmt.Printf("%-10s %d\n", path, resp.StatusCode)
	}

	// Before Up: liveness already answers 200, because a Live that stays
	// closed until boot finishes turns a slow start into a restart loop.
	probe("/healthz")
	probe("/readyz")

	// lc.OnStarted(h.Up) in a real service.
	h.Up()
	probe("/readyz")

	// lc.OnDrain(h.Drain): stop taking traffic, stay alive to finish it.
	h.Drain()
	probe("/readyz")
	probe("/healthz")

	// Output:
	// /healthz   200
	// /readyz    503
	// /readyz    200
	// /readyz    503
	// /healthz   200
}

// ExampleServer_Start binds a listener and returns once it is accepting, which
// is exactly the shape lifecycle expects of a component's start function --
// so the two compose with no import between them.
func ExampleServer_Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "v1")
	})

	// Bind :0 and ask for the resolved address, so nothing races on a port.
	srv := httpserver.New(mux, httpserver.Options{Addr: "127.0.0.1:0"})

	if err := srv.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
		return
	}
	defer srv.Shutdown(context.Background())

	resp, err := http.Get("http://" + srv.Addr() + "/version")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Print(resp.StatusCode, " ", string(body))

	// Output: 200 v1
}
