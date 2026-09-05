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

   Replace the example hostname with the URL used in the browser. The scheme,
   hostname, port, path, and trailing slash must match exactly. Redlaunch does
   not use a trailing slash on this callback path.
5. Create the client and copy the **Client ID** and **Client Secret**. Store the
   secret securely; do not commit it, paste it into an issue, or put it in a
   public file.

Google's [OAuth 2.0 web-server documentation](https://developers.google.com/identity/protocols/oauth2/web-server)
contains the current credential and redirect-URI rules.

### Choosing the callback URL

The bundled Compose file exposes Redlaunch on port <code>8080</code>; it does
not make the Redlaunch management UI public over HTTPS. The simplest secure
first login on a VPS is an SSH tunnel:

~~~sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
~~~

With that tunnel open, visit <code>http://localhost:8080</code> in your local
browser and use the default callback URL above.

If an HTTPS reverse proxy already publishes Redlaunch, use its public HTTPS URL
as <code>GOOGLE_REDIRECT_URL</code> and set
<code>AUTH_COOKIE_SECURE=true</code>. The Caddy service installed from
Redlaunch's first-run screen is for routing managed application services; it is
not an automatic reverse proxy for the Redlaunch UI itself.

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
- adds the first email address to Redlaunch's SQLite login allowlist; and
- builds and starts Redlaunch with <code>docker compose up -d --build</code>.

The command intentionally refuses to overwrite an existing <code>.env</code>
file. Keep <code>.env</code> private; it contains the OAuth client secret and
session secret.

The generated <code>.env</code> uses this default callback:

~~~text
GOOGLE_REDIRECT_URL='http://localhost:8080/auth/google/callback'
~~~

If you are using a public HTTPS management URL, edit <code>.env</code>
immediately after setup and restart the stack before signing in:

~~~text
GOOGLE_REDIRECT_URL='https://redlaunch.example.com/auth/google/callback'
AUTH_COOKIE_SECURE='true'
~~~

~~~sh
docker compose up -d
~~~

Check the stack with:

~~~sh
docker compose ps
docker compose logs --tail=100 app
~~~

Open Redlaunch at <code>http://localhost:8080</code> through the SSH tunnel, or
at the HTTPS URL configured above. Sign in with the authorized Google account.

## 4. Complete the Redlaunch first-run setup

After the first login, Redlaunch shows **Set up your server**:

- Select **Reverse proxy — Caddy** if you will publish applications on domains.
  Caddy listens on ports 80 and 443 and routes application traffic on the
  shared <code>redlaunch-common</code> network.
- Select **Docker Registry** if you want the local registry at
  <code>localhost:5000</code> for application images. It is optional when your
  images are already available from another registry.
- Click **Complete setup** and wait for the progress dialog to finish.

The selected core services are stored under the configured projects root in
<code>core/proxy</code> and <code>core/registry</code>.

For Caddy to obtain certificates for public domains, point the domains' DNS
records to the VPS and make TCP ports 80 and 443 reachable. Add UDP 443 if you
want HTTP/3.

## 5. First steps in Redlaunch

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

### Create services

Open the application and use **Services → Create service**. Choose the service
type from the menu:

- **PostgreSQL database**: enter a service name, PostgreSQL image version,
  database name, and database user. Enter a password or leave it blank to have
  Redlaunch generate one. The database starts immediately; its password is
  stored in <code>secrets.env</code>.
- **Redis cache**: enter a service name, Redis image version, and host port.
  Password authentication is optional. Enable **Persist to disk** when the
  cache should use append-only logging and snapshots. Redis is published on
  loopback by default and starts immediately.
- **Application**: enter the Compose service name and Docker image reference.
  **Automatically start container** is off by default, which is useful when
  the image is not available yet. Turn it on when the image can be pulled
  immediately.

Useful defaults are <code>db</code>/PostgreSQL 17, <code>redis</code>/Redis 7 on
port 6379, and <code>app</code> for a custom application container. All managed
services load both <code>vars.env</code> and <code>secrets.env</code>; keep
non-secret configuration in the former and credentials in the latter.

To bring in an existing Compose project, open an application that has no
registered services and choose **Import Docker Compose project...**. Upload its
Compose YAML file. Redlaunch validates the file, registers each Compose service,
and adds the managed container names, labels, <code>vars.env</code>, and
<code>secrets.env</code> references. Imported services are not started
automatically; start them from the Services tab when ready.

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
6. Enter the **Service path**, normally <code>/</code>, then click **Save**.

For example, this route sends all requests for <code>api.example.com</code> to
the application's <code>api</code> service and rewrites the request path to
<code>/</code> before proxying:

| Field | Value |
| --- | --- |
| Domain | <code>example.com</code> |
| Subdomain | <code>api</code> |
| Request path | <code>/</code> |
| Service | <code>api</code> |
| Service path | <code>/</code> |

Redlaunch saves the routing in SQLite, regenerates the managed Caddyfile, and
reloads Caddy. Add additional routes for other subdomains or paths as needed.
Longer path matchers take precedence when multiple routes share a host.

## Troubleshooting

- **<code>redirect_uri_mismatch</code>**: compare
  <code>GOOGLE_REDIRECT_URL</code> with the Google client's authorized
  redirect URI character for character, then restart the Compose stack.
- **Google login says the account is not authorized**: use the email entered
  during <code>make setup</code>; it must be a verified Google email and, for a
  Google app in Testing status, a configured test user.
- **Caddy routing is unavailable**: confirm that Caddy was selected during
  first-run setup, the domain resolves to the VPS, ports 80/443 are reachable,
  and the target service is running.
- **<code>make setup</code> cannot run Docker**: reconnect after adding the SSH
  user to the <code>docker</code> group, or verify the Docker daemon with
  <code>docker info</code>.
