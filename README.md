# Redlaunch

Redlaunch is a small self-hosted web application for managing Docker Compose
applications and core services. It uses Go's standard library and
server-rendered HTML/CSS assets.

## Run locally

Requires Go 1.25 or newer.

```sh
make test
make lint
make run
```

Open <http://localhost:8080>.
The server requires the Google OAuth settings described below before it will
start.
The development target uses the current user's systemd manager and stores
local backup files under `./data/backups`, so it does not need to write
`/etc/systemd/system`. The user systemd manager must be running; on a
headless host, use the system-wide configuration described below instead.

## Google authentication

Redlaunch enables Google OAuth when both `GOOGLE_CLIENT_ID` and
`GOOGLE_CLIENT_SECRET` are configured. Configure a Google OAuth web client
with the exact redirect URI from `GOOGLE_REDIRECT_URL`, then copy
`.env.example` to `.env` and fill in the values:

```sh
cp .env.example .env
# Edit .env and set the Google client ID, client secret, and redirect URL.
```

Generate the session secret on macOS or Linux with OpenSSL, then paste the
64-character result into `AUTH_SESSION_SECRET` in `.env`:

```sh
openssl rand -hex 32
```

When present, Redlaunch reads `.env` from its current working directory.
Process environment variables take precedence over values in the file. Set
`DOTENV_FILE` to use another dotenv file, or set it to an empty value to
disable dotenv loading. The real `.env` file is ignored by Git.

Add at least one account to the SQLite allowlist before signing in:

```sh
make add-authorized-email EMAIL=you@example.com
```

Once enabled, all application routes require a Google account whose verified
email is in this allowlist. The allowlist is checked on every request, so
removing an address revokes its access after the current request completes.
After signing in, the sidebar shows the Google profile name and picture when
available (otherwise initials), along with a logout control.

## Install on a Linux VPS

Requires Docker Engine with the Docker Compose plugin.

For a one-step installation, run the installer from the account that will own
the Redlaunch files:

```sh
wget -qO- https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh | bash
```

The installer clones Redlaunch into `~/redlaunch` and runs `make setup`. To use
a different directory, set `REDLAUNCH_INSTALL_DIR` on the shell running the
installer:

```sh
wget -qO- https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh \
  | REDLAUNCH_INSTALL_DIR=/srv/redlaunch bash
```

The installer refuses to overwrite an existing installation directory. The
equivalent manual steps are:

```sh
git clone <repository-url> redlaunch
cd redlaunch
make setup
```

`make setup` interactively asks for the Google client ID, Google client secret,
an optional auth session secret, and the first authorized email address. It
generates a secure session secret when one is not provided, writes a private
`.env` file, adds the email to the allowlist, and runs
`docker compose up -d --build`. The default OAuth callback URL is
`http://localhost:8080/auth/google/callback`; change `GOOGLE_REDIRECT_URL` in
`.env` if the VPS is accessed through another URL. The command refuses to
overwrite an existing `.env` file.

Docker Compose loads `.env` from the project directory for the variables used
by `docker-compose.yml`, including the Google OAuth settings.

The application is available on port `8080`, or the value of `APP_PORT`.
The provided Compose setup also mounts the Docker socket so the first-run
setup can start selected core services through Docker Compose and stores the
SQLite database in the named `app-data` volume.

On the first visit, Redlaunch creates the configured projects root with
`applications` and `core` directories, then shows the initial setup screen.
Select Caddy, the Docker Registry, or both. Selected services are written
under `core/proxy` and `core/registry`, each with private `vars.env` and
`secrets.env` files, and started with Docker Compose.

After setup, create applications from the Applications page. Each application
is recorded in SQLite and gets its own directory under `applications` with a
`compose.yml` file connected to the external `redlaunch-common` network and an
empty, private `vars.env` and `secrets.env` files. Managed services load both
files; non-secret variables belong in `vars.env` and secrets belong in
`secrets.env`.

