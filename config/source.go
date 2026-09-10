// Package config decodes environment variables into a struct once, at
// construction, and reports every violation at once.
//
// It reads only the environment (12-factor III). There is no file fallback in
// production: Dotenv exists for local development and must be named explicitly
// in main.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Source reports the value of a configuration key and whether it is set.
//
// Presence is distinct from emptiness: a variable set to the empty string
// returns ("", true) and must not be treated as absent. Source is the single
// seam config offers -- it is the production reader, the layering point, and
// the test double, so tests never need t.Setenv and always run in parallel.
type Source func(key string) (value string, ok bool)

// OSEnv reads the process environment.
func OSEnv() Source { return os.LookupEnv }

// Layer returns a Source that consults srcs in order and returns the first
// that reports the key as set.
//
// Put the real environment first -- Layer(OSEnv(), dot) -- so a stray .env
// file can never shadow a deployed value.
func Layer(srcs ...Source) Source {
	return func(key string) (string, bool) {
		for _, s := range srcs {
			if v, ok := s(key); ok {
				return v, true
			}
		}
		return "", false
	}
}

// Dotenv reads a .env file into a Source. It is for local development only.
//
// The accepted grammar is deliberately small:
//
//   - Blank lines and lines whose first non-space character is # are ignored.
//   - Each remaining line is KEY=VALUE, split at the first =. A line with no =
//     is an error naming its line number.
//   - An optional "export " prefix on the key is stripped.
//   - Key and value are trimmed of surrounding whitespace.
//   - A value wrapped in double quotes is unquoted with Go escape rules.
//   - A value wrapped in single quotes is taken literally.
//   - A duplicate key is an error. Last-wins is too surprising to allow
//     silently.
func Dotenv(path string) (Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: dotenv: %w", err)
	}

	vals := make(map[string]string)
	for i, line := range strings.Split(string(b), "\n") {
		lineNo := i + 1

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, rawVal, found := strings.Cut(trimmed, "=")
		if !found {
			return nil, fmt.Errorf("config: dotenv %s: line %d: not a KEY=VALUE assignment", path, lineNo)
		}

		key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "export "))
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("config: dotenv %s: line %d: empty key", path, lineNo)
		}
		if _, dup := vals[key]; dup {
			return nil, fmt.Errorf("config: dotenv %s: line %d: duplicate key %s", path, lineNo, key)
		}

		val, err := unquote(strings.TrimSpace(rawVal))
		if err != nil {
			return nil, fmt.Errorf("config: dotenv %s: line %d: %w", path, lineNo, err)
		}
		vals[key] = val
	}

	return func(key string) (string, bool) {
		v, ok := vals[key]
		return v, ok
	}, nil
}

func unquote(s string) (string, error) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		v, err := strconv.Unquote(s)
		if err != nil {
			return "", fmt.Errorf("malformed double-quoted value: %w", err)
		}
		return v, nil
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1], nil
	}
	return s, nil
}
