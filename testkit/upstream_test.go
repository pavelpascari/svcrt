package testkit

import (
	"io"
	"net/http"
	"sync"
	"testing"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

// TestUpstreamRepeatsItsLastResponse is the scripting contract. Repeat-last
// rather than exhaust-and-fail because both shapes a test needs fall out of
// it: "fails twice then recovers" is a 3-element script, and "is simply down"
// is a 1-element one.
func TestUpstreamRepeatsItsLastResponse(t *testing.T) {
	f := &fakeTB{}
	up := Upstream(f, Status(503), Status(503), JSON(200, `{"ok":true}`))
	defer f.runCleanups()

	for i, want := range []int{503, 503, 200, 200, 200} {
		got, _ := get(t, up.URL())
		if got != want {
			t.Fatalf("request %d: status = %d, want %d", i+1, got, want)
		}
	}
	if n := up.Requests(); n != 5 {
		t.Errorf("Requests() = %d, want 5", n)
	}
	if f.helpers == 0 {
		t.Error("Upstream did not call Helper")
	}
}

func TestUpstreamEmptyScriptAnswers200(t *testing.T) {
	f := &fakeTB{}
	up := Upstream(f)
	defer f.runCleanups()

	status, body := get(t, up.URL())
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
	// Repeat-last on an empty script is still 200, not a panic.
	if status, _ := get(t, up.URL()); status != http.StatusOK {
		t.Errorf("second request: status = %d, want 200", status)
	}
}

// TestUpstreamZeroStatusMeans200: a Response built by hand rather than through
// Status or JSON leaves Status at 0, and "0" is not a status code a client can
// receive. The helper reads it as the unset it is.
func TestUpstreamZeroStatusMeans200(t *testing.T) {
	f := &fakeTB{}
	up := Upstream(f, Response{Body: "hi"})
	defer f.runCleanups()

	status, body := get(t, up.URL())
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if body != "hi" {
		t.Errorf("body = %q, want hi", body)
	}
}

func TestUpstreamServesBodyAndHeaders(t *testing.T) {
	f := &fakeTB{}
	up := Upstream(f, JSON(201, `{"id":"1"}`))
	defer f.runCleanups()

	resp, err := http.Get(up.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"id":"1"}` {
		t.Errorf("body = %q", body)
	}
}

// TestUpstreamServesRepeatedHeaderValues: Header is an http.Header, so a
// scripted response carrying two values under one key must serve both.
func TestUpstreamServesRepeatedHeaderValues(t *testing.T) {
	f := &fakeTB{}
	h := make(http.Header)
	h.Add("X-Trial", "one")
	h.Add("X-Trial", "two")
	up := Upstream(f, Response{Status: 200, Header: h})
	defer f.runCleanups()

	resp, err := http.Get(up.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Values("X-Trial"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("X-Trial = %v, want [one two]", got)
	}
}

// TestUpstreamShutsDownViaCleanup: the test never closes it, which is the
// whole reason Cleanup is in the TB interface.
func TestUpstreamShutsDownViaCleanup(t *testing.T) {
	f := &fakeTB{}
	up := Upstream(f, Status(200))
	url := up.URL()

	if status, _ := get(t, url); status != http.StatusOK {
		t.Fatalf("upstream not serving before cleanup: status %d", status)
	}
	f.runCleanups()

	if resp, err := http.Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("upstream still serving after cleanup ran")
	}
}

// TestUpstreamCountsConcurrentRequests: Requests() is read from the test
// goroutine while the server's handlers write it.
func TestUpstreamCountsConcurrentRequests(t *testing.T) {
	f := &fakeTB{}
	up := Upstream(f, Status(200))
	defer f.runCleanups()

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(up.URL())
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	if n := up.Requests(); n != 32 {
		t.Fatalf("Requests() = %d, want 32", n)
	}
}

func TestStatusAndJSONConstructors(t *testing.T) {
	if got := Status(503); got.Status != 503 || got.Body != "" || got.Header != nil {
		t.Errorf("Status(503) = %+v", got)
	}
	got := JSON(200, `{}`)
	if got.Status != 200 || got.Body != `{}` {
		t.Errorf("JSON(200, {}) = %+v", got)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("JSON Content-Type = %q", ct)
	}
}