Open an application card to view its details and its database-backed Compose
services table. The Variables and Secrets tabs show the application's
`vars.env` and `secrets.env` files; variable values are shown without masking
and secret values are masked. Variables and secrets can be added, edited, or
deleted from their respective tabs; saving a change updates the corresponding
environment file. Secret values are password-masked in the editor, which can
also reveal an existing value or generate a secure random secret.
Use **Import variables...** on the Variables tab or **Import secrets...** on the
Secrets tab to replace the matching managed file with an uploaded `.env` file.
Uploaded secret values remain masked in the UI.
The Domains tab lists domain names associated with the application and allows
them to be added or deleted. Open Manage routing for a domain to create,
edit, or delete path mappings to the application's services; each change is
stored in SQLite and applied to the managed Caddy proxy.
The Settings tab contains a Danger zone for deleting an application. Deletion
requires typing the application name, then runs in the background while
Redlaunch removes its Docker Compose resources, SQLite metadata, and
application folder.
Select a service name to open its details. Database services have Dashboard,
Backups, and Logs tabs: Dashboard shows general runtime information, Backups
contains scheduled SQL backups and backup history, and Logs shows the most
recent 1,000 lines with a full-history download. Other services continue to
show their general runtime information and logs together. Resolved environment
variables are not displayed on the service details page. The Services tab shows
Docker runtime information when it is available, including the container name,
creation date, status, and ports. Use a service row's context menu to start,
stop, restart, or delete that Compose service. Deletion requires typing the
service name in a
confirmation dialog, then runs in the background while Redlaunch stops and
removes the container and deletes its service metadata. From
an application, choose PostgreSQL database or Redis cache to create the
container with a private `vars.env` file and `secrets.env` file for secrets,
persist the service metadata, and start the requested service immediately.
Redis supports an optional password, a published port, and optional durable
storage using append-only logging and one-second snapshots. Creation runs in
the background and the application page shows a progress dialog until the
configuration is saved, or the container starts or reports an actionable error
when automatic startup is enabled.
For an existing Compose project, use **Import Docker Compose project...** from
an application's Services tab before adding any services. Select the project's
Compose YAML file; Redlaunch validates it, registers each defined service, and
adds the managed container names, environment files, and labels. Imported
services remain stopped until you start them from the application page.
Use **Import variables...** on the Variables tab or **Import secrets...** on the
Secrets tab to replace the matching managed `vars.env` or `secrets.env` file
with an uploaded `.env` file.
PostgreSQL service details also provide scheduled SQL backups. Turn on
**Schedule backups** to choose an hourly, daily, or weekly schedule and set
retention, then use **Run backup now**, **Download**, **Restore**, or **Delete**
for an existing dump. Manual backups
require the PostgreSQL service to be running. Downloading streams the recorded
SQL file; deleting removes the backup file and its record. Each schedule gets
its own systemd service
and timer named from the application and service IDs, and backups are stored
under `BACKUP_ROOT` (by default `/var/backups/redlaunch/<application-folder>/<service-name>`).
In the provided Compose deployment, `BACKUP_HOST_ROOT` (default `./backups`)
is mounted at that path, and the host systemd unit directory, systemd runtime,
and system bus socket are mounted into the Redlaunch container. Compose uses
`dbus-send` for systemd control because the Alpine image does not ship
`systemctl`. AppArmor is
explicitly disabled for this container because the host system bus rejects
confined D-Bus clients. The generated units invoke the named Redlaunch
container through Docker, so recreate the
container after changing the Compose file:

```sh
docker compose up -d --build --force-recreate
```

These mounts and the AppArmor setting give Redlaunch host-level systemd
control in addition to the Docker socket access already required by the
application. Use this integration only on a trusted management host; on
systems without systemd, run Redlaunch directly on the host or leave
scheduled backups disabled.
From the same service menu, choose Application to add a custom application
container. Enter the image name and tag to use. The form defaults to
`app:latest`; with **Use Docker Registry** unchecked, Redlaunch automatically
adds the local registry prefix `localhost:5000/`. Check it to use the image
reference as entered. **Automatically start container** is off by default, so
the Compose definition can be created before the image is available. Turn it
on to start the container immediately. The service name changes the default
image path; for example, a service named `web` defaults to
`localhost:5000/web:latest` when the local registry is selected.

The Dashboard item is the first item in the main navigation. It shows current
host CPU, memory, and root filesystem usage, along with the ten most CPU- and
memory-intensive processes. The metrics refresh automatically every 10 seconds.

Use the Proxy item in the main navigation to inspect the managed Caddy proxy.
Its Dashboard tab shows the proxy container's creation time, status, image,
published ports, and the domains and request mappings currently routed through
it. The page header provides Start, Stop, and Restart controls for the proxy
container. The Logs tab shows the most recent 1,000 proxy log lines and
provides a download of the complete log history.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP listen address |
| `DB_PATH` | `./data/redlaunch.db` | SQLite database containing application metadata |
| `PROJECTS_ROOT` | `./projects` | Directory containing managed applications and core services |
| `BACKUP_ROOT` | `/var/backups/redlaunch` | Root directory for database backup files |
| `BACKUP_HOST_ROOT` | `./backups` | Host backup directory mounted by `docker-compose.yml` |
| `BACKUP_CONTAINER_NAME` | empty | Container name used by systemd backup units when set |
| `BACKUP_DOCKER_BINARY` | `/usr/bin/docker` | Docker binary used by container-backed systemd units |
| `SYSTEMD_UNIT_DIR` | `/etc/systemd/system` | Directory for managed backup service and timer units |
| `SYSTEMD_BINARY` | `systemctl` | Systemd control binary |
| `SYSTEMD_SCOPE` | `system` | Systemd manager scope: `system` or `user` |
| `DOTENV_FILE` | `.env` | Dotenv file loaded by the application; an empty value disables loading |
| `GOOGLE_CLIENT_ID` | empty | Google OAuth web-client ID; enables authentication with the client secret |
| `GOOGLE_CLIENT_SECRET` | empty | Google OAuth web-client secret |
| `GOOGLE_REDIRECT_URL` | `http://localhost:8080/auth/google/callback` | OAuth callback URL registered with Google |
| `AUTH_SESSION_SECRET` | empty | At least 32 bytes; required when Google authentication is enabled |
| `AUTH_COOKIE_SECURE` | `false` | Marks auth cookies `Secure`, useful behind a TLS-terminating proxy |
| `APP_PORT` | `8080` | Host port in `docker-compose.yml` only |

## Development checks

```sh
make test
make lint
make compose-config
make docker-build
```
