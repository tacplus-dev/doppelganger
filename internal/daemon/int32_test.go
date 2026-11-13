package daemon

import (
	"math"
	"testing"
)

func TestInt32Of(t *testing.T) {
	for _, c := range []struct {
		n    int
		want int32
	}{
		{0, 0},
		{4096, 4096},
		{math.MaxInt32, math.MaxInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{-1, 0},
	} {
		if got := int32Of(c.n); got != c.want {
			t.Errorf("int32Of(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}
