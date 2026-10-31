# doppelganger

Answers questions on Jody's behalf from his context repo (`~/workspace/jody`). Its only role is
to answer. Design and rules: `~/workspace/jody/agents.md`, "An agent that represents him".

## Shape

- A fleet service: the tacplus **C2S** service `Doppelganger`, **tenant-owned** (one instance
  per tenant, shared by that tenant's users), reached through the fleet's gateway by an
  authenticated user of the tenant. Every request names its target tenant's tid in `tenant`
  (each rpc's `route_field`, tacplus 2.5); the gateway routes it to that tenant's instance, and
  only members of that tenant (and `/system`) are let in. The generated service
  manager (`tacplus/s2sm`) serves it; `internal/daemon/server.go` is only the handler.
- The model is reached through **llmproxy** over S2S with doppelganger's own identity
  (`depends: [llmproxy]`, `(spec.grpc_s2s_client)`); the fleet build admits doppelganger as a
  caller. Doppelganger holds no model credential.
- Config (`state_dir`, `model_alias`) comes from the fleet's config state, delivered by the
  service manager at start and on change. Context is two layers of `ContextDocument` records in
  **stratum**, both written only through the service: the tenant's shared context
  (`UpdateTenantContext`, gated by that RPC's grant) and each user's own (`UpdateContext`, any
  user, their own files). Writing is publishing the record onto its topic (stratum stores it);
  reading is the generated record query plane. Answers rest on both; nothing is on the instance's
  disk. Sessions (`state_dir`) are next to move.
- Sessions belong to the user who opened them; another user of the tenant cannot read or
  extend one.
- Sessions: a caller opens one with context (who they are, who the answer is for, what it is
  for), asks, and pushes back by asking again in the same session with more context; the
  engine re-answers with the new information.
- The local client that authenticates as a user and calls through the gateway is a separate
  program, not written yet. `scripts/trials.sh` predates the fleet surface and needs it.

## Panel mode (#10)

`GeneratePanelAnswer` asks a panel of models from different vendors what the person would do
about a design question, and answers with recommendations, confidence and dissent, marked
**derived (panel)** (`PROVENANCE_DERIVED_PANEL`), never as the person's decision.

- **Seats:** `Config.panel_model_aliases`, one llmproxy alias per panelist (`openai-latest`,
  `xai-latest`, `gemini-latest`). llmproxy resolves each to its vendor's latest model; the panel
  never names a model id. Every answer records the alias, the resolved model id and the time.
  llmproxy does not return the resolved id yet (tacplus-dev/llmproxy#13), so it is empty until it
  does.
- **Phase 1** (alternatives): each seat gets only the question and the situation (no principles,
  no context) and lists up to eight options. Code merges them by normalized title, keeps every
  proposer, and always adds "Reuse what the platform already has" and "Do nothing / defer".
- **Phase 2** (what would the person do): each seat answers once under each lens (reuse and
  generator; invariants; failure from the other side; producer vs consumer; adversary) from the
  merged options and the person's principles, in a fixed schema: ranked option ids, confidence
  0–100, objections, would-change-mind. An answer outside the schema is asked again, then
  reported as a failure, never counted.
- **Principles only:** `Config.principles_paths` (files or directories of the tenant context).
  Only those Markdown files go to a vendor, and only in phase 2. None configured or none matching:
  the panel refuses (`FailedPrecondition`) before any vendor call.
- **Tally** (plain code, reproducible): Borda points and first places per option, the top option,
  agreement (share of answers ranking it first), every dissenting answer and every objection to
  the top option word for word. `MAY_ACT` only when every answer agrees, no seat failed, and the
  caller says the matter is reversible and touches neither security nor identity; otherwise
  `TO_PERSON`.
- **Track record:** every run is a `PanelRun` record in stratum. `UpdatePanelDecision`
  (entitlement gated) records the option the person chose; `GetPanelTrackRecord` scores decided
  runs per model (alias and resolved id) and per question kind.

### Vendor data terms

Before a vendor's seat goes live, its API's terms must be confirmed in writing: no training on
inputs, and the shortest retention available. Jody confirms them (tracked on
tacplus-dev/llmproxy#13); none is confirmed yet.

| Vendor | No training on API inputs | Retention | Confirmed |
|---|---|---|---|
| OpenAI | not yet confirmed | not yet confirmed | no |
| xAI | not yet confirmed | not yet confirmed | no |
| Google (Gemini) | not yet confirmed | not yet confirmed | no |

## Build

```sh
just tacplus   # tacplus (go) → ./tacplus  (git-ignored; run after proto changes)
just build
just test
```

## Notes

- The socket path is declared once, in the proto; the daemon and the CLI use the generated
  `sm.C2DSocketPath`.
- Runs locally now; remote later.
