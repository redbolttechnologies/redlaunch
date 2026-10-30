# Redlaunch

Redlaunch is a small, self-hosted control panel for running Docker Compose
applications on a Linux VPS. It gives you a browser-based way to create
projects, manage services, edit configuration, inspect logs, and route domains
without turning the server into a large platform.

Redlaunch is built with Go, SQLite, server-rendered HTML, HTMX, and a small
amount of Alpine.js. It runs as a single application and manages Docker through
the host's Docker socket.

> **Security first.** Redlaunch has host-level access through the Docker socket
> and can control host systemd units for scheduled backups. Run it only on a
> trusted management host, restrict access to its web interface, and authorize
> only trusted accounts.

## Documentation

| Guide | What it covers |
| --- | --- |
| [Installation](INSTALL.md) | Prepare a server, run the installer, create the first user, and enable HTTPS. |
| [Host dependencies](DEPENDENCIES.md) | Install Git, wget, make, Docker, and local development tools. |
| [Deployment recipes](RECIPES.md) | Complete GitHub Actions workflows for images, migrations, and deployments. |
| [Server SSH keys](SSH_KEYS.md) | Create, use, rotate, and revoke automation keys. |
| [API tokens](API_TOKENS.md) | Trigger one-off runs and poll their status from CI. |
| [One-off run reference](REFERENCE.md) | Arguments, environment overrides, output capture, and limits. |
| [MCP guide](MCP.md) | Connect Codex, OpenCode, or Claude Code to Redlaunch. |

Open **Help** in the sidebar or visit `/help` to read these guides inside the
application. The bundled help works without internet access after sign-in,
including before first-run setup is complete. External documentation links
still require internet access.

## Installation

### Before you install

You need a supported 64-bit Ubuntu or Debian server, an SSH account with
`sudo` access, and Docker Engine with the Compose plugin. Install the host
tools and Docker first:

- [Host dependencies](DEPENDENCIES.md) for Git, wget, make, OpenSSL, Docker,
  and verification commands.
- [Installation guide](INSTALL.md) for the complete VPS walkthrough, optional
  Google OAuth setup, networking, and first-run instructions.

### Run the installer

Download and run the installer as the user that should own the Redlaunch files:

```sh
wget https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh
bash install.sh
```

The installer asks for the installation directory (default `/opt/redlaunch`,
so the checkout is accessible to multiple users), clones Redlaunch there, then
asks for an email address and password and builds and starts the application.
Google OAuth settings are optional. It will not overwrite an existing
installation or `.env` file. System locations such as `/opt/redlaunch` use
`sudo` only to create the directory; the checkout itself is owned by the
invoking user.

### Open the web interface

After installation, open <http://localhost:8080>. To access Redlaunch on a
remote server, create an SSH tunnel from your computer:

```sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
```

Keep the tunnel open while using <http://localhost:8080> in your browser.
Alternatively, place the management interface behind an HTTPS reverse proxy.

When Google sign-in is enabled and one or more Redlaunch public-access domains
are configured in Settings, a login started at a configured public hostname
uses `https://<hostname>/auth/google/callback` automatically. Register each
public callback URI in the same Google OAuth client as the local callback URI.

For an external HTTPS reverse proxy that is not configured through Redlaunch's
Public access setting, set `GOOGLE_REDIRECT_URL` to that proxy's callback URL.

### Updating Redlaunch

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
`redbolt-redlaunch-updater` and restarts Redlaunch, so refresh the page shortly
after the update starts. While a rebuild is running, further update requests are
refused until it finishes. If stale updater containers from an older release
remain, remove them once with
`docker rm -f $(docker ps -aq --filter label=redlaunch.updater=true)`.

### Shell access

To open a shell inside the running Redlaunch container
(`redbolt-redlaunch`):

```sh
./redlaunch-terminal.sh
```

For host shell access from external automation, see the
[server SSH keys guide](SSH_KEYS.md). For machine-triggered one-off runs (for
example database migrations from GitHub Actions), see the
[API tokens guide](API_TOKENS.md).

