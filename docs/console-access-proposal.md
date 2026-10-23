# Proposal: Dashboard SSH console

**Status:** Proposal only. Nothing in this document is implemented yet.

## Summary

Add an **Open console** button to the Dashboard page header, next to **Refresh**. It opens a closable modal containing an xterm.js terminal. The browser connects to the Redlaunch server over a same-origin WebSocket; the server opens an SSH session to **the host itself** as the existing dedicated `redlaunch` user and bridges it to the terminal.

Recommended shape:

```text
Browser (xterm.js)
   ⇅  WSS /dashboard/console
Redlaunch HTTP handler (auth, CSRF/origin, framing)
   ⇅
Console service (session limits, key provisioning)
   ⇅  golang.org/x/crypto/ssh
Host sshd (localhost / host-gateway)
   ⇅
Host shell as `redlaunch`
```

The browser never receives an SSH private key. SSH is handled server-side using the already-present `golang.org/x/crypto` dependency. No new container, no database schema change, and no new runtime binary is required.

## Goals

* Add an “Open console” button with a terminal icon to the Dashboard header.
* Open a closeable modal overlay with an embedded xterm.js terminal.
* Connect to the server over SSH from the Redlaunch process.
* Reuse the existing dedicated `redlaunch` host user, host-key publications, and authorized-keys infrastructure.
* Keep the feature server-rendered, same-origin, authenticated, and consistent with the existing design system.
* Fail closed on host-key verification and setup problems.
* Keep terminal content out of logs and browser-exposed error messages.

## Non-goals

* A root shell.
* SFTP/file upload or download.
* Multiple terminal tabs or split panes.
* A shell inside the manager container. The existing `redlaunch-terminal.sh` / `docker exec` route remains the way to get a container shell.
* Exposing the console through the machine API or MCP.
* A role/permission system. Redlaunch currently has no roles; every signed-in user has full management access.

## User experience

Header:

```text
Dashboard                                      [>_ Open console] [Refresh]
Monitor resource usage, storage and services running on your Redlaunch server.
```

* `Open console` is a normal `button.button.button-secondary` with the shadcn/lucide `terminal` icon (`>_`).
* It sits in a small `.page-heading-actions` flex group with the existing Refresh button.
* The button is rendered only when the console is enabled and authentication is active.

Dialog:

```text
┌ Server console ─────────────────────────────────────────────── X ┐
│ Interactive SSH shell on this server as redlaunch@my-vps.        │
│                                                                  │
│ ┌──────────────────────────────────────────────────────────────┐ │
│ │ redlaunch@my-vps:~$                                           │ │
│ │                                                              │ │
│ └──────────────────────────────────────────────────────────────┘ │
│ ● Connected                               [Reconnect]   [Close]  │
└──────────────────────────────────────────────────────────────────┘
```

* Uses the existing `<dialog class="application-dialog">`, `.application-dialog-panel`, header, icon-button, and button patterns.
* Terminal area is a dark technical surface using terminal-specific design tokens based on `--code` / `--code-foreground`.
* States: `Connecting…`, `Connected`, `Disconnected`, and a sanitized error message.
* Closing the dialog terminates the SSH session, not just the modal.
* Reconnect starts a fresh SSH session and clears the terminal.
* Closing methods: header X, footer Close, and Escape when focus is outside the terminal. While the terminal is focused, Escape is sent to the shell (important for `vim`, prompts, etc.).
* Focus moves to the terminal on open and returns to the Open console button on close.
* The dialog is not closed by clicking the backdrop, to avoid accidental session termination.
* Uses the existing body `dialog-open` scroll lock.
* Responsive: full-width dialog on narrow screens; fit addon recalculates terminal columns/rows.

## Architecture

### Why server-side SSH

* The manager runs in a container and has no sshd.
* “Connect to the server itself” means the host's sshd via `host.docker.internal` / the Docker host gateway.
* Server-side SSH keeps private key material out of the browser.
* The project already has the dedicated `redlaunch` user, `authorized_keys` management, and published host public keys.

