package config

import (
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// decodeInto builds a decoder for T, runs it, and returns the result.
func decodeInto[T any](t *testing.T, raw string) (T, error) {
	t.Helper()
	var v T
	dec, err := decoderFor(reflect.TypeOf(v))
	if err != nil {
		t.Fatalf("decoderFor(%T): %v", v, err)
	}
	err = dec(raw, reflect.ValueOf(&v).Elem())
	return v, err
}

func TestDecodeScalars(t *testing.T) {
	t.Parallel()

	if v, err := decodeInto[string](t, "hello"); err != nil || v != "hello" {
		t.Errorf("string = (%q, %v)", v, err)
	}
	if v, err := decodeInto[bool](t, "true"); err != nil || v != true {
		t.Errorf("bool = (%v, %v)", v, err)
	}
	if v, err := decodeInto[int](t, "-42"); err != nil || v != -42 {
		t.Errorf("int = (%v, %v)", v, err)
	}
	if v, err := decodeInto[uint16](t, "65535"); err != nil || v != 65535 {
		t.Errorf("uint16 = (%v, %v)", v, err)
	}
	if v, err := decodeInto[float64](t, "1.5"); err != nil || v != 1.5 {
		t.Errorf("float64 = (%v, %v)", v, err)
	}
}

// A named string type (config.Secret is one) must decode as a string.
type namedString string

func TestDecodeNamedStringKind(t *testing.T) {
	t.Parallel()

	if v, err := decodeInto[namedString](t, "x"); err != nil || v != "x" {
		t.Errorf("namedString = (%q, %v)", v, err)
	}
}

// time.Duration is an int64. Without an explicit case ahead of the integer
// kinds it would decode "5s" as a parse failure and "5" as 5 nanoseconds.
func TestDecodeDurationTakesPrecedenceOverInt64(t *testing.T) {
	t.Parallel()

	v, err := decodeInto[time.Duration](t, "1500ms")
	if err != nil {
		t.Fatalf("duration: %v", err)
	}
	if v != 1500*time.Millisecond {
		t.Errorf("duration = %v, want 1.5s", v)
	}
	if _, err := decodeInto[time.Duration](t, "5"); err == nil {
		t.Error("bare 5 decoded as a duration; want a unit to be required")
	}
}

func TestDecodeTextUnmarshaler(t *testing.T) {
	t.Parallel()

	v, err := decodeInto[netip.Addr](t, "10.0.0.5")
	if err != nil {
		t.Fatalf("netip.Addr: %v", err)
	}
	if v.String() != "10.0.0.5" {
		t.Errorf("addr = %v, want 10.0.0.5", v)
	}
}

func TestDecodeSlice(t *testing.T) {
	t.Parallel()

	v, err := decodeInto[[]string](t, "a,b,c")
	if err != nil {
		t.Fatalf("[]string: %v", err)
	}
	if len(v) != 3 || v[0] != "a" || v[2] != "c" {
		t.Errorf("slice = %#v, want [a b c]", v)
	}

	ints, err := decodeInto[[]int](t, "1,2,3")
	if err != nil || len(ints) != 3 || ints[2] != 3 {
		t.Errorf("[]int = (%v, %v)", ints, err)
	}
}

// Same rule as spec §5.4's csv=: empty means empty, not one empty element.
func TestDecodeEmptyStringIsEmptySlice(t *testing.T) {
	t.Parallel()

	v, err := decodeInto[[]string](t, "")
	if err != nil {
		t.Fatalf("[]string: %v", err)
	}
	if v == nil {
		t.Fatal("empty input produced a nil slice; want an allocated empty slice")
	}
	if len(v) != 0 {
		t.Errorf("slice = %#v, want length 0", v)
	}
	if cap(v) != 0 {
		t.Errorf("slice cap = %d, want 0", cap(v))
	}
}

// Elements are not trimmed: no inferred conventions (spec P5).
func TestDecodeSliceDoesNotTrimElements(t *testing.T) {
	t.Parallel()

	v, err := decodeInto[[]string](t, "a, b")
	if err != nil {
		t.Fatal(err)
	}
	if v[1] != " b" {
		t.Errorf("element 1 = %q, want %q", v[1], " b")
	}
}

func TestDecodePointerAllocates(t *testing.T) {
	t.Parallel()

	v, err := decodeInto[*bool](t, "true")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil || *v != true {
		t.Errorf("*bool = %v, want pointer to true", v)
	}
}

func TestDecodePointerElementErrorPropagates(t *testing.T) {
	t.Parallel()

	if _, err := decodeInto[*int](t, "abc"); err == nil {
		t.Error("*int accepted abc")
	}
}

func TestDecodeErrorMessagesAreReadable(t *testing.T) {
	t.Parallel()

	_, err := decodeInto[int](t, "abc")
	if err == nil {
		t.Fatal("int accepted abc")
	}
	want := `invalid int: parsing "abc": invalid syntax`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	_, err = decodeInto[int8](t, "999")
	if err == nil {
		t.Fatal("int8 accepted 999")
	}
	if want := `invalid int8: parsing "999": value out of range`; err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestDecodeScalarErrors(t *testing.T) {
	t.Parallel()

	if _, err := decodeInto[bool](t, "notabool"); err == nil {
		t.Error("bool accepted notabool")
	}
	if _, err := decodeInto[uint8](t, "not-a-uint"); err == nil {
		t.Error("uint8 accepted not-a-uint")
	}
	if _, err := decodeInto[float64](t, "not-a-float"); err == nil {
		t.Error("float64 accepted not-a-float")
	}
}

func TestDecodeSliceElementErrorPropagates(t *testing.T) {
	t.Parallel()

	if _, err := decodeInto[[]int](t, "1,x,3"); err == nil {
		t.Error("[]int accepted 1,x,3")
	}
}

func TestDecoderForSliceElementTypeErrorPropagates(t *testing.T) {
	t.Parallel()

	// The outer slice is fine; it is the element type that has no decoder.
	if _, err := decoderFor(reflect.TypeOf([]struct{ A int }{})); err == nil {
		t.Error("decoderFor accepted []struct{ A int }")
	}
}

func TestDecoderForPointerElementTypeErrorPropagates(t *testing.T) {
	t.Parallel()

	if _, err := decoderFor(reflect.TypeOf(new(struct{ A int }))); err == nil {
		t.Error("decoderFor accepted *struct{ A int }")
	}
}

func TestDecodeTextUnmarshalerError(t *testing.T) {
	t.Parallel()

	if _, err := decodeInto[netip.Addr](t, "not-an-ip"); err == nil {
		t.Error("netip.Addr accepted not-an-ip")
	}
}

func TestDecoderForRejectsUnsupportedTypes(t *testing.T) {
	t.Parallel()

	// A nested collection cannot come from a single environment value.
	if _, err := decoderFor(reflect.TypeOf([][]string{})); err == nil {
		t.Error("decoderFor accepted [][]string")
	}
	if _, err := decoderFor(reflect.TypeOf(map[string]string{})); err == nil {
		t.Error("decoderFor accepted a map")
	}
	if _, err := decoderFor(reflect.TypeOf(struct{ A int }{})); err == nil {
		t.Error("decoderFor accepted a plain struct")
	}
}
