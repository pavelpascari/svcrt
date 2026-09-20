package testkit_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/pavelpascari/svcrt/testkit"
)

// exampleTB stands in for the *testing.T you pass in a real test. Every helper
// here takes testkit.TB rather than testing.TB precisely so it can be
// substituted -- an assertion that cannot be shown to fail is worse than none.
//
// In your own test this whole type disappears and you write testkit.Upstream(t).
type exampleTB struct{ cleanups []func() }

func (*exampleTB) Helper()                   {}
func (*exampleTB) Errorf(f string, a ...any) { fmt.Printf("FAIL: "+f+"\n", a...) }
func (*exampleTB) Fatalf(f string, a ...any) { panic(fmt.Sprintf(f, a...)) }
func (tb *exampleTB) Cleanup(fn func())      { tb.cleanups = append(tb.cleanups, fn) }
func (tb *exampleTB) run() {
	for i := len(tb.cleanups) - 1; i >= 0; i-- {
		tb.cleanups[i]()
	}
}

// ExampleUpstream scripts a dependency that fails twice and then recovers,
// which is the shape a retry or breaker test needs.
func ExampleUpstream() {
	tb := &exampleTB{}
	defer tb.run() // *testing.T does this for you.

	up := testkit.Upstream(tb,
		testkit.Status(http.StatusServiceUnavailable),
		testkit.Status(http.StatusServiceUnavailable),
		testkit.JSON(http.StatusOK, `{"id":"ord_1"}`),
	)

	// The last response repeats, so anything after the third request also
	// gets the 200. That is what makes "down" expressible as a one-entry
	// script and "flaky then healthy" as this one.
	var last string
	for i := 1; i <= 4; i++ {
		resp, err := http.Get(up.URL() + "/orders/ord_1")
		if err != nil {
			fmt.Println("get:", err)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = string(body)
		fmt.Printf("request %d: %d\n", i, resp.StatusCode)
	}
	fmt.Println("upstream served:", up.Requests())
	fmt.Println("last body:", last)

	// Output:
	// request 1: 503
	// request 2: 503
	// request 3: 200
	// request 4: 200
	// upstream served: 4
	// last body: {"id":"ord_1"}
}

// tooManyItems is the kind of error a service returns: a stable code plus the
// language-neutral values a client renders its own message from.
type tooManyItems struct{ max, got int }

func (tooManyItems) Error() string     { return "order.too_many_items" }
func (tooManyItems) ErrorCode() string { return "order.too_many_items" }
func (e tooManyItems) ErrorParams() map[string]any {
	return map[string]any{"max": e.max, "got": e.got}
}

// ExampleAssertCode asserts on the CODE, never on a message. That is svcrt's
// oldest cross-cutting rule, and asserting this way is what keeps prose from
// quietly becoming part of the wire contract.
func ExampleAssertCode() {
	tb := &exampleTB{}

	err := fmt.Errorf("placing order: %w", tooManyItems{max: 100, got: 250})

	testkit.AssertCode(tb, err, "order.too_many_items")
	testkit.AssertParams(tb, err, map[string]any{"max": 100, "got": 250})

	// Code is the non-asserting accessor, for a test that wants to branch.
	if code, ok := testkit.Code(err); ok {
		fmt.Println("code:", code)
	}

	// An error carrying no code at all reports so rather than panicking.
	_, ok := testkit.Code(errors.New("boom"))
	fmt.Println("plain error has a code:", ok)

	// Output:
	// code: order.too_many_items
	// plain error has a code: false
}

// ExampleLogger captures log output as decoded records, so a test asserts on
// the attribute a log pipeline actually reads rather than on a substring.
func ExampleLogger() {
	tb := &exampleTB{}

	log, records := testkit.Logger(tb)
	log.Info("order placed", "order_id", "ord_1")
	log.Warn("retrying", "order_id", "ord_1", "attempt", 2)

	last, ok := records.Last()
	fmt.Println("captured:", len(records.All()), "records; last ok:", ok)
	fmt.Printf("%s %q attempt=%v\n", last.Level, last.Message, last.Attrs["attempt"])
	fmt.Println("matching order_id:", len(records.Find("order_id", "ord_1")))

	// Output:
	// captured: 2 records; last ok: true
	// WARN "retrying" attempt=2
	// matching order_id: 2
}
