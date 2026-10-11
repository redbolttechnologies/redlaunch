# Using Redlaunch MCP

Redlaunch MCP lets your coding assistant read Redlaunch's guides and help you
write GitHub Actions workflows and manage server resources. You can ask for help in everyday language,
such as “Create a workflow to run my database migrations.” MCP is the connection
that gives the assistant access to these Redlaunch tools.

For a running VPS, use [remote OAuth MCP](#remote-oauth-rollout): configure its
URL and sign in, without installing a local Redlaunch binary or supplying token
environment variables. The URL can be committed; assistant credentials cannot.

For offline guides or existing stdio integrations, start with the setup below.
It can read guides and generate workflows
without connecting to your server. Later, you can optionally let the assistant
check an existing task's progress, start a one-off service, or manage applications
with a separate server-wide management token.

## Before you start

You need:

- **A working coding assistant:** Codex, OpenCode, or Claude Code installed and
  signed in or configured with a model provider.
- **The Redlaunch program on the same computer as your assistant.** If you use
  the assistant on your laptop, install or build the program there. Installing
  Redlaunch on your VPS alone does not provide the program on your laptop.
- **For building from source:** Git, Make, and Go with support for the version
  pinned in the repository's `Makefile`. The build downloads that Go toolchain
  and dependencies if needed, so the first build needs internet access.

The basic MCP setup does not need Docker, a running Redlaunch web application,
Google login credentials, a database, or an API token. Once built, its bundled
guides and workflow generator work offline; your assistant may still need
internet access to reach its model provider.

The terminal examples below use Linux or macOS paths. Run them on the computer
where you use your assistant.

## Step 1: Prepare the Redlaunch program

If you already have a compatible Redlaunch executable on this computer, use
its full path and skip the build. Otherwise, open a terminal and run:

```sh
git clone https://github.com/redbolttechnologies/redlaunch.git
cd redlaunch
make build
```

If you already have the source code, run `make build` in that checkout instead.
The program will be created at `bin/redlaunch` inside the checkout.

Find its full path:

```sh
pwd
```

Append `/bin/redlaunch` to the directory printed by `pwd`. For example, if it
prints `/home/alex/redlaunch`, your executable is
`/home/alex/redlaunch/bin/redlaunch`.

In every example below, replace `/absolute/path/to/redlaunch/bin/redlaunch`
with **your executable's full path**. Use a local build matching the version
on your VPS when you connect to a running server.

Your assistant starts `redlaunch mcp` automatically. You do not need to keep
another Redlaunch process running in a terminal. In client settings, this is
a **local command** connection, sometimes called **stdio**. The Redlaunch web
address is used only for the optional server connection in Step 4.

## Step 2: Connect your coding assistant

Follow just the instructions for your assistant. If a configuration file
already exists, merge the Redlaunch entry into it and keep your other settings.

### Codex

1. Open or create `~/.codex/config.toml` in your home directory.
2. Add this entry, replacing the executable path:

```toml
[mcp_servers.redlaunch]
command = "/absolute/path/to/redlaunch/bin/redlaunch"
args = ["mcp"]
```

3. Save the file and start a new Codex session.
4. Type `/mcp` in Codex to check that Redlaunch is connected. From a terminal,
   `codex mcp list` also shows the configured entry.

The Codex CLI and IDE extension share this configuration. Restart the extension
if you use it there. See the [official Codex MCP guide](https://developers.openai.com/codex/mcp/)
for other configuration options.

### OpenCode

1. Open or create `opencode.json` in the project where you use OpenCode.
2. Add the following configuration, replacing the executable path. In OpenCode
   V2, server names belong under `mcp.servers`. Check your installation with
   `opencode --version`; V1 used names directly under `mcp`.

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "redlaunch": {
        "type": "local",
        "command": ["/absolute/path/to/redlaunch/bin/redlaunch", "mcp"]
      }
    }
  }
}
```

3. Save the file, then run `opencode mcp list` from that project directory.
   Redlaunch should show as connected.
4. Start a new OpenCode session in the same directory.

To make it available in all projects, put the entry in
`~/.config/opencode/opencode.json` instead. Merge the `redlaunch` entry into
your existing `mcp.servers` object if other servers are already configured. See the
[official OpenCode V2 MCP guide](https://opencode.ai/v2/docs/mcp-servers).

### Claude Code

1. Open a terminal in the project where you use Claude Code.
2. Register Redlaunch, replacing the executable path:

```sh
claude mcp add --transport stdio --scope project redlaunch -- /absolute/path/to/redlaunch/bin/redlaunch mcp
```

3. This creates or updates `.mcp.json` in your project directory. Start a new
   Claude Code session there and approve the project MCP connection if prompted.
4. Type `/mcp` in Claude Code and check that Redlaunch is connected.
   You can also run `claude mcp list` in the terminal.

Use `--scope user` instead of `--scope project` if you want Redlaunch available
in all your projects. The optional configuration below uses the project file.
See the [official Claude Code MCP guide](https://code.claude.com/docs/en/mcp).

## Step 3: Try your first request

Ask your assistant:

> Use Redlaunch MCP to read the installation guide and explain how to create
> my first application. Do not make any changes.

The assistant should use Redlaunch's `read_guide` tool and answer using the
bundled guide. No server connection or token is needed.

Next, try a workflow request:

> Use Redlaunch MCP to generate a manual GitHub Actions workflow for application
> 7 and service migrate. Explain which GitHub variables and secrets I need,
> and do not run the service.

Replace `7` with your application ID and `migrate` with your service name.
Open the application in Redlaunch: a URL ending in `/applications/7` means
its ID is `7`. Use the service's name from `compose.yml`, not its container name.

The `generate_run_workflow` tool returns a workflow for you to review. To use it:

1. Save it as `.github/workflows/redlaunch-run.yml` in your application repository.
2. In that repository's **Settings → Secrets and variables → Actions**, add
   a variable named `REDLAUNCH_URL` with your Redlaunch HTTPS address and a
   secret named `REDLAUNCH_RUN_TOKEN` with a token for this application.
   Follow [API tokens](API_TOKENS.md#create-a-token) to create one.
3. Check that the GitHub runner can reach Redlaunch. If you use an SSH tunnel,
   the workflow must establish its own tunnel; your laptop's tunnel is not
   available to GitHub Actions. See [API reachability](API_TOKENS.md#reachability).
4. Commit the reviewed workflow. Start it manually from GitHub's **Actions** tab
   when you are ready to run the service.

Generating the workflow does not start a task or deploy anything. For image
publishing and SSH deployment workflows, ask the assistant to read
[Deployment recipes](RECIPES.md).

## Step 4: Connect to your VPS when needed

Skip this step if you only want guides and generated workflows.

A server connection lets the assistant check an existing service run's progress.
Starting a run requires the separate opt-in in Step 5. MCP does not provide
an application list through an application-scoped run token. Server-wide
management uses the separate token and tools below. Raw Compose editing, image
publishing, and token creation are not exposed through MCP.

### Prepare the server and token

1. Have Redlaunch running on your VPS, with the application and service
   you want to use already configured.
2. In Redlaunch, open **Settings → API tokens**, select **Create API token**,
   and choose that application and an expiry. Copy the token from the one-time
   dialog. Keep it in your password manager or secret store.
3. Choose how your computer will reach Redlaunch:
   - **HTTPS:** use your published management address, such as
     `https://redlaunch.example.com`.
   - **SSH tunnel:** in a separate terminal, run the command below with your
     SSH username and server. Keep it running while using the assistant.

```sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
```

For the tunnel, use `http://127.0.0.1:8080` as the address. Use the numeric
`127.0.0.1`, because plain HTTP addresses must be loopback IP addresses.
For HTTPS setup, see [Publish Redlaunch over HTTPS](INSTALL.md#publish-redlaunch-over-https).

### Make the credentials available to your assistant

In the terminal where you will launch your assistant, set these two environment
variables. An environment variable is a named setting that a program receives
when it starts. The following example uses **Bash** and prompts for the token
without displaying it or saving its value in shell history:

```bash
export REDLAUNCH_URL='https://redlaunch.example.com'
read -r -s -p 'Redlaunch API token: ' REDLAUNCH_RUN_TOKEN
printf '\n'
export REDLAUNCH_RUN_TOKEN
```

Replace the example address first; for a tunnel, use
`http://127.0.0.1:8080`. Use only the base address, without `/api`, an application
path, a query, or embedded login details. HTTPS requires a valid certificate.

Provide both variables together. MCP does not load them from a `.env` file
or open an SSH tunnel for you. Do not paste the token into chat, tool arguments,
configuration files, or generated workflows.

Update your assistant's MCP entry to pass the variables to Redlaunch:

**Codex:** add this line inside the existing `[mcp_servers.redlaunch]` entry:

```toml
env_vars = ["REDLAUNCH_URL", "REDLAUNCH_RUN_TOKEN"]
```

**OpenCode V2:** add `environment` inside `mcp.servers.redlaunch`. The complete entry
looks like this; keep any other servers already in your configuration:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "redlaunch": {
        "type": "local",
        "command": ["/absolute/path/to/redlaunch/bin/redlaunch", "mcp"],
        "environment": {
          "REDLAUNCH_URL": "{env:REDLAUNCH_URL}",
          "REDLAUNCH_RUN_TOKEN": "{env:REDLAUNCH_RUN_TOKEN}"
        }
      }
    }
  }
}
```

**Claude Code:** in your project's `.mcp.json`, update the Redlaunch entry to
include `env`, keeping any other servers:

```json
{
  "mcpServers": {
    "redlaunch": {
      "type": "stdio",
      "command": "/absolute/path/to/redlaunch/bin/redlaunch",
      "args": ["mcp"],
      "env": {
        "REDLAUNCH_URL": "${REDLAUNCH_URL}",
        "REDLAUNCH_RUN_TOKEN": "${REDLAUNCH_RUN_TOKEN}"
      }
    }
  }
}
```

The examples contain variable references, so the token stays out of the files.
Save your changes, then launch `codex`, `opencode`, or `claude` from the terminal
where you set the variables. A desktop app or a different terminal will need
the same variables in its own launch environment. Set them again in a new shell,
or use your secret manager to supply them.

The assistant now has `get_run_status`. Give it the application ID, service name,
and job ID from an existing run and ask it to check progress. It returns
`running`, `complete`, or `failed`; it does not return service logs.
If you do not have a job ID yet, Step 5 explains how to start a run.

## Step 5: Allow service runs when needed

Enable this only if you want the assistant to start one-off tasks on your VPS.
These tasks use the service command already configured in Redlaunch and can
change your application or database.

After completing Step 4, update the launch arguments:

- **Codex and Claude Code:** change `args` to `["mcp", "--allow-run"]`.
- **OpenCode:** change `command` to
  `["/absolute/path/to/redlaunch/bin/redlaunch", "mcp", "--allow-run"]`.

Save the configuration and start a new assistant session from the terminal
with your environment variables. The `run_service` tool will now be available.
The token limits runs to the application you chose when creating it.

For example:

> Use Redlaunch MCP to run service migrate in application 7 once, then check
> its progress until it finishes. Ask me before starting the run.

The assistant receives a job ID and uses it to check progress. If a request
times out or the connection drops, check the existing run before trying again:
it may still be running, and another request after completion starts a new run.
Closing the assistant does not stop a task already started on the VPS.

To return to checking progress only, remove `--allow-run` and restart the
assistant. The API token itself still authorizes runs; this flag controls
whether the MCP assistant has a tool to start them. Revoke the token in
Redlaunch if it should no longer grant access.

## Server-wide management

Create a token in **Settings → API tokens** with **Scope → Server-wide
management**. Store it as `REDLAUNCH_MANAGEMENT_TOKEN` in the MCP subprocess's
secure environment, alongside `REDLAUNCH_URL`. It grants access to every managed
application and the shared database cluster. Existing application run tokens
cannot call the management API, and management tokens cannot call the separate
application-run API.

Management credentials alone expose inventory and job polling. Enable mutations
with `redlaunch mcp --allow-manage` (client arguments `["mcp", "--allow-manage"]`).
`REDLAUNCH_RUN_TOKEN` and `--allow-run` remain independent and optional; both
credential types can be configured in the same subprocess.

For OpenCode V2, use the same `mcp.servers.redlaunch` structure with the management
credential references and mutation flag:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "redlaunch": {
        "type": "local",
        "command": ["/absolute/path/to/redlaunch/bin/redlaunch", "mcp", "--allow-manage"],
        "environment": {
          "REDLAUNCH_URL": "{env:REDLAUNCH_URL}",
          "REDLAUNCH_MANAGEMENT_TOKEN": "{env:REDLAUNCH_MANAGEMENT_TOKEN}"
        }
      }
    }
  }
}
```

