# Installing Redlaunch

This guide installs Redlaunch on an Ubuntu or Debian VPS and walks through the
first setup steps. Redlaunch runs as a Docker Compose application and stores
managed applications under the configured projects root.

## Prerequisites

Use a supported 64-bit Ubuntu or Debian VPS with:

- an SSH account with <code>sudo</code> access;
- <code>git</code>, <code>make</code>, <code>bash</code>, <code>openssl</code>, and
  <code>wget</code>;
- Docker Engine and the Docker Compose plugin;
- a hostname and DNS records pointing to the VPS if you will publish services
  through Caddy; and
- <code>systemd</code> if you plan to use scheduled PostgreSQL backups.

The installation below uses Docker's official APT repository. It installs the
Compose v2 plugin, so the command is <code>docker compose</code>, not the legacy
<code>docker-compose</code> command.

Docker-published ports can bypass some host firewall rules. Review Docker's
[firewall guidance](https://docs.docker.com/engine/install/ubuntu/#firewall-limitations)
before exposing services.

The included Compose deployment uses the following ports:

| Port | Use |
| --- | --- |
| <code>22/tcp</code> | SSH administration |
| <code>8080/tcp</code> | Redlaunch HTTP interface; restrict it to administrators or use an SSH tunnel |
| <code>80/tcp</code> | Caddy HTTP and certificate challenges, when Caddy is installed |
| <code>443/tcp</code> and <code>443/udp</code> | Caddy HTTPS and HTTP/3, when Caddy is installed |
| <code>5000/tcp</code> | Local Docker Registry, bound to loopback only |
| <code>2222/tcp</code> | Restricted GitHub Actions SSH forwarding gateway, when configured |

## 1. Install Docker and host tools

Run these commands as your normal SSH user, using <code>sudo</code> only for
package and system changes.

### Ubuntu

These commands follow the [official Docker Engine instructions for
Ubuntu](https://docs.docker.com/engine/install/ubuntu/):

~~~sh
sudo apt update
sudo apt install -y ca-certificates curl wget git make bash openssl

sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
~~~

### Debian

These commands follow the [official Docker Engine instructions for
Debian](https://docs.docker.com/engine/install/debian/):

~~~sh
sudo apt update
sudo apt install -y ca-certificates curl wget git make bash openssl

sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/debian/gpg \
  -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: $(. /etc/os-release && echo "$VERSION_CODENAME")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
~~~

If the VPS already has distribution packages such as <code>docker.io</code>,
<code>docker-compose</code>, <code>containerd</code>, or <code>runc</code>, check
Docker's list of conflicting packages before installing the official packages.

Allow the SSH user to run Docker without <code>sudo</code>, then start a new
login session:

~~~sh
sudo usermod -aG docker "$USER"
~~~

The <code>docker</code> group grants root-equivalent access to the host. Use it
only for trusted administrators. After logging out and back in, verify both
Docker and Compose:

~~~sh
docker run hello-world
docker compose version
~~~

## 2. Create Google OAuth credentials

Redlaunch uses Google OAuth for login. You need a **Web application** OAuth
client, which provides the <code>GOOGLE_CLIENT_ID</code> and
<code>GOOGLE_CLIENT_SECRET</code> used by Redlaunch.

1. Open the [Google Cloud Console Credentials
   page](https://console.cloud.google.com/apis/credentials) and select an
   existing project or create a new one.
2. Complete the Google Auth Platform registration or consent-screen steps if
   Google prompts you to do so. If the application is in **Testing** status,
   add every account that will sign in as a test user.
3. Open **Google Auth Platform → Clients**, choose **Create Client**, and select
   **Web application**.
4. Add the exact Redlaunch callback URL under **Authorized redirect URIs**:
   - for the default local/SSH-tunnel setup:
     <code>http://localhost:8080/auth/google/callback</code>
   - for an HTTPS management URL:
     <code>https://redlaunch.example.com/auth/google/callback</code>

   Add both URIs when you will use both local and public access. Replace the
   example hostname with the public hostname configured in Redlaunch. The
   scheme, hostname, port, path, and trailing slash must match exactly.
   Redlaunch does not use a trailing slash on this callback path.
5. Create the client and copy the **Client ID** and **Client Secret**. Store the
   secret securely; do not commit it, paste it into an issue, or put it in a
   public file.

Google's [OAuth 2.0 web-server documentation](https://developers.google.com/identity/protocols/oauth2/web-server)
contains the current credential and redirect-URI rules.

### Choosing the callback URL

The bundled Compose file defaults to <code>MANAGEMENT_ACCESS_MODE=ssh-only</code>
and binds port <code>8080</code> to <code>127.0.0.1</code>. The simplest secure
first login on a VPS is an SSH tunnel:

~~~sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
~~~

With that tunnel open, visit <code>http://localhost:8080</code> in your local
browser and use the default callback URL above.

For a managed HTTPS deployment through the bundled Caddy proxy, set these
values in <code>.env</code> before restarting the stack:

~~~text
MANAGEMENT_ACCESS_MODE='managed-https'
APP_BIND_ADDRESS='0.0.0.0'
~~~

The wider bind is required because Caddy reaches the manager through the host
gateway. Restrict the VPS firewall to the intended entry points and do not use
managed HTTPS without a reachable TLS proxy. Managed HTTPS forces secure
session and CSRF cookies; SSH-only keeps the loopback HTTP callback usable.
When running the binary outside Docker, the default HTTP listener is also
loopback; set <code>HTTP_ADDR</code> explicitly only when a local reverse proxy
needs to reach that listener.

When Redlaunch public access is enabled in Settings, a login started at the
configured public hostname automatically uses that hostname for the HTTPS
callback URL. Keep <code>GOOGLE_REDIRECT_URL</code> as the local fallback and
register both callback URLs in Google. Set
<code>AUTH_COOKIE_SECURE=true</code> is optional in managed HTTPS because the
mode forces secure cookies. The Caddy
service installed from Redlaunch's first-run screen is for routing managed
application services until public access is enabled; it is not an automatic
reverse proxy for the Redlaunch UI itself.

For an external HTTPS reverse proxy that is not configured through Redlaunch's
Public access setting, set <code>GOOGLE_REDIRECT_URL</code> to that proxy's
callback URL instead.

## 3. Install Redlaunch and run <code>make setup</code>

The one-step installer clones Redlaunch into <code>~/redlaunch</code> and runs
<code>make setup</code>:

~~~sh
wget -qO- https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh | bash
~~~

To install somewhere else, set <code>REDLAUNCH_INSTALL_DIR</code> on the Bash
side of the pipeline:

~~~sh
wget -qO- https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh \
  | REDLAUNCH_INSTALL_DIR=/srv/redlaunch bash
~~~

The installer refuses to overwrite an existing installation directory. If you
prefer to clone manually, use:

~~~sh
git clone <repository-url> redlaunch
cd redlaunch
make setup
~~~

The setup script asks for:

1. the Google Client ID;
2. the Google Client Secret (input is hidden);
3. an optional authentication session secret; press Enter to generate one; and
4. the first authorized Google email address.

<code>make setup</code> then:

- creates a private <code>.env</code> file;
- generates a secure session secret when one was not supplied;
- creates the external <code>redlaunch_app-data</code> Docker volume for the
  SQLite database;
- adds the first email address to Redlaunch's SQLite login allowlist; and
- builds and starts Redlaunch with <code>docker compose up -d --build</code>.

The command intentionally refuses to overwrite an existing <code>.env</code>
file. Keep <code>.env</code> private; it contains the OAuth client secret and
session secret. The <code>redlaunch_app-data</code> volume is external to the
Compose project and must not be removed when cleaning up Docker resources; it
contains the Redlaunch SQLite database.

The Docker deployment mounts the managed <code>projects</code> directory at the
same absolute path inside the Redlaunch container and on the VPS. Redlaunch
starts managed Compose projects through the host Docker socket, so this shared
path is required for bind-mounted files such as the Caddyfile. The default is
the <code>projects</code> directory beside the Compose file. If you set
<code>PROJECTS_ROOT</code> in <code>.env</code>, use an absolute path on the VPS
and run the Compose commands from the installation directory.

The Dashboard is manager-scoped by default in the bundled container: its
resource values describe the manager's visible process and filesystem
environment. To display VPS-wide values, deliberately add read-only host
mounts for procfs and the filesystem, then set
<code>METRICS_SCOPE=vps</code>, <code>METRICS_PROC_ROOT</code>, and
<code>METRICS_FILESYSTEM_ROOT</code> to the corresponding paths inside the
container. Running the binary directly on the VPS can use the default
<code>/proc</code> and <code>/</code> paths with <code>METRICS_SCOPE=vps</code>.

The generated <code>.env</code> uses this local fallback callback:

~~~text
GOOGLE_REDIRECT_URL='http://localhost:8080/auth/google/callback'
~~~

If you are using a public HTTPS management URL through the bundled proxy, use
the managed deployment mode before signing in through it:

~~~text
MANAGEMENT_ACCESS_MODE='managed-https'
APP_BIND_ADDRESS='0.0.0.0'
~~~

~~~sh
docker compose up -d
~~~

Check the stack with:

~~~sh
docker compose ps
docker compose logs --tail=100 app
~~~

Open Redlaunch at <code>http://localhost:8080</code> through the SSH tunnel and
sign in with the authorized Google account. The public URL becomes available
after the first-run Caddy setup described below.

## 4. Complete the Redlaunch first-run setup

After the first login, Redlaunch shows **Set up your server**:

- Select **Reverse proxy — Caddy** if you will publish applications on domains.
  Caddy listens on ports 80 and 443 and routes application traffic on the
  shared <code>redlaunch-common</code> network.
- Select **Docker Registry** if you want the local registry at
  <code>localhost:5000</code> for application images. It is optional when your
  images are already available from another registry.
- Click **Complete setup** and wait for the progress dialog to finish.

Redlaunch creates and labels the shared <code>redlaunch-common</code> Docker
network independently of the optional services. This happens when Caddy, the
registry, both, or neither is selected, so applications created after setup
can use the same network. An existing network with that name is used only when
it has the Redlaunch management labels; an unrelated network is refused.

The selected core services are stored under the configured projects root in
<code>core/proxy</code> and <code>core/registry</code>. The GitHub Actions wizard
later adds <code>core/github-actions-tunnel</code> when you configure a
repository deployment.

For Caddy to obtain certificates for public domains, point the domains' DNS
records to the VPS and make TCP ports 80 and 443 reachable. Add UDP 443 if you
want HTTP/3.

## 5. First steps in Redlaunch

### Configure a GitHub Actions image build

After the local Docker Registry is running, open an application, select its
**Deployment** tab, and choose **GitHub Actions deployment**. The wizard starts a
restricted forwarding gateway, generates a repository-specific SSH key, and
shows the exact GitHub Actions variables, secrets, and workflow file to add.
Allow inbound TCP port <code>2222</code> to the VPS before running the workflow.
See the [GitHub Actions image deployment guide](GITHUB_ACTIONS.md) for the
complete handoff and troubleshooting steps.

### Create an application

1. Open **Applications** in the main navigation.
2. Click **Create application**.
3. Enter an **Application name**, such as <code>Status page</code>.
4. The **Folder name** is filled with a lowercase slug containing only letters,
   numbers, and dashes as you type. Edit it if needed; it must be one directory
   name under <code>applications</code>. Do not enter a path or include
   <code>/</code>.
5. Click **Create application**.

Redlaunch creates a dedicated Compose project under
<code>&lt;projects-root&gt;/applications/status-page/</code>, including
<code>compose.yml</code>, <code>vars.env</code>, and <code>secrets.env</code>.
The project is connected to the shared <code>redlaunch-common</code> network.

### Publish Redlaunch over HTTPS

If Caddy was selected during first-run setup, open an application's
**Settings** tab and find **Public access**. Enter the hostname that should
serve the Redlaunch management interface, enable **Enable access Redlaunch
publicly (https)**, and save. Use only a hostname such as
<code>redlaunch.example.com</code>, without a scheme or path.

Redlaunch stores this installation-wide setting in SQLite, adds the hostname
to the managed Caddyfile, routes it through Docker's host gateway to the
configured Redlaunch listener port, and reloads Caddy. The default listener is
<code>0.0.0.0:8080</code>; if <code>HTTP_ADDR</code> uses another port, Caddy
uses that port instead. The same setting is shown from every application's
Settings tab.
Point the hostname's DNS record to the VPS before enabling it.

Google authentication automatically uses
<code>https://redlaunch.example.com/auth/google/callback</code> when sign-in is
started at the configured public hostname. Add that exact callback URL to the
Google OAuth client, keep the local callback URI registered if local access is
also needed, and set <code>AUTH_COOKIE_SECURE=true</code> in Redlaunch's
<code>.env</code> before using the public URL. Replace the example hostname with
the configured public hostname.

### Create services

Open the application and use **Services → Create service**. Choose the service
type from the menu:

- **PostgreSQL database**: enter a service name, PostgreSQL image version,
  database name, and database user. Enter a password or leave it blank to have
  Redlaunch generate one. The database starts immediately and includes a
  production-ready <code>pg_isready</code> healthcheck. The service loads the
  project <code>vars.env</code>/<code>secrets.env</code> files plus its own
  <code>&lt;service&gt;.vars.env</code> and
  <code>&lt;service&gt;.secrets.env</code> files, so multiple PostgreSQL services
  retain separate credentials.
- **Redis cache**: enter a service name, Redis image version, and host port.
  Password authentication is optional. Enable **Persist to disk** when the
  cache should use append-only logging and snapshots. Redis is published on
  loopback by default, includes a production-ready <code>redis-cli</code>
  healthcheck, and starts immediately. Password-protected Redis services use
  the same service-specific environment-file convention.
- **Application**: enter the Compose service name and Docker image reference.
  **Automatically start container** is off by default, which is useful when
  the image is not available yet. Turn it on when the image can be pulled
  immediately.

Useful defaults are <code>db</code>/PostgreSQL 17, <code>redis</code>/Redis 7 on
port 6379, and <code>app</code> for a custom application container. All managed
services load both <code>vars.env</code> and <code>secrets.env</code>; keep
non-secret configuration in the former and credentials in the latter.

The managed environment editor supports one assignment per physical line and
preserves comments, CRLF/BOM markers, quotes, dollar escapes, and interpolation
tokens when a value is only renamed or moved. Multiline dotenv continuations
are intentionally unsupported by the editor; edit those files outside
Redlaunch and re-import them if needed. A replacement or explicit clear is a
separate operation and may intentionally write a new literal token.

When an older generated project has database services that use only the shared
environment files, the first later database change migrates each unambiguous
legacy service to its own files without changing its mounted volume. If more
than one legacy service of the same database type still shares those files, or
one of its scoped files is missing, Redlaunch stops with an ambiguity error for
operator resolution; it does not guess or rotate credentials.

To bring in an existing Compose project, open an application that has no
registered services and choose **Import Docker Compose project...**. Upload its
Compose YAML file. Redlaunch validates the file, registers each Compose service,
and adds the managed container names, labels, <code>vars.env</code>, and
<code>secrets.env</code> references. Imported services are not started
automatically; start them from the Services tab when ready.

Imports intentionally support a local, managed subset: service definitions,
declared images, local build contexts, relative
<code>env_file</code>/<code>dockerfile</code> paths, and named or
application-directory bind volumes. Remote file sources, Compose
includes/extensions, file-backed Compose secrets/configs, host capabilities
such as privileged mode/devices/host namespaces or Docker socket mounts, and
paths that escape the application directory are rejected before the upload is
written or Compose is run. Declared image references may still be pulled when
an operator starts a service. The upload also cannot choose its Compose project
name; Redlaunch supplies a stable installation-, scope-, and
resource-specific identity.

Compose is invoked with project-level <code>.env</code> loading disabled. Its
configuration subprocess receives Docker settings and explicitly referenced
application interpolation variables, while Redlaunch session, OAuth, database,
and listener settings are filtered out. Policy errors identify the rejected
Compose line and leave the existing files and SQLite service metadata
unchanged.

Compose anchors, aliases, merge keys, and unsupported inline structures are
rejected where Redlaunch must edit the structure; supported inline
<code>env_file</code>/<code>labels</code> forms are normalized while preserving
their entries. Final Compose validation runs after Redlaunch adds managed
container names, labels, and required environment files. A failed validation
leaves the original files and metadata unchanged.

To import application configuration, choose **Import variables...** on the
Variables tab or **Import secrets...** on the Secrets tab. Upload a dotenv file
for the matching managed file. Redlaunch validates the entries, keeps both
managed files in the application directory, and masks secret values in the UI.

For a custom application image, the **Use Docker Registry** switch controls how
the image reference is interpreted:

- leave it off to add the local registry prefix <code>localhost:5000/</code>; or
- turn it on to use the image reference exactly as entered, for example
  <code>ghcr.io/example/status-page:1.2.0</code>.

The custom application service must be running before a domain route can serve
traffic successfully.

### Add one or more domains

1. Open the application's **Domains** tab.
2. Click **Add domain**.
3. Enter a hostname such as <code>example.com</code> or
   <code>www.example.com</code>. Enter only the hostname, not
   <code>https://</code> and not a path.
4. Click **Save** and repeat for each additional hostname.

Before testing a domain, create its DNS A/AAAA record(s) pointing to the VPS.
If Caddy is installed and ports 80/443 are reachable, it can manage HTTPS for
the public hostname.

### Route a domain to a service

1. In the **Domains** tab, click **Manage routing** next to a domain.
2. Click **Add routing**.
3. Leave **Subdomain** empty to use the main domain, or enter a label such as
   <code>api</code> to use <code>api.example.com</code>.
4. Enter the incoming **Request path**, normally <code>/</code> or
   <code>/api</code>.
5. Select an existing application **Service** by its Compose service name.
6. Enter the **Service port**, the port the service listens on inside its
   container (usually <code>80</code>, or <code>3000</code>/<code>8080</code> for
   common application servers).
7. Enter the **Service path**, normally <code>/</code>, then click **Save**.

For example, this route sends all requests for <code>api.example.com</code> to
the application's <code>api</code> service and rewrites the request path to
<code>/</code> before proxying:

| Field | Value |
| --- | --- |
| Domain | <code>example.com</code> |
| Subdomain | <code>api</code> |
| Request path | <code>/</code> |
| Service | <code>api</code> |
| Service port | <code>3000</code> |
| Service path | <code>/</code> |

Redlaunch saves the routing in SQLite, regenerates the managed Caddyfile, and
reloads Caddy. Add additional routes for other subdomains or paths as needed.
Longer path matchers take precedence when multiple routes share a host.

## Before a managed-resource migration

Run this inventory and backup procedure before a release that changes Compose
project identities, container ownership, volume mappings, database service
configuration, routing metadata, or backup timers. Use a maintenance window.
The procedure records labels and non-secret metadata; it never prints
<code>.env</code>, <code>vars.env</code>, or <code>secrets.env</code> contents.

From the Redlaunch installation directory, choose the configured projects root
(the default is the <code>projects</code> directory), create a private backup
directory outside that tree, and record the current resource inventory:

~~~sh
umask 077
projects_root="$(pwd)/projects"
backup_dir="$(dirname "$(pwd)")/redlaunch-pre-migration-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 0700 "$backup_dir"

find "$projects_root/applications" "$projects_root/core" \
  -mindepth 1 -maxdepth 1 -type d -print | sort > "$backup_dir/project-directories.txt"
docker ps -a --format '{{.ID}}\t{{.Names}}\t{{.Label "redlaunch.managed"}}\t{{.Label "com.docker.compose.project"}}\t{{.Label "com.docker.compose.project.working_dir"}}\t{{.Label "com.docker.compose.project.config_files"}}' \
  > "$backup_dir/containers.tsv"
docker volume ls --filter label=com.docker.compose.project \
  --format '{{.Name}}\t{{.Label "com.docker.compose.project"}}\t{{.Label "com.docker.compose.volume"}}' \
  > "$backup_dir/volumes.tsv"
docker network ls --filter name=redlaunch-common --no-trunc \
  > "$backup_dir/networks.txt"
docker network inspect redlaunch-common --format '{{json .}}' \
  > "$backup_dir/network-redlaunch-common.json"
systemctl list-unit-files 'redlaunch-backup-*.timer' --no-pager \
  > "$backup_dir/backup-timer-files.txt"
systemctl list-timers 'redlaunch-backup-*.timer' --all --no-pager \
  > "$backup_dir/backup-timers.txt"
~~~

If <code>PROJECTS_ROOT</code> is customized, set <code>projects_root</code> to
that absolute host path. Review <code>containers.tsv</code> for duplicate Compose
project labels, especially an application and core component that both use a
folder such as <code>proxy</code>. Review <code>volumes.tsv</code> before any
container recreation; a named volume is part of the data identity even when a
Compose project name changes.

Redlaunch derives each managed Compose project name from the absolute projects
root, its <code>applications</code>/<code>core</code> scope, and the resource
folder. Destructive service and project operations also require both the
derived Compose project label and <code>redlaunch.managed=true</code>. A legacy
container without that label is refused rather than guessed at; inventory it,
map its volumes, and perform a reviewed adoption before removing anything.

The shared <code>redlaunch-common</code> network is adopted, never deleted
blindly. Installations predating managed-network ownership created it through
the proxy Compose project (Compose labels, no Redlaunch labels) or plain
<code>docker network create</code> (no labels). Setup accepts such a network
in place when <code>network-redlaunch-common.json</code> shows a local bridge
driver, no <code>Internal</code> flag, and no foreign
<code>redlaunch.owner</code> label; any other shape stays refused. Because
Docker network labels are immutable, adoption is re-validated on every call
rather than recorded. Do not remove an in-use shared network to "fix"
ownership: attached containers would lose connectivity.

Map legacy project-scoped volumes before recreating anything.
<code>volumes.tsv</code> pairs each volume name with its owning Compose project
and volume key: an old <code>OLD_PROJECT_KEY</code> volume holding mounted
data must be mapped to the new <code>NEW_PROJECT_KEY</code> identity from the
release instructions (same key, new project prefix) and confirmed present
before the old project is stopped. Do not proceed when a project-scoped volume
in the inventory has no mapped new identity.

Stop the manager so SQLite is closed, then copy the complete data volume,
managed project tree, and installation settings without displaying their
contents:

~~~sh
docker compose stop app
docker run --rm \
  --mount type=volume,src=redlaunch_app-data,dst=/source,readonly \
  --mount type=bind,src="$backup_dir",dst=/backup \
  alpine:3.22 tar -C /source -czf /backup/app-data.tar.gz .
tar -C "$projects_root" -czf "$backup_dir/projects.tar.gz" .
tar -czf "$backup_dir/installation-settings.tar.gz" .env docker-compose.yml
chmod 0600 "$backup_dir"/*.tar.gz
mkdir -m 0700 "$backup_dir/app-data"
tar -C "$backup_dir/app-data" -xzf "$backup_dir/app-data.tar.gz"
~~~

The archives contain credentials and must remain readable only by the operator.
Do not attach them to issues or copy them into the repository. To inventory the
non-secret SQLite metadata, install the distribution's <code>sqlite3</code>
command if necessary and run:

~~~sh
sqlite3 -header -separator $'\t' "$backup_dir/app-data/redlaunch.db" \
  'SELECT a.id, a.folder_name, s.id AS service_id, s.name AS service_name, s.service_type FROM applications a LEFT JOIN services s ON s.application_id = a.id ORDER BY a.id, s.id' \
  > "$backup_dir/application-services.tsv"
sqlite3 -header -separator $'\t' "$backup_dir/app-data/redlaunch.db" \
  'SELECT application_id, domain_id, subdomain, path, service_name, service_port, service_path FROM routings ORDER BY application_id, domain_id, id' \
  > "$backup_dir/routings.tsv"
sqlite3 -header -separator $'\t' "$backup_dir/app-data/redlaunch.db" \
  'SELECT service_id, enabled, schedule_type, hour, minute, weekday, retention_days, backup_location FROM backup_schedules ORDER BY service_id' \
  > "$backup_dir/backup-schedules.tsv"
docker compose start app
~~~

Confirm that the manager is healthy and that the backup contains the expected
applications, multiple database rows, routes, named volumes, and enabled timer
rows before proceeding. If any step after <code>docker compose stop app</code>
fails, keep the private backup directory and restart the manager before
troubleshooting. Never use an old ambiguous project-wide
<code>down --volumes</code> as a migration shortcut.

### Interrupted backups and deletion recovery

Web backup and restore requests return a progress operation while the database
command continues under the manager's execution deadline. Web operations are
further bounded by the 15-minute tracked-job timeout; every lease-holding
backup, restore, retention, and deletion operation is additionally capped at
25 minutes, below the 30-minute service lease, and the generated systemd unit
stops overruns at the same boundary with <code>TimeoutStartSec=1500</code>.
The same service lease is used by the scheduled <code>backup-run</code>
command, so do not manually remove lease rows during a running operation. A
crashed process leaves an expiring lease; the next operation can reclaim it
after the lease window. Old temporary dump files are removed conservatively
by a later successful backup; files without Redlaunch's temporary filename
prefix are never touched.

Restores accept only plain SQL dumps and apply them inside a single database
transaction: a failure or an interrupted connection rolls the dump back and
leaves the database unchanged, so retrying the same backup file is safe.
Custom, tar, or directory archives are rejected before any database work.
A failed restore reports that the database was left unchanged; check that the
database service is running and submit the restore again. Restore operations
share the 25-minute execution bound above.

Application deletion writes a tombstone before stopping Docker resources. If a
stage fails, submit the deletion again with the exact application name after
fixing the reported issue. The service resumes the recorded stage and keeps
backup schedules, routing state, deployment-key cleanup, metadata, and the
application folder coordinated. The application page reads the retained
tombstone after a manager restart, so the interrupted stage remains
operator-visible. Do not delete the SQLite database or manually remove the
application directory while a deletion tombstone is incomplete. Folder names
stay reserved until the tombstone reaches completion: creating a replacement
application with the same folder is rejected while the previous deletion is
incomplete, and retrying the old deletion never removes a replacement folder.
A retry after the folder is already gone completes the tombstone instead of
reporting "application not found".

Service deletion follows the same coordinated guarantees: it records its own
durable tombstone, disables the service's backup timer first, holds the
service backup lease across the remaining stages so a running backup is never
interrupted, removes the service's routing rows with a Caddy reload, then
removes the container, Compose entry, and metadata. If a stage fails, submit
the deletion again; the recorded stage resumes. Service backup files are
retained as operator-managed artifacts while schedule and history records
cascade with the service metadata; export or remove those files separately
after confirming the retention policy.
Backup files under <code>BACKUP_ROOT/&lt;folder&gt;/&lt;service&gt;</code> are retained
when application metadata is deleted; export or remove those operator-managed
artifacts separately after confirming the retention policy.

When a reviewed release explicitly instructs you to adopt new Compose project
identities, use the recorded project label and absolute configuration path to
stop each old project once, without deleting volumes:

~~~sh
  docker compose --project-name OLD_PROJECT --env-file /dev/null -f ABSOLUTE_CONFIG_FILE down --remove-orphans
~~~

Never add <code>--volumes</code> here: stopping must not delete data volumes.
When <code>containers.tsv</code> shows two resources sharing one Compose
project label (for example an application folder and a core component that
both resolve to <code>proxy</code>), stop each resource with its own absolute
configuration file from the inventory instead of addressing the shared label
once; a single project-wide command would touch both resources' containers.

Do not proceed when <code>volumes.tsv</code> shows a project-scoped volume whose
new identity has not been mapped by the release's migration instructions. After
updating and rebuilding Redlaunch, recreate each reviewed project under the
identity calculated by the same production binary:

~~~sh
while IFS= read -r project_dir; do
  if [ -f "$project_dir/compose.yml" ]; then
    compose_file="$project_dir/compose.yml"
  elif [ -f "$project_dir/compose.yaml" ]; then
    compose_file="$project_dir/compose.yaml"
  else
    continue
  fi
  project_name=$(docker compose exec -T app \
    redlaunch compose-project-name --directory "$project_dir")
  docker compose --project-name "$project_name" --env-file /dev/null -f "$compose_file" up -d
done < "$backup_dir/project-directories.txt"
~~~

The helper prints only the derived project name. Keep the configured absolute
projects-root path stable: it is part of the installation-scoped identity.

## Troubleshooting

- **<code>redirect_uri_mismatch</code>**: register both the configured local
  <code>GOOGLE_REDIRECT_URL</code> and, when public access is enabled, the exact
  <code>https://&lt;public-hostname&gt;/auth/google/callback</code> URI in the
  Google client. The scheme, host, port, path, and trailing slash must match.
- **Google login says the account is not authorized**: use the email entered
  during <code>make setup</code>; it must be a verified Google email and, for a
  Google app in Testing status, a configured test user.
- **Caddy routing is unavailable**: confirm that Caddy was selected during
  first-run setup, the domain resolves to the VPS, ports 80/443 are reachable,
  and the target service is running.
- **Caddy reports “mount ... Caddyfile ... not a directory”**: update the
  deployment so the managed-project path is shared with the host Docker
  daemon, then recreate Redlaunch and start the proxy again:

  ~~~sh
  cd ~/redlaunch
  git pull
  docker compose up -d --build
  docker compose -f projects/core/proxy/compose.yml up -d --force-recreate
  ~~~

  Replace <code>~/redlaunch</code> with the installation directory when needed.
- **<code>make setup</code> cannot run Docker**: reconnect after adding the SSH
  user to the <code>docker</code> group, or verify the Docker daemon with
  <code>docker info</code>.
