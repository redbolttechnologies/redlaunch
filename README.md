# Redlaunch

Redlaunch is a small, self-hosted control panel for running Docker Compose
applications on a Linux server. It gives you a browser-based way to create
projects, manage services, edit configuration, inspect logs, and route domains
without turning the server into a large platform.

Redlaunch is built with Go, SQLite, server-rendered HTML, HTMX, and a small
amount of Alpine.js. It runs as a single application and manages Docker through
the host's Docker socket.

## Installation

Download and run the installer as the user that should own the Redlaunch
files:

```sh
wget https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh
bash install.sh
```

The installer asks for the installation directory (default `/opt/redlaunch`,
so the checkout is accessible to multiple users), clones Redlaunch there,
then asks for an email address and password and builds and starts
the application. Google OAuth settings are optional. It will not overwrite an existing
installation or `.env` file. System locations such as `/opt/redlaunch` use
`sudo` only to create the directory; the checkout itself is owned by the
invoking user.

See the [installation guide](INSTALL.md) for prerequisites, optional Google OAuth setup,
networking, alternative installation options, and the complete first-run
walkthrough.

After installation, open <http://localhost:8080>. To access Redlaunch on a
remote server, create an SSH tunnel from your computer:

```sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
```

Keep the tunnel open while using <http://localhost:8080> in your browser.
Alternatively, place the management interface behind an HTTPS reverse proxy.

When Google sign-in is enabled and one or more Redlaunch public-access domains are configured in Settings, a
login started at a configured public hostname uses
`https://<hostname>/auth/google/callback` automatically. Register each public
callback URI in the same Google OAuth client as the local callback URI.

For an external HTTPS reverse proxy that is not configured through Redlaunch's
Public access setting, set `GOOGLE_REDIRECT_URL` to that proxy's callback URL.

To update an existing installation, run this from the Redlaunch directory:

```sh
make update
```

This pulls the latest Git changes and rebuilds and restarts the Docker Compose
deployment. Installations from before the Settings SSH keys tab also need the
one-time `redlaunch` user setup in the [server SSH keys guide](SSH_KEYS.md)
before keys can be created.

The same operation is available in the web interface: open **Settings**, find
**Update Redlaunch**, and click **Update**. It pulls the latest version from
GitHub and then rebuilds with `docker compose up -d --build` in the Redlaunch
directory. The rebuild runs in a detached helper container named
`redbolt-redlaunch-updater` and restarts Redlaunch, so refresh the page
shortly after the update starts. While a rebuild is running, further update
requests are refused until it finishes. If stale updater containers from an
older release remain, remove them once with
`docker rm -f $(docker ps -aq --filter label=redlaunch.updater=true)`.

To open a shell inside the running Redlaunch container (`redbolt-redlaunch`):

```sh
./redlaunch-terminal.sh
```

For host shell access from external automation, see the
[server SSH keys guide](SSH_KEYS.md).

For machine-triggered one-off runs (for example database migrations from
GitHub Actions), see the [API tokens guide](API_TOKENS.md).

For end-to-end tutorials (pushing images, Drizzle migrations, and deploying
from GitHub Actions via the registry or via direct image copy), see the
[recipes guide](RECIPES.md).

Connect your coding assistant to Redlaunch with `redlaunch mcp`. The
[Redlaunch MCP guide](MCP.md) walks through prerequisites, setup for Codex,
OpenCode, and Claude Code, your first workflow request, and an optional
connection to your VPS for checking progress and running one-off services.
Use a separate server-wide management token to manage applications, services,
environment values, routing, and managed databases.

Open **Help** in the sidebar (or visit `/help`) for Getting started, First
steps, how-to guides, and troubleshooting. The online help includes these
Markdown guides with a section index and copyable commands, scripts, and code
examples. It is bundled with the application, works without internet access,
and is available after signing in, including before first-run setup is complete.
External documentation links still require internet access. Replace example
hostnames, application IDs, paths, and GitHub variables before running examples.

## What it can do

- Create and manage Docker Compose applications.
- Add PostgreSQL, Redis, and custom application containers.
- Configure application container entrypoints, healthchecks, restart policies,
  dependencies, port mappings, and volumes.
