package contract_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/pavelpascari/svcrt/contract"
)

// tooManyItems is the shape an API error takes in svcrt: a stable code plus
// the language-neutral values a client substitutes into a message it owns.
// Nothing here is display prose -- deciding what English speakers read is the
// client's job, and the wire carries only the code and the scalars.
type tooManyItems struct{ Max, Got int }

func (e tooManyItems) Error() string     { return "order.too_many_items" }
func (e tooManyItems) ErrorCode() string { return "order.too_many_items" }
func (e tooManyItems) ErrorParams() map[string]any {
	return map[string]any{"max": e.Max, "got": e.Got}
}

// Declare the implementation so a typo is a compile error rather than an
// errors.As that silently never matches.
var _ contract.Detailed = tooManyItems{}

// ExampleCoded reads an error's code back out after it has been wrapped, which
// is the only thing a caller ever does with one.
func ExampleCoded() {
	err := fmt.Errorf("placing order: %w", tooManyItems{Max: 100, Got: 250})

	var coded contract.Coded
	if errors.As(err, &coded) {
		fmt.Printf("%s=%s\n", contract.KeyCode, coded.ErrorCode())
	}

	// Detailed is optional: implement it only when there is something to
	// substitute. Read the params by name -- ranging over the map would
	// print in a different order on every run.
	var detailed contract.Detailed
	if errors.As(err, &detailed) {
		p := detailed.ErrorParams()
		fmt.Printf("max=%v got=%v\n", p["max"], p["got"])
	}

	// Output:
	// code=order.too_many_items
	// max=100 got=250
}

// ExampleChain shows the argument order: the first middleware is outermost, so
// it observes the call first and returns last, matching net/http convention.
func ExampleChain() {
	trace := func(name string) contract.Middleware[string, int] {
		return func(next contract.Handler[string, int]) contract.Handler[string, int] {
			return func(ctx context.Context, req string) (int, error) {
				fmt.Println(name, "in")
				res, err := next(ctx, req)
				fmt.Println(name, "out")
				return res, err
			}
		}
	}

	count := func(_ context.Context, req string) (int, error) { return len(req), nil }

	h := contract.Chain(trace("outer"), trace("inner"))(count)

	n, err := h(context.Background(), "hello")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("result:", n)

	// Output:
	// outer in
	// inner in
	// inner out
	// outer out
	// result: 5
}
