# Doppelganger — pick-up state (written 2026-10-23, updated same day after the repo move)

Read this first after a context clear. Memory pointer: `~/.claude/.../memory/doppelganger-repo-lockdown.md`
(that file is the 2026-10-09 → 10-14 decision log; this file is the current truth where they differ).

## What doppelganger is
Answers questions on Jody's behalf from his context repo. Only role: answer. Design and rules live in the
context repo at `agents.md` ("An agent that represents him"). Callers are usually agentic processes wanting a
deep answer. Quotes are enforced in code: any ≥3-word quoted phrase must match Jody's marked words (`*"..."*`)
verbatim, else retry; on the third failure the quote marks are stripped (content survives, never presented as
his words).

## Where things are
- Code: `~/workspace/github/tacplus-dev/doppelganger` (moved 2026-10-23 from gitlab/seabeam/tools/). Remote = github.com/tacplus-dev/doppelganger
  (PRIVATE, org tacplus-dev). Module `github.com/tacplus-dev/doppelganger`; generated output in `tacplus/` (git-ignored). Branches:
  `main`, `engine-provider` (all of 2026-10-23's work; not merged to main yet).
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

## Corrections 2026-10-23 (Jody, afternoon)
- Doppelganger does NOT run locally. Fleet service only. Locally: a client that signs in, gets a platform token, calls it.
  The launchd daemon on the Mac is a leftover. The C2D surface will be replaced by a client-facing one with a caller token.
- Model calls go through **llmproxy** (S2S). llmproxy holds the federated identity to Anthropic. No API keys anywhere.
  The Anthropic SDK provider was removed; `ClaudeCLI` stays as the stand-in behind the `Provider` seam until the generated
  llmproxy client replaces it. llmproxy was copied wholesale to github.com/tacplus-dev/llmproxy (private; README has the
  divergence rule). Registry: llmproxy id 75 now maps to that repo.
- Composition: the kernel build follows dependencies (manifest -> doppelganger -> llmproxy), never the reverse. Generating
  llmproxy alone fails on the "kernel" state label today; that is the first thing composition must fix (roadmap item
  tacplus-registry-and-floor).
- ~/workspace/github/tacplus-dev/tacplus was deleted then RESTORED (checked out at 699cbe4): sonar/backend/build.sh pins
  TACPLUS_REPO to that path and sha. Conflicts with "no code on disk unless working" — Jody to decide.

## tacplus side as of 2026-10-23 evening (branch `registry-and-floor`, github.com/tacplus-dev/tacplus)
- Sys contract: `sys/minter.proto` (minter.Minter, vservice 102, host label kernel), `sys/servicehealth.proto`,
  well-known state host label, config state routing. A service generates alone (llmproxy proved it).
- `tacplus.yaml` `depends: [llmproxy]` resolves through the registry and merges llmproxy's proto. For doppelganger:
  add that line plus `option (spec.grpc_s2s_client) = {service: "llmproxy.LlmProxy"};` in doppelganger.proto when
  the C2D surface becomes an S2S/C2S fleet surface; then the generated `LlmProxyClient` is the engine's Provider.
- llmproxy still needs: crew import removed (issue #1), an output-schema field (issue #2), `doppelganger` in its
  allow-list or a fleet-level allow-list (issue #3).
- Not done: the fleet build (manifest, transitive deps, whole-fleet checks); kernel hosting minter.Minter / the sys
  ServiceHealth is unverified until then. Cross-test fixtures deferred.
- Fleet build DONE (first deliverable): `tacplus fleet build` in github.com/tacplus-dev/fleet-dev (private; fleet.yaml =
  kernel + llmproxy + doppelganger; fleet.lock has the commits; build/proto is the merged tree). doppelganger `main` was
  fast-forwarded to `engine-provider` (the fleet build caught the stale lock on main). Kernel generation against
  build/proto fails ONLY on crew.* residency records: llmproxy's crew.proto import (public issue #1). Everything else
  in the fleet pipeline passed (routing, host labels, minter uniqueness, registry).

## 2026-10-23 night — the dev fleet exists and kernel generates from it
- tacplus-dev now holds: registry, tacplus (branch `registry-and-floor`, all of today's work, not merged), kernel, opa, gateway,
  stratum, eventbus, kmaker, oauth, letsencrypt, blobstore, operator, messaging, mailbox, llmproxy, doppelganger, fleet-dev.
  Boneyard (Jody): saml, sentinel (returns as Admiral), argus, timedb, graphdb, llmsettings, vanilla, personalauth, agentsensor.
- `tacplus fleet build` in fleet-dev resolves 11 services from those repos (no libraries: the agentsensor reference was a
  mistake and is gone from gateway); `tacplus build --src build/proto` in kernel generates clean, with minter.Minter and the
  sys ServiceHealth generated for kernel to host.
- Not done: kernel's Java implementing minter.Minter; gateway/operator generation; images; the small cluster; doppelganger's
  own surface (C2D -> fleet service with caller token) and its llmproxy client; cross-test fixtures; proto layout flattening;
  `kind:` consistency checks. The private platform's pushed state is 2026-08-29; everything newer is local.

## 2026-10-24 early — fleet-side services
- kernel implements minter.Minter and hosts the sys health root (compiles; tests being rebased off launch-bundle samples).
- gateway proxies minting to the sys minter, reads the sys health root; 88 tests green. Dead/product tests removed.
- operator is a LIBRARY (Jody): `kind: library`, out of the fleet's service list; a deployment repo builds its operator
  from it against build/proto. Harness rendering leaves the library (fleet layer). Module split is the follow-up.
- Dev fleet: 13 services (kernel, opa, gateway, stratum, eventbus, kmaker, oauth, letsencrypt, blobstore, messaging,
  mailbox, llmproxy, doppelganger). Stratum already ported (Jody re-confirmed).

## 2026-10-24 — every repo generates on its own through `depends`
- kernel tests: 441 run, 0 failures (one fleet-aware assumption skipped); pushed. The one holdout was a test
  listing `kernel.Mint/CreatePersonalPrincipal` as self-authenticating; its declaration never was
  (`auth_services: ["*"]`, no flag), so the test now lists it as enforced.
- tacplus (`registry-and-floor`, 250c3e1): Go state clients for sys state roots (the `Sys()` group);
  **`depends` is transitive** (a contract's own tacplus.yaml depends are followed, each name once);
  `hasSysState` no longer counts the injected health state (moving ServiceHealth into sys/ had switched on the
  runtime-config subscription for every service, which only store services had).
- Contract dependencies now declared where the proto says so: kernel → opa (opa_import); blobstore, letsencrypt,
  messaging, mailbox → stratum (record query plane) + eventbus (topics); letsencrypt → kernel (Identity state);
  mailbox → messaging (MessageBus.stream). Each of those repos now runs `tacplus build` alone and (Go) builds.
- operator (library) builds, vets and tests green standalone (fc422f5). Its tacplus.yaml lists what the
  hand-written bootstrap still names: gateway TLS mode, eventbus node role, blobstore volume groups, example config,
  llmproxy app-service tests, and an embedded registry.json copied from the model. **That is the launch fleet's
  composition living in a library**; it moves to the fleet deployment repo (and Skipper's tenant spec) — follow-up,
  not done.
- Admiral / Skipper (Jody's question, my read): same library, two callers. Admiral: namespace lifecycle + base fleet
  in the platform namespace. Skipper: the tenant's set in the tenant namespace with namespace-scoped credentials.
  The library needs an "external kernel at <addr>, trust this CA" input path for Skipper; today it assumes it
  bootstraps kernel itself (that is Admiral's path). Not started.
- gateway regeneration from fleet-dev build/proto with the new binary: in progress at time of writing.

## 2026-10-24 — doppelganger is a fleet service; the proof fleet exists
- Jody: fleet build supplies llmproxy's caller list (not runtime config). tacplus: `service_s2s
  {auth_services_from_fleet: true}`; the fleet build stamps `auth_services` from the fleet services declaring
  `(spec.grpc_s2s_client)` of it; standalone admits nobody. llmproxy#3 closed. Also: with `pkg` omitted the
  project's own proto dir names the self package (also under an explicit fleet `--src`).
- doppelganger (5111580+): `service_c2s`, every RPC `RPC_FLAG_AUTH_ANY_USER` (MCP tool descriptions required and
  written), `(spec.grpc_s2s_client) = llmproxy.LlmProxy`, `depends: [llmproxy]`, Config {repo_dir, state_dir,
  model_alias} delivered by the generated service manager; provider = LlmProxy over the generated client (schema in
  the prompt until llmproxy#2); ClaudeCLI and the socket surface are gone. The LOCAL CLIENT (user token through the
  gateway) is not written; scripts/trials.sh waits on it.
- **fleet-seatrials** (github.com/tacplus-dev/fleet-seatrials, private): the proof fleet, 8 services (kernel, opa,
  gateway, letsencrypt, eventbus, stratum, llmproxy, doppelganger — the closure gateway→letsencrypt→eventbus+stratum
  forces). Stamped: `llmproxy.LlmProxy auth_services: ["doppelganger"]`; llmproxy's generated authorizer carries
  exactly that; doppelganger and llmproxy generate and test green from its build/proto. Runs on a small cluster
  created/destroyed per trial (Jody). fleet-dev is now redundant — ask Jody before deleting.
- Second proof (Jody): a trivial tenant-owned service in two tenant namespaces; gateway must route each tenant's
  call to that tenant's instance. **tenantecho** (id 103, github.com/tacplus-dev/tenantecho, public) is written:
  `tenant_owned`, C2S `TenantEcho.Ping` (any user) answering {message, served_by = own identity path from its own
  token, caller_namespace, greeting}; in fleet-seatrials (now 9 services). Deploying it into two tenant namespaces
  is the fleet-layer work still open (tenants in the operator Config, Skipper shape).
- Admiral/Skipper (Jody): per-tenant namespaces; Admiral creates/deletes them and deploys the tenant's Skipper;
  Skipper deploys the tenant's services; both use the operator library; Admiral deploys the base fleet.
- Generator quirks noted, not fixed: every Go service hand-writes the ConfigState "not hosted" refusal and a
  `<Svc>ConfigStateConfig` no-op (tacplus emits a config hook per gRPC service incl. the appended state service).

## 2026-10-24 — every proof-fleet service generates from fleet-seatrials
- The fleet is 10 services: kernel, opa, gateway, letsencrypt, eventbus, stratum, oauth (user login; kernel and gateway
  reference its contract), llmproxy, doppelganger, tenantecho. Lock pushed; llmproxy admits exactly ["doppelganger"].
- Generated from build/proto and verified: doppelganger (tests), tenantecho (tests), opa (build), letsencrypt (build),
  llmproxy (build), gateway (88 tests), oauth (104 tests, standalone), eventbus (compile), stratum (main compiles),
  kernel (441 tests, 0 failures, 1 skipped). tacplus also learned to emit GcsArchiveSink only for the record store
  (the fleet-wide pin had handed it, and the SDK dependency, to every Java service).
- Fleet-knowledge fixed along the way: oauth's MintToken → sys minter (`minter.Minter` client); eventbus now OWNS the
  trace topic (`eventbus.TraceEventBus.trace_events`; it used to be agentsensor's); gateway tests: tenantecho is the
  live tenant-owned set, routing test takes any-user routes; kernel ConfigOverridesTest fixture uses oauth/llmproxy,
  not mailbox; stratum's agentsensor hot path (typed map, Calcite SQL, cold-start replay) deleted.
- Standalone `depends` added: oauth → kernel; eventbus → kernel. GCS SDK pinned (2.69.0) in stratum and the fleet.
- stratum's test suite retargeted to letsencrypt.LetsEncryptCert (stratum 473c459): 133 run, 0 failures, 7 skipped —
  the hot tier's write-path tests skip in a fleet with no (spec.store_epoch) record; 3 agentsensor-only tests deleted. OPEN: fleet-dev is redundant now —
  Jody to say delete. OPEN: cluster create/destroy + deploying the 10 services and two tenant namespaces.

## 2026-10-24 — deploying the proof fleet (in progress)
- fleet-seatrials now carries: `cluster/create.sh|destroy.sh` (GKE zonal `seatrials`, 3× e2-standard-2 amd64, us-central1-a;
  static IP `seatrials-gateway` = 146.148.80.132), `images/` (Cloud Build: `images/build.sh tacplus|all|<svc>` — clones each
  repo at the lock's commit, copies build/proto in, builds in `ci/tacplus-build:<tacplus commit>` = ghcr build image +
  pinned tacplus; pushes `<AR>/schematic/<svc>:<fleet commit>` and `:local`), `deploy/build-operator.sh` (operator library
  @ pinned commit generated against build/proto → bin/schematic-operator), `deploy/bootstrap/` (operator.yaml: one of
  everything, tenants alpha+beta, app_services llmproxy/doppelganger/tenantecho, nip.io fqdn, cert-chain TLS; services/*.yaml).
  build/proto is committed. `schematic-operator generate` renders 56 chart files incl. tenantecho-alpha/-beta.
- tacplus: a library's own proto joins an explicit fleet --src tree (operator against build/proto works). operator: blobstore
  volume groups + mailbox example writer removed (Seabeam's fleet repo carries them); green against the proof fleet.
- Found on the way: the operator's hardcoded platform set is kernel/eventbus/gateway/oauth/opa/stratum/kmaker(+optional
  letsencrypt etc.); everything else is `app_services`. kmaker is always rendered → added to the fleet (11 services).
  Tenant-owned instances share ONE k8s namespace today (`tenantecho-alpha`, `-beta`); per-tenant namespaces = Admiral/Skipper.
- OPEN before the proof answers: (1) llmproxy's four Anthropic federation ids (services/llmproxy.yaml) — Jody; (2) an OAuth
  provider (Google client id/secret, redirect to the nip.io fqdn) so a user can log in — Jody; (3) doppelganger's context
  repo at repo_dir (a git-sync sidecar with a deploy key, fleet-layer) — until then NEEDS_CONTEXT answers; (4) the local
  client itself (oauth Session bidi + kernel CliTokenBroker, as cavalier's curl-login.sh does).
- Jody 2026-10-24: **every service image runs under the launcher** (github.com/tacplus-dev/launcher, public, ported from
  schematic/launcher: PID 1 that forwards signals, propagates the exit code, kills a deadlocked child on gRPC health).
  All 11 Dockerfiles: `COPY launcher-linux-${TARGETARCH} /usr/local/bin/launcher`, `ENV LAUNCH_CMD=<svc> HEALTH_PORT=8443`
  (kernel 5000), `ENTRYPOINT ["launcher"]`. The fleet's build image (`ci/tacplus-build:<tacplus commit>`) carries it.
- FIRST APPLY to the seatrials cluster (2026-10-24 ~05:48Z): all 11 workloads scheduled, images pulled from Artifact
  Registry, launcher as PID 1 everywhere. kernel, gateway (LB 146.148.80.132, "tenant-owned services route per caller
  tenant: tenantecho"), llmproxy, oauth, kmaker came up 1/1. Go services (opa, doppelganger, tenantecho) and Java
  (stratum, eventbus) stuck: X509Mint denied — "delegate … not authorized for service minter". Root cause in tacplus:
  moving ServiceHealth into sys/ made the chassis read the state store's identity as the proto package (servicehealth)
  and left the minter package unmapped, so services minted for audiences kernel never serves. Fixed in tacplus 1eca5e4
  (DiscoverStateRoot carries the root's host label; the minter package maps to its host; Go/Java/Rust/TS). Images
  rebuilding at that pin; redeploy next. deploy/secrets.sh creates the (empty) oauth pipeline secret the chart expects.
- 2026-10-24 ~06:40Z: after the audience fix, opa, tenantecho-alpha, tenantecho-beta and doppelganger are 1/1 and SERVING
  (doppelganger needed /var/lib/doppelganger owned by its user in the image). Java (kernel ok; stratum, eventbus) also
  pinned TLS identity "servicehealth" when dialing kernel's state store → fixed in tacplus 89f3393 (Java service manager
  uses the health root's host label); Java images rebuilding.
- 2026-10-24 ~07:15Z: 10/11 pods 1/1 (stratum waiting on its 2-writer floor; set stratum_nodes: 2). The LOCAL CLIENT
  exists: fleet-seatrials/client (`seatrials`, a tacplus `kind: client` project generated against build/proto; registry
  id 104). Auth path = cred_pair: keygen → public JWK into operator.yaml root_cred_pairs (root) or a tenant's
  kernel.cred_pairs; login signs a proof (kid = pair id, aud kernel, jti, namespace) → kernel.Mint/MintClientToken through
  the gateway (self-authenticating) → token. **Root login works** (subject cred-pair:…, namespace "/"). Found: kernel's
  cred_pair tokens carried no `principal` claim, so kernel's own authenticator refused them on TenantAdmin ("token
  missing caller identity"); fixed in kernel (setPrincipal), image rebuilding. Next: tenant create/cred alpha+beta,
  login as each, ping (served_by must name the caller's tenant), ask (doppelganger → llmproxy; federation ids still
  needed from Jody for the model call itself).
- 2026-10-24 ~07:50Z: tenants alpha and beta exist and both LOG IN through the gateway. Three findings on the way:
  (1) kernel requires every tenant config to hold a cred_pair that can administer the tenant (a kernel.TenantAdmin
  grant) AND every pair's grants to sit inside the tenant's `constraints.entitlements_ceiling`, which a bare
  CreateTenant leaves empty — so `seatrials tenant create` now sets the ceiling to
  KERNEL_TENANT_ADMIN_{GET,UPDATE}_TENANT_CONFIG and tenant pairs default to exactly those grants (tenantecho and
  doppelganger take any authenticated user, so nothing else is needed). `tenant delete` added; kernel has no tenant
  update RPC. (2) A kernel-minted client token carries aud "kernel"; tenantecho refused it ("token audience kernel
  does not match target service tenantecho"). The right path already exists in generated code: client_im
  .ClientIdentityManager.GetTokenFor(service) exchanges the access token through gateway.Mint/MintToken
  (minter.Minter hosted by kernel) for a service token per target. The generated c2sm only accepted one token for
  every target, so tacplus 81f9462 adds c2sm.NewWithTokenProviderFor (per-service provider; New keeps its shape);
  seatrials now implements client_im.TokenSource for a cred pair (login = EnsureAuthenticated, saves the kernel
  token; every call exchanges per service). Root is correctly refused by tenantecho ("/" is not tenant-scoped).
  (3) The gateway then dialed a placeholder for `tenantecho-alpha`: its health index filed instances only under the
  bare service id (and a mis-split SPIFFE path), while the proxy routes a tenant-owned service by
  `<svc>-<tenant>` from the caller's token. gateway 2e31461 indexes a tenant-owned instance under that key from its
  SPIFFE identity (`spiffe://local.dev/tenants/<t>/services/<svc>`); image building, then rollout restart.
  Also: operator e06f0e4 (a topic's callers reach its broker) is deployed and kernel restarted; stratum-0 then refused
  a HOT RESTORE ("only 1 of 2 instances contributed a durable copy, below min_durable_copies 2") after its earlier
  crash loop — reset both stratum PVCs (proof data is disposable). Open for Jody: a 2-node stratum cannot recover a
  lost node through that guard without an operator path. Fleet-side note: the fleet.lock gateway pin commit also
  swept in the regenerated client (one commit, 2 changes).
- 2026-10-24 ~08:05Z: the gateway fix alone did not route: the health index never saw a tenant identity, because the
  SERVICE'S OWN health identity was wrong in two ways, both in tacplus. (a) `ServiceLabel` derived the service name
  from the module directory above the generated output, so every project generating into `<module>/tacplus`
  (tenantecho, doppelganger — the derived-package layout) was labelled "tacplus": its authorizer accepted only aud
  "tacplus" and its health SPIFFE id named no real service. Now the label is the proto package (SelfPackage), the id
  kernel/gateway/audiences already use. (b) The health identity was assembled from config via ServiceSpiffeID, which
  can only ever say `/system/<location>/services/<label>`; a tenant-owned instance's identity is
  `spiffe://<td>/tenants/<t>/services/<svc>` and only the token kernel minted says so. Go, Java and Rust service
  managers now publish the SUBJECT OF THEIR OWN SERVICE TOKEN (validated as a SPIFFE id, fail loudly without one).
  tacplus 8f6e972 (tests + full suite green; kernel Java compiles, llmproxy cargo check clean). Fleet pin updated;
  ci/tacplus-build rebuilding, then tenantecho + doppelganger images, then restart and the ping proof.
  Also: tenantecho 77883f6 reports `served_by` = its SPIFFE identity (the bare service path is "tenantecho" for every
  tenant, which is exactly what the proof must distinguish). Open question for Jody: the gateway's generic SPIFFE
  endpoint index (`extract_spiffe_service_path`, org/cluster/service split) predates the current identity shape and
  yields "alpha/services/tenantecho"; unused by routing today, left as is.
- 2026-10-24 ~08:25Z: **TWO-TENANT PROOF PASSES.** Through the gateway at 146.148.80.132, `seatrials ping --as alpha`
  answers `served_by: spiffe://local.dev/tenants/alpha/services/tenantecho, caller_namespace: /tenants/alpha`; beta
  answers from `/tenants/beta/services/tenantecho`; root (namespace "/") is refused by the gateway as not tenant-scoped.
  The gateway's health index now carries `tenantecho-alpha` and `tenantecho-beta` as route keys. All 13 pods 1/1
  (stratum cold-started). `ask --as alpha` reaches doppelganger (per-service token accepted) and fails cleanly on the
  context repo: `/var/lib/doppelganger/repo: no such file or directory` — the open fleet-layer question of how the
  context repo is populated (git-sync?) plus llmproxy's federation ids, both Jody's call.
- 2026-10-24 ~15:40Z (Jody's decisions): stratum runs 3 nodes (2 is below its durability floor: a node restoring from
  peers needs two durable copies besides itself; the hot-restore guard is right, 2 nodes is just unsupported).
  Doppelganger context comes in THROUGH THE SERVICE (option 2), stored on the instance's disk until stratum is ported,
  then in stratum. Doppelganger is TENANT-OWNED (one instance per tenant, shared by its users) with two context
  layers: the tenant's (UpdateTenantContext, grant DOPPELGANGER_DOPPELGANGER_UPDATE_TENANT_CONTEXT, which the
  tenant ceiling and tenant-admin pairs now carry) and each user's own (UpdateContext, any user). Sessions belong to
  their opener. doppelganger 6e5d65c; gateway fde72dd (registry test: tenantecho + doppelganger); client gets
  `context push DIR [--tenant]`. Every image is now built on tacplus 8f6e972 and restarted (all 1/1); operator
  re-applied → doppelganger-alpha0/beta0. Found: deploy/build-operator.sh picks `tacplus` from PATH unless TACPLUS is
  set, and ~/.local/bin/tacplus is a stale local build (version 0.0.0) — run it with TACPLUS=<pinned build>.
- 2026-10-24 ~16:30Z: doppelganger-alpha0/beta0 crash-looped on "RepoDir is required": kernel's per-service config
  root (<Svc>ConfigState) is namespace-chrooted, and a tenant-owned instance is a confined caller with no entry of
  its own, so every per-tenant instance read an EMPTY config (tenantecho's greeting was empty for the same reason,
  unnoticed because nothing there is required). Fixed in kernel codegen 847ce57: a service's config root reads the
  fleet's system entry as the tenant's default; a tenant-level entry, once written, wins; writes stay chrooted. That
  is the "tenant level config" shape with sane defaults, no per-tenant fan-out at install. Also: ContextFile.content
  now allows 1 MiB (the default bytes cap is 32 KiB; the jody repo has an 84 KB file). Kernel + doppelganger images
  rebuilding, then restart and the context push / ask proof.
- 2026-10-24 ~17:05Z: **ask path proven to the provider's door.** `seatrials ask --as alpha` → gateway → doppelganger-alpha
  (tenant instance, answers from alpha's uploaded user context) → llmproxy → Anthropic, which answers 401 "x-api-key
  header is required": the four federation ids in services/llmproxy.yaml are still empty. That is the last input and
  it is Jody's. Beta's instance correctly has no context ("the tenant has none and the user has uploaded none").
  tenantecho now shows its configured greeting (the config-root fix verified). Along the way: doppelganger's
  model_alias is "claude-sonnet", the one alias llmproxy's table serves (anthropic.rs maps it to claude-sonnet-4-5 —
  a dated id, Jody's call); a bundle change (services/*.yaml → apply) needs a kernel restart to load, only overrides
  are live. The gateway validates proxied messages against the protos it was BUILT with, so a service proto change
  (ContextFile.content 1 MiB) needs a gateway image rebuild too — rebuilding now, then the tenant-context push of the
  jody repo (84 KB file) goes through.
- 2026-10-24 ~17:40Z: full prompt goes through. llmproxy 24a3af2 sizes text blocks, system and the answer at 4 MiB
  (the 32 KiB default refused a 213 KB prompt). With the tenant's whole jody repo (9 files, 184 KB) plus alpha's own
  file uploaded, `ask --as alpha` reaches Anthropic and stops at 401 (no federation ids). Note: a per-tenant pod has
  no volume, so uploaded context does not survive a restart until it lives in stratum; re-push after a rollout.
  ALL 14 PODS 1/1. Waiting on Jody: the four Anthropic federation ids (services/llmproxy.yaml), and whether
  llmproxy's alias table should move off claude-sonnet-4-5.
- 2026-10-24 ~18:40Z (Jody): "The config should be held in kernel state. The tenant/user config in stratum. we should
  go ahead and port stratum now." Clarified: services/<svc>.yaml in the fleet repo is only the SOURCE of a service's
  typed Config; the operator renders it into the kernel bundle and kernel serves it as config state (so it already is
  kernel state; a bundle change needs a kernel restart, only overrides are live). Stratum is already a fleet service
  (3 nodes), so "port" = move doppelganger's context onto the tacplus record plane with stratum as the store:
  doppelganger a172d21 declares `ContextDocument` as a (spec.record) (key document_id = digest of tenant/scope/owner/
  path, owner, store_namespace = tenant_namespace, scope "tenant"|"user", (spec.deleted_flag) deleted, residency 90d
  idle / 30d deleted), s2s producer + s2s_read doppelganger, s2s_subscribe stratum, file-level (spec.record_read),
  Config publishes on `doppelganger.ContextDocumentBus.stream`; repo_dir is gone. Writing = publishing (stratum stores
  on its subscription); a replacement republishes every file and publishes the dropped rows deleted; reading = the
  record query plane (CreateContextDocumentQuery → ListContextDocument → Delete). Same shape as letsencrypt's cert
  record. Sessions (state_dir) are the next thing to move. llmproxy 8f342f7: Config.models (alias, provider_model,
  prices) replaces the in-code alias table and LP4(b)'s in-code pricing — the model changes by config push. Lesson:
  the KERNEL carries every service's typed Config, so a Config field change needs a kernel image rebuild (bundle
  fields the kernel's types do not know are dropped on parse) — same class as the gateway/proto rule. Images for
  kernel, stratum (new subscription), eventbus (new topic), doppelganger building; operator rebuild + apply next.
- 2026-10-24 ~18:45Z: **CONTEXT IS IN STRATUM AND SURVIVES THE POD.** With kernel, stratum, eventbus, doppelganger
  rebuilt and the operator re-applied (14/14 pods 1/1): `context push --tenant` (9 files, 184 KB) and a user push
  publish ContextDocument rows; stratum's RecordMap logs Create/List/Delete ContextDocument queries on every answer;
  after `rollout restart deploy/doppelganger-alpha0` with NO re-push, `ask --as alpha` still reaches Anthropic (401,
  federation ids). Beta's instance still has no context. llmproxy resolves `claude-sonnet` from Config.models now
  that the kernel's types know the field. Waiting on Jody: the four federation ids (see 17:05Z entry and the recap);
  next move: sessions (state_dir) into stratum the same way, then nothing is on any instance's disk.
- 2026-10-24 ~19:40Z: **SESSIONS ARE IN STRATUM TOO; NOTHING IS ON ANY DOPPELGANGER DISK.** doppelganger 5a27bbd/256a44f:
  `Session` record (key session_id, owner, store_namespace tenant_namespace, context, turns as SessionTurn{question,
  GenerateAnswerResponse}, deleted_flag, 30d/30d residency), published on `doppelganger.SessionBus.stream`, read by
  point query on the key; a write-through cache (10 min) of this instance's own publishes covers the open-then-ask
  gap before stratum has stored the row. state_dir gone from Config. Proof: `ask` opened session 27fb416e…, then
  `rollout restart deploy/doppelganger-alpha0`, then `ask --session 27fb416e…` on the NEW pod continued it (reached
  llmproxy → Anthropic 401), while a made-up id is NotFound. Context likewise survived the rollout with no re-push.
  Kernel, stratum, eventbus, doppelganger rebuilt; operator re-applied; 14/14 pods 1/1. Client: `ask --session ID`.
  personalauth is boneyard (Jody) — not in the port list. Still waiting on the four Anthropic federation ids.
- 2026-10-24 ~20:30Z: **The fleet is a public OIDC issuer: https://seatrials.tacplus.dev.** Jody: the nip.io name with a
  private CA is not valid for Anthropic's federation; use Let's Encrypt and a tacplus.dev hostname (persistable). A
  record seatrials → 146.148.80.132 added at GoDaddy (via Chrome, his session). operator.yaml: fqdn
  seatrials.tacplus.dev, tls.letsencrypt true; apply deployed letsencrypt0, opened port 80 on the gateway LB for
  HTTP-01, kernel restarted on the new issuer; LE issued CN=seatrials.tacplus.dev (notAfter 2027-01-22) and the
  gateway swapped to it; discovery + JWKS now verify with public CAs (ssl_verify=0). kernel.yaml gained the
  letsencrypt delegate (operator-written). Client default gateway is seatrials.tacplus.dev:443, no SEATRIALS_CA.
  Anthropic console: issuer (discovery, no CA), service account, rule (sub prefix
  spiffe://local.dev/system/services/llmproxy, aud https://api.anthropic.com, scope workspace:inference) → the ids
  go into services/llmproxy.yaml. llmproxy's delegate already carries external_audiences [https://api.anthropic.com].
- 2026-10-24 ~21:15Z: **Federation works.** Console (via Chrome, Jody's session): issuer `seatrials`
  fdis_01JKxG3CyMXqfgcG8HoqorvT (https://seatrials.tacplus.dev, discovery, no CA, jti single-use), service account
  `llmproxy-seatrials` svac_01EoVk8wqp7sVYgBkRyyd5MT (developer, Default workspace), rule `llmproxy-seatrials`
  fdrl_01Fvxu1oSKHN6rwtdZmRbGY8 (sub spiffe://local.dev/system/services/llmproxy, aud https://api.anthropic.com,
  scope workspace:developer — the console offered only developer/org:admin — 10-min tokens). Org
  deec78e0-a50d-4817-89e4-043b3d94a5a0, workspace wrkspc_017AfVbSBgLY399kXGDRtQ6e; all four in services/llmproxy.yaml
  (ids, not secrets). Authentication events show Success; credits moved, so Anthropic generated a reply. The reply
  failed llmproxy's parser: "no text or tool_use block in content" — block type unknown to it. llmproxy 34fa79c now
  names the stop_reason and block types in that error. doppelganger 4faf7c0: context reads see this instance's own
  push before stratum has the rows (same write-through cache as sessions; beta's push-then-ask read "no context").
- 2026-10-24 ~22:10Z: **DOPPELGANGER ANSWERS.** llmproxy 56814d7 defaults max_tokens to 4096 when a caller sets none (0
  on the wire gave Anthropic no room: stop_reason max_tokens, no content). With that, `ask --as beta` (one user file)
  and `ask --as alpha` (the whole jody repo as tenant context + a user file) both return real, sourced answers
  through gateway → doppelganger-<tenant> → stratum (context, session) → llmproxy → Anthropic (federated token). The
  proof fleet does everything Jody asked. Also underway (Jody: "no web UI; base login should be part of this"):
  gateway/webapp — a tacplus TypeScript client (registry id 105, proto/clients/webapp.proto: W2S oauth.Auth +
  gateway.Mint) that is the gateway's own web console, today the sign-in page (ListProviders → Authorize with the
  page as callback → MintCallbackTokens → identity; sign out revokes). Built in the gateway image step from webapp/
  into webapp-dist/ (the fleet's service.cloudbuild rust step). Lessons: the gateway's .gitignore `clients/` swallowed
  proto/clients/ (now excepted); a proto cannot sit at the proto root. Blocked on Jody for a real Google OAuth client
  (oauth.yaml has placeholders) and where its client_secret lives (proposed: oauth.Secrets.google_client_secret via
  oauth-pipeline-secrets, not the yaml).
