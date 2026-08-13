/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package engine

import "testing"

// TestCoerceScalarValue_IntegerTypes is a regression test for a real bug
// caught by `convctl test --live`: client-go's dynamic client decodes whole
// JSON numbers as int64 (via apimachinery's unstructured JSON scheme),
// unlike the real admission-webhook path (plain encoding/json), which
// always produces float64. coerceScalarValue must accept both.
func TestCoerceScalarValue_IntegerTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"float64", float64(9)},
		{"int64", int64(9)},
		{"int", int(9)},
		{"int32", int32(9)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := coerceScalarValue(c.in, FieldKindString)
			if err != nil {
				t.Fatalf("unexpected error coercing %T to string: %v", c.in, err)
			}
			if got != "9" {
				t.Fatalf("expected \"9\", got %v (%T)", got, got)
			}
		})
	}
}

// TestCoerceScalarValue_LargeIntegerToStringIsExact is a regression test
// for a CodeRabbit finding: float64 can't exactly represent every int64
// (anything beyond +/-2^53), so routing an int64/int/int32 input through
// AsFloat64 before formatting it as a string could silently corrupt a
// value client-go's dynamic client decoded exactly. coerceScalarValue must
// format these integer types directly instead.
func TestCoerceScalarValue_LargeIntegerToStringIsExact(t *testing.T) {
	const large int64 = 1<<53 + 1 // not exactly representable as float64
	if int64(float64(large)) == large {
		t.Fatalf("test setup: expected %d to lose precision when round-tripped through float64", large)
	}
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"int64", large, "9007199254740993"},
		{"int", int(1 << 40), "1099511627776"},
		{"int32", int32(1 << 30), "1073741824"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := coerceScalarValue(c.in, FieldKindString)
			if err != nil {
				t.Fatalf("unexpected error coercing %T to string: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("expected %q, got %v (%T)", c.want, got, got)
			}
		})
	}
}

func TestCoerceScalarValue_IntegerTypesToNumber(t *testing.T) {
	cases := []any{float64(42), int64(42), int(42), int32(42)}
	for _, in := range cases {
		got, err := coerceScalarValue(in, FieldKindInteger)
		if err != nil {
			t.Fatalf("unexpected error coercing %T to a number: %v", in, err)
		}
		f, ok := got.(float64)
		if !ok || f != 42 {
			t.Fatalf("expected float64(42), got %v (%T)", got, got)
		}
	}
}

func TestCoerceScalarValue_StringAndBoolUnaffected(t *testing.T) {
	if got, err := coerceScalarValue("already a string", FieldKindString); err != nil || got != "already a string" {
		t.Fatalf("unexpected result: got=%v err=%v", got, err)
	}
	if got, err := coerceScalarValue(true, FieldKindBoolean); err != nil || got != true {
		t.Fatalf("unexpected result: got=%v err=%v", got, err)
	}
	if got, err := coerceScalarValue(true, FieldKindString); err != nil || got != "true" {
		t.Fatalf("unexpected result: got=%v err=%v", got, err)
	}
}

func TestCoerceScalarValue_UncoercibleTypeErrors(t *testing.T) {
	if _, err := coerceScalarValue([]any{1, 2}, FieldKindString); err == nil {
		t.Fatalf("expected an error coercing a slice to string")
	}
	if _, err := coerceScalarValue(map[string]any{}, FieldKindInteger); err == nil {
		t.Fatalf("expected an error coercing a map to a number")
	}
}

func TestCoerceScalarValue_RejectsNonFinite(t *testing.T) {
	if _, err := coerceScalarValue("NaN", FieldKindNumber); err == nil {
		t.Fatal("expected an error coercing NaN")
	}
	if _, err := coerceScalarValue("+Inf", FieldKindInteger); err == nil {
		t.Fatal("expected an error coercing +Inf")
	}
	if _, err := coerceScalarValue("-Inf", FieldKindNumber); err == nil {
		t.Fatal("expected an error coercing -Inf")
	}
}

func TestCoerceScalarValue_FractionalIntegerPolicy(t *testing.T) {
	if _, err := coerceScalarValue(1.7, FieldKindInteger); err == nil {
		t.Fatal("default Error policy should reject 1.7 -> integer")
	}
	got, err := coerceScalarValueWithPolicy(1.7, FieldKindInteger, FractionalIntegerTruncate)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if got != float64(1) {
		t.Fatalf("expected 1, got %#v", got)
	}
	got, err = coerceScalarValueWithPolicy(1.7, FieldKindInteger, FractionalIntegerRound)
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	if got != float64(2) {
		t.Fatalf("expected 2, got %#v", got)
	}
	// number destinations keep the fractional value
	got, err = coerceScalarValue(1.7, FieldKindNumber)
	if err != nil || got != 1.7 {
		t.Fatalf("number dest should keep 1.7, got %#v err=%v", got, err)
	}
}

func TestAsFloat64(t *testing.T) {
	cases := []any{float64(7), int64(7), int(7), int32(7)}
	for _, in := range cases {
		f, ok := AsFloat64(in)
		if !ok || f != 7 {
			t.Fatalf("AsFloat64(%v %T) = (%v, %v), want (7, true)", in, in, f, ok)
		}
	}
	if _, ok := AsFloat64("7"); ok {
		t.Fatalf("expected AsFloat64 to reject a string")
	}
}
