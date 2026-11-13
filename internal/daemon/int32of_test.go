// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"math"
	"testing"
)

func TestInt32Of(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want int32
	}{
		{"zero", 0, 0},
		{"normal count", 1000, 1000},
		{"max int32", math.MaxInt32, math.MaxInt32},
		{"above max int32 is clamped", math.MaxInt32 + 1, math.MaxInt32},
		{"negative is clamped", -1, 0},
	}
	for _, c := range cases {
		if got := int32Of(c.n); got != c.want {
			t.Errorf("%s: int32Of(%d) = %d, want %d", c.name, c.n, got, c.want)
		}
	}
}