Supply both environment variables when launching OpenCode, then run
`opencode mcp list` to verify the connection. Remove `--allow-manage` to expose
only management inventory and job polling.

| Tools | Required arguments |
| --- | --- |
| `list_applications` | None |
| `get_application`, `delete_application`, `list_services`, `list_environment`, `list_domains` | `application_id` |
| `create_application` | `name`, `folder_name` |
| `rename_application` | `application_id`, `name` |
| `create_service` | `application_id`, `configuration` |
| `update_service` | `application_id`, `service_name`, `configuration` |
| `delete_service` | `application_id`, `service_name` |
| `service_action` | `application_id`, `service_name`, `action` (`start`, `stop`, `restart`, or `run`) |
| `add_variable`, `add_secret` | `application_id`, `name`, `value` |
| `update_variable`, `update_secret` | `application_id`, `original_name`, `name`, `value` |
| `delete_variable`, `delete_secret`, `create_domain`, `delete_domain` | `application_id`, `name` |
| `list_routings` | `application_id`, `domain_id` |
| `create_routing` | `application_id`, `domain_id`, `routing` |
| `update_routing` | `application_id`, `domain_id`, `routing_id`, `routing` |
| `delete_routing` | `application_id`, `domain_id`, `routing_id` |
| `get_database_cluster`, `list_databases`, `list_database_users`, `disable_database_cluster` | None |
| `enable_database_cluster` | `version`, `default_user`, `password`; optional `provider` (PostgreSQL only) |
| `database_cluster_action` | `action` (`start`, `stop`, or `restart`) |
| `create_database` | `name`, `owner` (an existing database user) |
| `delete_database` | `name` |
| `create_database_user` | `username`, `password`, `databases` (an array, possibly empty) |
| `update_database_user_password` | `username`, `password` |
| `set_database_user_permissions` | `username`, `databases` (replaces the access list) |
| `delete_database_user` | `username` |
| `get_management_job` | `job_id` |

