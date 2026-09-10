package config_test

import (
	"errors"
	"testing"

	"github.com/pavelpascari/svcrt/config"
)

func TestViolationErrorIncludesEnvAndField(t *testing.T) {
	t.Parallel()

	v := config.Violation{
		Env:   "DATABASE_URL",
		Field: "AppConfig.DBURL",
		Kind:  config.KindRequired,
		Err:   errors.New("required, not set"),
	}

	want := "DATABASE_URL (AppConfig.DBURL): required, not set"
	if got := v.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestViolationErrorOmitsEmptyEnv(t *testing.T) {
	t.Parallel()

	// Whole-struct violations (Validate) have no env var to name.
	v := config.Violation{
		Field: "AppConfig",
		Kind:  config.KindValidate,
		Err:   errors.New("TLS_KEY required when TLS_CERT is set"),
	}

	want := "AppConfig: TLS_KEY required when TLS_CERT is set"
	if got := v.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestErrorRendersAllViolations(t *testing.T) {
	t.Parallel()

	e := &config.Error{Violations: []config.Violation{
		{Env: "DATABASE_URL", Field: "AppConfig.DBURL", Kind: config.KindRequired, Err: errors.New("required, not set")},
		{Env: "PORT", Field: "AppConfig.Port", Kind: config.KindDecode, Err: errors.New(`invalid int: parsing "abc": invalid syntax`)},
	}}

	want := "config: 2 problems\n" +
		"  DATABASE_URL (AppConfig.DBURL): required, not set\n" +
		`  PORT (AppConfig.Port): invalid int: parsing "abc": invalid syntax`
	if got := e.Error(); got != want {
		t.Errorf("Error() =\n%s\nwant\n%s", got, want)
	}
}

func TestErrorUsesSingularForOneProblem(t *testing.T) {
	t.Parallel()

	e := &config.Error{Violations: []config.Violation{
		{Env: "PORT", Field: "C.Port", Kind: config.KindRequired, Err: errors.New("required, not set")},
	}}

	want := "config: 1 problem\n  PORT (C.Port): required, not set"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestErrorUnwrapReachesIndividualCauses(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("sentinel cause")
	e := &config.Error{Violations: []config.Violation{
		{Env: "A", Field: "C.A", Kind: config.KindDecode, Err: errors.New("other")},
		{Env: "B", Field: "C.B", Kind: config.KindDecode, Err: sentinel},
	}}

	if !errors.Is(e, sentinel) {
		t.Error("errors.Is did not reach a violation cause through Error.Unwrap")
	}
}

func TestViolationKindString(t *testing.T) {
	t.Parallel()

	for kind, want := range map[config.ViolationKind]string{
		config.KindSchema:   "schema",
		config.KindRequired: "required",
		config.KindDecode:   "decode",
		config.KindValidate: "validate",
	} {
		if got := kind.String(); got != want {
			t.Errorf("ViolationKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}
