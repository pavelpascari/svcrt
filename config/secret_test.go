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

	for _, verb := range []string{"%v", "%s", "%q"} {
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

	if got := fmt.Sprintf("%+v", cfg); strings.Contains(got, "hunter2") {
		t.Errorf("struct formatting leaked the secret: %s", got)
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

func TestSecretDecodesFromConfig(t *testing.T) {
	t.Skip("Load lands in Task 8")
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
