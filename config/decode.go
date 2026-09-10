package config

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// decoder writes the parsed form of raw into dst, which is settable and of
// the type the decoder was built for.
type decoder func(raw string, dst reflect.Value) error

var (
	durationType        = reflect.TypeOf(time.Duration(0))
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

// decoderFor builds a decoder for t, or reports that t is unsupported.
//
// It is called during planning, never on a hot path.
func decoderFor(t reflect.Type) (decoder, error) {
	// time.Duration is an int64, so it must be matched before the integer
	// kinds or "5s" fails to parse and "5" silently means 5 nanoseconds.
	if t == durationType {
		return decodeDuration, nil
	}

	// TextUnmarshaler covers time.Time, netip.Addr, and any user type. It is
	// checked before the kind switch so a named type's own parsing wins.
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return decodeText, nil
	}

	switch t.Kind() {
	case reflect.String:
		return func(raw string, dst reflect.Value) error {
			dst.SetString(raw)
			return nil
		}, nil

	case reflect.Bool:
		return func(raw string, dst reflect.Value) error {
			v, err := strconv.ParseBool(raw)
			if err != nil {
				return numError("bool", err)
			}
			dst.SetBool(v)
			return nil
		}, nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		name := t.Kind().String()
		bits := t.Bits()
		return func(raw string, dst reflect.Value) error {
			v, err := strconv.ParseInt(raw, 10, bits)
			if err != nil {
				return numError(name, err)
			}
			dst.SetInt(v)
			return nil
		}, nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		name := t.Kind().String()
		bits := t.Bits()
		return func(raw string, dst reflect.Value) error {
			v, err := strconv.ParseUint(raw, 10, bits)
			if err != nil {
				return numError(name, err)
			}
			dst.SetUint(v)
			return nil
		}, nil

	case reflect.Float32, reflect.Float64:
		name := t.Kind().String()
		bits := t.Bits()
		return func(raw string, dst reflect.Value) error {
			v, err := strconv.ParseFloat(raw, bits)
			if err != nil {
				return numError(name, err)
			}
			dst.SetFloat(v)
			return nil
		}, nil

	case reflect.Slice:
		elem, err := decoderFor(t.Elem())
		if err != nil {
			return nil, fmt.Errorf("slice of %s: %w", t.Elem(), err)
		}
		if t.Elem().Kind() == reflect.Slice {
			return nil, fmt.Errorf("unsupported type %s: nested collections cannot come from one value", t)
		}
		return func(raw string, dst reflect.Value) error {
			// Same rule as the generator's csv= binding: empty means empty,
			// not a single empty element.
			if raw == "" {
				dst.Set(reflect.MakeSlice(t, 0, 0))
				return nil
			}
			parts := strings.Split(raw, ",")
			out := reflect.MakeSlice(t, len(parts), len(parts))
			for i, p := range parts {
				if err := elem(p, out.Index(i)); err != nil {
					return fmt.Errorf("element %d: %w", i, err)
				}
			}
			dst.Set(out)
			return nil
		}, nil

	case reflect.Pointer:
		elem, err := decoderFor(t.Elem())
		if err != nil {
			return nil, err
		}
		return func(raw string, dst reflect.Value) error {
			p := reflect.New(t.Elem())
			if err := elem(raw, p.Elem()); err != nil {
				return err
			}
			dst.Set(p)
			return nil
		}, nil

	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
}

func decodeDuration(raw string, dst reflect.Value) error {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration: %q (want a unit, such as 1500ms or 5s)", raw)
	}
	dst.SetInt(int64(d))
	return nil
}

func decodeText(raw string, dst reflect.Value) error {
	u, ok := dst.Addr().Interface().(encoding.TextUnmarshaler)
	if !ok {
		return fmt.Errorf("internal: %s is not a TextUnmarshaler", dst.Type())
	}
	if err := u.UnmarshalText([]byte(raw)); err != nil {
		return fmt.Errorf("invalid %s: %w", dst.Type(), err)
	}
	return nil
}

// numError rewrites strconv's error to name the target type rather than the
// strconv function, so the message reads "invalid int: ..." not
// "strconv.ParseInt: ...".
func numError(typeName string, err error) error {
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		return fmt.Errorf("invalid %s: parsing %q: %w", typeName, ne.Num, ne.Err)
	}
	return fmt.Errorf("invalid %s: %w", typeName, err)
}

// Load is a placeholder stub. Full implementation lands in Task 8.
func Load[T any](opts ...any) (T, error) {
	var zero T
	return zero, fmt.Errorf("Load not yet implemented")
}

// WithSource is a placeholder stub. Full implementation lands in Task 8.
func WithSource(src Source) any {
	return nil
}
