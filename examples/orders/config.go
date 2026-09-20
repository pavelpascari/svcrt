package main

import (
	"time"

	"github.com/pavelpascari/svcrt/config"
)

// AppConfig exercises most of what svcrt/config offers -- a default, a
// required field, a tri-state pointer, a prefixed nested struct, an optional
// nested block, a Secret and a duration -- so this type doubles as a worked
// reference for the config module as well as this service's configuration.
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

	// AdminAddr is the admin surface: probes, and pprof if you mount it. It
	// belongs on a port separate from application traffic so it is not
	// publicly routable.
	AdminAddr string `env:"ADMIN_ADDR" default:":9090"`

	// DrainDelay is how long to keep serving after readiness goes false, so a
	// load balancer notices before the listener closes. Zero drops traffic
	// that arrives in that window.
	DrainDelay time.Duration `env:"DRAIN_DELAY" default:"5s"`

	// PricingURL is the base URL of the upstream pricing service.
	PricingURL string `env:"PRICING_URL" default:"http://localhost:9091"`
}

type OTelConfig struct {
	Endpoint string        `env:"ENDPOINT" default:"localhost:4317"`
	Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
}

type TLSConfig struct {
	Cert string `env:"CERT"`
	Key  string `env:"KEY"`
}
