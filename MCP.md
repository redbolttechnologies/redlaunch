# Using Redlaunch MCP

Redlaunch MCP lets your coding assistant read Redlaunch's guides and help you
write GitHub Actions workflows. You can ask for help in everyday language,
such as “Create a workflow to run my database migrations.” MCP is the connection
that gives the assistant access to these Redlaunch tools.

Start with the basic setup below. It can read guides and generate workflows
without connecting to your server. Later, you can optionally let the assistant
check an existing task's progress or start a one-off service, such as a migration.

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
2. Add the configuration for your version, replacing the executable path.
   Run `opencode --version` if you are unsure which version you have.

For OpenCode v1:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "redlaunch": {
      "type": "local",
      "command": ["/absolute/path/to/redlaunch/bin/redlaunch", "mcp"],
      "enabled": true
    }
  }
}
```

For OpenCode v2:

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
`~/.config/opencode/opencode.json` instead. See the official
[OpenCode v1](https://opencode.ai/docs/mcp-servers/) or
[OpenCode v2](https://opencode.ai/v2/docs/mcp-servers/) MCP guide.

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
an application list, edit Compose files, publish images, create tokens, or
deploy applications through the API.

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

**OpenCode:** add an `environment` field inside the existing `redlaunch` entry
(under `mcp` in v1 or `mcp.servers` in v2):

```json
"environment": {
  "REDLAUNCH_URL": "{env:REDLAUNCH_URL}",
  "REDLAUNCH_RUN_TOKEN": "{env:REDLAUNCH_RUN_TOKEN}"
}
```

This is a field to insert into the existing JSON object, not a complete file.
Add a comma between it and the preceding field.

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

## Troubleshooting

| Problem | What to do |
| --- | --- |
| Redlaunch is missing from the assistant's tools | Check the configuration file location, executable path, and JSON or TOML syntax. Start a new session in the configured project. |
| “Command not found” or “Permission denied” | Use the full path to the built executable on the assistant's computer. Rebuild with `make build` if needed. |
| Running `redlaunch mcp` manually appears to do nothing | This is normal: it waits for the assistant. Stop it with Ctrl+C and let your assistant launch it. |
| Guides work, but progress checks are unavailable | Pass both `REDLAUNCH_URL` and `REDLAUNCH_RUN_TOKEN` to the MCP process, then restart the assistant from that environment. |
| Starting a service is unavailable | Complete Step 4 and add `--allow-run` as shown in Step 5. |
| Connection fails | Check the HTTPS address and certificate, or make sure your SSH tunnel is still running. Open the management address in your browser to check reachability. |
| HTTP 401 | The token is invalid, expired, or revoked. Create a replacement and update your environment. |
| HTTP 403 | The token belongs to another application. Use a token for the application you requested. |
| HTTP 404 | Check the application ID, service name, and job ID. Jobs disappear after a Redlaunch restart and expire one hour after finishing. Investigate the previous run before starting another. |

For token rotation and API errors, see [API tokens](API_TOKENS.md). For complete
deployment workflows, see [Deployment recipes](RECIPES.md).