- Import an existing Compose project.
- Clone an application into an independent environment variant (dev, stage) from its Settings tab. Secrets copy as-is for review, domains copy without routing, volume data is not copied, and all services stay stopped.
- Start, stop, restart, and remove managed services.
- Run a managed service once as a one-off task (for example a database
  migration) with `docker compose run --rm` semantics; the container is
  removed afterwards and secrets stay on the server.
- Keep regular settings in `vars.env` and secrets in `secrets.env`.
- Generate secrets in the Add/Edit secret dialog with a configurable length
  of 16–1024 characters (default: 16). Generation uses Web Crypto's
  cryptographic random source and a uniform 64-character URL-safe alphabet.
  Leaving an edited secret value blank preserves its existing value.
- Show container status, ports, logs, and explicitly scoped resource usage.
- List the images previously pushed to the local registry from the Registry page.
- Manually purge older tags per repository with a keep count (keep the N most
  recent images, 0 for unlimited, max 100); deployed tags are always kept.
- Route domains and paths through an optional managed Caddy proxy, including
  an HTTPS hostname for Redlaunch itself.
- Configure global proxy defaults and application overrides for compression,
  security headers, request body limits, upstream timeouts, and Cache-Control.
- Enable one server-level managed Postgres cluster from the Databases page,
  then create/drop databases, manage users and per-database access, and
  schedule, restore, download, and remove PostgreSQL backups. The enabled
  overview shows database/user totals, the Postgres version, container status,
  and a database list searchable by name and filterable by owner. Each database's
  View connection string action opens a dialog with its accessible managed users
  and a copyable, read-only PostgreSQL URI. The user selector is hidden when only
  one user qualifies. Replace `PASSWORD` with the user's URL-encoded password;
  stored passwords cannot be retrieved. The address `redbolt-databases:5432`
  is reachable from application containers on `redlaunch-common`, not directly
  from outside Docker. Use Create
  database to open the creation dialog. Leave Username empty to use the database
  name. Missing users are created with a strong random password and read/write
  access to their new database only; existing users keep their credentials and permissions. Save
  new credentials from the confirmation dialog: reveal or copy the password,
  or download `<username>.creds.env` containing `POSTGRES_USER` and
  `POSTGRES_PASSWORD`. Credentials are shown once and never stored in SQLite.
  New databases disable PUBLIC connection access; legacy PUBLIC access is
  preserved as explicit grants for existing roles when creating a new user.
  Use Manage users for access management and Settings for cluster
  start/stop/restart controls. The Users tab has
  searchable roles and dialogs for passwords and database grants. The Backups
  tab keeps the selected database while editing schedules or managing backup
  history, with confirmation before restoring or deleting a backup. Settings
  separates cluster details and runtime controls from the disable action.
- Schedule, restore, download, and remove PostgreSQL backups.
- Update a PostgreSQL service's database user and password from its service
  details page. The live database role is updated first, then the managed
  environment files, and the service restarts to apply them.
- Create and revoke server SSH keys for external automation from Settings.
- Create and revoke per-application API tokens from Settings for
  machine-triggered one-off service runs.
- Sign in with email/password credentials, with optional Google sign-in for the same users. Manage accounts in Settings → Users.

## First steps

On the first visit, choose whether Redlaunch should install a Caddy reverse
proxy, a local Docker Registry, or both. You can then create an application and
add services from its **Services** tab.

Managed projects use this directory layout:

```text
projects/
├── applications/
│   └── my-application/
│       ├── compose.yml
│       ├── vars.env
│       └── secrets.env
└── core/
    ├── proxy/
    └── registry/
```

Each application has its own Compose file and environment files. Secret values
are stored outside SQLite and are masked in the web interface.

Dashboard log views use a bounded byte tail. Full log downloads are streamed
with a 64 MiB safety limit and at most two active downloads per Redlaunch
process.

PostgreSQL and Redis services also load service-specific
environment files (for example, `db.vars.env` and `db.secrets.env`) after the
project-wide files. This keeps multiple database services from overwriting one
another's credentials. Legacy projects with ambiguous shared database
credentials are refused for operator review.

