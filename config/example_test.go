package config_test

import (
	"errors"
	"fmt"
	"time"

	"github.com/pavelpascari/svcrt/config"
)

// ExampleLoad decodes a struct once, at startup. Every field carries an env
// tag: there is no inference from field names.
func ExampleLoad() {
	type Config struct {
		Addr    string        `env:"ADDR" default:":8080"`
		DBURL   config.Secret `env:"DATABASE_URL"`
		Timeout time.Duration `env:"TIMEOUT" default:"5s"`
	}

	// A Source is the one seam config offers. Production passes nothing and
	// reads config.OSEnv(); a test passes a map, so it never touches process
	// state and can run in parallel.
	env := map[string]string{
		"DATABASE_URL": "postgres://orders:hunter2@db:5432/orders",
		"TIMEOUT":      "2s",
	}
	src := config.Source(func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	})

	cfg, err := config.Load[Config](config.WithSource(src))
	if err != nil {
		fmt.Println(err)
		return
	}

	// DBURL is a Secret, so printing it redacts. Reading the real value needs
	// an explicit string(cfg.DBURL), which is visible at the call site.
	fmt.Printf("addr=%s timeout=%s db=%s\n", cfg.Addr, cfg.Timeout, cfg.DBURL)

	// Output:
	// addr=:8080 timeout=2s db=[REDACTED]
}

// ExampleLoad_violations shows the other half of Load's contract: it never
// stops at the first problem, so an operator fixing configuration sees the
// whole list in one restart rather than discovering it one restart at a time.
func ExampleLoad_violations() {
	type Config struct {
		Addr  string `env:"ADDR"`
		Port  int    `env:"PORT"`
		Debug bool   `env:"DEBUG" default:"false"`
	}

	env := map[string]string{"PORT": "not-a-number", "DEBUG": "maybe"}
	src := config.Source(func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	})

	_, err := config.Load[Config](config.WithSource(src))

	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) {
		fmt.Println("unexpected:", err)
		return
	}
	// Violations are reported in field order, and Kind says whose problem it
	// is: a schema violation is the programmer's, the rest are the operator's.
	for _, v := range cfgErr.Violations {
		fmt.Printf("%s: %s\n", v.Kind, v.Env)
	}

	// Output:
	// required: ADDR
	// decode: PORT
	// decode: DEBUG
}
