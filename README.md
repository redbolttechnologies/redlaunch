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

The installer clones Redlaunch to `~/redlaunch`, asks for the Google OAuth
settings and first authorized email address, then builds and starts the
application. It will not overwrite an existing installation or `.env` file.

See the [installation guide](INSTALL.md) for prerequisites, Google OAuth setup,
networking, alternative installation options, and the complete first-run
walkthrough.

After installation, open <http://localhost:8080>. To access Redlaunch on a
remote server, create an SSH tunnel from your computer:

```sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
```

Keep the tunnel open while using <http://localhost:8080> in your browser.
Alternatively, place the management interface behind an HTTPS reverse proxy.

When one or more Redlaunch public-access domains are configured in Settings, a
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
deployment.

The same operation is available in the web interface: open **Settings**, find
**Update Redlaunch**, and click **Update**. It pulls the latest version from
GitHub and then runs `docker compose up -d --build` in the Redlaunch
directory.

For the GitHub Actions wizard, generated workflow, and secure handoff steps,
see the [GitHub Actions image deployment guide](GITHUB_ACTIONS.md).

## What it can do

- Create and manage Docker Compose applications.
- Add PostgreSQL, Redis, and custom application containers.
- Configure application container entrypoints, healthchecks, restart policies,
  dependencies, port mappings, and volumes.
- Import an existing Compose project.
- Start, stop, restart, and remove managed services.
- Keep regular settings in `vars.env` and secrets in `secrets.env`.
- Show container status, ports, logs, and explicitly scoped resource usage.
- Route domains and paths through an optional managed Caddy proxy, including
  an HTTPS hostname for Redlaunch itself.
- Schedule, restore, download, and remove PostgreSQL backups.
- Configure repository-specific GitHub Actions image builds through a restricted
  SSH tunnel to the local registry.
- Limit access to an allowlist of Google accounts.

## First steps

On the first visit, choose whether Redlaunch should install a Caddy reverse
proxy, a local Docker Registry, or both. You can then create an application and
add services from its **Services** tab. To configure a repository-specific
image build, open an application's **Deployment → GitHub Actions deployment**
panel after the registry is running.

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
    ├── registry/
    └── github-actions-tunnel/
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
# Add your Google OAuth client ID, client secret, and local fallback redirect URL to .env.
openssl rand -hex 32
# Add the generated value to .env as AUTH_SESSION_SECRET.
make add-authorized-email EMAIL=you@example.com
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
| `GOOGLE_REDIRECT_URL` | `http://localhost:8080/auth/google/callback` | Fallback Google OAuth callback URL for local access |
| `AUTH_COOKIE_SECURE` | `false` | Use secure authentication cookies when TLS terminates in front of Redlaunch |
| `MANAGEMENT_ACCESS_MODE` | `ssh-only` | `ssh-only` keeps the host listener private; `managed-https` requires a configured TLS proxy |
| `METRICS_SCOPE` | `manager` | Dashboard visibility scope: `manager` reads the manager process environment; `vps` is for explicitly mounted host paths |
| `METRICS_PROC_ROOT` | empty (`/proc`) | Procfs path used by the dashboard metrics collector; set this to a read-only host procfs mount for VPS scope in Docker |
| `METRICS_FILESYSTEM_ROOT` | empty (`/`) | Filesystem path used for dashboard disk metrics; set this to a read-only host-root mount for VPS scope in Docker |
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

## License

Redlaunch is available under the [MIT License](LICENSE).
