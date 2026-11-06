// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Context, sessions and panel runs are written through stratum's generated
// record write surface (InsertIfAbsent<T>, ReplaceIfUnchanged<T>, Apply) and
// read through Get<T> and the rows: stratum ingests nothing from the event bus
// (stratum#2). A write is answered once the tenant's redo has it at the type's
// declared durability, so a row written is a row read: nothing here caches.

// tenantKey carries the tid of the tenant a request is for (its route field,
// tenant) to every record store call the request causes: stratum is one
// instance per tenant, and each call names its instance in target_tenant
// (tacplus#207). The generated route check holds that tid to this instance's
// own before a handler runs.
type tenantKey struct{}

func withTargetTenant(ctx context.Context, tid string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tid)
}

// targetTenant is the tid of the tenant ctx's request is for, "" when none.
func targetTenant(ctx context.Context) string {
	tid, _ := ctx.Value(tenantKey{}).(string)
	return tid
}

// maxWriteAttempts bounds a read-modify-write that keeps losing to concurrent
// writers of the same row (ABORTED): the caller then sees ABORTED and retries.
const maxWriteAttempts = 3

// storeError is the status a handler answers with when the record store
// refused it: UNAVAILABLE (the redo is unreachable, nothing was written; try
// again), FAILED_PRECONDITION (a durability the store cannot give yet) and
// ABORTED (the row kept changing underneath) reach the caller as they are;
// anything else is fallback.
func storeError(err error, fallback codes.Code) error {
	switch c := status.Code(err); c {
	case codes.Unavailable, codes.FailedPrecondition, codes.Aborted:
		return status.Error(c, err.Error())
	}
	return status.Error(fallback, err.Error())
}