`configuration` uses `service_name`, `image_name`, `auto_start`, `entrypoint`,
`healthcheck`, `depends_on`, `restart_policy`, `port_mappings`, and
`volume_mappings`. Creation/update uses the existing custom application-service
workflow, including managed labels, both environment files, mount validation,
and Compose validation. Update replaces the custom service configuration;
it does not patch individual settings. PostgreSQL/Redis presets are not
created by this tool; existing services can be listed, acted on, and deleted.

`routing` uses `subdomain`, `path`, `service_name`, `service_port`, and
`service_path`. IDs come from the domain and routing inventories. Resource
metadata inside `result` retains the application's existing Go field names
(for example `ID`, `Name`, and `ApplicationID`); tool arguments use snake case.

Environment inventory returns names and file availability only, including for
variables, since credentials may have been misplaced in `vars.env`. Writes
never echo values. Empty values are supported, including clearing secrets.
Existing environment parser/writer semantics apply; updates do not automatically
restart application services. Restart or recreate them separately as appropriate.

Create a database user with a supplied password and an empty `databases` array
before creating a database owned by that user. Database creation requires an
existing owner to avoid generating undisclosed credentials. Database/user
permissions and deletion use the same services as the web interface.

All mutations return `status: running` and `job_id`; poll `get_management_job`
until `complete` (with `result`) or `failed`. Results exclude secret values,
credentials, logs, and process error output. Failures are deliberately generic.
Do not retry mutations blindly after failure, timeout, or a lost response:
operations can have partial effects, and retries do not reuse previous jobs.
A completed deletion can remove application files and database data.