A one-off run can pass ordered arguments and environment overrides that apply
only to that container, for example a maintenance script that needs
`NODE_ENV=development` and CLI flags. Overrides are limited to `NODE_ENV`, `TZ`,
`LANG`, `LC_ALL`, and `LOG_LEVEL`; secrets must stay in `secrets.env`. Run
parameters never modify `compose.yml`, `vars.env`, or `secrets.env`, and
arguments may be visible in host process listings, so they must never contain
secrets. See the [one-off run reference](REFERENCE.md) for the full contract,
limits, and output handling, and the
[API tokens guide](API_TOKENS.md#run-once-with-parameters) for the HTTP request
shape and an example.

For end-to-end tutorials (pushing images, Drizzle migrations, and deploying from
GitHub Actions via the registry or via direct image copy), see the
[recipes guide](RECIPES.md).

## First steps

On the first visit, choose whether Redlaunch should install a Caddy reverse
proxy, a local Docker Registry, or both. You can then create an application
and add services from its **Services** tab.

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

To create your first application, open **Applications → Create application**
and enter a name. The generated folder name must be a single directory name,
without `/`.

Open the application and choose **Services → Create service** to add an
application container, PostgreSQL, Redis, or a service from the catalog. Add
ordinary settings on the **Variables** tab and credentials on the **Secrets**
tab. Start the service when its image and configuration are ready.

To publish a service through Caddy, add its hostname on the **Domains** tab,
then use **Manage routing** to select the service and its container port.

Redlaunch stores its SQLite database in the external Docker volume
`redlaunch_app-data`. The setup script creates this volume, and it is kept
outside the Compose project lifecycle so rebuilding or recreating the Redlaunch
container does not remove the database.

## Features

### Applications and services

- **Docker Compose applications.** Create an application and manage its
  services from the browser. Each application has its own Compose file,
  variables, and secrets.
- **Multiple service types.** Add application containers, PostgreSQL, Redis,
  or a service from the catalog.
- **Full service configuration.** Configure entrypoints, healthchecks, restart
  policies, dependencies, port mappings, and volumes.
- **Import and clone.** Import an existing Compose project. Clone an
  application into an independent environment variant (for example dev and
  stage) from its Settings tab. Secrets copy as-is for review, domains copy
  without routing, volume data is not copied, and all services stay stopped.
- **Service lifecycle.** Start, stop, restart, and delete services. Restart
  recreates the selected service with the current saved variables, secrets, and
  Compose configuration, leaving dependencies untouched.
- **One-off tasks.** Run a managed service once with `docker compose run --rm`
  semantics, for example a database migration. The container is removed
  afterwards and secrets stay on the server. A run may include ordered
  arguments and allowlisted environment overrides without changing the service
  definition. Captured stdout, stderr, the real exit code, duration, and
  truncation state are available in the web UI, HTTP API, and MCP. See the
  [one-off run reference](REFERENCE.md).
- **Status and logs.** Show container status, ports, logs, and explicitly
  scoped resource usage. Dashboard log views use a bounded byte tail. Full log
  downloads are streamed with a 64 MiB safety limit and at most two active
  downloads per Redlaunch process.

### Configuration and secrets

- **Separate environment files.** Keep regular settings in `vars.env` and
  credentials in `secrets.env`. Every managed project has both files.
- **Service-specific database variables.** PostgreSQL and Redis services also
  load service-specific environment files (for example, `db.vars.env` and
  `db.secrets.env`) after the project-wide files. This keeps multiple database
  services from overwriting one another's credentials. Legacy projects with
  ambiguous shared database credentials are refused for operator review.
- **Secrets stay out of the database.** Secret values are stored outside SQLite
  and are masked in the web interface.
- **Secret generation.** Generate secrets in the Add/Edit secret dialog with a
  configurable length of 16–1024 characters (default: 16). Generation uses Web
  Crypto's cryptographic random source and a uniform 64-character URL-safe
  alphabet. Leaving an edited secret value blank preserves its existing value.

### Images, versions, and registry

- **Local registry.** Optionally run a local Docker registry and list the images
  previously pushed to it from the Registry page.
- **Automatic retention and garbage collection.** Retain the configured number
  of newest distinct image versions per local-registry repository (default 2),
  then reclaim unreferenced blobs. See
  [registry retention](#registry-retention-and-garbage-collection) for the full behavior.
- **Manual cleanup.** Configure a global retention count plus per-repository
  overrides, then purge older versions from all repositories with one action.
  A manual garbage-collection action reclaims unused blob storage. Deployed and
  running images are always kept.
- **Service versions.** Open a service's Versions list to see the pushed images
  of its repository grouped by digest with their tags, including untagged
  images retained in the registry. Pushed times come from manifest revisions
  and stay unchanged when switching versions. The deployed image is flagged,
  and switching moves the service's deployment tag (for example `development`
  or `production`) to the selected image and recreates the service. An
  application-wide **Deploy version** action moves every service's deployment
  tag to the image carrying one chosen tag.

### Domains and proxy

- **Managed Caddy proxy.** Route domains and paths through an optional managed
  Caddy proxy, including an HTTPS hostname for Redlaunch itself.
- **Proxy policies.** Configure global defaults and application overrides for
  compression, security headers, request body limits, upstream timeouts, and
  Cache-Control. See [proxy settings](#proxy-settings).

### Managed PostgreSQL

- **One server-level cluster.** Enable a managed Postgres cluster from the
  Databases page, then create and drop databases, manage users and
  per-database access, and schedule, restore, download, and remove PostgreSQL
  backups.
- **Database overview.** The enabled overview shows database and user totals,
  the Postgres version, container status, and a database list searchable by
  name and filterable by owner.
- **Connection strings.** Each database's **View connection string** action
  opens a dialog with its accessible managed users and a copyable, read-only
  PostgreSQL URI. The user selector is hidden when only one user qualifies.
  Replace `PASSWORD` with the user's URL-encoded password; stored passwords
  cannot be retrieved. The address `redbolt-databases:5432` is reachable from
  application containers on `redlaunch-common`, not directly from outside
  Docker.
- **Create databases safely.** Leave Username empty to use the database name.
  Missing users are created with a strong random password and read/write access
  to their new database only; existing users keep their credentials and
  permissions. Save new credentials from the confirmation dialog: reveal or
  copy the password, or download `<username>.creds.env` containing
  `POSTGRES_USER` and `POSTGRES_PASSWORD`. Credentials are shown once and never
  stored in SQLite. New databases disable PUBLIC connection access; legacy
  PUBLIC access is preserved as explicit grants for existing roles when
  creating a new user. Use Manage users for access management and Settings for
  cluster start/stop/restart controls. The Users tab has searchable roles and
  dialogs for passwords and database grants. The Backups tab keeps the selected
  database while editing schedules or managing backup history, with
  confirmation before restoring or deleting a backup. Settings separates
  cluster details and runtime controls from the disable action.
- **SQL workspace.** Click a database name on **Databases → Databases** to open
  its SQL workspace. Schemas and tables appear on the left, with an SQL editor
  above query results on the right. Expand a table to see its columns in
  database order. Right-click a table (or use its actions button or Shift+F10)
  and choose **Show data (max. 1000 rows)** to load and automatically execute a
  quoted SELECT script. Edited SQL and write operations require **Run Query**
  or Ctrl/⌘+Enter; writes take effect immediately. The console includes table
  search, syntax highlighting, line numbers, and a whitespace-only formatter
  (Ctrl/⌘+K or Query actions → Format SQL). Results paginate locally in groups
  of 25 rows and can be downloaded as CSV or JSON without rerunning the query.
  CSV downloads protect spreadsheet formula-like values; JSON preserves the
  displayed values. **Refresh schemas and tables** reloads the schema browser
  after schema changes and clears the editor and results.
- **SQL safety limits.** Queries run as the database owner through the
  container's local socket, not as the cluster administrator. Administrative
  owners and owners with host-file or program privileges cannot use the
  workspace. SQL execution uses a 30-second statement timeout, a 5-second lock
  timeout, and a 4 MiB output limit. Tabular results display at most 1000 rows;
  use `LIMIT` in custom queries to bound work. Multi-result scripts with
  differing column counts display escaped text output. psql backslash commands
  are not supported.
- **Dump import.** **Import dump file…** opens a confirmation dialog for
  restoring into the selected managed database. Plain UTF-8 `.sql` and
  PostgreSQL custom-format dumps (`pg_dump -Fc`, commonly `.dump` or `.backup`)
  are supported, up to 32 MiB. Externally compressed (`.gz`/`.zip`), tar, and
  directory-format dumps are not supported; compression inside custom-format
  archives is supported. Take a backup first and import only trusted files.
  Generate portable SQL dumps with `pg_dump --no-owner --no-privileges`; custom
  imports ignore ownership and grants. SQL import requires a current PostgreSQL
  client supporting psql restricted mode (PostgreSQL 17.6 / 16.10 / 15.14 or
  newer); older clients fail closed. COPY data is supported, but
  shell/file/connection-switching psql commands are blocked. Restores execute
  as the non-administrative database owner, stop at the first error, and use a
  single transaction and a 10-minute execution deadline. Explicit transaction
  control in SQL dumps can prevent complete rollback. Existing conflicting
  objects are not automatically dropped, and source roles are not created.
  Successful import refreshes the workspace and displays a confirmation toast;
  dump contents are not displayed or retained by Redlaunch after the request.
- **Database credentials.** Update a PostgreSQL service's database user and
  password from its service details page. The live database role is updated
  first, then the managed environment files, and the service restarts to apply
  them.

### Monitoring

- **Managed dashboard.** After setup, `/` opens the Dashboard. It lists
  individual containers labeled `redlaunch.managed=true`, with core projects
  first, shows their application names, and reports CPU, working-set memory,
  cumulative network traffic, and Docker status.
- **Docker data.** These values come from the local Docker Engine at
  `/var/run/docker.sock`, independently of the host-metrics scope. Attached
  named volumes show their owning application names and actual used bytes; bind
  mounts and per-volume capacity estimates are excluded. Shared volumes list
  every owning application, and core resources show `Core`. Trends retain up to
  one hour of dashboard observations in memory and reset when the manager
  restarts; collection occurs on dashboard refreshes.
- **Image pruning.** **Prune unused images** requires confirmation and removes
  all local Docker images not referenced by any container, including tagged
  images. Images used by running or stopped containers are retained.
  Containers, volumes, networks, and registry tags are not pruned. Deleted
  images must be pulled or rebuilt before future use. The dashboard reports
  reclaimed bytes after cleanup.

### Automation and access

- **Local accounts.** Sign in with email/password credentials, with optional
  Google sign-in for the same users. Manage accounts in Settings → Users.
- **Server SSH keys.** Create and revoke server SSH keys for external
  automation from Settings. See the [server SSH keys guide](SSH_KEYS.md).
- **API tokens.** Create and revoke per-application API tokens from Settings
  for machine-triggered one-off service runs. See the
  [API tokens guide](API_TOKENS.md).
- **MCP.** Connect OpenCode, Codex, or Claude Code to your VPS by adding its MCP
  URL and signing in to Redlaunch in your browser. No API token or token
  environment variables are needed. See the
  [MCP browser sign-in guide](MCP.md#connect-to-your-server-with-browser-sign-in).
  The [full MCP guide](MCP.md) also covers the local `redlaunch mcp` setup for
  offline guides, workflow generation, and token-based stdio connections.

## Configuration

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

See [.env.example](.env.example) for the authentication settings. Environment
variables override values loaded from `.env`.

### Management access modes

The default deployment is SSH-only: Docker binds port 8080 to loopback, so use
an SSH tunnel. If `APP_PORT` is changed, the bundled container listener and
managed Caddy endpoint follow that port.

To publish the management UI through the bundled Caddy proxy, set
`MANAGEMENT_ACCESS_MODE=managed-https` and `APP_BIND_ADDRESS=0.0.0.0`, configure
the public hostname in Redlaunch, and restrict the host firewall to the intended
HTTP/HTTPS entry points. Managed HTTPS forces secure session and CSRF cookies;
SSH-only keeps the local HTTP callback usable through the SSH tunnel. Do not
rely on arbitrary forwarded headers to select a mode.

### Dashboard metrics scope

The Dashboard reports the resource scope shown above and does not claim that a
container-scoped sample represents the whole VPS. The bundled Compose file
defaults to `METRICS_SCOPE=manager`; selecting `vps` in Docker is meaningful
only after deliberately adding read-only host mounts and setting
`METRICS_PROC_ROOT` and `METRICS_FILESYSTEM_ROOT` to their in-container paths.
Running the binary directly on a VPS can use `METRICS_SCOPE=vps` with the
default `/proc` and `/` paths.

## Operations

### Service deletion

Choose **Delete** from the application's service-list context menu, or **Delete
service** on the service details page. Type the service name exactly to confirm.
The container is stopped and removed, its Compose definition and metadata are
deleted, and its routing and scheduled backups are cleaned up.

Volume data is **retained by default**. Check individual named volumes in the
confirmation dialog to permanently delete their data. Only exclusively used,
project-owned, non-external volumes are eligible. Shared volumes, external
volumes, anonymous volumes, bind-mounted directories, and backup files are
retained. Docker refuses removal of a volume still in use by another container.
If volume choices cannot be loaded, a new service deletion retains all volume
data; a retry still uses the original saved selections.

Deletion progress shows failures. Retry deletion from the application service
list to resume an interrupted operation; the original volume selections are
saved and cannot be changed on retry. Existing API deletion calls retain volume
data for new operations.

### Registry retention and garbage collection

One retention policy controls both automatic cleanup and the manual **Purge
images** action:

- The global default keep count is 2. Configure it on the Registry page, with
  optional per-repository overrides. `0` means unlimited; the maximum is 100.
- Keep the newest distinct image versions per repository, including untagged
  versions. Multiple tags on one digest count as one version.
- Always keep versions used by running containers, configured or pinned service
  images, and versions with unknown push times. These do not consume the keep
  quota.
- Preserve multi-platform child manifests of every retained image.
- Remove tags and manifest revision links for obsolete versions, deleting
  parent indexes before their children.
- Cleanup skips deletion when inspection fails, the inventory exceeds 1,000
  entries, or manifest dependencies are incomplete. Failures are recorded in
  the server log.

Automatic retention and garbage collection run at startup and are each due at
most once every 24 hours. When both are due they share one read-only
maintenance window. The manual **Run garbage collection** action runs
collection immediately. The maintenance window puts the registry in read-only
mode: pulls remain available except during brief restarts, while pushes and
uploads fail until it finishes. Garbage collection removes only blobs no
manifest references, so retained untagged versions remain available for
rollback and version switching.

Recovery state for an interrupted maintenance window is stored under
`core/registry/.maintenance` so Redlaunch can restore the original
configuration and writable registry mode after a crash. If recovery fails,
maintenance actions stay blocked and the Registry page reports the problem.
Registry configurations that mount the config file from the host, rely on
`REGISTRY_*` environment overrides, or do not use the managed filesystem
storage layout are rejected rather than modified.

### Backups and recovery

Backup and restore actions run as tracked operations rather than being tied to
the browser request. One SQLite lease coordinates web, scheduled, and CLI
operations for each database service; leases expire after a crashed process.
Every lease-holding operation is capped at 25 minutes, below the 30-minute
lease, and scheduled units stop overruns at the same boundary.

Completed backup status updates do not overwrite later schedule edits, and
completed dumps are published with collision-safe filenames. If a process is
interrupted, a later backup conservatively removes only old Redlaunch temporary
dump files.

A crashed backup or restore may leave a service lease that expires after 30
minutes. Do not manually remove lease rows while an operation is running.

### Application deletion

Application deletion records a durable tombstone and can resume from its last
completed stage; the application page shows that retained checkpoint after a
manager restart. Keep the SQLite database and project directory available until
the deletion progress reaches completion. Folder names stay reserved until the
tombstone completes, and a retry never removes a replacement folder.

Service deletion records its own durable tombstone, disables the service timer,
holds the backup lease, and removes service routing with a Caddy reload; backup
files are retained as operator-managed artifacts.

Application deletion retains backup files under the configured `BACKUP_ROOT`
as separate operator artifacts; backup history records are removed with the
application metadata.

## Security

Redlaunch has access to the Docker socket, and its Compose deployment can also
control host systemd units for scheduled backups. These capabilities are
effectively host-level access. Run Redlaunch only on a trusted management host,
restrict access to its web interface, and authorize only trusted accounts.

Never commit `.env`, `vars.env`, `secrets.env`, databases, or backup files.

Managed projects live under `projects/applications/<name>/` and
`projects/core/<component>/`, each with its own `compose.yml`, `vars.env`, and
`secrets.env`. New projects always write `compose.yml`; `compose.yaml` is only
accepted when reading older installations.

A managed bind mount must name a file or subdirectory below its project
directory: mounting the project directory itself is rejected because it would
expose sibling managed files such as `secrets.env` to the workload. Symlinked
ancestors or files inside the managed trees are rejected for the same reason.
The bundled manager service follows the same rule: it carries the
`redlaunch.managed=true` label and loads both `vars.env` and `secrets.env`.

## Proxy settings

Open **Proxy → Settings** for global defaults, or the **Proxy** tab on an
application details page for overrides. These policies apply to application
routes; Redlaunch's management interface keeps its existing policy. An unchecked
application category inherits the global category. A checked category replaces
all fields in that category, so an empty category can disable an inherited
policy. Existing installations keep Caddy defaults until settings are
configured. A server-wide management token can read and replace both scopes
through the MCP `get_proxy_settings`, `update_proxy_settings`,
`get_application_proxy_settings`, and `update_application_proxy_settings`
tools; see [MCP server-wide management](MCP.md#server-wide-management).

### Compression

Compression supports gzip, Zstandard, and Brotli, negotiated using the client's
`Accept-Encoding` header, with Zstandard preferred, then Brotli, then gzip. The
minimum response size is configurable in bytes (0 uses Caddy's 512-byte
default). Enabling Brotli builds a Caddy 2.11.4 image with a pinned Brotli
extension under `core/proxy/`. This requires access to the image and Go module
registries and can take several minutes. The extended image remains available
after Brotli is disabled. Custom proxy images/builds are not automatically
replaced.

### Security headers

Security headers include X-Content-Type-Options, X-Frame-Options,
Referrer-Policy, Content-Security-Policy, Permissions-Policy, and
Strict-Transport-Security. Nonempty values replace upstream response headers;
blank values preserve them. Header values must be printable ASCII, at most 4096
bytes, without Caddy placeholders (`{...}`). Configure CSP and HSTS for your
application's requirements before enabling them.

### Limits and caching

Request body limits are bytes (0 means unlimited, up to 1 TiB). Timeouts cover
upstream connection, response headers, reads, and writes. Use Go durations such
as `30s` or `2m`, up to `24h`; blank uses Caddy defaults. For connections, `0s`
uses Caddy's 3s default; for response headers, reads, and writes, `0s` disables
the corresponding timeout.

Cache-Control configures a response header, for example `private, no-store`. A
blank value preserves the upstream header. The policy applies to every response
for the application; use `public` only when all content is safe for shared
caching. Redlaunch does not store cached responses in the proxy.

Saving validates inputs and applies the full proxy configuration in a
background job. A failed apply restores the previous stored policy and proxy
configuration. Settings are persisted in SQLite and removed when their
application is deleted.

## Local development

Local development uses the pinned Go 1.26.8 toolchain and requires a running
user systemd manager. With Go toolchain auto-downloads enabled, the Make targets
select that patch release through `GOTOOLCHAIN`.

Install the required tools with [Host dependencies](DEPENDENCIES.md), then:

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
separately from the local host binary, validates Compose, builds the production
image, and records binary/image identity under `bin/`. The vulnerability
database and container base images require network access; generated release
identity files remain excluded from Git.

## License

Redlaunch is available under the [MIT License](LICENSE).
