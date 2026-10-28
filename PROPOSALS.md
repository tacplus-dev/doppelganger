# Proposals: doppelganger

Written by loopers. Each proposal: what, why, links. Jody promotes accepted ones into ROADMAP.md (via an agent);
the looper then marks it `promoted` or `declined` here with the date.

## Open
- **Handlers take the tenant from the instance, not the token** (2026-10-28, #6): `callerIdentity` (internal/daemon/server.go) uses the caller token's namespace as the tenant. The tenant door also admits `/system` callers, whose namespace is not `/tenants/<tid>`, so their context or sessions would be keyed under `/system`. Since 2.5 the request's `tenant` (the route field, checked against the instance by the generated server) or the instance's own tenant says which tenant is meant; use that, and refuse or define what a `/system` caller may do.
- **tacplus: a route field marked `assert_required` breaks the generated MCP tests** (2026-10-28, #6): with `string tenant = N [(spec.assert_required) = true]` and `route_field: "tenant"`, the generated `doppelganger_mcp_test.go` passes `tenant` as a tool argument and dispatch refuses it ("tenant is set by the server from your session's tenant; it is not a tool argument"), so `go test ./tacplus/...` fails. Doppelganger leaves the field unasserted (the route field is already refused empty or malformed). Worth a tacplus bug, or `ValidateTenantRouting` refusing the combination.

## Promoted / declined
