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

When Redlaunch public access is enabled, a login started at its configured
public hostname uses `https://<hostname>/auth/google/callback` automatically.
Register that public callback URI in the same Google OAuth client as the local
callback URI.

For an external HTTPS reverse proxy that is not configured through Redlaunch's
Public access setting, set `GOOGLE_REDIRECT_URL` to that proxy's callback URL.

## What it can do

- Create and manage Docker Compose applications.
- Add PostgreSQL, Redis, and custom application containers.
- Import an existing Compose project.
- Start, stop, restart, and remove managed services.
- Keep regular settings in `vars.env` and secrets in `secrets.env`.
- Show container status, ports, logs, and host resource usage.
- Route domains and paths through an optional managed Caddy proxy, including
  an HTTPS hostname for Redlaunch itself.
- Schedule, restore, download, and remove PostgreSQL backups.
- Limit access to an allowlist of Google accounts.

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

Redlaunch stores its SQLite database in the external Docker volume
`redlaunch_app-data`. The setup script creates this volume, and it is kept
outside the Compose project lifecycle so rebuilding or recreating the
Redlaunch container does not remove the database.

## Local development

Local development requires Go 1.25 or newer and a running user systemd manager.

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
| `HTTP_ADDR` | `:8080` | Address used by the HTTP server |
| `DB_PATH` | `./data/redlaunch.db` | SQLite database path |
| `PROJECTS_ROOT` | `./projects` | Root directory for managed projects |
| `BACKUP_ROOT` | `/var/backups/redlaunch` | PostgreSQL backup directory |
| `GOOGLE_REDIRECT_URL` | `http://localhost:8080/auth/google/callback` | Fallback Google OAuth callback URL for local access |
| `AUTH_COOKIE_SECURE` | `false` | Use secure authentication cookies with HTTPS |
| `APP_PORT` | `8080` | Host port used by Docker Compose |

See [.env.example](.env.example) for the authentication settings. Environment
variables override values loaded from `.env`.

Run the project checks with:

```sh
make test
make lint
make compose-config
make docker-build
```

## Security

Redlaunch has access to the Docker socket, and its Compose deployment can also
control host systemd units for scheduled backups. These capabilities are
effectively host-level access. Run Redlaunch only on a trusted management host,
restrict access to its web interface, and authorize only trusted accounts.

Never commit `.env`, `vars.env`, `secrets.env`, databases, or backup files.

## License

Redlaunch is available under the [MIT License](LICENSE).
