package lifecycle_test

import (
	"context"
	"fmt"

	"github.com/pavelpascari/svcrt/lifecycle"
)

// step returns a start/stop pair that announces itself, so the example's
// output is the ordering rather than a description of it.
func step(name string) (lifecycle.StartFunc, lifecycle.StopFunc) {
	return func(context.Context) error {
			fmt.Println("start", name)
			return nil
		}, func(context.Context) error {
			fmt.Println("stop", name)
			return nil
		}
}

// ExampleLifecycle_Add wires three components and lets one signal shut the
// process down. Shutdown is the exact reverse of startup: After declares an
// edge in one direction and both directions follow from it.
func ExampleLifecycle_Add() {
	lc := lifecycle.New(lifecycle.Config{})

	dbStart, dbStop := step("db")
	db := lc.Add("db", dbStart, dbStop)

	cacheStart, cacheStop := step("cache")
	cache := lc.Add("cache", cacheStart, cacheStop, lifecycle.After(db))

	// The API server starts last and stops first, so it is refusing traffic
	// while its dependencies are still up.
	apiStart, apiStop := step("api")
	lc.Add("api", apiStart, apiStop, lifecycle.After(cache))

	// A real main passes lifecycle.SignalContext(context.Background()) and
	// blocks until SIGINT or SIGTERM. Cancelling from OnStarted keeps the
	// example finite: it shuts down the moment everything is up.
	ctx, cancel := context.WithCancel(context.Background())
	lc.OnStarted(cancel)

	if err := lc.Run(ctx); err != nil {
		fmt.Println("run:", err)
	}

	// Output:
	// start db
	// start cache
	// start api
	// stop api
	// stop cache
	// stop db
}

// ExampleLifecycle_Fatal reports a component that died AFTER it started -- a
// listener lost at 3am -- and gets the same ordered shutdown a signal would.
func ExampleLifecycle_Fatal() {
	lc := lifecycle.New(lifecycle.Config{})

	dbStart, dbStop := step("db")
	db := lc.Add("db", dbStart, dbStop)

	lc.Add("api", func(context.Context) error {
		fmt.Println("start api")
		return nil
	}, func(context.Context) error {
		fmt.Println("stop api")
		return nil
	}, lifecycle.After(db))

	// Report the failure once everything is up. In a real service this is
	// httpserver.Options.OnServeError wired to lc.Fatal.
	lc.OnStarted(func() { lc.Fatal(fmt.Errorf("listener closed")) })

	fmt.Println("run:", lc.Run(context.Background()))

	// Output:
	// start db
	// start api
	// stop api
	// stop db
	// run: listener closed
}