Rejected alternatives:

* **Browser-side SSH / xterm.js with an SSH library in Wasm:** would require handling a private key or password in the browser.
* **`docker exec` into the manager container:** not SSH, and not a host shell.
* **SSH as root:** unnecessarily broad; `redlaunch` is already root-equivalent when in the `docker` group.
* **SSE + POST instead of WebSocket:** avoids a WebSocket dependency but adds latency, many requests, and awkward binary/resize handling.
* **Loading xterm.js from a CDN:** conflicts with the offline/CSP/self-hosted model.

### Backend: console service

New `internal/service/console.go`:

* `ConsoleService` constructed only when enabled.
* `Enabled() bool` for conditional rendering.
* `Open(ctx, userKey, cols, rows) (ConsoleSession, error)`.
* `ConsoleSession` implements `io.ReadWriteCloser` plus `Resize(cols, rows) error`.
* Session limits: 2 per user, 4 globally.
* Idle timeout: 15 minutes; hard maximum: 4 hours.
* SSH dial timeout: 10 seconds.
* Graceful `CloseAll` on Redlaunch shutdown.
* Per-session tracking so limits and shutdown are deterministic.

SSH details:

* `ssh.Dial`/`ssh.NewClientConn` to the configured address.
* Public-key authentication only.
* `session.RequestPty("xterm-256color", rows, cols, ...)`.
* `session.Shell()`.
* `session.WindowChange(rows, cols)` for resize.
* Read from `session.Stdout()` into WebSocket binary frames.
* Write browser binary frames into `session.Stdin`.
* Close the SSH session/client when the WebSocket closes or the context is canceled.

### Key management

The manager needs its own SSH key. Reusing an operator-created Settings key is not possible because private keys are returned once and never stored.

Plan:

* Generate an Ed25519 key pair on first console use using Go's `crypto/ed25519` and `ssh.MarshalPrivateKey`.
* Store it at `CONSOLE_SSH_KEY_PATH` (default in Compose: `/data/console/id_ed25519`).
* Create the directory with `0700`; key file with `0600`; reject symlinks and overly broad permissions.
* Install the public key into `/home/redlaunch/.ssh/authorized_keys` with the comment `redlaunch-console`.
* Add a small `EnsureAuthorizedKey(ctx, comment, publicKey)` method to `ServerSSHKeyService` so it shares the existing mutex and in-place file writer. Existing unmanaged lines remain preserved.
* The console key is separate from operator-managed `redlaunch-ssh-key:<id>` entries and is not listed/revocable in Settings. Document it clearly.
* Do not expose the private key, its contents, or raw parse errors through HTTP.

Host-key verification:

* Read the host public keys already published by `scripts/setup.sh` in `/home/redlaunch/.ssh/host_keys`.
* Pin the set of published public keys and accept only a key in that set.
* Prefer Ed25519 but allow any published key type.
* If no host keys are available, fail closed with an actionable message. No `ssh.InsecureIgnoreHostKey` fallback.

### Handler: WebSocket endpoint

New route:

```text
GET /dashboard/console
```

Flow:

1. Requires completed setup.
2. Requires authentication. For WebSocket upgrade requests, return `401` instead of redirecting to `/login`, so the browser can show “Sign in again.”
3. Requires a same-origin `Origin`. Reuse the existing `h.sameRequestOrigin` logic, which already handles the bundled Caddy HTTPS scheme upgrade.
4. Upgrade to WebSocket using `github.com/coder/websocket`.
5. Wait up to 5 seconds for a start message carrying the CSRF token and initial terminal size.
6. Validate the CSRF token against the existing CSRF cookie.
7. Open the SSH session.
8. Send `{"type":"ready"}`.
9. Bridge binary frames ↔ SSH stdin/stdout.
10. Close the session and log the event when either side disconnects.

Protocol:

