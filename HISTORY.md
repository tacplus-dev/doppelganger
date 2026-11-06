# History: doppelganger

Append-only: add entries at the end, never edit or remove one (CI enforces it). One line each:
`- <YYYY-MM-DD> <who: looper|agent|jody> <kind: issue|decision|release|deploy> <one line> (<links>)`

- 2026-10-29 looper issue Bump golang.org/x/net to v0.62.0+ (GO-2026-6866, GO-2026-6860) (#8, 1931c4b)
- 2026-10-29 looper issue go.mod: go 1.26.9 → 1.26.10 (crypto/x509 GO-2026-6809, reachable) (#9, 283607c)
- 2026-11-02 looper issue Context repo moved to github.com/jodydadescott/jody: PRs instead of MRs; main is not server-protected (#11, 15d9801)
- 2026-11-03 looper issue Trace propagation: no handler forwards its inbound trace_id to llmproxy, the record query plane or its publishes, and none is tested (#12, c1f62db)
- 2026-11-06 looper issue Declare the tacplus floor: 2.4 (#1)
- 2026-11-06 looper issue Base image: alpine:3.22 → alpine:3.24 (latest Alpine line) (#5)
- 2026-11-06 looper issue Write records to Stratum through its write surface: Stratum no longer ingests them from the event bus (#7, 89f440c)
