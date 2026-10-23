# Doppelganger — pick-up state (written 2026-10-23)

Read this first after a context clear. Memory pointer: `~/.claude/.../memory/doppelganger-repo-lockdown.md`
(that file is the 2026-10-09 → 10-14 decision log; this file is the current truth where they differ).

## What doppelganger is
Answers questions on Jody's behalf from his context repo. Only role: answer. Design and rules live in the
context repo at `agents.md` ("An agent that represents him"). Callers are usually agentic processes wanting a
deep answer. Quotes are enforced in code: any ≥3-word quoted phrase must match Jody's marked words (`*"..."*`)
verbatim, else retry; on the third failure the quote marks are stripped (content survives, never presented as
his words).

## Where things are
- Code: `~/workspace/gitlab/seabeam/tools/doppelganger` (Go, main, 7 commits, clean). No remote configured yet.
- Context repo (data): `~/workspace/jody`, origin = **gitlab.com/secondstar/jody** (private project 87501898 in
  private top-level group `secondstar`, id 144515190, "Second Star", Free tier). `main` is protected: push = No one,
  merge = Maintainers (Jody), no force. Local main == origin/main (444f2f2). `inbox/` is untracked on purpose
  (unreviewed material). Changes already flow as MRs (branch `decisions-2026-10-15` was merged). No open MRs.
- Daemon: launchd `com.seabeam.doppelganger` (plist in `dist/`), runs `/Users/jody/go/bin/doppelganger daemon
  --claude /Users/jody/.local/bin/claude`, is loaded and running. Socket `/tmp/doppelganger.sock` (0600).
  Session state: one JSON per session under `~/.doppelganger/sessions/` (39 files; no stale-session cleanup yet).

## Code shape (as of 002d704)
- `cmd/doppelganger/main.go`: `daemon`, `session create|update`, `ask`. CLI prints JSON only (for claude/codex).
- `internal/daemon/`: `engine.go` (Engine.Answer → up to 3 attempts of `run()`), `quotes.go` (enforcement),
  `sessions.go`, `server.go` (handler only), `prompt.md`, tests. Proto: `proto/services/doppelganger.proto`
  (C2D service `Doppelganger`: CreateSession, UpdateSession, GenerateAnswer).
- Since this session (other session, 2026-10-15): **3c66817** hosts the daemon with the tacplus-generated
  standalone C2D host (`sm.ServeC2DStandalone`, socket path from generated `sm.C2DSocketPath`); the hand-written
  Unix listener is gone. That generator lives in the PUBLIC tacplus (`~/workspace/github/seabeam/tacplus`,
  `pkg/internal/go/types/handler_gen.go`, commit cb02690 "c2d: generated standalone host"), NOT in the GitLab
  schematic tacplus. `just tacplus` must run with that tacplus. Then 254e2f5/002d704 added the
  `header:` (Seabeam LLC) to `tacplus.yaml`. `model/` is git-ignored generated output.
- **Provider seam (branch `engine-provider`, 2026-10-23):** `internal/daemon/provider.go` defines `Provider.Complete(ctx, Request)`;
  the engine owns prompt, retries and quote enforcement above it. Providers: `provider_anthropic.go` (Messages API via the
  official Go SDK, repo in a cached system block, structured outputs from `answerSchema`, default `claude-opus-5-5`, effort high,
  key from `~/.doppelganger/anthropic.key` which must be mode 0600) and `provider_claude.go` (the old `claude -p` path, interim).
  Daemon flag `--provider anthropic|claude`, default anthropic; the installed plist pins `--provider claude` until a key exists.
  Offline tests cover the engine loop against a scripted provider and the Anthropic request shape against an httptest server.
  NOT yet run live against the API (no key on this machine yet).

## Decisions that STAND for doppelganger (2026-10-09 → 10-14, Jody)
1. **Self-sufficient engine.** No external programs. Call model APIs directly: Anthropic (via API), OpenAI, Gemini,
   Grok (OpenAI-compatible). Same contract for every provider: no tools, whole repo as input, schema-constrained
   output, quotes enforced in code. Seam = `Engine.run(ctx, input) (Answer, error)`. Intended to come from the
   tacplus LLM chassis (provider adapters as opt-in chassis selected in tacplus.yaml; response JSON Schema
   generated from the `Answer` message) — see `llmproxy-retirement-direction.md`. If the chassis isn't there yet,
   a provider interface at that seam is the minimal step.
2. **Model selection in config + "round table".** Chair triages: simple factual → chair-only; deeper → panel
   (members answer independently, in parallel, each quote-checked). Chair summarizes (also quote-checked) and names
   agreement vs split; a split stays a split (NEEDS_CONTEXT / BLOCKED_ON_HUMAN), never resolved by the chair.
