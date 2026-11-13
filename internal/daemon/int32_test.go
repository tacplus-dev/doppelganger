// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"math"
	"testing"
)

func TestInt32Of(t *testing.T) {
	cases := []struct {
		n    int
		want int32
	}{
		{0, 0},
		{80, 80},
		{math.MaxInt32, math.MaxInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{-1, 0},
	}
	for _, c := range cases {
		if got := int32Of(c.n); got != c.want {
			t.Errorf("int32Of(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}
