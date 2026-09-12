package main

import "time"

// WorkerConfig is deliberately smaller than the service's: a worker needs an
// admin port and a drain delay, and nothing else here.
type WorkerConfig struct {
	AdminAddr  string        `env:"ADMIN_ADDR" default:":9090"`
	DrainDelay time.Duration `env:"DRAIN_DELAY" default:"5s"`
}
