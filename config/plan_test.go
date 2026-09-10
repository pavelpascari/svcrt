package config

import (
	"reflect"
	"testing"
	"time"
)

func planFor[T any](t *testing.T, prefix string) (*plan, []Violation) {
	t.Helper()
	var v T
	return buildPlan(reflect.TypeOf(v), prefix)
}

func envNames(p *plan) []string {
	out := make([]string, len(p.bindings))
	for i, b := range p.bindings {
		out[i] = b.env
	}
	return out
}

type nested struct {
	Endpoint string        `env:"ENDPOINT" default:"localhost:4317"`
	Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
}

type flatFragment struct {
	Region string `env:"REGION" default:"eu"`
}

type basic struct {
	Port  int          `env:"PORT" default:"8080"`
	DBURL string       `env:"DATABASE_URL"`
	Debug *bool        `env:"DEBUG"`
	OTel  nested       `envPrefix:"OTEL_"`
	Flat  flatFragment `envPrefix:""`
	Skip  string       `env:"-"`
	priv  string
}

func TestBuildPlanCollectsBindingsInDeclarationOrder(t *testing.T) {
	t.Parallel()

	p, vs := planFor[basic](t, "")
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}

	want := []string{"PORT", "DATABASE_URL", "DEBUG", "OTEL_ENDPOINT", "OTEL_TIMEOUT", "REGION"}
	if got := envNames(p); !reflect.DeepEqual(got, want) {
		t.Errorf("env names = %v, want %v", got, want)
	}
}

func TestBuildPlanRecordsDefaults(t *testing.T) {
	t.Parallel()

	p, _ := planFor[basic](t, "")
	byEnv := map[string]binding{}
	for _, b := range p.bindings {
		byEnv[b.env] = b
	}

	if b := byEnv["PORT"]; !b.hasDef || b.def != "8080" {
		t.Errorf("PORT default = (%q, %v), want (\"8080\", true)", b.def, b.hasDef)
	}
	if b := byEnv["DATABASE_URL"]; b.hasDef {
		t.Error("DATABASE_URL has a default; it must be required")
	}
}

func TestBuildPlanNamesFieldsWithDottedPath(t *testing.T) {
	t.Parallel()

	p, _ := planFor[basic](t, "")
	for _, b := range p.bindings {
		if b.env == "OTEL_ENDPOINT" && b.field != "basic.OTel.Endpoint" {
			t.Errorf("field = %q, want %q", b.field, "basic.OTel.Endpoint")
		}
	}
}

// envPrefix:"" is how a fragment embeds flat. A *missing* tag is fatal.
func TestBuildPlanEmptyEnvPrefixAddsNothing(t *testing.T) {
	t.Parallel()

	p, vs := planFor[basic](t, "")
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	found := false
	for _, b := range p.bindings {
		if b.env == "REGION" {
			found = true
		}
	}
	if !found {
		t.Error("flat fragment did not contribute REGION")
	}
}

func TestBuildPlanComposesPrefixesTransitively(t *testing.T) {
	t.Parallel()

	type inner struct {
		Endpoint string `env:"ENDPOINT"`
	}
	type outer struct {
		Exporter inner `envPrefix:"EXPORTER_"`
	}
	type root struct {
		OTel outer `envPrefix:"OTEL_"`
	}

	p, vs := planFor[root](t, "APP_")
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	if got := envNames(p); len(got) != 1 || got[0] != "APP_OTEL_EXPORTER_ENDPOINT" {
		t.Errorf("env names = %v, want [APP_OTEL_EXPORTER_ENDPOINT]", got)
	}
}

func TestBuildPlanRecordsOptionalBlocks(t *testing.T) {
	t.Parallel()

	type tlsCfg struct {
		Cert string `env:"CERT"`
		Key  string `env:"KEY"`
	}
	type root struct {
		Addr string  `env:"ADDR" default:":8080"`
		TLS  *tlsCfg `envPrefix:"TLS_"`
	}

	p, vs := planFor[root](t, "")
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	if len(p.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(p.blocks))
	}
	if p.blocks[0].field != "root.TLS" {
		t.Errorf("block field = %q, want %q", p.blocks[0].field, "root.TLS")
	}

	var under int
	for _, b := range p.bindings {
		if underBlock(b.index, p.blocks[0].index) {
			under++
		}
	}
	if under != 2 {
		t.Errorf("%d bindings under the TLS block, want 2", under)
	}
}

