// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import "context"

// traceKey carries the trace_id (tacplus field 200) of the request a handler
// is serving to every downstream call that request causes: llmproxy, the
// record store's reads and writes (tacplus USER.md, Distributed
// Tracing). Each handler sets it from its request; each call site reads it,
// so the engine's retries, every panel seat and the moderator carry it too.
type traceKey struct{}

func withTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}

// traceID is the trace_id of the request ctx serves, "" when it carries none.
func traceID(ctx context.Context) string {
	id, _ := ctx.Value(traceKey{}).(string)
	return id
}
