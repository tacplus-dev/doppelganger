// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import "math"

// int32Of is n as an int32, clamped to 0..math.MaxInt32 (every caller's n is a bounded count
// or score). The check sits at the conversion so that it holds, and gosec's G115 sees it, even
// if the bound the caller relies on moves.
func int32Of(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}
