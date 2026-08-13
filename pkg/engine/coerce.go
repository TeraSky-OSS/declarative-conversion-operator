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

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// isCoercibleScalar reports whether k is a JSON scalar kind coerceScalarValue
// knows how to convert to and from — the four kinds every strategy that
// works with scalar leaves (TypeCoerce, ScalarToFields/FieldsToScalar,
// ListJoin/ListSplit) supports.
func isCoercibleScalar(k FieldKind) bool {
	switch k {
	case FieldKindString, FieldKindInteger, FieldKindNumber, FieldKindBoolean:
		return true
	}
	return false
}

// coerceScalarValue converts v (as decoded from JSON/YAML) into the
// representation `to` expects. Integer destinations use the fail-closed
// Error policy for fractional values; TypeCoerce passes an explicit policy
// via coerceScalarValueWithPolicy.
func coerceScalarValue(v any, to FieldKind) (any, error) {
	return coerceScalarValueWithPolicy(v, to, FractionalIntegerError)
}

func coerceScalarValueWithPolicy(v any, to FieldKind, frac FractionalIntegerPolicy) (any, error) {
	frac = normalizeFractionalPolicy(frac)
	switch to {
	case FieldKindString:
		switch t := v.(type) {
		case string:
			return t, nil
		case bool:
			return strconv.FormatBool(t), nil
		case int64:
			// Format directly rather than routing through AsFloat64:
			// float64 can't exactly represent every int64 (anything beyond
			// +/-2^53), which would silently corrupt a value client-go's
			// dynamic client decoded exactly.
			return strconv.FormatInt(t, 10), nil
		case int:
			return strconv.FormatInt(int64(t), 10), nil
		case int32:
			return strconv.FormatInt(int64(t), 10), nil
		default:
			if f, ok := AsFloat64(v); ok {
				return formatNumber(f), nil
			}
			return nil, fmt.Errorf("cannot coerce %T to string", v)
		}
	case FieldKindInteger:
		f, err := numericAsFloat64(v)
		if err != nil {
			return nil, err
		}
		return applyFractionalInteger(f, frac)
	case FieldKindNumber:
		return numericAsFloat64(v)
	case FieldKindBoolean:
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(t))
			if err != nil {
				return nil, fmt.Errorf("cannot coerce %q to boolean: %w", t, err)
			}
			return b, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to boolean", v)
		}
	default:
		return nil, fmt.Errorf("unsupported coercion target kind %q", to)
	}
}

func numericAsFloat64(v any) (float64, error) {
	if f, ok := AsFloat64(v); ok {
		if err := rejectNonFinite(f); err != nil {
			return 0, err
		}
		return f, nil
	}
	switch t := v.(type) {
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("cannot coerce %q to a number: %w", t, err)
		}
		if err := rejectNonFinite(f); err != nil {
			return 0, fmt.Errorf("cannot coerce %q to a number: %w", t, err)
		}
		return f, nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot coerce %T to a number", v)
	}
}

func rejectNonFinite(f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("cannot coerce non-finite number %v", f)
	}
	return nil
}

func applyFractionalInteger(f float64, frac FractionalIntegerPolicy) (float64, error) {
	if f == math.Trunc(f) {
		return f, nil
	}
	switch frac {
	case FractionalIntegerTruncate:
		return math.Trunc(f), nil
	case FractionalIntegerRound:
		return math.Round(f), nil
	default:
		return 0, fmt.Errorf("cannot coerce %v to integer: fractional part is not allowed (onFractionalInteger=Error)", f)
	}
}

// AsFloat64 normalizes any Go numeric type a JSON/YAML decoder in this
// codebase's various call paths might produce into the float64
// representation the rest of this package treats as canonical.
func AsFloat64(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	default:
		return 0, false
	}
}

// formatNumber renders a float64 the way encoding/json would render it as
// a numeric literal (no trailing ".0" for whole numbers), so a value that
// round-trips through a string representation comes back identical.
func formatNumber(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