Jobs use Redlaunch's tracked operation runtime (eight concurrent operations
across the application, with a 15-minute deadline). Management jobs are retained
for one hour after completion, with at most 1,024 retained jobs. Token expiry
and revocation apply on every dispatch and poll; revocation does not cancel work
already admitted. Server restarts remove jobs; a `404` needs resource inspection
before another dispatch.

## Troubleshooting

| Problem | What to do |
| --- | --- |
| OpenCode rejects the MCP configuration | Check `opencode --version`: V2 uses `mcp.servers.redlaunch`; V1 used `mcp.redlaunch`. Do not mix the two formats. |
| Redlaunch is missing from the assistant's tools | Check the configuration file location, executable path, and JSON or TOML syntax. Start a new session in the configured project. |
| “Command not found” or “Permission denied” | Use the full path to the built executable on the assistant's computer. Rebuild with `make build` if needed. |
| Running `redlaunch mcp` manually appears to do nothing | This is normal: it waits for the assistant. Stop it with Ctrl+C and let your assistant launch it. |
| Guides work, but progress checks are unavailable | Pass both `REDLAUNCH_URL` and `REDLAUNCH_RUN_TOKEN` to the MCP process, then restart the assistant from that environment. |
| Starting a service is unavailable | Complete Step 4 and add `--allow-run` as shown in Step 5. |
| Connection fails | Check the HTTPS address and certificate, or make sure your SSH tunnel is still running. Open the management address in your browser to check reachability. |
| HTTP 401 | The token is invalid, expired, or revoked. Create a replacement and update your environment. |
| HTTP 403 | Check token scope: run tools need a token for the requested application; management tools need a server-wide management token. |
| HTTP 404 | Check the application ID, service name, and job ID. Jobs disappear after a Redlaunch restart and expire one hour after finishing. Investigate the previous run before starting another. |

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

For token rotation and API errors, see [API tokens](API_TOKENS.md). For complete
deployment workflows, see [Deployment recipes](RECIPES.md).
## Remote OAuth rollout

The OAuth authorization-server foundation is implemented: dynamic public-client
registration, signed-in browser consent with S256 PKCE, single-use one-minute
codes, token exchange, refresh rotation, and revocation. Access tokens last one
hour; refresh grants expire after 30 days idle or 90 days absolute. Only token
hashes are persisted. A 60-second in-memory replay window handles lost refresh
responses; that window does not survive a server restart.

Remote MCP is now served over stateless Streamable HTTP at:

- `https://your-vps.example.com/mcp/manage` for server-wide management.
- `https://your-vps.example.com/mcp/apps/123` for runs pinned to application 123.

Configure the URL as a remote MCP server in your assistant, then complete the
assistant's browser sign-in flow. Only the URL belongs in committed MCP
configuration; OAuth credentials are stored and refreshed by the assistant,
not in repository files or Redlaunch environment variables. Changing VPS URLs
requires a separate approval for the new server.

The server publishes resource-specific discovery documents under
`/.well-known/oauth-protected-resource/mcp/manage` and
`/.well-known/oauth-protected-resource/mcp/apps/123`. Tokens are bound to the
exact approved origin and endpoint: management and application endpoints cannot
share tokens. Public access requires a configured Redlaunch domain over HTTPS.
Loopback HTTP is supported and tested with OpenCode and Codex. An actual SSH
forward and public-HTTPS deployment still need end-to-end verification.

The transport accepts single JSON-RPC POSTs with `Content-Type: application/json`
and `Accept: application/json, text/event-stream`. It returns JSON responses or
empty 202 responses for notifications, without session IDs or an SSE stream.
GET and DELETE return 405. Tool calls use the existing machine APIs in-process.
There is no read-only grant split: approval enables the full existing `manage`
or application-pinned `run` permissions. Treat client names as unverified and
approve only connections you initiated.

Existing stdio configuration and manually issued API tokens continue to work
unchanged, but manually issued tokens are not accepted at the OAuth remote MCP
endpoints.

### Remote client configuration

These examples grant **server-wide management**, including mutations. For
application-only service runs, replace `/mcp/manage` with `/mcp/apps/123` and
use the `run` scope instead of `manage`. Do not add an Authorization header,
API token, client secret, or token environment-variable reference.

