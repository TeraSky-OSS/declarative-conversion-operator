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
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// quantityOp converts between a resource.Quantity string and an integer
// millivalue (Quantity.MilliValue). toInteger true means parse the source
// Quantity string and write MilliValue; false means treat the source as a
// millivalue integer and write the canonical Quantity string.
type quantityOp struct {
	src, dst  FieldPath
	toInteger bool
}

func (o quantityOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.src)
	if !ok {
		return nil
	}
	if o.toInteger {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("quantity: value at %q is not a string", o.src)
		}
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return fmt.Errorf("quantity: parse %q: %w", s, err)
		}
		return setValue(ctx.output, o.dst, float64(q.MilliValue()))
	}
	f, ok := AsFloat64(v)
	if !ok {
		return fmt.Errorf("quantity: value at %q is not numeric", o.src)
	}
	q := resource.NewMilliQuantity(int64(f), resource.DecimalSI)
	return setValue(ctx.output, o.dst, q.String())
}

// durationOp converts between a Go duration string ("5m", "1h30m") and an
// integer number of seconds. toInteger true means parse the source string
// and write seconds; false means format seconds as a canonical duration.
type durationOp struct {
	src, dst  FieldPath
	toInteger bool
}

func (o durationOp) apply(ctx *execContext) error {
	v, ok := getValue(ctx.input, o.src)
	if !ok {
		return nil
	}
	if o.toInteger {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("duration: value at %q is not a string", o.src)
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("duration: parse %q: %w", s, err)
		}
		if d%time.Second != 0 {
			return fmt.Errorf("duration: %q is not a whole number of seconds", s)
		}
		return setValue(ctx.output, o.dst, float64(int64(d/time.Second)))
	}
	f, ok := AsFloat64(v)
	if !ok {
		return fmt.Errorf("duration: value at %q is not numeric", o.src)
	}
	return setValue(ctx.output, o.dst, (time.Duration(int64(f)) * time.Second).String())
}
