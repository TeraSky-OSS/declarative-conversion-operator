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

// coerceScalarValue converts v (as decoded from JSON/YAML — so numbers
// always arrive as float64, never int) into the representation `to`
// expects. It never needs to know v's declared source kind: it type-
// switches on the concrete Go value it was actually given, which is
// enough to pick the right conversion in every direction.
func coerceScalarValue(v any, to FieldKind) (any, error) {
	switch to {
	case FieldKindString:
		switch t := v.(type) {
		case string:
			return t, nil
		case bool:
			return strconv.FormatBool(t), nil
		case float64:
			return formatNumber(t), nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to string", v)
		}
	case FieldKindInteger, FieldKindNumber:
		switch t := v.(type) {
		case float64:
			return t, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
			if err != nil {
				return nil, fmt.Errorf("cannot coerce %q to a number: %w", t, err)
			}
			return f, nil
		case bool:
			if t {
				return float64(1), nil
			}
			return float64(0), nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to a number", v)
		}
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

// formatNumber renders a float64 the way encoding/json would render it as
// a numeric literal (no trailing ".0" for whole numbers), so a value that
// round-trips through a string representation comes back identical.
func formatNumber(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
