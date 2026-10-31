# State: doppelganger

Overwritten to stay current (loopers and agents). Last updated: 2026-10-31 by the looper (#10).

- **Version / line:** unreleased (no tag yet, #2); tacplus floor 2.5
- **Works:** the tenant-owned C2S service `Doppelganger` (sessions, answers, user and tenant context), the model through llmproxy, context and sessions as stratum records. Panel mode's doppelganger half (#10): `GeneratePanelAnswer` (two phases over `panel_model_aliases`, principles from `principles_paths` only, plain-code tally with dissent), `PanelRun` records, `UpdatePanelDecision` and `GetPanelTrackRecord`, tested against a fake provider. Every rpc routes by its `tenant` field (`route_field`, the caller's own tenant; #6).
- **Released / deployed:** nowhere yet
- **Known gaps:** no local client that signs in and calls through the gateway (`scripts/trials.sh` predates the fleet surface); callers must fill `tenant` on every request; the panel has not run live: llmproxy routes to Anthropic only and returns no resolved model id (tacplus-dev/llmproxy#13), so the acceptance backtests (tacplus#212, s2s-never-crosses-tenants, rust-default-language) wait; no vendor's data terms are confirmed; no automatic rerun when a vendor's latest changes
- **Blocked on:** the live panel and its backtests on tacplus-dev/llmproxy#13; `just gates` through the looper's `tacplus` wrapper fails in the compiler image (no `go` for goimports, tacplus-dev/tacplus#188); the gates pass with the same binary (2.5.1) run in the looper container
