package testkit

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

func TestLoggerCapturesLevelMessageAndAttrs(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)

	log.Info("hello", "code", "order_not_found", "count", 3)

	got, ok := rec.Last()
	if !ok {
		t.Fatal("no record captured")
	}
	if got.Message != "hello" {
		t.Errorf("Message = %q", got.Message)
	}
	if got.Level != "INFO" {
		t.Errorf("Level = %q, want INFO", got.Level)
	}
	if got.Attrs["code"] != "order_not_found" {
		t.Errorf("Attrs[code] = %v", got.Attrs["code"])
	}
	if f.helpers == 0 {
		t.Error("Logger did not call Helper")
	}
}

// TestLoggerCapturesDebug: the helper sets Debug so a test sees everything its
// code emitted. logging.New defaults to Info, so this is a real choice.
func TestLoggerCapturesDebug(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)

	log.Debug("quiet")

	if _, ok := rec.Last(); !ok {
		t.Fatal("Logger filtered out a Debug record")
	}
}

// TestLoggerDropsTime: a timestamp is never what a test asserts on, and
// leaving it in Attrs would make an exact-attrs assertion impossible.
func TestLoggerDropsTime(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)

	log.Info("hello")

	got, _ := rec.Last()
	if _, ok := got.Attrs[slog.TimeKey]; ok {
		t.Errorf("time leaked into Attrs: %v", got.Attrs)
	}
	if len(got.Attrs) != 0 {
		t.Errorf("Attrs = %v, want empty", got.Attrs)
	}
}

// TestLoggerForwardsExtractors is why Logger is variadic: a consumer supplies
// telemetry.LogExtractor() themselves, so testkit never imports telemetry and
// never pulls OTel into a consumer's test dependencies.
func TestLoggerForwardsExtractors(t *testing.T) {
	f := &fakeTB{}
	marker := func(context.Context) []slog.Attr {
		return []slog.Attr{slog.String("trace_id", "abc123")}
	}
	log, rec := Logger(f, marker)

	log.InfoContext(context.Background(), "hello")

	got, _ := rec.Last()
	if got.Attrs["trace_id"] != "abc123" {
		t.Fatalf("extractor attrs did not reach the record: %v", got.Attrs)
	}
}

func TestRecordsLastReportsAbsence(t *testing.T) {
	f := &fakeTB{}
	_, rec := Logger(f)
	if _, ok := rec.Last(); ok {
		t.Fatal("Last reported a record from an empty log")
	}
}

func TestRecordsAllOnAnEmptyLogIsEmpty(t *testing.T) {
	f := &fakeTB{}
	_, rec := Logger(f)
	if n := len(rec.All()); n != 0 {
		t.Fatalf("All() = %d records on an empty log, want 0", n)
	}
}

func TestRecordsAllAndFind(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)

	log.Info("first", "code", "a")
	log.Info("second", "code", "b")
	log.Info("third", "code", "a")

	if n := len(rec.All()); n != 3 {
		t.Fatalf("All() = %d records, want 3", n)
	}
	found := rec.Find("code", "a")
	if len(found) != 2 {
		t.Fatalf("Find(code, a) = %d records, want 2", len(found))
	}
	if found[0].Message != "first" || found[1].Message != "third" {
		t.Errorf("Find returned %q and %q, want first and third",
			found[0].Message, found[1].Message)
	}
	if len(rec.Find("code", "nope")) != 0 {
		t.Error("Find matched a value that was never logged")
	}
	if len(rec.Find("absent_key", "a")) != 0 {
		t.Error("Find matched on a key no record carries")
	}
}

// TestRecordsFindIgnoresNumericWidth: a JSON round trip turns an int 3 into a
// float64 3, so a test asking for 3 must not have to know that.
func TestRecordsFindIgnoresNumericWidth(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)

	log.Info("counted", "count", 3)

	if n := len(rec.Find("count", 3)); n != 1 {
		t.Fatalf("Find(count, 3) = %d records, want 1", n)
	}
}

// TestRecordsAllReturnsACopy: a caller mutating the returned slice must not
// corrupt the recorder.
func TestRecordsAllReturnsACopy(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)
	log.Info("one")

	all := rec.All()
	all[0].Message = "mutated"

	again, _ := rec.Last()
	if again.Message != "one" {
		t.Fatalf("mutating All()'s result changed the recorder: %q", again.Message)
	}
}

// TestRecordsIsSafeUnderConcurrentLogging: the logger is written from many
// goroutines and read from the test goroutine. -race asserts the rest.
func TestRecordsIsSafeUnderConcurrentLogging(t *testing.T) {
	f := &fakeTB{}
	log, rec := Logger(f)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range 25 {
				log.Info("concurrent", "worker", i)
			}
		}(i)
	}
	// Read while writes are in flight.
	for range 50 {
		_ = rec.All()
	}
	wg.Wait()

	if n := len(rec.All()); n != 32*25 {
		t.Fatalf("captured %d records, want %d", n, 32*25)
	}
}