func TestBuildPlanRejectsUntaggedExportedField(t *testing.T) {
	t.Parallel()

	type bad struct {
		Port int `env:"PORT"`
		Oops string
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 1 {
		t.Fatalf("violations = %v, want exactly 1", vs)
	}
	if vs[0].Kind != KindSchema {
		t.Errorf("kind = %v, want %v", vs[0].Kind, KindSchema)
	}
	if vs[0].Field != "bad.Oops" {
		t.Errorf("field = %q, want %q", vs[0].Field, "bad.Oops")
	}
}

func TestBuildPlanRejectsUntaggedNestedStruct(t *testing.T) {
	t.Parallel()

	type bad struct {
		OTel nested // no envPrefix tag at all
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 1 || vs[0].Kind != KindSchema {
		t.Fatalf("violations = %v, want one schema violation", vs)
	}
}

func TestBuildPlanRejectsDuplicateEnvKey(t *testing.T) {
	t.Parallel()

	type bad struct {
		A string `env:"SAME"`
		B string `env:"SAME"`
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 1 || vs[0].Kind != KindSchema {
		t.Fatalf("violations = %v, want one schema violation", vs)
	}
	if vs[0].Env != "SAME" {
		t.Errorf("env = %q, want SAME", vs[0].Env)
	}
}

func TestBuildPlanRejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	type bad struct {
		M map[string]string `env:"M"`
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 1 || vs[0].Kind != KindSchema {
		t.Fatalf("violations = %v, want one schema violation", vs)
	}
}

func TestBuildPlanCollectsAllSchemaViolations(t *testing.T) {
	t.Parallel()

	type bad struct {
		Untagged string
		M        map[string]string `env:"M"`
		A        string            `env:"SAME"`
		B        string            `env:"SAME"`
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 3 {
		t.Errorf("violations = %d (%v), want 3", len(vs), vs)
	}
}

func TestBuildPlanRejectsNonStruct(t *testing.T) {
	t.Parallel()

	_, vs := buildPlan(reflect.TypeOf(42), "")
	if len(vs) != 1 || vs[0].Kind != KindSchema {
		t.Fatalf("violations = %v, want one schema violation", vs)
	}
}

func TestFieldByIndexAllocMaterializesPointers(t *testing.T) {
	t.Parallel()

	type leaf struct{ V string }
	type mid struct{ L *leaf }
	type root struct{ M *mid }

	var r root
	f := fieldByIndexAlloc(reflect.ValueOf(&r).Elem(), []int{0, 0, 0})
	f.SetString("set")

	if r.M == nil || r.M.L == nil {
		t.Fatal("pointers were not allocated")
	}
	if r.M.L.V != "set" {
		t.Errorf("V = %q, want %q", r.M.L.V, "set")
	}
}

// Not in the brief verbatim, added to close a coverage gap: a field cannot
// carry both env and envPrefix -- it can't be both a value and a group.
func TestBuildPlanRejectsFieldWithBothEnvAndEnvPrefix(t *testing.T) {
	t.Parallel()

	type bad struct {
		Weird string `env:"WEIRD" envPrefix:"WEIRD_"`
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 1 || vs[0].Kind != KindSchema {
		t.Fatalf("violations = %v, want one schema violation", vs)
	}
	if vs[0].Field != "bad.Weird" {
		t.Errorf("field = %q, want %q", vs[0].Field, "bad.Weird")
	}
}

// Not in the brief verbatim, added to close a coverage gap: env:"" (present
// but empty) is distinct from a missing env tag and must also be rejected.
func TestBuildPlanRejectsEmptyEnvTag(t *testing.T) {
	t.Parallel()

	type bad struct {
		Oops string `env:""`
	}

	_, vs := planFor[bad](t, "")
	if len(vs) != 1 || vs[0].Kind != KindSchema {
		t.Fatalf("violations = %v, want one schema violation", vs)
	}
	if vs[0].Field != "bad.Oops" {
		t.Errorf("field = %q, want %q", vs[0].Field, "bad.Oops")
	}
}

func TestUnderBlock(t *testing.T) {
	t.Parallel()

	if !underBlock([]int{1, 0}, []int{1}) {
		t.Error("[1 0] should be under [1]")
	}
	if underBlock([]int{2, 0}, []int{1}) {
		t.Error("[2 0] should not be under [1]")
	}
	if underBlock([]int{1}, []int{1, 0}) {
		t.Error("a shorter path cannot be under a longer one")
	}
}

// Not in the brief verbatim: a path is never "under" itself. This kills a
// mutant that weakens underBlock's length guard from <= to <, which would
// make the block's own binding index (equal length, equal path) satisfy
// slices.Equal and be misreported as living under the block.
func TestUnderBlockRejectsEqualPath(t *testing.T) {
	t.Parallel()

	if underBlock([]int{1, 2}, []int{1, 2}) {
		t.Error("a path equal to the block's own index must not be under it")
	}
}

// Not in the brief verbatim: time.Time parses itself via TextUnmarshaler, so
// a field of this type must be treated as a decodable value (env tag), not
// walked as a nested struct (envPrefix tag). This exercises the ordering
// rule called out in the task: check TextUnmarshaler before deciding a field
// is a nested struct.
func TestBuildPlanTreatsTextUnmarshalerStructAsValue(t *testing.T) {
	t.Parallel()

	type withTime struct {
		At time.Time `env:"AT"`
	}

	p, vs := planFor[withTime](t, "")
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	if got := envNames(p); len(got) != 1 || got[0] != "AT" {
		t.Errorf("env names = %v, want [AT]", got)
	}
}

// Same rule, pointer form: *time.Time also parses itself (UnmarshalText has
// a pointer receiver, so *time.Time's pointee implements TextUnmarshaler via
// its own pointer). It must bind as a value too, not become an optional
// block.
func TestBuildPlanTreatsPointerToTextUnmarshalerStructAsValue(t *testing.T) {
	t.Parallel()

	type withTimePtr struct {
		At *time.Time `env:"AT"`
	}

	p, vs := planFor[withTimePtr](t, "")
	if len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	if len(p.blocks) != 0 {
		t.Errorf("blocks = %v, want none: *time.Time is a value, not a group", p.blocks)
	}
	if got := envNames(p); len(got) != 1 || got[0] != "AT" {
		t.Errorf("env names = %v, want [AT]", got)
	}
}

// Not in the brief verbatim: every "record the violation and move to the
// next field" branch must actually continue the loop, not abort it. This
// single struct walks past every skip/violation kind in the walker (skip via
// unexported, skip via env:"-", both-tags conflict, nested-missing-prefix,
// empty env tag, duplicate env key) and checks that a field declared after
// all of them is still reached. A mutant turning any of those `continue`
// statements into `break` truncates the field list and this test catches it.
func TestBuildPlanContinuesPastEverySkipAndViolation(t *testing.T) {
	t.Parallel()

	type bad struct {
		priv        string //nolint:unused // exercises the unexported-field skip
		SkipField   string `env:"-"`
		BothTags    string `env:"X" envPrefix:"X_"`
		NestedNoTag nested
		EmptyEnv    string `env:""`
		DupA        string `env:"SAME"`
		DupB        string `env:"SAME"`
		Final       string `env:"FINAL"`
	}

	p, vs := planFor[bad](t, "")
	if len(vs) != 4 {
		t.Fatalf("violations = %d (%v), want 4", len(vs), vs)
	}

	names := envNames(p)
	hasSame, hasFinal := false, false
	for _, n := range names {
		if n == "SAME" {
			hasSame = true
		}
		if n == "FINAL" {
			hasFinal = true
		}
	}
	if !hasSame {
		t.Error("DupA did not bind SAME; loop may have stopped early")
	}
	if !hasFinal {
		t.Error("Final field not reached; loop may have stopped early (continue vs break)")
	}
}
