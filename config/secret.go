package config

import "log/slog"

// Secret is a string whose value must not appear in output.
//
// It redacts under fmt, encoding/json, and log/slog -- including
// slog.JSONHandler and any other handler, because the redaction is a property
// of the value rather than of a particular writer. That is why Secret lives
// here, at the point secrets are born, and not in svcrt/logging: a handler
// can only protect its own users.
//
// Reading the real value requires an explicit conversion:
//
//	db, err := sql.Open("postgres", string(cfg.DBURL))
//
// so accidental exposure redacts while intentional use is visible at the call
// site.
type Secret string

// String implements fmt.Stringer, covering %v, %s, and %q.
func (s Secret) String() string { return "[REDACTED]" }

// LogValue implements slog.LogValuer, which slog prefers over Stringer.
func (s Secret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// MarshalText implements encoding.TextMarshaler, which encoding/json uses.
//
// Note this makes Secret deliberately lossy through a JSON round trip. A
// config value should never be reconstructed from serialized output, so
// losing it is the safe direction.
func (s Secret) MarshalText() ([]byte, error) { return []byte("[REDACTED]"), nil }
