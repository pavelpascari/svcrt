package main

import (
	"fmt"
	"net/url"
	"time"
)

type AppConfig struct {
	APIAddr     string        `env:"ADDR" default:":8080"`
	AdminAddr   string        `env:"ADMIN_ADDR" default:":9090"`
	ProviderURL string        `env:"PROVIDER_URL"`
	DrainDelay  time.Duration `env:"DRAIN_DELAY" default:"5s"`
}

func (c AppConfig) Validate() error {
	u, err := url.Parse(c.ProviderURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("PROVIDER_URL must be an absolute HTTP or HTTPS URL")
	}
	return nil
}