| Direction | Type | Meaning |
| --- | --- | --- |
| Browser → server | text JSON | `start` (CSRF + cols/rows), `resize` (cols/rows) |
| Browser → server | binary | Raw terminal input |
| Server → browser | text JSON | `ready`, `error`, `exit` |
| Server → browser | binary | Raw terminal output |

Limits:

* WebSocket read limit: 1 MB.
* Terminal size: 2–500 columns, 2–300 rows.
* Per-write timeout: 10 seconds.
* Disable WebSocket compression.
* One serialized writer for all server → browser frames.
* Close with policy-violation for malformed control messages.

Security middleware:

* Extend `withOriginCheck` or add a WS-specific check so WebSocket upgrades are not treated like ordinary GET requests. The existing middleware currently skips all GETs, which would otherwise leave the shell endpoint without origin validation.
* Keep the CSRF first-message check because the CSRF middleware also skips GETs.
* Rely on the existing session auth middleware for handshake authentication.

Audit logging:

* Log session open/close with the Redlaunch user identity, remote address, session ID, duration, and safe close reason.
* Never log terminal input, output, secrets, or private-key material.
* Map internal SSH errors to safe user-facing messages:
  * “The server console is not enabled.”
  * “The server console is temporarily busy. Try again shortly.”
  * “The server SSH host key could not be verified. Refresh the host keys with `make setup`.”
  * “SSH authentication failed. Check that the `redlaunch` user exists.”
  * “The server is unreachable over SSH.”
  * “This page expired. Refresh and try again.”

## Security model

| Concern | Mitigation |
| --- | --- |
| Unauthenticated access | Existing session auth; console route requires auth; no unauthenticated production mode |
| CSRF / cross-site WebSocket hijacking | Same-origin `Origin` check plus CSRF token in the first WebSocket message |
| Key disclosure | Private key stays on the server, 0600, never rendered or logged |
| MITM | Host-key pinning from published host public keys; fail closed |
| Privilege escalation | Dedicated `redlaunch` user, not root; no sudo |
| Resource abuse | Global/per-user session limits, idle and max duration, read limits, size bounds |
| Secret leakage in logs | Never log terminal content; sanitize errors |
| Terminal output XSS | Raw bytes go to xterm.js, never into the DOM; no server-side HTML rendering of output |
| Third-party script supply chain | xterm.js vendored locally and pinned; no CDN |
| Accidental disconnect | No backdrop close; explicit X/Close; Escape behavior respects terminal focus |

Residual risk to call out in documentation:

* `make setup` adds `redlaunch` to the `docker` group, which is root-equivalent. A console session is therefore a full host-administrator capability.
* Every currently signed-in Redlaunch user would get this capability. If console access should be narrower than manager access, a role/permission model must be added first.
* The console private key lives in the app data volume (`/data`). Protect volume snapshots/backups accordingly.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `CONSOLE_ENABLED` | `false` (recommended) | Enables the Dashboard console button and route |
| `CONSOLE_SSH_ADDR` | empty; Compose sets `host.docker.internal:22` | SSH target `host:port` |
| `CONSOLE_SSH_USER` | `redlaunch` | Host SSH login user |
| `CONSOLE_SSH_KEY_PATH` | empty; Compose sets `/data/console/id_ed25519` | Persistent console private-key path |
| `CONSOLE_SSH_HOST_KEY_DIR` | from `ServerSSHKeyService` (`/home/redlaunch/.ssh/host_keys`) | Published host public keys for pinning |

Docker Compose changes:

* Add `extra_hosts: ["host.docker.internal:host-gateway"]` so the Linux container can reach the host SSH daemon reliably.
* Pass the console environment variables.
* The existing `/home/redlaunch/.ssh` mount is already required for the authorized-keys write.
* No Dockerfile change is needed: SSH is implemented in-process, and xterm assets are embedded in the Go binary.

The service validates the address format, absolute key path, and username at startup. A misconfigured enabled console fails fast; a disabled console has no effect. A missing host mount or `redlaunch` user does not prevent the manager from starting; opening the console shows an actionable error.