**OpenCode V2** — merge into `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "redlaunch": {
        "type": "remote",
        "url": "https://your-vps.example.com/mcp/manage",
        "oauth": { "scope": "manage" }
      }
    }
  }
}
```

Run `opencode mcp list`. In OpenCode, use `/mcps`, select Redlaunch, and sign
in. OAuth is enabled by default; the scope selects the resource's permission.
The installed 2.0.26 CLI also supports `opencode mcp auth redlaunch`.

**Codex** — merge into `~/.codex/config.toml` (or your trusted project config):

```toml
[mcp_servers.redlaunch]
url = "https://your-vps.example.com/mcp/manage"
```

Then run:

```sh
codex mcp login redlaunch --scopes manage
```

Use `/mcp` in Codex to inspect the connection. With Codex 0.156.1, the explicit
scope is required: omitting it requests both `manage` and `run`, which Redlaunch
rejects for a single resource. For headless login, add `--no-browser` and paste
the callback URL as prompted. Redlaunch supports dynamic client registration;
`--oauth-client-registration dcr` explicitly selects it if needed.

**Claude Code** — documented configuration; not live-tested here:

```sh
claude mcp add --transport http --scope project redlaunch https://your-vps.example.com/mcp/manage
```

Use `/mcp` in Claude Code to authenticate. Project scope writes the URL to
`.mcp.json`; keep OAuth credentials in Claude Code's own credential storage.
Claude Code was not installed in the verification environment, so its scope
selection, refresh, and loopback behavior remain unverified.

### Loopback and SSH tunnels

Keep the tunnel running on the assistant's computer:

```sh
ssh -N -L 18080:127.0.0.1:8080 user@your-vps.example.com
```

Replace `8080` with the VPS's actual loopback management port. Configure
`http://127.0.0.1:18080/mcp/manage` as the remote URL and sign in through that
same address. Keep the local port stable: changing the origin, port, or resource
requires a new approval. A public-HTTPS approval cannot be reused through the
tunnel. Only the URL changes in assistant configuration; no token variables
are needed. Loopback tests validate the HTTP OAuth flow, not SSH connectivity.

### Interoperability verification

Verified with disposable real-client processes on macOS, using isolated homes
and SQLite fixtures; consent uses a fixture's signed-in session, not a manual
browser login. No model-provider account or Docker workload is used.

| Client | Verified over loopback HTTP |
| --- | --- |
| OpenCode 2.0.26 | Management and application URLs: discovery, DCR, PKCE, consent/code exchange, MCP connection, refresh rotation, reconnect, rejection after revocation |
| Codex 0.156.1 | Management and application URLs: discovery, DCR, PKCE, consent/code exchange, MCP initialization (39 management / 4 application tools), refresh rotation |
| Claude Code | Not installed; live verification pending |

Refresh tests shorten the advertised expiry and simulate an expired access
token through test-only service wrappers; production lifetimes are unchanged.
Resource/application/origin isolation is covered by the regular server tests.
Public HTTPS, an actual SSH tunnel, manual browser login, and live assistant
tool execution against Docker remain separate deployment acceptance checks.

To repeat installed-client checks (absent clients skip):

```sh
REDLAUNCH_MCP_INTEROP_TEST=1 GOTOOLCHAIN=go1.26.8 go test -race -count=1 -timeout 120s -run 'TestMCP(OpenCode|Codex)Interop' ./internal/handler
```

### Manage approved connections

Open **Settings → MCP connections** to inspect each approval's client name and
public client ID, access scope, exact resource URL, last use, and refresh-grant
expiry. Client names are self-reported, not verified identities. Expired grants
remain listed until removed. Last use records successful API authentication
and survives token refreshes; grant expiry is separate from the hourly access
token's expiry.

Choose **Revoke**, check the client and resource in the confirmation dialog,
then confirm. This removes both the current access token and the refresh grant,
including superseded-token verifiers. Subsequent access and refresh requests
fail; the assistant must sign in again to reconnect. Revocation does not cancel
operations already dispatched. OAuth access tokens are hidden from
**Settings → API tokens**, which remains the list for manually issued CI and
stdio credentials. No token values or token hashes are shown in connection
management.
