# doppelganger

Answers questions on Jody's behalf from his context repo (`~/workspace/jody`). Its only role is
to answer. Design and rules: `~/workspace/jody/agents.md`, "An agent that represents him".

## Shape

- A fleet service: the tacplus **C2S** service `Doppelganger`, **tenant-owned** (one instance
  per tenant, shared by that tenant's users; the gateway routes each caller to theirs), reached
  through the fleet's gateway by an authenticated user of the tenant. The generated service
  manager (`tacplus/s2sm`) serves it; `internal/daemon/server.go` is only the handler.
- The model is reached through **llmproxy** over S2S with doppelganger's own identity
  (`depends: [llmproxy]`, `(spec.grpc_s2s_client)`); the fleet build admits doppelganger as a
  caller. Doppelganger holds no model credential.
- Config (`repo_dir`, `state_dir`, `model_alias`) comes from the fleet's config state, delivered
  by the service manager at start and on change. Context is two layers under `repo_dir`, both
  written only through the service: the tenant's shared context (`UpdateTenantContext`, gated by
  that RPC's grant) and each user's own (`UpdateContext`, any user, their own files). Answers
  rest on both. It lives on the instance's disk until stratum is ported, when it moves there.
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
