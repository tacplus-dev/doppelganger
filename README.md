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

- **Seats:** `Config.panel_model_aliases`, one llmproxy alias per panelist, in seat order. Jody's
  panel (2026-10-31): Claude is #1, OpenAI #2, Gemini #3 (`claude-opus-latest`, `openai-latest`,
  `gemini-latest`); Grok joins when xAI offers keyless auth. llmproxy resolves each alias to its
  vendor's latest model; the panel never names a model id. Every answer records the alias, the
  model llmproxy resolved it to (`provider_model`) and the time.
- **Same inputs for every seat:** in each phase, and under each lens, every seat's request is the
  same bytes; on the wire only the alias llmproxy routes by differs. A seat on the moderator's
  alias is a separate instance, and no moderator call feeds it.
- **Moderator:** `Config.panel_moderator_alias` (Claude). It never votes, never sees the
  principles or any other context, and is never told which panelist proposed or dissented. It makes three calls, each checked by code, each falling back to
  code and reported as a failure if it fails:
  - **frame**: restates the caller's question, requirements and situation for phase 1 (the
    caller's text as given otherwise);
  - **merge**: groups the phase-1 proposals, shown without their proposers. Code checks every
    proposal is in exactly one option, none is invented and the fixed options stay apart, and
    keeps every proposal and proposer itself (code merges by normalized title otherwise);
  - **summary**: explains the tally for the person. It changes nothing in the tally; code keeps it
    only if it names the top option and quotes every dissent (`D1`, `D2`, ..., each objection and
    would-change-mind word for word).
- **Phase 1** (alternatives): each seat gets only the question and the situation (no principles,
  no context, no person) and lists up to eight options. They are merged (above), keeping every
  proposer, and "Reuse what the platform already has" and "Do nothing / defer" are always first.
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
  `TO_PERSON`. A moderator failure is neither dissent nor a missing seat.
- **Track record:** every run is a `PanelRun` record in stratum. `UpdatePanelDecision`
  (entitlement gated) records the option the person chose; `GetPanelTrackRecord` scores decided
  runs per model (alias and resolved id, Claude included) and per question kind.

### Vendor data terms

A vendor's seat goes live only once its terms are recorded here. Jody accepted each vendor's
default API terms, with the settings below (tacplus-dev/llmproxy#13, 2026-10-31). Zero data
retention needs the vendor's approval and is not needed while the panel receives only the
engineering principles and the design question (`principles_paths`); apply for it if that ever
changes. llmproxy turns a vendor on separately (its `LIVE_VENDORS`, with the same terms in its
README); as of 2026-10-31 only Anthropic is live there, so OpenAI's and Gemini's seats fail
(reported, never counted) until it turns them on.

| Vendor | No training on API inputs | Retention and settings | Terms |
|---|---|---|---|
| Anthropic (Claude) | not recorded: live in llmproxy before its data-terms rule | not recorded | — |
| OpenAI | yes: API data is not used for training unless we opt in, and we do not | abuse-monitoring logs per OpenAI's policy; never use extended prompt caching | [your data](https://developers.openai.com/docs/guides/your-data) |
| Google (Gemini, Vertex AI) | yes: no training on customer data without permission | abuse-monitoring logs per Google's policy; durable caching and session resumption off on the GCP project (the hub applies it) | [data governance](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/data-governance) |
| xAI (Grok) | — | sits out until xAI offers keyless auth | — |

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
