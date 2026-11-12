package daemon

import "math"

// int32Of is n as an int32, clamped to [0, math.MaxInt32]: every caller's n
// is a bounded count, and the guard at the conversion keeps it safe if the
// bound that holds it today (a proto size cap, say) ever moves.
func int32Of(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}
