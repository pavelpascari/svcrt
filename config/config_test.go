package config_test

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

type otelCfg struct {
	Endpoint string        `env:"ENDPOINT" default:"localhost:4317"`
	Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
}

type tlsCfg struct {
	Cert string `env:"CERT"`
	Key  string `env:"KEY"`
}

type appCfg struct {
	Addr  string        `env:"ADDR" default:":8080"`
	DBURL config.Secret `env:"DATABASE_URL"`
	Tags  []string      `env:"TAGS" default:"a,b"`
	Debug *bool         `env:"DEBUG"`
	OTel  otelCfg       `envPrefix:"OTEL_"`
	TLS   *tlsCfg       `envPrefix:"TLS_"`
}

func TestLoadAppliesDefaultsAndRequiredValues(t *testing.T) {
	t.Parallel()

	got, err := config.Load[appCfg](config.WithSource(mapSource(map[string]string{
		"DATABASE_URL": "postgres://localhost/db",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Addr != ":8080" {
		t.Errorf("Addr = %q, want %q", got.Addr, ":8080")
	}
	if string(got.DBURL) != "postgres://localhost/db" {
		t.Errorf("DBURL = %q", string(got.DBURL))
	}
	if len(got.Tags) != 2 || got.Tags[0] != "a" {
		t.Errorf("Tags = %v, want [a b]", got.Tags)
	}
	if got.Debug != nil {
		t.Errorf("Debug = %v, want nil when DEBUG is unset", got.Debug)
	}
	if got.OTel.Timeout != 5*time.Second {
		t.Errorf("OTel.Timeout = %v, want 5s", got.OTel.Timeout)
	}
	if got.TLS != nil {
		t.Errorf("TLS = %+v, want nil when no TLS_ variable is set", got.TLS)
	}
}

func TestLoadTriStatePointerDistinguishesUnsetFromZero(t *testing.T) {
	t.Parallel()

	got, err := config.Load[appCfg](config.WithSource(mapSource(map[string]string{
		"DATABASE_URL": "x",
		"DEBUG":        "false",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Debug == nil {
		t.Fatal("Debug = nil, want pointer to false")
	}
	if *got.Debug != false {
		t.Errorf("*Debug = %v, want false", *got.Debug)
	}
}

func TestLoadMaterializesOptionalBlockWhenAnyMemberIsSet(t *testing.T) {
	t.Parallel()

	got, err := config.Load[appCfg](config.WithSource(mapSource(map[string]string{
		"DATABASE_URL": "x",
		"TLS_CERT":     "/certs/tls.crt",
		"TLS_KEY":      "/certs/tls.key",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TLS == nil {
		t.Fatal("TLS = nil, want it materialized")
	}
	if got.TLS.Cert != "/certs/tls.crt" || got.TLS.Key != "/certs/tls.key" {
		t.Errorf("TLS = %+v", *got.TLS)
	}
}

func TestLoadEnforcesRequiredFieldsInsideAMaterializedBlock(t *testing.T) {
	t.Parallel()

	_, err := config.Load[appCfg](config.WithSource(mapSource(map[string]string{
		"DATABASE_URL": "x",
		"TLS_CERT":     "/certs/tls.crt",
		// TLS_KEY deliberately absent
	})))
	if err == nil {
		t.Fatal("Load accepted a half-set TLS block")
	}

	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	if len(cerr.Violations) != 1 || cerr.Violations[0].Env != "TLS_KEY" {
		t.Errorf("violations = %v, want one for TLS_KEY", cerr.Violations)
	}
}

// A default inside an optional block must not bring the block into existence,
// or a block containing any defaulted field could never be absent.
func TestLoadDefaultInsideOptionalBlockDoesNotMaterializeIt(t *testing.T) {
	t.Parallel()

	type inner struct {
		Mode string `env:"MODE" default:"auto"`
	}
	type root struct {
		Name  string `env:"NAME"`
		Inner *inner `envPrefix:"INNER_"`
	}

	got, err := config.Load[root](config.WithSource(mapSource(map[string]string{"NAME": "n"})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Inner != nil {
		t.Errorf("Inner = %+v, want nil", got.Inner)
	}
}

func TestLoadReportsEveryViolationAtOnce(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port  int    `env:"PORT"`
		DBURL string `env:"DATABASE_URL"`
		Level string `env:"LEVEL"`
	}

	_, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{
		"PORT": "abc",
	})))
	if err == nil {
		t.Fatal("Load succeeded on a broken environment")
	}

	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	if len(cerr.Violations) != 3 {
		t.Errorf("violations = %d (%v), want 3", len(cerr.Violations), cerr.Violations)
	}
}

// A missing required field must report exactly one violation. If the
// required branch fell through into a decode attempt on the empty raw
// value, an int field would additionally fail to parse "" and produce a
// spurious second violation for the same field.
func TestLoadRequiredViolationDoesNotAlsoAttemptDecode(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port int `env:"PORT"`
	}

	_, err := config.Load[cfg](config.WithSource(mapSource(nil)))
	if err == nil {
		t.Fatal("Load succeeded with PORT unset")
	}

	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	if len(cerr.Violations) != 1 || cerr.Violations[0].Kind != config.KindRequired {
		t.Errorf("violations = %v, want exactly one KindRequired", cerr.Violations)
	}
}

// A field that lives after a skipped (absent, optional) block in the
// binding list must still be decoded. This exercises the skip-and-continue
// loop rather than an early exit that would silently drop every binding
// following the skipped one.
func TestLoadContinuesPastSkippedBindingsToLaterFields(t *testing.T) {
	t.Parallel()

	type inner struct {
		X string `env:"X"`
	}
	type cfg struct {
		Opt  *inner `envPrefix:"OPT_"`
		Name string `env:"NAME"`
	}

	got, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{"NAME": "hi"})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Opt != nil {
		t.Errorf("Opt = %+v, want nil", got.Opt)
	}
	if got.Name != "hi" {
		t.Errorf("Name = %q, want %q", got.Name, "hi")
	}
}

// Each optional block's presence is decided independently. A materialized
// earlier block must not short-circuit the presence check for a later,
// still-absent block.
func TestLoadEvaluatesEveryBlockIndependently(t *testing.T) {
	t.Parallel()

	type blockA struct {
		V string `env:"V"`
	}
	type blockB struct {
		V string `env:"V"`
	}
	type cfg struct {
		A *blockA `envPrefix:"A_"`
		B *blockB `envPrefix:"B_"`
	}

	got, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{"A_V": "x"})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.A == nil || got.A.V != "x" {
		t.Errorf("A = %+v, want materialized with V=x", got.A)
	}
	if got.B != nil {
		t.Errorf("B = %+v, want nil", got.B)
	}
}

func TestLoadAggregatedMessageMatchesGolden(t *testing.T) {
	t.Parallel()

	type cfg struct {
		DBURL string `env:"DATABASE_URL"`
		Port  int    `env:"PORT"`
	}

	_, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{"PORT": "abc"})))
	if err == nil {
		t.Fatal("Load succeeded on a broken environment")
	}

	golden := filepath.Join("testdata", "aggregated.golden")
	if *update {
		if werr := os.WriteFile(golden, []byte(err.Error()), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	want, rerr := os.ReadFile(golden)
	if rerr != nil {
		t.Fatalf("read golden (run: go test ./... -update): %v", rerr)
	}
	if err.Error() != string(want) {
		t.Errorf("message =\n%s\nwant\n%s", err.Error(), want)
	}
}

type validatedCfg struct {
	Cert string `env:"CERT" default:""`
	Key  string `env:"KEY" default:""`
}

func (c validatedCfg) Validate() error {
	if c.Cert != "" && c.Key == "" {
		return errors.New("KEY required when CERT is set")
	}
	return nil
}

func TestLoadRunsValidate(t *testing.T) {
	t.Parallel()

	_, err := config.Load[validatedCfg](config.WithSource(mapSource(map[string]string{"CERT": "c"})))
	if err == nil {
		t.Fatal("Load ignored Validate")
	}

	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	if len(cerr.Violations) != 1 || cerr.Violations[0].Kind != config.KindValidate {
		t.Fatalf("violations = %v, want one KindValidate", cerr.Violations)
	}
	if cerr.Violations[0].Field != "validatedCfg" {
		t.Errorf("field = %q, want %q", cerr.Violations[0].Field, "validatedCfg")
	}
}

// Validate must not see a half-decoded struct, so it is skipped entirely when
// an earlier stage failed.
func TestLoadSkipsValidateWhenEarlierStagesFailed(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port int    `env:"PORT"`
		Cert string `env:"CERT" default:""`
	}

	_, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{"PORT": "abc"})))
	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	for _, v := range cerr.Violations {
		if v.Kind == config.KindValidate {
			t.Error("Validate ran despite an earlier decode failure")
		}
	}
}

func TestLoadWithPrefix(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port int `env:"PORT" default:"1"`
	}

	got, err := config.Load[cfg](
		config.WithPrefix("APP_"),
		config.WithSource(mapSource(map[string]string{"APP_PORT": "9"})),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Port != 9 {
		t.Errorf("Port = %d, want 9", got.Port)
	}
}

func TestLoadTreatsEmptyStringAsPresent(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Name string `env:"NAME"`
	}

	got, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{"NAME": ""})))
	if err != nil {
		t.Fatalf("Load rejected an explicitly empty value: %v", err)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty", got.Name)
	}
}

