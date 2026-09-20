package main

import (
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/config"
)

func TestConfigLoadsMinimalEnvironment(t *testing.T) {
	t.Parallel()
	values := map[string]string{"PROVIDER_URL": "https://provider.example"}
	source := config.Source(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})

	cfg, err := config.Load[AppConfig](config.WithSource(source))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIAddr != ":8080" || cfg.AdminAddr != ":9090" {
		t.Errorf("addresses = %q %q, want :8080 :9090", cfg.APIAddr, cfg.AdminAddr)
	}
	if cfg.DrainDelay != 5*time.Second {
		t.Errorf("DrainDelay = %s, want 5s", cfg.DrainDelay)
	}
}

func TestConfigRejectsNonHTTPProviderURL(t *testing.T) {
	t.Parallel()
	values := map[string]string{"PROVIDER_URL": "provider.internal"}
	source := config.Source(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})

	if _, err := config.Load[AppConfig](config.WithSource(source)); err == nil {
		t.Fatal("Load succeeded with a provider URL that has no HTTP scheme")
	}
}
