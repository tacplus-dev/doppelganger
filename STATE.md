# State: doppelganger

Overwritten to stay current (loopers and agents). Last updated: 2026-10-28 by the looper (#6).

- **Version / line:** unreleased (no tag yet, #2); tacplus floor 2.5
- **Works:** the tenant-owned C2S service `Doppelganger` (sessions, answers, user and tenant context), the model through llmproxy, context and sessions as stratum records. Every rpc routes by its `tenant` field (`route_field`, the caller's own tenant; #6).
- **Released / deployed:** nowhere yet
- **Known gaps:** no local client that signs in and calls through the gateway (`scripts/trials.sh` predates the fleet surface); callers must fill `tenant` on every request
- **Blocked on:** `just gates` through the looper's `tacplus` wrapper fails in the compiler image (no `go` for goimports, tacplus-dev/tacplus#188); the gates pass with the same 2.5.0 binary run in the looper container
