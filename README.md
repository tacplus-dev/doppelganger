# doppelganger

Answers questions on Jody's behalf from his context repo (github.com/jodydadescott/jody, locally `~/workspace/jody`). Its only role is
to answer. Design and rules: `~/workspace/jody/agents.md`, "An agent that represents him".

## Shape

- A fleet service: the tacplus **C2S** service `Doppelganger`, **tenant-owned** (one instance
  per tenant, shared by that tenant's users), reached through the fleet's gateway by an
  authenticated user of the tenant. Every request names its target tenant's tid in `tenant`
  (each rpc's `route_field`, since tacplus 2.5; floor 2.6); the gateway routes it to that tenant's instance, and
  only members of that tenant (and `/system`) are let in. The generated service
  manager (`tacplus/s2sm`) serves it; `internal/daemon/server.go` is only the handler.
- The model is reached through **llmproxy** over S2S with doppelganger's own identity
  (`depends: [llmproxy]`, `(spec.grpc_s2s_client)`); the fleet build admits doppelganger as a
  caller. Doppelganger holds no model credential.
- Config (`state_dir`, `model_alias`) comes from the fleet's config state, delivered by the
  service manager at start and on change. Context is two layers of `ContextDocument` records in
  **stratum**, both written only through the service: the tenant's shared context
  (`UpdateTenantContext`, gated by that RPC's grant) and each user's own (`UpdateContext`, any
  user, their own files). Context, sessions and panel runs are written through stratum's
  generated record write surface (`InsertIfAbsent`, `ReplaceIfUnchanged` on the revision read,
  `Apply`), acked once the tenant's redo has them (`DURABILITY_LOCAL`), and read back with `Get`
  and the record plane's rows; nothing is published on the event bus (stratum ingests nothing
  from it, #7). While the redo is unreachable a write is refused `UNAVAILABLE` and nothing is
  written. Answers rest on both layers; nothing is on the instance's disk.
- Sessions belong to the user who opened them; another user of the tenant cannot read or
  extend one.
- Sessions: a caller opens one with context (who they are, who the answer is for, what it is
  for), asks, and pushes back by asking again in the same session with more context; the
  engine re-answers with the new information.
- The local client that authenticates as a user and calls through the gateway is a separate
  program, not written yet. `scripts/trials.sh` predates the fleet surface and needs it.

## Context repo access

Jody's context repo is **github.com/jodydadescott/jody** (private; moved 2026-11-02 from
GitLab, #11). Today doppelganger does not read it: its context arrives as `ContextDocument`
records (above). When it reads the repo itself and opens a review PR per recorded answer
(DOPPELGANGER-STATE.md, items 4–5), it does so as:

- **What:** a GitHub App of its own (none exists yet; Jody creates it), installed **only** on
  `jodydadescott/jody`, the platform's pattern for GitHub (tacplus-dev/roadmap
  `items/looper-identity-2026-10-29.md`). GitHub accepts no external OIDC, so there is no
  federation to GitHub itself; the next best: the App's private key is a non-exportable Cloud KMS
  key that only doppelganger's workload identity (through Workload Identity Federation) may sign
  with, and each use trades the signed JWT for a one-hour installation token. No key file, no
  personal token.
- **Scope:** read the repo, plus PR creation: metadata read, contents write (a PR needs its branch
  pushed), pull requests write. Nothing else.
- **Not enforced by GitHub:** contents write reaches `main` too, and GitHub Free gives a private
  repo no branch protection or rulesets. Doppelganger only ever pushes a review branch and opens
  a PR; it never pushes to `main`. Today it has no git or GitHub write path at all, and
  `internal/daemon/nopush_test.go` fails if one is added without its own test of that rule.

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

## Releasing

doppelganger is released by version, and fleets pin a release line, `MAJOR.MINOR`, which is always that line's
highest patch ([tacplus#180](https://github.com/tacplus-dev/tacplus/issues/180)). Versions are `0.x` until
SeaBeam's launch (2026-12-01), when `1.0.0` is cut; on `0.x` a minor may break, a patch never does.

A change merged to main is released by the same run that merged it: a fix as the next patch, a feature as the
next minor. To cut one, from the commit to release on green main:

```sh
git tag -a v0.1.1 -m "doppelganger 0.1.1: ..."   # a patch to an older line is tagged on release/MAJOR.MINOR
git push origin v0.1.1
```

**Not yet:** `.github/workflows/release.yml` is not on main. Its scripts are, but no looper token may push a
workflow file until tacplus-dev/looper#115, and the workflow itself is on #2; until it lands a tag publishes
nothing. Once it is:

The tag runs `.github/workflows/release.yml`: the gates on the tag, in the build image its
`tacplus.lock` pins ([scripts/release-build.sh](scripts/release-build.sh)), then
`ghcr.io/tacplus-dev/doppelganger:<version>` from the [Dockerfile](Dockerfile)
([scripts/release-image.sh](scripts/release-image.sh)), labelled with the version and commit
(`org.opencontainers.image.version`, `.revision`), and `:<MAJOR.MINOR>` moved to it only when it is the line's
highest patch, so the line tag never goes backwards and no other line moves
([scripts/release-tags.sh](scripts/release-tags.sh)). The image is for linux/arm64, what the fleets run on;
amd64 is off by default, as in the fleets, and `ARCHS: arm64 amd64` in release.yml builds both. The GitHub
release's notes are the commits since the previous release ([scripts/release-notes.sh](scripts/release-notes.sh)),
and it is marked Latest only when it is the highest release, never a patch on an older line. A pre-release
(`v0.2.0-rc.1`) publishes only its own tag. The package is public: pulls need no credential.

Running the workflow by hand releases an existing tag as it was tagged (its source, gates, `tacplus.lock` and
Dockerfile), for a tag made before the workflow, as v0.1.0 was; without `publish` it is a dry run that
publishes nothing, of that tag or of HEAD as a new version, and prints the tags and notes it would get:

```sh
gh workflow run release.yml -f version=v0.1.0 -f publish=true   # publish the existing tag v0.1.0
gh workflow run release.yml -f version=v0.1.1                   # dry run: HEAD as v0.1.1
```

A tag is never moved or deleted. The image runs under the [launcher](https://github.com/tacplus-dev/launcher),
built from source at the commit `LAUNCHER_COMMIT` in release.yml names.

## Notes

- The socket path is declared once, in the proto; the daemon and the CLI use the generated
  `sm.C2DSocketPath`.
- Runs locally now; remote later.
