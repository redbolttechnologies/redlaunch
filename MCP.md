# Redlaunch MCP server

`redlaunch mcp` is a local stdio server for agents building GitHub Actions
workflows and Redlaunch integrations. It uses the existing binary, requires
no additional dependencies, and provides versioned guides bundled with that
binary. Documentation and workflow generation work offline without OAuth,
SQLite, Docker, systemd, a local `.env`, or a running Redlaunch instance.

## Build and connect

From the Redlaunch checkout:

```sh
make build
./bin/redlaunch mcp
```

The process waits for newline-delimited MCP JSON-RPC messages on stdin. stdout
contains protocol messages only; startup failures go to stderr. Closing stdin
ends the session. Let your MCP client launch the process with an absolute path:

```json
{
  "mcpServers": {
    "redlaunch": {
      "command": "/absolute/path/to/redlaunch/bin/redlaunch",
      "args": ["mcp"]
    }
  }
}
```

Use your client's equivalent stdio configuration if its format differs.
Keep the local binary up to date with the server to match the deployed API.

## Tools and resources

| Tool | Arguments | Behavior |
| --- | --- | --- |
| `read_guide` | `topic` | Read `api-tokens`, `recipes`, `ssh-keys`, `installation`, `reference`, or `mcp`. |
| `generate_run_workflow` | `application_id`, `service_name` | Return a manual GHA workflow that dispatches a run and polls it. It uses `vars.REDLAUNCH_URL` and `secrets.REDLAUNCH_RUN_TOKEN`; no run is dispatched. |
| `get_run_status` | `application_id`, `service_name`, `job_id` | Poll the configured Redlaunch API. Available when API credentials are configured. |
| `run_service` | `application_id`, `service_name` | Dispatch a registered one-off service. Available only with credentials and `--allow-run`. |

The same guides are available through `resources/list` and `resources/read`
at `redlaunch://guides/<topic>`. Only these bundled public guides can be read;
resource URIs do not grant access to the local filesystem or environment files.

Example agent requests:

- "Read Redlaunch's recipes and build a workflow that pushes an image to its
  registry, then deploys the managed application over SSH."
- "Generate a manual migration workflow for application 7, service migrate.
  List the GitHub variables and secrets I need to configure."
- "Use the API tokens guide to implement a client that dispatches a one-off
  service and polls its status."

The API supports registered one-off service runs and job polling. It does not
provide application inventory, image publishing, deployments, arbitrary shell
commands, Compose editing, or token creation. Agents should use the `recipes`
and `ssh-keys` guides for SSH deployments rather than inventing API endpoints.
Find the application ID in the web URL `/applications/<id>`; use the exact
Compose service name, not a container name.

## Optional live API tools

Create a token in **Settings → API tokens**, pinned to the intended application.
Set these environment variables in the MCP subprocess using your client's
secret storage or your shell's secure environment configuration:

| Variable | Purpose |
| --- | --- |
| `REDLAUNCH_URL` | Management HTTPS origin, for example `https://redlaunch.example.com`, or a loopback HTTP origin such as `http://127.0.0.1:8080` through an established SSH tunnel. |
| `REDLAUNCH_RUN_TOKEN` | Application-scoped Bearer token. Never put its value in tool arguments, chat, tracked client configuration, or generated workflows. |

Both variables must be provided together. URLs must have no embedded
credentials, path (apart from `/`), query, or fragment. Plain HTTP requires a
loopback IP literal. Remote access uses HTTPS with normal certificate
verification; redirects are refused so credentials cannot be forwarded to a
new destination. The process does not open an SSH tunnel itself.

Credentials alone enable status polling. To allow agents to dispatch services,
change the client's command arguments to `["mcp", "--allow-run"]`.
This option exposes a mutating tool: services may run migrations or other
admin-defined tasks. The existing API still enforces token expiry and
application scope. There is no additional MCP credential or permission scope.

`run_service` returns `status` and `job_id`. Pass that ID to `get_run_status`
until it returns `complete` or `failed`. Polling returns only the status, with
service output omitted. Transport errors report generic messages and HTTP
errors report status codes without forwarding upstream response bodies.

Concurrent duplicate dispatches reuse an active job. A dispatch after completion
starts another run, so do not retry blindly after a timeout or lost response.
Make migration services idempotent. Jobs are process-local, disappear after a
Redlaunch restart, and expire one hour after finishing. A `404` requires
operator investigation before redispatching.

## Protocol and limits

The server implements the [MCP stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)
with initialization, ping, tools, and resources. It negotiates revisions
`2024-11-05`, `2025-03-26`, `2025-06-18`, and `2025-11-25`; other requested
versions receive `2025-11-25` for the client to accept or reject. It does not
advertise subscriptions, prompts, tasks, or list-change notifications.

Requests are handled sequentially. Messages are limited to 1 MiB and API
responses to 64 KiB. Each API request has a 30-second timeout. Client
cancellation notifications do not cancel an admitted server-side job;
terminating the MCP process cancels its HTTP request, but the Redlaunch job may
continue. The generated workflow caps the job at 15 minutes and its polling
at 60 attempts; timeout does not stop the server-side task.

For authentication, reachability, status codes, and rotation, see
[API tokens](API_TOKENS.md). For complete build and deployment workflows, see
[Recipes](RECIPES.md).
