# agent-controller

agent-controller starts, watches and stops sandboxed agent runs on Kubernetes.
Agents and operators use it through an MCP server and a REST API. A policy
object in Git sets the outer limits for everything that callers create.

## Roles

One binary serves two roles. Deploy each role as its own Deployment.

- `agent-controller controller` admits queued `AgentRun` objects, writes the
  resolved run to a ConfigMap and creates the launcher Job. It mirrors the Job
  state into the run status and deletes run resources at the end.
- `agent-controller server` serves the REST API (`/v1`), the MCP server
  (`/mcp`) and the owner-only Logins page (`/logins`).

## Custom resources

All resources are in the API group `agents.mudler.dev/v1alpha1`:

| Kind | Written by | Purpose |
|---|---|---|
| `AgentPolicy` | Git only | Allowed harnesses, egress hosts, model endpoints and limits |
| `Login` | Owner only | Metadata of a credential. The value is in a Secret in the logins namespace |
| `HarnessProfile` | API or MCP | Harness kind, version, model, endpoint and login |
| `AgentProfile` | API or MCP | Repository, prompt, egress, budget, resources and schedule |
| `AgentRun` | API, MCP or schedule | One run of a profile, with an optional task |

## Build

```sh
make generate manifests   # deep-copy code, CRDs and RBAC
make test                 # unit and envtest tests
make build                # bin/agent-controller
```

## Example

```sh
agent-controller token new --name planner --scopes runs:read,runs:write,profiles:write
curl -H "Authorization: Bearer $TOKEN" -d '{"profile":"example","task":"Fix the flaky test"}' \
  http://agent-controller.agents.svc:8080/v1/runs
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
