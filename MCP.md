# Using Redlaunch MCP

Redlaunch MCP lets your coding assistant read Redlaunch's guides and help you
write GitHub Actions workflows and manage server resources. You can ask for help in everyday language,
such as “Create a workflow to run my database migrations.” MCP is the connection
that gives the assistant access to these Redlaunch tools.

For a running VPS, use [browser sign-in](#connect-to-your-server-with-browser-sign-in): configure its
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
## Connect to your server with browser sign-in

You can connect your assistant directly to Redlaunch on your VPS. Add the
server's MCP address to your assistant, sign in to Redlaunch in your browser,
and approve the connection. This is called **OAuth authentication**.

**You do not need to create an API token, set token environment variables, or
install the Redlaunch program on your laptop.** Your assistant saves its
credentials privately and renews them automatically. Signing in to OpenCode,
Codex, or Claude Code itself does not replace signing in to Redlaunch.

### Before you connect

You need a running Redlaunch server, an account that can sign in to it, and
your coding assistant installed on the computer where you want to use it.
For a public connection, configure a Redlaunch domain in **Settings → Domains**
and make sure you can open Redlaunch at its HTTPS address in your browser.
Under **Management access mode** on that same Settings tab, select **Managed
HTTPS**, confirm, and choose **Save and restart**. Wait for Redlaunch to return,
then refresh Settings and check the running mode. Adding a domain alone does
not enable the HTTPS startup mode; without it, public MCP discovery can return
404 even though the login page works. This action does not change your port
bindings or firewall. See [publishing Redlaunch over HTTPS](INSTALL.md#publish-redlaunch-over-https)
if the proxy cannot reach the management port.
If your server is only reachable through SSH, use the [tunnel instructions](#loopback-and-ssh-tunnels).

Choose how much access to give the assistant:

| What you want to do | MCP address | Permission name |
| --- | --- | --- |
| Manage the server and its applications | `https://your-vps.example.com/mcp/manage` | `manage` |
| Run services in one application only | `https://your-vps.example.com/mcp/apps/123` | `run` |

Replace `your-vps.example.com` with your Redlaunch domain. For application-only
access, replace `123` with that application's ID; you can find it in the
application page's address, such as `/applications/123`.

**Management access can change all applications and shared server resources.
Application access can start service runs that change that application's data.
Neither option is read-only.** Approve only connections you started yourself;
the client name on the approval page is self-reported, not a verified identity.

The instructions below use management access. Follow only the section for
your assistant:

- [OpenCode](#sign-in-from-opencode)
- [Codex](#sign-in-from-codex)
- [Claude Code](#sign-in-from-claude-code)

If you already have a local/stdio server named `redlaunch`, replace that entry
with the remote one below, or choose a different name such as `redlaunch-vps`.
Use the same name in the configuration and all commands.

### Sign in from OpenCode

These instructions are for **OpenCode V2**.

1. Open your project folder and open or create `opencode.json`.
2. Add the entry below, replacing the example domain. If the file already has
   settings or other MCP servers, keep them and add `redlaunch` under
   `mcp.servers` rather than replacing the whole file.

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

3. Start OpenCode in that project folder. Type `/mcps`, select **redlaunch**,
   and choose the sign-in option.
4. In the browser, sign in to **your Redlaunch server** if asked. Check the
   requested access and approve the connection.
5. Return to OpenCode. Use `/mcps` to check that **redlaunch** is connected.
   You can also run `opencode mcp list` from the project folder.

For application-only access, change the URL to end in `/mcp/apps/123` and change
`"scope": "manage"` to `"scope": "run"` before signing in.

To use the same connection in every project, put the entry in
`~/.config/opencode/opencode.json` instead. OpenCode keeps OAuth credentials
outside these configuration files; do not add a token or `client_secret`.

### Sign in from Codex

1. Open or create `~/.codex/config.toml`.
2. Add this entry, replacing the example domain. Keep any existing settings
   and other server entries:

```toml
[mcp_servers.redlaunch]
url = "https://your-vps.example.com/mcp/manage"
```

3. In a terminal, start the Redlaunch sign-in:

```sh
codex mcp login redlaunch --scopes manage
```

4. Follow the browser sign-in, sign in to **your Redlaunch server** if asked,
   and approve the requested access.
5. Start a new Codex session and type `/mcp` to check the connection.
   `codex mcp list` in a terminal also lists the configured server.

For application-only access, use a URL ending in `/mcp/apps/123` and sign in with:

```sh
codex mcp login redlaunch --scopes run
```

**Keep `--scopes manage` or `--scopes run` in the login command.** With the tested
Codex 0.156.1, leaving it out requests both permissions, which Redlaunch rejects
for a single connection.

If a browser cannot open automatically, add `--no-browser` to the login command.
Open the displayed sign-in link yourself, complete approval, and paste the
callback URL back into the terminal when prompted. Treat that callback URL as
private: do not put it in chat, logs, or Git.

You can use `.codex/config.toml` in a trusted project instead of the global
configuration. Either way, Codex stores OAuth credentials separately; do not
add `bearer_token_env_var` or an Authorization header for this connection.

### Sign in from Claude Code

These steps follow Claude Code's official instructions. **Redlaunch's
connection has not yet been live-tested with Claude Code.** If authentication
fails, do not work around it by copying an API token into the configuration.

1. Open a terminal in your project folder.
2. Add Redlaunch, replacing the example domain:

```sh
claude mcp add --transport http --scope project redlaunch https://your-vps.example.com/mcp/manage
```

3. Start Claude Code in that folder. If it asks whether to allow the project's
   MCP server, review and allow **redlaunch**. This lets Claude Code load the
   configuration; it is separate from approving access in Redlaunch.
4. Type `/mcp`, select **redlaunch**, and choose **Authenticate**. Follow the
   browser sign-in, sign in to Redlaunch if asked, and approve the access.
5. Return to Claude Code and use `/mcp` to check that the connection is ready.

The command saves the server address in your project's `.mcp.json`. You can
commit that URL-only configuration; each team member still signs in separately.
Use `--scope user` instead of `--scope project` if you want the connection
available in all your projects without sharing its configuration.

For application-only access, use `/mcp/apps/123` in the address. Claude Code
handles the sign-in permissions itself; its scope selection and token renewal
with Redlaunch are still unverified.

### Check that it works

Once your assistant reports the connection is ready, try a non-mutating request:

> Use Redlaunch MCP to read the service-run guide. Do not change anything on
> my server.

With management access, you can also ask it to list your applications. A server
appearing in a configuration list only means it was added; check its connection
status and that its tools are available before relying on it.

### Switch to another VPS or sign in again

To switch servers, change the domain in the configured URL and repeat your
assistant's sign-in steps. **Do not copy credentials from the old server.** The
new server needs its own approval. To keep both servers available, add a second
entry with a different name, such as `redlaunch-staging`.

The same rule applies when changing from management access to an application
URL, or when changing the port of an SSH tunnel: each address and access level
needs its own approval.

Normally, your assistant renews the connection in the background. If it asks
you to authenticate again, repeat the browser sign-in. Approval lasts at most
90 days and expires sooner if the assistant goes 30 days without renewing it.
Revoking a connection also requires a new sign-in before it can be used again.

### If sign-in does not work

| What you see | What to check |
| --- | --- |
| Needs authentication | Start sign-in from `/mcps` in OpenCode, `codex mcp login redlaunch --scopes manage` in Codex, or `/mcp` in Claude Code. Use `run` for a Codex application connection. |
| The server cannot be reached | Open its Redlaunch HTTPS address in your browser. Check the domain and connection; for SSH access, keep the tunnel running. |
| Codex rejects the requested permissions | Match the URL and login permission: `/mcp/manage` with `--scopes manage`, or `/mcp/apps/123` with `--scopes run`. |
| Access denied after changing the URL | Sign in again for the new address. A connection approved for another server or application cannot be reused. |
| Sign-in was revoked or has expired | Start a fresh sign-in from your assistant. In OpenCode, `opencode mcp logout redlaunch` clears stored credentials if needed; then sign in through `/mcps`. |
| Claude Code says pending approval | Start Claude Code in the project and allow the project MCP configuration before authenticating through `/mcp`. |

For client-specific options, see the official guides for
[OpenCode V2](https://opencode.ai/v2/docs/mcp-servers),
[Codex](https://developers.openai.com/codex/mcp/), and
[Claude Code](https://code.claude.com/docs/en/mcp).

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

### Remove an assistant's access

In Redlaunch, open **Settings → MCP connections**. This lists the clients you
approved, what they can access, when they last used the connection, and when
the approval expires. Credentials are never displayed here.

To remove access:

1. Find the connection you want to remove and choose **Revoke**.
2. Check the client name and server/application address in the dialog.
3. Confirm revocation.

The connection's credentials stop working immediately. The assistant must sign
in again to reconnect. **Revocation does not stop work already started on the
server.** Expired connections stay listed until you remove them.

Use **Settings → MCP connections** for browser-approved assistants.
**Settings → API tokens** is the separate list for manually created CI and
local/stdio credentials; those tokens cannot authenticate remote OAuth MCP.

### What has been tested

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

### Technical details for integrations

You do not need these details to follow the sign-in instructions above.

- OAuth supports dynamic public-client registration, S256 PKCE, signed-in
  consent, and single-use authorization codes valid for one minute.
- Access tokens last one hour. Refresh grants expire after 30 days without a
  refresh or 90 days since approval. Tokens rotate on refresh; only token hashes
  are persisted. A 60-second in-memory replay window handles lost refresh
  responses and does not survive a server restart.
- Tokens are bound to the exact approved origin and endpoint. Public access
  requires a configured Redlaunch domain over HTTPS; loopback HTTP is allowed.
- Resource-specific discovery is published at
  `/.well-known/oauth-protected-resource/mcp/manage` and
  `/.well-known/oauth-protected-resource/mcp/apps/123`.
- MCP uses stateless Streamable HTTP: single JSON-RPC POSTs with
  `Content-Type: application/json` and
  `Accept: application/json, text/event-stream`. Responses are JSON or empty
  202 responses for notifications. There are no session IDs or SSE streams;
  GET and DELETE return 405. Tool calls use the existing machine APIs in-process.
- The connection's last-use timestamp records successful API authentication
  and survives token refreshes. Its displayed expiry is the refresh-grant
  expiry, not the hourly access-token expiry.
