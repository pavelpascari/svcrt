package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pavelpascari/svcrt/resilience"
	"github.com/pavelpascari/svcrt/telemetry"
	"github.com/pavelpascari/svcrt/testkit"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestAcceptanceNotificationIsQueuedRetriedAndDelivered(t *testing.T) {
	provider := testkit.Upstream(t,
		testkit.Status(http.StatusServiceUnavailable),
		testkit.Status(http.StatusAccepted),
	)
	log, records := testkit.Logger(t)

	stack := buildStack(stackConfig{
		APIAddr:     "127.0.0.1:0",
		AdminAddr:   "127.0.0.1:0",
		ProviderURL: provider.URL(),
		Logger:      log,
		Retry:       resilience.Policy{MaxAttempts: 2, Backoff: resilience.Constant(time.Millisecond)},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- stack.lifecycle.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("lifecycle: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("lifecycle did not stop")
		}
	})

	adminAddr := waitForBoundAddress(t, stack.admin)
	apiAddr := waitForBoundAddress(t, stack.api)
	waitForStatus(t, "http://"+adminAddr+"/readyz", http.StatusOK)

	body := bytes.NewBufferString(`{"recipient":"ada@example.com","message":"build complete"}`)
	resp, err := http.Post("http://"+apiAddr+"/notifications", "application/json", body)
	if err != nil {
		t.Fatalf("POST /notifications: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /notifications = %d, want 202", resp.StatusCode)
	}

	var accepted struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode accepted notification: %v", err)
	}
	if accepted.ID != "notification-1" || accepted.Status != "queued" {
		t.Fatalf("accepted = %+v, want notification-1 queued", accepted)
	}

	waitForNotificationStatus(t, apiAddr, accepted.ID, "delivered")
	if got := provider.Requests(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
	if got := len(records.Find("notification_id", accepted.ID)); got == 0 {
		t.Fatal("no structured log record carries the notification id")
	}
}

func TestAcceptanceInvalidNotificationReturnsContractErrorWithoutCallingProvider(t *testing.T) {
	provider := testkit.Upstream(t, testkit.Status(http.StatusAccepted))
	log, _ := testkit.Logger(t)
	stack := buildStack(stackConfig{
		APIAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0",
		ProviderURL: provider.URL(), Logger: log,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- stack.lifecycle.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("lifecycle: %v", err)
		}
	})

	apiAddr := waitForBoundAddress(t, stack.api)
	body := bytes.NewBufferString(`{"recipient":"","message":"build complete"}`)
	resp, err := http.Post("http://"+apiAddr+"/notifications", "application/json", body)
	if err != nil {
		t.Fatalf("POST /notifications: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /notifications = %d, want 400", resp.StatusCode)
	}

	var envelope struct {
		Error struct {
			Code   string         `json:"code"`
			Params map[string]any `json:"params"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.Code != "notification.invalid" {
		t.Errorf("code = %q, want notification.invalid", envelope.Error.Code)
	}
	if envelope.Error.Params["field"] != "recipient" {
		t.Errorf("params = %v, want field=recipient", envelope.Error.Params)
	}
	if got := provider.Requests(); got != 0 {
		t.Fatalf("provider requests = %d, want 0", got)
	}
}

func TestAcceptanceTraceCrossesTheQueueIntoProviderAndWorkerLog(t *testing.T) {
	providerTrace := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerTrace <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(provider.Close)

	log, records := testkit.Logger(t, telemetry.LogExtractor())
	stack := buildStack(stackConfig{
		APIAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0",
		ProviderURL: provider.URL, Logger: log,
		Retry: resilience.Policy{MaxAttempts: 1},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- stack.lifecycle.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("lifecycle: %v", err)
		}
	})

	apiAddr := waitForBoundAddress(t, stack.api)
	req, err := http.NewRequest(http.MethodPost, "http://"+apiAddr+"/notifications",
		bytes.NewBufferString(`{"recipient":"ada@example.com","message":"build complete"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", testTraceparent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /notifications: %v", err)
	}
	resp.Body.Close()

	select {
	case got := <-providerTrace:
		if got != testTraceparent {
			t.Fatalf("provider traceparent = %q, want %q", got, testTraceparent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not called")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		matches := records.Find(telemetry.KeyTraceID, "4bf92f3577b34da6a3ce929d0e0e4736")
		for _, record := range matches {
			if record.Message == "notification delivered" && record.Attrs["notification_id"] == "notification-1" {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("delivery log did not retain the inbound trace id across the queue")
}

func TestAcceptanceShutdownDrainsAQueuedDelivery(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(provider.Close)

	log, records := testkit.Logger(t)
	stack := buildStack(stackConfig{
		APIAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0",
		ProviderURL: provider.URL, Logger: log, DrainDelay: 100 * time.Millisecond,
		Retry: resilience.Policy{MaxAttempts: 1},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := false
	go func() { done <- stack.lifecycle.Run(ctx) }()
	t.Cleanup(func() {
		if stopped {
			return
		}
		cancel()
		select {
		case release <- struct{}{}:
		default:
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("lifecycle did not stop")
		}
	})

	apiAddr := waitForBoundAddress(t, stack.api)
	adminAddr := waitForBoundAddress(t, stack.admin)
	waitForStatus(t, "http://"+adminAddr+"/readyz", http.StatusOK)
	resp, err := http.Post("http://"+apiAddr+"/notifications", "application/json",
		bytes.NewBufferString(`{"recipient":"ada@example.com","message":"build complete"}`))
	if err != nil {
		t.Fatalf("POST /notifications: %v", err)
	}
	resp.Body.Close()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider delivery did not start")
	}
	cancel()
	waitForStatus(t, "http://"+adminAddr+"/readyz", http.StatusServiceUnavailable)

	select {
	case err := <-done:
		t.Fatalf("lifecycle returned before queued delivery completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}

	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("lifecycle: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle did not finish after delivery completed")
	}
	if got := len(records.Find("notification_id", "notification-1")); got == 0 {
		t.Fatal("drained notification has no completion log")
	}
}

type addressReporter interface {
	Addr() string
}

func waitForBoundAddress(t *testing.T, server addressReporter) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := server.Addr(); addr != "127.0.0.1:0" {
			return addr
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server did not bind")
	return ""
}

func waitForStatus(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s did not reach status %d", url, want)
}

func waitForNotificationStatus(t *testing.T, addr, id, want string) {
	t.Helper()
	url := "http://" + addr + "/notifications/" + id
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			var body struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if decodeErr == nil && body.Status == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("notification %s did not reach %q", id, want)
}
