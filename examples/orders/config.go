package main

import (
	"time"

	"github.com/pavelpascari/svcrt/config"
)

// AppConfig exercises every config feature the module offers, because this
// example is the acceptance gate for R0 as well as a sample.
type AppConfig struct {
	// Optional: a default makes it so.
	Addr string `env:"ADDR" default:":8080"`

	// Required: no default, so an absent DATABASE_URL fails the boot.
	// Secret keeps it out of logs and JSON dumps.
	DBURL config.Secret `env:"DATABASE_URL"`

	// Tri-state: nil means DEBUG was never set, which is distinct from
	// DEBUG=false.
	Debug *bool `env:"DEBUG"`

	// Nested with a prefix: OTEL_ENDPOINT, OTEL_TIMEOUT.
	OTel OTelConfig `envPrefix:"OTEL_"`

	// Optional block: nil unless some TLS_ variable is set. Once any is set,
	// both Cert and Key become required.
	TLS *TLSConfig `envPrefix:"TLS_"`
}

type OTelConfig struct {
	Endpoint string        `env:"ENDPOINT" default:"localhost:4317"`
	Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
}

type TLSConfig struct {
	Cert string `env:"CERT"`
	Key  string `env:"KEY"`
}
