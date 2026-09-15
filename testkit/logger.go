package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/pavelpascari/svcrt/logging"
)

// Record is one captured log line, decoded.
type Record struct {
	Level   string
	Message string
	Attrs   map[string]any
}

// Records holds captured log output.
//
// It is written by whichever goroutine logged and read by the test goroutine,
// so every method takes the mutex. Tests that log concurrently are the normal
// case, not an edge one.
type Records struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer for the logger to write into.
func (r *Records) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// All returns every captured record, oldest first. The slice is a copy.
func (r *Records) All() []Record {
	r.mu.Lock()
	raw := r.buf.String()
	r.mu.Unlock()

	var out []Record
	dec := json.NewDecoder(strings.NewReader(raw))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		out = append(out, recordFrom(m))
	}
	return out
}

// Last returns the most recent record, reporting false when nothing was
// logged. A zero Record with no signal would let a test that expected output
// pass silently.
func (r *Records) Last() (Record, bool) {
	all := r.All()
	if len(all) == 0 {
		return Record{}, false
	}
	return all[len(all)-1], true
}

// Find returns every record whose attribute key equals value, oldest first.
func (r *Records) Find(key string, value any) []Record {
	var out []Record
	for _, rec := range r.All() {
		if v, ok := rec.Attrs[key]; ok && fmt.Sprintf("%v", v) == fmt.Sprintf("%v", value) {
			out = append(out, rec)
		}
	}
	return out
}

func recordFrom(m map[string]any) Record {
	rec := Record{Attrs: make(map[string]any, len(m))}
	for k, v := range m {
		switch k {
		case slog.LevelKey:
			rec.Level, _ = v.(string)
		case slog.MessageKey:
			rec.Message, _ = v.(string)
		case slog.TimeKey:
			// dropped: a timestamp is never what a test asserts on
		default:
			rec.Attrs[k] = v
		}
	}
	return rec
}

// Logger returns a logger writing into the returned Records.
//
// Extractors are forwarded to logging.New, so a consumer testing trace
// correlation writes testkit.Logger(t, telemetry.LogExtractor()) -- and this
// package never imports telemetry, so asserting on a log line does not pull
// the OTel module graph into a consumer's test dependencies.
//
// The level is Debug: a test wants everything its code emitted, and filtering
// is the test's job rather than the helper's.
func Logger(tb TB, ex ...logging.Extractor) (*slog.Logger, *Records) {
	tb.Helper()
	rec := &Records{}
	return logging.New(rec, logging.Options{Level: slog.LevelDebug}, ex...), rec
}