## UI and design system

Reuse before adding:

* `.page-heading` / `.page-heading-copy`
* `.button`, `.button-secondary`
* `.icon-button`
* `.application-dialog`, `.application-dialog-panel`, `.application-dialog-header`
* `.service-status-badge` with `service-status-running`, `service-status-neutral`, or `service-status-failed` for the connection state
* Existing focus-ring, border, radius, spacing, and muted-foreground tokens

New, small additions:

* `.page-heading-actions` for the header action group.
* `.dashboard-console-dialog`, `.dashboard-console-panel`, `.dashboard-console-terminal`, `.dashboard-console-footer`, and `.dashboard-console-status` for the terminal layout.
* Terminal-specific design tokens (background, foreground, cursor, selection, and standard ANSI colors) aligned with `--code` / `--code-foreground`, documented in `DESIGN.md` as a technical component extension.

No arbitrary colors, font sizes, spacing, or radii outside the existing tokens. Terminal uses the existing monospace stack.

xterm.js integration:

* Vendor `@xterm/xterm@6.0.0` (MIT) and `@xterm/addon-fit@0.11.0` (MIT) under `internal/handler/static/xterm/`.
* Use the ESM builds (`lib/xterm.mjs`, `lib/addon-fit.mjs`) and `css/xterm.css`.
* A single local ES module `console.js` imports them; no npm build step and no client-side framework.
* Load xterm CSS and the module script only on the Dashboard when the console is enabled.
* `defer`/module ordering ensures xterm is available before the console initializer.
* Terminal options: `xterm-256color`, 5000 scrollback, cursor blink, `minimumContrastRatio: 4.5`, screen-reader mode, fit addon, ResizeObserver-driven resize.
* Binary output is passed to xterm as `Uint8Array`, preserving split UTF-8 sequences; input is encoded with `TextEncoder`.

## Dependencies

* **`github.com/coder/websocket`** — small, idiomatic, context-aware WebSocket implementation; ISC license; actively maintained; no transitive dependencies. Go's standard library has no WebSocket server.
* **xterm.js 6.0.0 + addon-fit 0.11.0** — MIT, vendored locally, pinned with license files.
* **`golang.org/x/crypto/ssh`** — already a direct dependency; no new module needed for SSH.

## Files

New:

* `internal/service/console.go`
* `internal/service/console_test.go`
* `internal/handler/console.go`
* `internal/handler/console_test.go`
* `internal/handler/static/console.js`
* `internal/handler/static/xterm/xterm.mjs`
* `internal/handler/static/xterm/addon-fit.mjs`
* `internal/handler/static/xterm/xterm.css`
* `internal/handler/static/xterm/LICENSE`
* `internal/handler/static/xterm/VERSION`

Modified:

* `go.mod`, `go.sum`
* `internal/config/config.go` and tests
* `cmd/redlaunch/main.go`
* `internal/handler/handler.go` (field, route, dependency type switch)
* `internal/handler/dependencies.go` (console service wiring)
* `internal/handler/dashboard.go` (console availability/target data)
* `internal/handler/templates/dashboard.html`
* `internal/handler/templates/layout.html` (conditional xterm assets)
* `internal/handler/static/app.css`
* `internal/service/ssh_keys.go` (`EnsureAuthorizedKey`)
* `docker-compose.yml`
* `.env.example`
* `README.md`
* `INSTALL.md`
* `SSH_KEYS.md`
* `DESIGN.md` (terminal component/token documentation)

No changes to SQLite schema, migrations, repository layers, or Docker image contents.

## Testing and verification

Unit tests:

* Console key generation, directory/file permissions, symlink rejection, invalid existing key, no regeneration on parse failure.
* `EnsureAuthorizedKey` preserves existing unmanaged and operator-managed lines; replacement is idempotent; concurrent create/ensure is safe under `-race`.
* Host-key selection and fail-closed behavior when no keys are available.
* Session limits, idle/max duration, resize bounds, close idempotency.

