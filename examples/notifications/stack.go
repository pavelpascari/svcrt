package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/pavelpascari/svcrt/httpclient"
	"github.com/pavelpascari/svcrt/httpserver"
	"github.com/pavelpascari/svcrt/lifecycle"
	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/telemetry"
)

type stackConfig struct {
	APIAddr     string
	AdminAddr   string
	ProviderURL string
	DrainDelay  time.Duration
	Logger      *slog.Logger
	Retry       resilience.Policy
}

type appStack struct {
	lifecycle *lifecycle.Lifecycle
	health    *httpserver.Health
	api       *httpserver.Server
	admin     *httpserver.Server
}

func buildStack(cfg stackConfig) *appStack {
	lc := lifecycle.New(lifecycle.Config{Logger: cfg.Logger, DrainDelay: cfg.DrainDelay})
	health := httpserver.NewHealth()
	lc.OnStarted(health.Up)
	lc.OnDrain(health.Drain)

	client := httpclient.New(httpclient.Options{
		Middleware: httpclient.Chain(
			resilience.Breaker(resilience.BreakerPolicy{}),
			resilience.Retry(cfg.Retry),
			telemetry.Client(telemetry.Options{}),
		),
	})
	provider := &providerClient{baseURL: cfg.ProviderURL, http: client}
	clientRef := lc.Add("provider-client",
		func(context.Context) error { return nil },
		func(context.Context) error {
			provider.closeIdleConnections()
			return nil
		})

	store := newNotificationStore()
	worker := newDeliveryWorker(store, provider, cfg.Logger)
	workerRef := lc.Add("delivery-worker", worker.start, worker.stop, lifecycle.After(clientRef))

	api := httpserver.New(newHandler(store, cfg.Logger), httpserver.Options{
		Addr: cfg.APIAddr, OnServeError: lc.Fatal,
	})
	lc.Add("api", api.Start, api.Shutdown, lifecycle.After(workerRef))

	admin := httpserver.New(httpserver.AdminMux(health), httpserver.Options{
		Addr: cfg.AdminAddr, OnServeError: lc.Fatal,
	})
	lc.Add("admin", admin.Start, admin.Shutdown, lifecycle.After(workerRef))

	return &appStack{lifecycle: lc, health: health, api: api, admin: admin}
}
