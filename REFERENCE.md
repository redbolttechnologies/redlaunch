# One-off run reference

This reference describes **Run with parameters…**, the parameterized form of
the existing one-off service run. It applies to the web UI, the application
HTTP API, MCP, and the management API.

## What a run does

Redlaunch executes the registered service through Docker Compose:

```text
docker compose --project-name <project> <env-file args> -f <compose> --ansi never \
  run --rm --quiet-pull -T \
  --env KEY=VALUE ... \
  <service> <arg1> <arg2> ...
```

- The service's configured image, command, and entrypoint remain authoritative.
- Positional arguments are appended after the service name, in order, as
  separate argv entries. They are never parsed by a shell.
- Environment overrides travel as repeated `--env KEY=VALUE` flags. They are
  not written to `vars.env` or `secrets.env`.
- The container is always removed after the run (`--rm`).
- The service definition and managed files are never modified.

## Parameters

```json
{
  "args": ["--database-name", "example", "--confirm"],
  "env": {"NODE_ENV": "development"}
}
```

### Argument limits

- At most 64 arguments.
- At most 4096 bytes per argument.
- At most 64 KiB in total.
- NUL, carriage return, newline, and other control characters are rejected.
- Order is preserved exactly.

### Environment allowlist

Only these keys may be overridden for a single run:

| Key | Purpose |
| --- | --- |
| `NODE_ENV` | Node.js runtime mode |
| `TZ` | Container time zone |
| `LANG`, `LC_ALL` | Locale |
| `LOG_LEVEL` | Application log verbosity |

The allowlist is explicit rather than a denylist. Execution-affecting
variables such as `PATH`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `NODE_OPTIONS`,
`BASH_ENV`, `ENV`, `PYTHONPATH`, `RUBYOPT`, and `PERL5OPT` are never accepted.

Other environment limits:

- At most 16 entries.
- At most 4096 bytes per value.
- At most 32 KiB in total, including keys.
- NUL, carriage return, newline, and other control characters are rejected.

Run parameters are not a secret channel. Arguments and environment values may
be visible in host process listings, so never include passwords, API keys,
tokens, or other credentials. Put secrets in `secrets.env` and let the service
read them from its normal environment.

## Result

```go
type OneOffRunResult struct {
    ExitCode  int
    Stdout    string
    Stderr    string
    Truncated bool
    Duration  time.Duration
}
```

- `ExitCode` is `-1` when no process exit code is available, including
  cancellation, timeout, or a CLI start failure.
- `Stdout` and `Stderr` are captured separately.
- At most 64 KiB per stream and 128 KiB total are retained.
- `Truncated` is true when raw or retained output was dropped.
- The result is returned even when the command exits non-zero or is canceled.

Output processing:

1. Up to 1 MiB per stream is captured while the process runs.
2. ANSI CSI/OSC sequences and other terminal control sequences are removed.
3. Invalid UTF-8 is replaced with the Unicode replacement character.
4. Known values from `secrets.env` and `<service>.secrets.env` are redacted.
5. Sensitive `KEY=value` patterns are redacted.
6. The result is truncated to the retained limits.

Output is held in process memory only, for the existing one-hour job lifetime.
It is not written to SQLite and disappears when the job expires or Redlaunch
restarts. Captured output is never written to logs; the server logs only the
application ID, service name, job ID, exit code, byte counts, and truncation
state.

## Web UI

Use **Run with parameters…** on the service page or in the service actions
menu. The dialog provides:

- Ordered argument rows with add, remove, move up, and move down controls.
- Environment rows whose key selector exposes only the allowlist.
- A live effective-command preview.
- An explicit confirmation checkbox.
- A warning that arguments may be visible in process listings.

After completion the toast reports the exit code. **View output** opens a safe
text dialog with separate stdout and stderr sections, a truncation notice, and
copy and download actions.

## HTTP API

`POST /api/v1/applications/{id}/services/{service}/run` accepts an optional
body:

```json
{
  "parameters": {
    "args": ["--database-name", "example", "--confirm"],
    "env": {"NODE_ENV": "development"}
  }
}
```

An empty body or omitted `parameters` is the legacy run-once request. Bodies
are limited to 64 KiB and unknown fields are rejected.

Status responses include:

```json
{
  "status": "complete",
  "exit_code": 0,
  "output": {
    "stdout": "migration complete\n",
    "stderr": "",
    "truncated": false
  }
}
```

Failed runs also include a sanitized `detail`. Output in API responses is
already redacted and bounded. See [API tokens](API_TOKENS.md#run-once-with-parameters)
for a curl example and status codes.

## MCP

`run_service` accepts the optional `parameters` object. `get_run_status`
returns the exit code and captured output. See the [MCP guide](MCP.md#step-5-allow-service-runs-when-needed)
for an example. `generate_run_workflow` remains backward compatible; the
generated workflow dispatches a run without parameters, and a `--data` body
can be added if parameters are required.

## Management API

`service_action` with `action: "run"` accepts the same optional `parameters`
object. Parameters are rejected for `start`, `stop`, and `restart`. The
completed management job result is the captured `OneOffRunResult`.

`get_proxy_settings`, `update_proxy_settings`,
`get_application_proxy_settings`, and `update_application_proxy_settings` read
and replace proxy policies. Updates replace the complete policy; omitted
categories inherit globally for applications and are disabled at global scope.
See the [MCP management catalog](MCP.md#server-wide-management) for the
`settings` shape.

## Security boundaries

- Parameters are untrusted input.
- Only the environment allowlist is accepted.
- Entrypoint and command overrides are not supported.
- Start, stop, restart, and run share the existing per-service serialization
  lock so operations cannot interleave.
- Captured output is rendered as text, never as HTML.
- Secrets are redacted before output is stored or displayed.