Backup and restore actions run as tracked operations rather than being tied to
the browser request. One SQLite lease coordinates web, scheduled, and CLI
operations for each database service; leases expire after a crashed process.
Every lease-holding operation is capped at 25 minutes, below the 30-minute
lease, and scheduled units stop overruns at the same boundary. Service
deletion records its own durable tombstone, disables the service timer, holds
the backup lease, and removes service routing with a Caddy reload; backup
files are retained as operator-managed artifacts.
Completed backup status updates do not overwrite later schedule edits, and
completed dumps are published with collision-safe filenames. If a process is
interrupted, a later backup conservatively removes only old Redlaunch temporary
dump files. Application deletion records a durable tombstone and can resume
from its last completed stage; the application page shows that retained
checkpoint after a manager restart. Keep the SQLite database and project
directory available until the deletion progress reaches completion. Folder
names stay reserved until the tombstone completes, and a retry never removes
a replacement folder. Application deletion
retains backup files under the configured `BACKUP_ROOT` as separate operator
artifacts; backup history records are removed with the application metadata.

Redlaunch stores its SQLite database in the external Docker volume
`redlaunch_app-data`. The setup script creates this volume, and it is kept
outside the Compose project lifecycle so rebuilding or recreating the
Redlaunch container does not remove the database.

## Local development

Local development uses the pinned Go 1.26.8 toolchain and requires a running
user systemd manager. With Go toolchain auto-downloads enabled, the Make targets
select that patch release through <code>GOTOOLCHAIN</code>.

```sh
cp .env.example .env
openssl rand -hex 32
# Add the generated value to .env as AUTH_SESSION_SECRET.
# Create the first local user without placing the password in shell history.
read -r -s -p 'Password: ' password; printf '\n'
printf '%s\n' "$password" | go run ./cmd/redlaunch auth-create-user --email admin@example.com --password-stdin
unset password
make run
```

Then open <http://localhost:8080>.

The main configuration values are:

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `127.0.0.1:8080` | Address used by the standalone HTTP server; the bundled Compose deployment aligns its listener with `APP_PORT` |
| `DB_PATH` | `./data/redlaunch.db` | SQLite database path |
| `PROJECTS_ROOT` | `./projects` | Root directory for managed projects |
| `BACKUP_ROOT` | `/var/backups/redlaunch` | PostgreSQL backup directory |
| `REDLAUNCH_DIR` | `.` (Compose defaults to `${PWD}`) | Redlaunch checkout directory used by the Settings update action |
| `REDLAUNCH_IMAGE` | `redlaunch:local` | Image of the Redlaunch container and its detached Settings-update helper |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | empty | Optional Google sign-in; configure both together |
| `AUTH_SESSION_SECRET` | empty | Required shared session signing key, at least 32 bytes |
| `GOOGLE_REDIRECT_URL` | `http://localhost:8080/auth/google/callback` | Fallback Google OAuth callback URL for local access |
| `AUTH_COOKIE_SECURE` | `false` | Use secure authentication cookies when TLS terminates in front of Redlaunch |
| `MANAGEMENT_ACCESS_MODE` | `ssh-only` | `ssh-only` keeps the host listener private; `managed-https` requires a configured TLS proxy |
| `METRICS_SCOPE` | `manager` | Dashboard visibility scope: `manager` reads the manager process environment; `vps` is for explicitly mounted host paths |
| `METRICS_PROC_ROOT` | empty (`/proc`) | Procfs path used by the dashboard metrics collector; set this to a read-only host procfs mount for VPS scope in Docker |
| `METRICS_FILESYSTEM_ROOT` | empty (`/`) | Filesystem path used for dashboard disk metrics; set this to a read-only host-root mount for VPS scope in Docker |
| `HOST_HOSTNAME` | empty (host `HOSTNAME` via Compose) | Host name shown in the header; inside Docker `os.Hostname` would otherwise return the container ID |
| `HOST_PUBLIC_IP` | empty (auto-detected) | Public IP shown in the header; overrides the automatic container-egress detection |
| `APP_BIND_ADDRESS` | `127.0.0.1` | Host bind address for the Docker deployment; use `0.0.0.0` only with managed HTTPS and firewalling |
| `APP_PORT` | `8080` | Host port used by Docker Compose |

The default deployment is SSH-only: Docker binds port 8080 to loopback, so use
an SSH tunnel. If `APP_PORT` is changed, the bundled container listener and
managed Caddy endpoint follow that port. To publish the management UI through
the bundled Caddy proxy,
set `MANAGEMENT_ACCESS_MODE=managed-https` and `APP_BIND_ADDRESS=0.0.0.0`,
configure the public hostname in Redlaunch, and restrict the host firewall to
the intended HTTP/HTTPS entry points. Managed HTTPS forces secure session and
CSRF cookies; SSH-only keeps the local HTTP callback usable through the SSH
tunnel. Do not rely on arbitrary forwarded headers to select a mode.