Integration tests:

* In-process SSH server using `golang.org/x/crypto/ssh` (test host key, public-key auth, PTY request, shell channel, echo output, window-change recording).
* Full console service bridge: open session, write input, read output, resize, close.
* WebSocket endpoint with `httptest.NewServer` and a real WebSocket client:
  * unauthenticated upgrade → 401/redirect policy
  * wrong origin → 403
  * missing/invalid CSRF → close
  * valid start → ready, binary input/output, resize
  * unavailable/busy service → safe error
* Template tests: button/dialog present only when enabled; correct icon, labels, script/style tags, and no key path/secret in the HTML.
* Static asset test: `/static/xterm/xterm.mjs` served as JavaScript; `/static/xterm/xterm.css` available.

Manual QA:

* Light and dark mode, desktop and mobile.
* Open/close, reconnect, Escape behavior in a shell program.
* Copy/paste, resize while a full-screen program is running.
* Host SSH failure modes: missing `redlaunch` user, wrong host key, unreachable host, old installation without `host_keys`.
* Access via SSH tunnel and through managed HTTPS/Caddy (WebSocket upgrade).
* Confirm no terminal content appears in server logs.
* Confirm CSP allows the same-origin WebSocket in Chrome, Firefox, and Safari; adjust only if a browser specifically requires it.
* Confirm long sessions survive the existing HTTP server timeouts after hijack.

Commands:

```sh
make test
make lint
make fmt-check
make secret-scan
make compose-config
make docker-build
```

## Documentation

* `README.md`: add the console environment variables and a short “Dashboard console” description.
* `INSTALL.md`: how to enable it, prerequisites (`make setup`, host sshd reachability, `redlaunch` user), security warning, troubleshooting.
* `SSH_KEYS.md`: document the `redlaunch-console` line, how to check it, how to revoke/rotate it, and that it is separate from operator-managed keys.
* `.env.example`: disabled-by-default console settings.
* `DESIGN.md`: terminal palette/component rules.
* Settings SSH keys tab: add a short note that the Dashboard console maintains a separate `redlaunch-console` key. Public-key visibility only; no secret exposure.

## Risks and limitations

* Host sshd may not be reachable from the Docker bridge due to firewall/bind configuration; `CONSOLE_SSH_ADDR` provides an override.
* Existing installations may not have the `redlaunch` user or `host_keys` directory; the console must show an actionable error rather than failing the manager.
* The `redlaunch` user in the `docker` group is root-equivalent. This must be explicit in the UI/docs.
* The console key persists in the app data volume; volume backups/snapshots now contain a host-access credential.
* WebSocket support must be preserved by any reverse proxy; Caddy handles it, but other proxies need explicit configuration.
* xterm.js adds roughly hundreds of kilobytes of embedded static assets and one Go module.
* Logout does not invalidate an already-open console session; the hard maximum duration bounds this. If logout must terminate shells immediately, server-side session revocation is a separate change.
* The host shell does not know which Redlaunch user opened it; the manager logs the UI identity, but host process auditing sees only `redlaunch`.

## Open questions

1. **Host or container shell?** The recommendation is the host shell as `redlaunch`, matching “the server itself” and the existing SSH-key model. Confirm.
2. **Enabled by default?** The recommendation is opt-in via `CONSOLE_ENABLED=true` for a privileged host shell. If the console should be visible immediately after `docker compose up`, the Compose default can be `true` instead.
3. **Who may use it?** Every signed-in user currently has full management access, so a console would give each of them a host shell. Is that acceptable, or should a role/permission model come first?
4. **SSH user?** `redlaunch` is recommended. `root` is possible but should be an explicit, separately documented choice.
5. **Dependency approval?** Confirm adding `github.com/coder/websocket` and vendoring xterm.js 6.0.0 / addon-fit 0.11.0.
6. **Console key visibility?** The recommendation is to document it and add a read-only note in Settings, not list it as a revocable operator key.
