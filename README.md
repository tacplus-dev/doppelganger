# doppelganger

Answers questions on Jody's behalf from his context repo (`~/workspace/jody`). Its only role is
to answer. Design and rules: `~/workspace/jody/agents.md`, "An agent that represents him".

## Shape

- A daemon (`doppelganger daemon`) serving the tacplus **C2D** service `Doppelganger` on
  `/tmp/doppelganger.sock`. The socket is 0600; filesystem permissions are the boundary.
- A CLI that prints **JSON only**. It is for agents we do not write (claude, codex). Tools we
  write (sideband) call the C2D API with the tacplus-generated client
  (`model/c2dm`).
- Sessions: `session create` returns a `session_id`. Context can be given at creation or later
  with `session update`. Caller context is on the honor system for now. State is one JSON file
  per session in `~/.doppelganger/sessions/`. Stale-session cleanup is not implemented yet.
- Answers come from `claude -p` with **no tools**: the whole repo (minus `inbox/`, which holds
  unreviewed material) is given as input, and the output is constrained to a JSON schema. Each
  answer carries `kind` (ANSWER / NEEDS_CONTEXT / BLOCKED_ON_HUMAN), `reasoning`,
  `would_change_mind`, `provenance` (QUOTED / DERIVED), `sources` and `missing`.
- **Pushback is the next `ask` in the same session.** Earlier turns are replayed, and the
  daemon re-answers with the new information.

```sh
doppelganger daemon &
S=$(doppelganger session create --context "caller: claude agent on schematic" | jq -r .session_id)
doppelganger session update "$S" --context "audience: the agent itself"
doppelganger ask "$S" "Rewrite or fix?"
```

## Build

```sh
just tacplus   # tacplus (go) → ./model  (git-ignored; run after proto changes)
just build
just test
```

## Notes

- Tacplus generates a C2D *server* wrapper only for Rust. For Go it emits the client manager and
  the gRPC registration, so `internal/daemon/server.go` wires the Unix listener itself.
- The socket path is declared in the proto and repeated as a constant in
  `cmd/doppelganger/main.go`, because tacplus does not export it to Go. Keep them in sync.
- Runs locally now; remote later.
