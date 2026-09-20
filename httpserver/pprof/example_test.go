package pprof_test

import (
	"fmt"
	"net/http/httptest"

	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/httpserver/pprof"
)

// ExampleHandler mounts profiling on the admin mux, which is the only place it
// belongs: the endpoints expose heap dumps and goroutine traces, so they must
// not be publicly routable.
//
// The handler routes the whole /debug/pprof/ subtree, so it is mounted once at
// that prefix rather than five times.
func ExampleHandler() {
	admin := httpserver.AdminMux(httpserver.NewHealth())
	admin.Handle("/debug/pprof/", pprof.Handler())

	srv := httptest.NewServer(admin)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/debug/pprof/cmdline")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	defer resp.Body.Close()

	fmt.Println("/debug/pprof/cmdline:", resp.StatusCode)
	fmt.Println("content-type:", resp.Header.Get("Content-Type"))

	// The probes AdminMux already served are untouched.
	live, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	defer live.Body.Close()
	fmt.Println("/healthz:", live.StatusCode)

	// Output:
	// /debug/pprof/cmdline: 200
	// content-type: text/plain; charset=utf-8
	// /healthz: 200
}
