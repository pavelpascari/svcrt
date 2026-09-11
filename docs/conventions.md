# svcrt conventions

Decisions that R0 made by doing rather than by writing down. Each is recorded
here because the next module has to make the same choice, and guessing would
produce a second dialect.

These are conventions, not laws. Departing from one is fine; departing from
one silently is not — amend this file in the same change.

---

## 1. Functional options vs. an `Options` struct

Both shapes are in the tree:

| Package | Shape |
|---|---|
| `config` | `type Option func(*options)`, `WithSource(...)`, `WithPrefix(...)` |
| `logging` | `type Options struct { Level; AddSource; ReplaceAttr }` |

That is not an inconsistency; the two cases are different, and the rule that
separates them is:

- **Configuring a *value*, where the knobs mirror a standard-library struct —
  use a struct, and mirror it field for field.** `logging.Options` is
  `slog.HandlerOptions` with the same three fields under the same names. A
  reader who knows one knows the other, and its zero value is a meaningful
  default. Wrapping those three fields in `WithLevel`/`WithAddSource`/
  `WithReplaceAttr` would make a caller learn a second dialect for knobs they
  already know, and would buy nothing.

- **Configuring a *call*, where the common case passes nothing — use
  functional options.** Production calls `config.Load[AppConfig]()` with no
  arguments at all; `WithSource` exists mainly so a test can supply a map
  instead of the process environment. Functional options keep the common call
  empty and let a caller add exactly one thing.

**When neither clearly applies, prefer the struct.** It is inspectable,
zero-valued, and costs no closure per setting. Reach for functional options
when an option must carry behaviour, when options are expected to be added by
modules that do not exist yet, or when the zero value of some field would be a
dangerous default that a struct literal lets a caller omit by accident.

## 2. `Middleware` means two things — do not add a third

- `contract.Middleware[Req, Res]` is a **type**: the name of a shape,
  `func(Handler[Req, Res]) Handler[Req, Res]`. It exists so hand-written and
  generated code spell the same thing the same way.
- `logging.Middleware(l *slog.Logger)` is a **constructor function** returning
  `func(http.Handler) http.Handler`. That is the `net/http` idiom, and a
  transport-level middleware is a `net/http` citizen.

The split is along the transport/call boundary, not along taste:

- **Call-level middleware** — anything that needs the decoded, typed request —
  uses `contract.Middleware[Req, Res]`. A new module must not declare its own
  generic middleware type; it imports `contract`, which is what `contract` is
  for.
- **Transport-level middleware** — anything that needs only
  `http.ResponseWriter` and `*http.Request` (logging, tracing, request IDs) —
  exports a **constructor** returning `func(http.Handler) http.Handler`. Name
  it `Middleware` when the package has exactly one, as `logging` does, and
  name it for its job (`RecoverMiddleware`, `RequestIDMiddleware`) when it has
  several.

A new module that declares `type Middleware ...` for an HTTP-level shape adds
a third meaning to the word inside one repo. Don't.

## 3. Dead code: when to delete a clause and when to keep and test it

The rule, arrived at three times during R0 and settled at the third:

> **Delete a clause that is inert through every path including a direct call;
> keep and test one whose difference some caller can observe.**

Worked examples from R0:

- **Deleted.** A second `strings.TrimSpace(key)` immediately after an outer
  one. `TrimSpace` is idempotent, so no input distinguishes the two programs
  at the statement's own level. Deleted rather than recorded as an equivalent
  mutant.
- **Deleted.** A `t == durationType ||` operand in `isNestedStruct`.
  `time.Duration` is an `int64`, so both programs return `false` on the only
  candidate input. The "it mirrors `decoderFor` for readability" defence is
  about intent, not correctness, and does not survive this rule.
- **Kept and tested.** `decodeText`'s `!ok` guard is unreachable *through
  `decoderFor`* but perfectly reachable at the function's own level, which is
  where an internal white-box test operates. Deleting it would turn a future
  wiring bug into a raw panic inside a package whose reason for existing is
  structured errors instead of crashes.
- **Kept and tested.** `NewService(nil)`'s nil-map substitution. Invisible
  through `GetOrder` (reads from a nil map are harmless) but observable by a
  direct call followed by a write.

The corollary matters as much as the rule: **a mutation-survivors file that
accumulates "this code does nothing" entries makes dead code permanent**,
which inverts the purpose of the gate. An equivalence proof is a reason to
look harder, not a reason to stop.

And when a guard is kept for a reason mutation testing is structurally blind
to — cost, not behaviour — record *why the guard exists* next to the
equivalence proof. Otherwise the next reader deletes it citing that very
proof.

## 4. Supplied tests are a floor, never a target

A test suite handed down in a plan, a brief, or a task description is the
minimum, not the goal. R0 measured this twice:

- The plan's own tests for `config`'s plan walker reached 98.1% coverage and
  0.9298 mutation score. The implementer's additions took the same code to
  100% and 0.9838 over an identical 185 mutants — and caught six real bugs the
  supplied tests missed, including two `time.Time` ordering bugs and six
  `continue`→`break` truncation mutants.
- The brief for `examples/orders` left both `Error()` methods unexercised
  (83.3%), and the assertion that eventually covered them had to pin the whole
  string: a `Contains` check on either number passes with the arguments
  swapped.

So: run coverage *and* mutation, treat every survivor as killable until proven
otherwise, and expect to write tests nobody asked for. Conversely, a test that
exists only to touch a line is a defect even when it raises the number.
