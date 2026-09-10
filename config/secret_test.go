package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/config"
)

const redacted = "[REDACTED]"

func TestSecretRedactsUnderFmt(t *testing.T) {
	t.Parallel()

	s := config.Secret("hunter2")

	for _, verb := range []string{"%v", "%s", "%q", "%#v"} {
		got := fmt.Sprintf(verb, s)
		if strings.Contains(got, "hunter2") {
			t.Errorf("fmt %s leaked the secret: %s", verb, got)
		}
		if !strings.Contains(got, redacted) {
			t.Errorf("fmt %s = %s, want it to contain %s", verb, got, redacted)
		}
	}
}

func TestSecretRedactsInsideAStruct(t *testing.T) {
	t.Parallel()

	// The realistic leak: someone logs or prints the whole config struct.
	cfg := struct {
		Name string
		Pass config.Secret
	}{Name: "svc", Pass: "hunter2"}

	for _, verb := range []string{"%+v", "%#v"} {
		if got := fmt.Sprintf(verb, cfg); strings.Contains(got, "hunter2") {
			t.Errorf("struct formatting %s leaked the secret: %s", verb, got)
		}
	}
}

func TestSecretRedactsUnderJSON(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(struct {
		Pass config.Secret `json:"pass"`
	}{Pass: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("hunter2")) {
		t.Errorf("json.Marshal leaked the secret: %s", b)
	}
}

// Redaction must work through a handler config never imported -- that is the
// reason Secret lives in config rather than in logging.
func TestSecretRedactsUnderPlainSlogJSONHandler(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("boot", "db", config.Secret("hunter2"))

	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("slog.JSONHandler leaked the secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), redacted) {
		t.Errorf("log line = %s, want it to contain %s", buf.String(), redacted)
	}
}

// Reading the real value is possible but must be a visible cast at the call
// site, so a reviewer can see it.
func TestSecretRevealsViaExplicitConversion(t *testing.T) {
	t.Parallel()

	if got := string(config.Secret("hunter2")); got != "hunter2" {
		t.Errorf("string(Secret) = %q, want %q", got, "hunter2")
	}
}

// TestSecretMethodsAreIndividuallyDefined ensures each method is actually
// implemented and cannot be accidentally deleted without breaking compilation.
// This guards against fallback behavior that might hide missing methods.
func TestSecretMethodsAreIndividuallyDefined(t *testing.T) {
	t.Parallel()

	s := config.Secret("hunter2")

	// Test String() method exists and redacts
	got := s.String()
	if got != redacted {
		t.Errorf("String() = %q, want %q", got, redacted)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("String() leaked the secret: %s", got)
	}

	// Test GoString() method exists and redacts
	got = s.GoString()
	if got != redacted {
		t.Errorf("GoString() = %q, want %q", got, redacted)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("GoString() leaked the secret: %s", got)
	}

	// Test LogValue() method exists and redacts
	logVal := s.LogValue()
	logStr := logVal.String()
	if !strings.Contains(logStr, redacted) {
		t.Errorf("LogValue().String() = %q, want it to contain %q", logStr, redacted)
	}
	if strings.Contains(logStr, "hunter2") {
		t.Errorf("LogValue() leaked the secret: %s", logStr)
	}

	// Test MarshalText() method exists and redacts
	b, err := s.MarshalText()
	if err != nil {
		t.Errorf("MarshalText() error = %v, want nil", err)
	}
	if string(b) != redacted {
		t.Errorf("MarshalText() = %q, want %q", string(b), redacted)
	}
	if bytes.Contains(b, []byte("hunter2")) {
		t.Errorf("MarshalText() leaked the secret: %s", b)
	}
}