func TestLoadReturnsZeroValueOnError(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Name string `env:"NAME"`
	}

	got, err := config.Load[cfg](config.WithSource(mapSource(nil)))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got.Name != "" {
		t.Errorf("got = %+v, want the zero value on error", got)
	}
}

func TestLoadSchemaViolationsPreemptValueViolations(t *testing.T) {
	t.Parallel()

	type bad struct {
		Untagged string
		Name     string `env:"NAME"`
	}

	_, err := config.Load[bad](config.WithSource(mapSource(nil)))
	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	// The struct is unloadable regardless of environment; reporting a missing
	// NAME alongside would be noise.
	if len(cerr.Violations) != 1 || cerr.Violations[0].Kind != config.KindSchema {
		t.Errorf("violations = %v, want one KindSchema", cerr.Violations)
	}
}

func TestSecretDecodesFromConfig(t *testing.T) {
	t.Parallel()
	type cfg struct {
		Pass config.Secret `env:"PASS"`
	}
	got, err := config.Load[cfg](config.WithSource(mapSource(map[string]string{"PASS": "hunter2"})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(got.Pass) != "hunter2" {
		t.Errorf("Pass = %q, want %q", string(got.Pass), "hunter2")
	}
}

// node is declared at package scope because a locally-declared type cannot
// refer to itself.
type node struct {
	Name string `env:"NAME" default:"x"`
	Next *node  `envPrefix:"NEXT_"`
}

// A cyclic config type must fail the load, not hang it. Before the cycle
// guard this call never returned: walk recursed forever, allocating a binding
// per level rather than overflowing the stack, so the symptom was a service
// that silently never finished booting.
func TestLoadRejectsCyclicConfigTypeInsteadOfHanging(t *testing.T) {
	t.Parallel()

	_, err := config.Load[node](config.WithSource(mapSource(nil)))
	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err is %T, want *config.Error", err)
	}
	if len(cerr.Violations) != 1 || cerr.Violations[0].Kind != config.KindSchema {
		t.Fatalf("violations = %v, want one KindSchema", cerr.Violations)
	}
	if cerr.Violations[0].Field != "node.Next" {
		t.Errorf("field = %q, want %q", cerr.Violations[0].Field, "node.Next")
	}
}
