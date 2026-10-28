# Roadmap: doppelganger

Owned by Jody; changed only by an agent at Jody's request. Loopers propose in PROPOSALS.md.

## Now
Answers questions on Jody's behalf from his context repo: a tenant-owned C2S fleet service that reaches the model only through llmproxy.
Go. Unreleased; tacplus floor 2.4 (declared, #1 still open).

## Next
- Declare route fields (target tenant on every tenant-owned rpc), floor 2.5 (#6, after tacplus#198)
- Nothing approved and unblocked; every open approved issue is in Later.

## Later
- Declare the tacplus floor 2.4 and pass the gates on it (#1), blocked on tacplus-dev/tacplus#188
- Release v0.1.0 and publish its image (#2), blocked on tacplus-dev/looper#12 and #1
- golang.org/x/net to v0.60.0+ for five HTTP/2 advisories (#3), blocked on #4
- Add a `gates` recipe to the justfile (#4), blocked on #1
- Base image alpine:3.22 to alpine:3.24 (#5), blocked on #1
- Local client that signs in, gets a platform token and calls doppelganger through the gateway (tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
- Workload callers (loopers) allowed in on their GitLab claims, default deny (tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
- Claim-to-disclosure-class table (needs Jody's decision; tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
- Human callers: same service or the local daemon (needs Jody's decision; tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
- Settle the header entity (Seabeam LLC vs Jody Scott) before the repo goes public (tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
- Open-source distribution beside tacplus, exploring only, not before launch (tacplus-dev/roadmap:items/explore-proto-ownership.md)

## Not doing
- Running locally: doppelganger is a fleet service only (Jody, 2026-10-23; tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
- Holding a model credential or using a vendor SDK: the model is reached only through llmproxy (tacplus-dev/roadmap:items/doppelganger-fleet-service.md)
