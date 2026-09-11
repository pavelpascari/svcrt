package config

import (
	"strconv"
	"strings"
)

// ViolationKind classifies a configuration problem by its audience.
type ViolationKind int

const (
	// KindSchema is a programmer error in the config struct itself: an
	// unsupported field type, a missing or malformed tag, or two fields
	// resolving to the same environment key. No environment can satisfy it.
	KindSchema ViolationKind = iota

	// KindRequired is an operator error: a field with no default: tag and no
	// pointer type whose environment variable is not set.
	KindRequired

	// KindDecode is an operator error: a value that is set but cannot be
	// parsed into the field's type.
	KindDecode

	// KindValidate is an operator error reported by the config type's own
	// Validate method.
	KindValidate
)

func (k ViolationKind) String() string {
	switch k {
	case KindSchema:
		return "schema"
	case KindRequired:
		return "required"
	case KindDecode:
		return "decode"
	case KindValidate:
		return "validate"
	default:
		return "unknown"
	}
}

// Violation is a single configuration problem.
type Violation struct {
	// Env is the full environment variable name, or "" for a violation that
	// concerns the whole struct rather than one variable.
	Env string

	// Field is the dotted Go path, such as "AppConfig.DBURL".
	Field string

	Kind ViolationKind
	Err  error
}

func (v Violation) Error() string {
	if v.Env == "" {
		return v.Field + ": " + v.Err.Error()
	}
	return v.Env + " (" + v.Field + "): " + v.Err.Error()
}

func (v Violation) Unwrap() error { return v.Err }

// Error aggregates every violation found during a single Load.
//
// Load never stops at the first problem: an operator fixing configuration
// should see the whole list once, not discover it one restart at a time.
type Error struct {
	Violations []Violation
}

func (e *Error) Error() string {
	noun := "problems"
	if len(e.Violations) == 1 {
		noun = "problem"
	}

	var b strings.Builder
	b.WriteString("config: ")
	b.WriteString(strconv.Itoa(len(e.Violations)))
	b.WriteByte(' ')
	b.WriteString(noun)
	for _, v := range e.Violations {
		b.WriteString("\n  ")
		b.WriteString(v.Error())
	}
	return b.String()
}

// Unwrap exposes each violation so errors.Is and errors.As reach the
// individual causes.
func (e *Error) Unwrap() []error {
	errs := make([]error, len(e.Violations))
	for i, v := range e.Violations {
		errs[i] = v
	}
	return errs
}