3. **Rebuttal** = `reconsider` on the session (separate verb from `ask`, with the requester's objection). One
   round: members see all answers + sources + objection, hold or revise and say why (`would_change_mind`), chair
   re-summarizes noting who moved. Open: cap on rounds (I proposed config, default 2).
4. **Recorded panel answers**, reused by the chair for similar questions. They are not sources: reuse is marked
   DERIVED "from precedent" (unreviewed ones "pending review", linked to their MR), never quotable as him, and only
   reused while the cited files are unchanged at the recorded commit (staleness check in code). Each record names
   the record it reuses.
5. **Review = one MR per recorded answer** in secondstar/jody. Merge = accept (becomes a permanent source, his
   words); edit-then-merge = his version wins; close = reject (deleted from the store). Jody reviews ~daily.
   Open: reuse while pending (his last words were "edited then merged", ambiguous between strict and
   normal-flow; ask).
6. **Disclosure by caller identity, three classes.** Disclosable (per identity, file-layout scopes: e.g. `work/`
   visible to seabeam identities), **understanding-only** (sent to the model so it understands him, never cited
   or quoted; output checked against verbatim passages + a deny-list of facts, same mechanism as quotes),
   **excluded** (never sent: legal). "Not going for perfection." Next concrete step: sort `life.md` and `legal/`
   into the classes with him.
7. **Jody's own identity** comes via Google/Apple sign-in; a desktop agent using it is "agent of Jody" with a broad
   personal scope below full. Full context is only ever handed to an agent by Jody directly.
8. **Hosting direction:** one instance, off his desktop, as an ordinary tacplus service in the fleet (dev domain,
   see below), answering by caller identity. The desktop/macOS-user/deploy-key plan is dead.
9. **Build on branches; never block the loopers.**

## SUPERSEDED by other sessions (do not act on these from the old memory file)
- The whole identity/auth design of this session (GitLab satellite, token sidecar, reflectors, minters,
  C2D peer-uid MCP, kernel-rooted Biscuits via D24 facade): auth was redesigned elsewhere. Current truth is
  `fleets-and-canary-huddle.md` (2026-10-22: **reflector DROPPED**, admission = kernel op by quorum, gateway keeps
  JWKS, Hazelcast stays, near term = one cluster per domain, dev + prod) and `identity-graph-huddle.md`. Biscuits
  remain an exploration doc only (https://claude.ai/code/artifact/a1206ebc-c1d5-40d5-8b46-b08429ed68f4).
- "GitLab-pipeline loopers replace moonshot" → superseded by `looper-hierarchy-2026-10-22.md` (one looper per
  repo + a project looper from roadmap epics, local launchd for now).
- "Tender-class ship / build ship in the fleet" wording is from 10-13; with 10-22's "one cluster per domain, dev
  and prod", doppelganger simply runs in dev. Re-confirm the class name if it matters.
- The "rescind" item (replace Jody's plaintext personal glab token with a seabeam group token) was never done;
  it belongs to whatever the auth sessions decided, not to doppelganger.

## Open / not yet done (doppelganger-only)
- [x] Engine: provider seam + Anthropic adapter built and unit-tested (branch `engine-provider`).
- [ ] Engine: create a new Anthropic API key (Jody), drop it in `~/.doppelganger/anthropic.key` (0600), run live, then switch the
      plist to `--provider anthropic` and delete `provider_claude.go` and `--claude`. OpenAI/Grok/Gemini adapters after that.
- [ ] Round table + chair triage + `reconsider` (items 2–3). Proto change: new rpc + Answer fields for member
      positions / precedent reference.
- [ ] Recorded answers store + staleness check + MR creation into secondstar/jody (items 4–5). Needs a
      write-capable credential for the daemon (deploy key or project token); `main` protection already in place.
- [ ] Disclosure classes: sort `life.md`, `legal/`, `code.md`, `working-with-me.md` (item 6); enforce in code
      (excluded files never loaded; understanding-only files output-checked).
- [ ] Stale-session cleanup under `~/.doppelganger/sessions/`.
- [ ] Live quote-enforcement trials have NOT been rerun since e2e280e (strip-on-last-attempt). Last run before it:
      5/6 clean, 1 failed closed, zero fabricated quotes delivered.
- [ ] MCP surface: earlier plan (tacplus MCP on c2d via peer uid) is moot once it's a fleet service; the normal
      generated `Broker<Service>McpSession` carrier applies there. For the local daemon, agents keep using the
      JSON CLI.
- [ ] Give the code repo a remote (seabeam/tools group on GitLab, where sideband lives).

## Related artifacts
- Identity design doc (historical now): https://claude.ai/code/artifact/cf57d392-7fc0-4820-946d-fd2e3f585562
- Biscuit spec study: https://claude.ai/code/artifact/a1206ebc-c1d5-40d5-8b46-b08429ed68f4
- Suggested task chip "Close tacplus REST dispatch parity gap" (task_3381e09d) — tacplus, not doppelganger.

## Plan agreed 2026-10-23 (Jody: "Yes on all")
1. Registry repo under github/tacplus-dev (local seed DONE at ~/workspace/github/tacplus-dev/registry, commit 3ec47b3, no remote yet;
   doppelganger registered as id 101). 2. tacplus branch `registry-and-floor` in ~/workspace/github/seabeam/tacplus (DONE locally:
   global registry + locks + `tacplus registry register`, `tacplus:` MAJOR.MINOR floor in tacplus.yaml; ids 101-2047 new range).
   3. Doppelganger repo moves to github/tacplus-dev/doppelganger (NOT done; header entity Seabeam LLC vs Jody Scott to settle first).
   4. Build + test (engine seam DONE on branch `engine-provider`; live trials need the API key). 5. Kernel Rust rewrite, Hazelcast out
   of kernel (stays in Stratum) — own track, merges after 2026-12-01 launch. 6. Merge back. Test deploy: a SMALL SEPARATE CLUSTER from
   the branch (a namespace on the launch cluster would still change its kernel). `proto/registry.yaml` is now a lock; its `global:` is a
   local path until the registry repo has a remote.

## Direction noted 2026-10-23 (Jody, not yet acted on)
Doppelganger must deploy as a service on any fleet/cluster, optional per cluster, likely from an
open-source home. Today "part of the fleet" means "in schematic's type tree", which doppelganger
should not be. The fix is tacplus composition: declare which types/services a deployment loads and
build the kernel from that set. Not built yet; being discussed on the tacplus side. Today's goal:
finish the open items above, then size the tacplus work so it does not disturb the 2026-12-01 launch.
