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