The Dashboard reports the resource scope shown above and does not claim that a
container-scoped sample represents the whole VPS. The bundled Compose file
defaults to `METRICS_SCOPE=manager`; selecting `vps` in Docker is meaningful
only after deliberately adding read-only host mounts and setting
`METRICS_PROC_ROOT` and `METRICS_FILESYSTEM_ROOT` to their in-container paths.
Running the binary directly on a VPS can use `METRICS_SCOPE=vps` with the
default `/proc` and `/` paths.

See [.env.example](.env.example) for the authentication settings. Environment
variables override values loaded from `.env`.

Run the project checks with:

```sh
make test
make lint
make compose-config
make docker-build
```

To test the secret dialog's browser logic with Bun (optional):

```sh
bun test ./internal/handler/testdata/application-environment.test.cjs
```

For a release candidate, run the complete release gate:

```sh
make release-check
```

It runs the race-enabled test suite (including migration coverage), vet, a
gofmt check, a tracked-secret scan, then builds and scans the Linux artifact
separately from the local host binary, validates Compose, builds the
production image, and records binary/image identity under `bin/`. The
vulnerability database and container base images require network access;
generated release identity files remain excluded from Git.

## Security

Redlaunch has access to the Docker socket, and its Compose deployment can also
control host systemd units for scheduled backups. These capabilities are
effectively host-level access. Run Redlaunch only on a trusted management host,
restrict access to its web interface, and authorize only trusted accounts.

Never commit `.env`, `vars.env`, `secrets.env`, databases, or backup files.

Managed projects live under `projects/applications/<name>/` and
`projects/core/<component>/`, each with its own `compose.yml`, `vars.env`,
and `secrets.env`. New projects always write `compose.yml`; `compose.yaml`
is only accepted when reading older installations. A managed bind mount must
name a file or subdirectory below its project directory: mounting the project
directory itself is rejected because it would expose sibling managed files
such as `secrets.env` to the workload. Symlinked ancestors or files inside
the managed trees are rejected for the same reason. The bundled manager
service follows the same rule: it carries the `redlaunch.managed=true` label
and loads both `vars.env` and `secrets.env`.

## Proxy settings

Open **Proxy → Settings** for global defaults, or **Proxy settings** on an
application page for overrides. These policies apply to application routes;
Redlaunch's management interface keeps its existing policy. An unchecked
application category inherits the global category. A checked category replaces
all fields in that category, so an empty category can disable an inherited
policy. Existing installations keep Caddy defaults until settings are configured.

Compression supports gzip, Zstandard, and Brotli, negotiated using the client's
`Accept-Encoding` header, with Zstandard preferred, then Brotli, then gzip.
The minimum response size is configurable in bytes (0 uses Caddy’s 512-byte
default). Enabling Brotli builds a Caddy 2.11.4 image with a pinned Brotli extension under
`core/proxy/`. This requires access to the image and Go module registries and
can take several minutes. The extended image remains available after Brotli is
disabled. Custom proxy images/builds are not automatically replaced.

Security headers include X-Content-Type-Options, X-Frame-Options,
Referrer-Policy, Content-Security-Policy, Permissions-Policy, and
Strict-Transport-Security. Nonempty values replace upstream response headers;
blank values preserve them. Header values must be printable ASCII, at most
4096 bytes, without Caddy placeholders (`{...}`). Configure CSP and HSTS for
your application's requirements before enabling them.

Request body limits are bytes (0 means unlimited, up to 1 TiB). Timeouts cover
upstream connection, response headers, reads, and writes. Use Go durations such
as `30s` or `2m`, up to `24h`; blank uses Caddy defaults. For connections,
`0s` uses Caddy’s 3s default; for response headers, reads, and writes, `0s`
disables the corresponding timeout.

Cache-Control configures a response header, for example `private, no-store`.
A blank value preserves the upstream header. The policy applies to every
response for the application; use `public` only when all content is safe for
shared caching. Redlaunch does not store cached responses in the proxy.

Saving validates inputs and applies the full proxy configuration in a background
job. A failed apply restores the previous stored policy and proxy configuration.
Settings are persisted in SQLite and removed when their application is deleted.

## License

Redlaunch is available under the [MIT License](LICENSE).
