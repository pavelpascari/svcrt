package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pavelpascari/svcrt/config"
)

// mapSource is the pattern every config test uses instead of t.Setenv: it
// touches no process state, so tests run in parallel.
func mapSource(m map[string]string) config.Source {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLayerFirstHitWins(t *testing.T) {
	t.Parallel()

	high := mapSource(map[string]string{"A": "from-high"})
	low := mapSource(map[string]string{"A": "from-low", "B": "only-low"})

	s := config.Layer(high, low)

	if v, ok := s("A"); !ok || v != "from-high" {
		t.Errorf("A = (%q, %v), want (%q, true)", v, ok, "from-high")
	}
	if v, ok := s("B"); !ok || v != "only-low" {
		t.Errorf("B = (%q, %v), want (%q, true)", v, ok, "only-low")
	}
	if _, ok := s("MISSING"); ok {
		t.Error("MISSING reported present")
	}
}

func TestLayerPreservesEmptyStringAsPresent(t *testing.T) {
	t.Parallel()

	// An explicitly empty value is set. It must stop the search rather than
	// falling through to a lower layer.
	s := config.Layer(
		mapSource(map[string]string{"A": ""}),
		mapSource(map[string]string{"A": "fallback"}),
	)

	v, ok := s("A")
	if !ok {
		t.Fatal("A reported absent, want present")
	}
	if v != "" {
		t.Errorf("A = %q, want empty string", v)
	}
}

func TestOSEnvReadsProcessEnvironment(t *testing.T) {
	t.Setenv("SVCRT_TEST_OSENV", "hello")

	if v, ok := config.OSEnv()("SVCRT_TEST_OSENV"); !ok || v != "hello" {
		t.Errorf("= (%q, %v), want (%q, true)", v, ok, "hello")
	}
}

func TestDotenvParsesSupportedForms(t *testing.T) {
	t.Parallel()

	content := "" +
		"# a comment\n" +
		"\n" +
		"PLAIN=value\n" +
		"  SPACED  =  spaced value  \n" +
		"export EXPORTED=exported\n" +
		"DQUOTED=\"has \\\"quotes\\\" and\\ttab\"\n" +
		"SQUOTED='raw \\n not escaped'\n" +
		"EMPTY=\n" +
		"HAS_EQUALS=a=b=c\n"

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := config.Dotenv(path)
	if err != nil {
		t.Fatalf("Dotenv: %v", err)
	}

	want := map[string]string{
		"PLAIN":      "value",
		"SPACED":     "spaced value",
		"EXPORTED":   "exported",
		"DQUOTED":    "has \"quotes\" and\ttab",
		"SQUOTED":    `raw \n not escaped`,
		"EMPTY":      "",
		"HAS_EQUALS": "a=b=c",
	}
	for k, w := range want {
		if got, ok := s(k); !ok || got != w {
			t.Errorf("%s = (%q, %v), want (%q, true)", k, got, ok, w)
		}
	}
	if _, ok := s("# a comment"); ok {
		t.Error("comment line was parsed as a key")
	}
}

func TestDotenvRejectsMalformedLine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GOOD=1\nthis is not an assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Dotenv(path)
	if err == nil {
		t.Fatal("Dotenv accepted a malformed line")
	}
	if got := err.Error(); !strings.Contains(got, "line 2") {
		t.Errorf("error %q does not name the offending line", got)
	}
}

func TestDotenvRejectsDuplicateKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=1\nA=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Dotenv(path); err == nil {
		t.Fatal("Dotenv accepted a duplicate key; last-wins is too surprising to allow")
	}
}

func TestDotenvReportsMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := config.Dotenv(filepath.Join(t.TempDir(), "nope.env")); err == nil {
		t.Fatal("Dotenv accepted a missing file")
	}
}

func TestDotenvRejectsEmptyKey(t *testing.T) {
	t.Parallel()

	// "export " with nothing else becomes empty after removing the prefix
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("  =value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Dotenv(path)
	if err == nil {
		t.Fatal("Dotenv accepted an empty key")
	}
	if got := err.Error(); !strings.Contains(got, "empty key") {
		t.Errorf("error %q does not mention empty key", got)
	}
}

func TestDotenvHandlesInvalidDoubleQuoteEscape(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(`KEY="invalid \z escape"`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Dotenv(path)
	if err == nil {
		t.Fatal("Dotenv accepted invalid escape sequence in double-quoted value")
	}
}

func TestDotenvDoubleQuoteExactlyTwoCharacters(t *testing.T) {
	t.Parallel()

	content := `KEY1=""
KEY2="a"`

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := config.Dotenv(path)
	if err != nil {
		t.Fatalf("Dotenv: %v", err)
	}

	// "" is valid and becomes empty string
	if v, ok := s("KEY1"); !ok || v != "" {
		t.Errorf("KEY1 = (%q, %v), want empty string", v, ok)
	}

	// "a" becomes a
	if v, ok := s("KEY2"); !ok || v != "a" {
		t.Errorf("KEY2 = (%q, %v), want a", v, ok)
	}
}

func TestDotenvSingleQuoteExactlyTwoCharacters(t *testing.T) {
	t.Parallel()

	content := `KEY1=''
KEY2='a'`

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := config.Dotenv(path)
	if err != nil {
		t.Fatalf("Dotenv: %v", err)
	}

	// '' is valid and becomes empty string
	if v, ok := s("KEY1"); !ok || v != "" {
		t.Errorf("KEY1 = (%q, %v), want empty string", v, ok)
	}

	// 'a' becomes a
	if v, ok := s("KEY2"); !ok || v != "a" {
		t.Errorf("KEY2 = (%q, %v), want a", v, ok)
	}
}

func TestDotenvSingleQuotePreservesEscapes(t *testing.T) {
	t.Parallel()

	content := `KEY='escaped\ttab'`

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := config.Dotenv(path)
	if err != nil {
		t.Fatalf("Dotenv: %v", err)
	}

	// Single quoted value preserves literal backslash and t
	if v, ok := s("KEY"); !ok || v != `escaped\ttab` {
		t.Errorf("KEY = %q, want literal \\ttab", v)
	}
}

func TestDotenvQuoteExactlyOneCharacter(t *testing.T) {
	t.Parallel()

	// Single quote or double quote alone are not treated as quoted
	content := `KEY1="
KEY2='
KEY3=a"
KEY4=a'`

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := config.Dotenv(path)
	if err != nil {
		t.Fatalf("Dotenv: %v", err)
	}

	// These are not quoted, so passed as-is
	if v, ok := s("KEY1"); !ok || v != `"` {
		t.Errorf("KEY1 = %q", v)
	}
	if v, ok := s("KEY2"); !ok || v != `'` {
		t.Errorf("KEY2 = %q", v)
	}
	if v, ok := s("KEY3"); !ok || v != `a"` {
		t.Errorf("KEY3 = %q", v)
	}
	if v, ok := s("KEY4"); !ok || v != `a'` {
		t.Errorf("KEY4 = %q", v)
	}
}
