# Installing Redlaunch

Use the install script to set up Redlaunch on an Ubuntu or Debian VPS. It
asks for your login settings, builds the application, and starts it with
Docker Compose.

## Before you start

You need:

- A supported 64-bit Ubuntu or Debian server and an SSH account with `sudo`
  access.
- Docker Engine with the Docker Compose plugin, running and accessible to
  your SSH user.
- `wget`, `git`, `make`, `bash`, and `openssl` installed on the server.
- Optionally, a Google OAuth client and an authorized Google email address
  if you want Google sign-in (see the next section).

The installer does **not** install Docker or these host tools. If they are
missing, use [the troubleshooting steps below](#missing-host-tools-or-docker).
Scheduled PostgreSQL backups also require `systemd` on the host.

Redlaunch has host-level access through Docker. Allow only trusted
administrators to sign in. By default, its web interface is available only
through an SSH tunnel; you do not need to expose port 8080 publicly.

## 1. Choose your login method

Email/password login is the default. The installer asks for your first user’s
email address and a confirmed password containing 8–128 characters.
Spaces, passphrases and special characters are supported; there are no
uppercase, number, symbol or common-password requirements. Passwords are
stored only as salted Argon2id hashes in SQLite.

You can skip Google configuration entirely. To additionally enable Google
sign-in, create an OAuth client before running the installer:

1. Open the [Google Cloud Console](https://console.cloud.google.com/apis/credentials)
   and select or create a project.
2. Complete the consent-screen setup if prompted. If the app is in **Testing**
   status, add your Google account as a test user.
3. Under **Google Auth Platform → Clients**, create a **Web application** client.
4. Add this exact **Authorized redirect URI**:

   ```text
   http://localhost:8080/auth/google/callback
   ```

5. Save the **Client ID** and **Client Secret** for the installer.

Use the localhost callback even when installing on a remote server: the SSH
tunnel in step 3 lets your browser reach Redlaunch at that address.
Keep the client secret private.

## 2. Run the installer

Connect to the VPS over SSH. Run these commands as the user who should own the
Redlaunch files:

```sh
wget https://raw.githubusercontent.com/redbolttechnologies/redlaunch/master/install.sh
bash install.sh
```

The installer asks for:

| Prompt | What to enter |
| --- | --- |
| Installation directory | Press Enter for `/opt/redlaunch`, or enter another absolute path that does not already exist. |
| First user email address | Enter a valid email address. Email addresses are case-insensitive. |
| Password / Confirm password | Choose an 8–128 character password. Both inputs are hidden. |
| Enable Google sign-in? | Press Enter for no; answer yes to enter the optional Google settings below. |
| Google Client ID | The client ID from step 1. |
| Google Client Secret | The client secret from step 1. Input is hidden. |

The installer clones Redlaunch and runs `make setup`. Setup creates private
configuration files and a random session signing secret, prepares the dedicated
`redlaunch` SSH user, creates your local login in SQLite, and builds and starts
Redlaunch. If Google sign-in is enabled, the same email automatically works
with Google. It uses `sudo`
for host changes; the checkout belongs to the invoking user.

It also creates the persistent Docker volume `redlaunch_app-data` for
Redlaunch's SQLite database. Keep this volume and the installation's `.env`,
`vars.env`, and `secrets.env` files: they contain data or credentials. Never
commit them to Git or include them in a public issue.

The installer refuses to overwrite an existing installation directory, and
setup refuses to overwrite an existing `.env` file. To update an existing
installation, use [Updating Redlaunch](#updating-redlaunch).

## 3. Open Redlaunch

On **your computer**, open an SSH tunnel to the VPS:

```sh
ssh -N -L 8080:127.0.0.1:8080 your-user@your-server
```

Replace `your-user` and `your-server` with your SSH login and server address.
Keep the command running, then open <http://localhost:8080> in your browser.
Sign in with the email and password you entered during installation, or
use Google if you enabled it.

### Complete first-run setup

On the **Set up your server** screen:

- Select **Reverse proxy — Caddy** to publish applications on domains.
- Select **Docker Registry** to store application images locally at
  `localhost:5000`. You can skip it if you use another registry.
- Click **Complete setup** and wait for it to finish.

Both components are optional. Setup prepares the shared `redlaunch-common`
network even if you select neither.

To publish applications with Caddy, point their DNS records to the VPS and
allow incoming TCP ports 80 and 443. UDP port 443 is optional for HTTP/3.
The local registry stays bound to loopback. Docker-published ports can bypass
some host firewall rules; see
[Docker's firewall guidance](https://docs.docker.com/engine/install/ubuntu/#firewall-limitations).

## 4. Create your first application

### Create an application

Open **Applications → Create application** and enter a name. The generated
folder name must be a single directory name, without `/`.

### Create services

Open the application and choose **Services → Create service** to add an
application container, PostgreSQL, Redis, or a service from the catalog.
Add ordinary settings on the **Variables** tab and credentials on the
**Secrets** tab. Start the service when its image and configuration are ready.

### Add one or more domains

To publish a service through Caddy, add its hostname on the **Domains** tab,
then use **Manage routing** to select the service and its container port.

Managed files live inside the installation's `projects` directory:

```text
projects/
├── applications/
│   └── my-app/
│       ├── compose.yml
│       ├── vars.env
│       └── secrets.env
└── core/
    ├── proxy/
    └── registry/
```

For deployment examples, see [Recipes](RECIPES.md). Open **Help** in Redlaunch
for the full how-to guides, or see [README.md](README.md) for configuration
options and application features.

## Publish Redlaunch over HTTPS

You can keep using the SSH tunnel. If you want a public management hostname,
first install Caddy through the server setup above, then:

1. Point a hostname such as `redlaunch.example.com` to the VPS and make TCP
   ports 80 and 443 reachable.
2. While signed in through the tunnel, open **Settings → Public access** and
   add the hostname, without a scheme or path.
3. If Google sign-in is enabled, add its exact callback URI to the same OAuth client:

   ```text
   https://redlaunch.example.com/auth/google/callback
   ```

4. In the installation's `.env` file, set:

   ```text
   MANAGEMENT_ACCESS_MODE='managed-https'
   APP_BIND_ADDRESS='0.0.0.0'
   ```

5. From the installation directory, apply the changes:

   ```sh
   docker compose up -d
   ```

Then open your HTTPS URL. Managed HTTPS enables secure authentication cookies.
Restrict direct access to port 8080 with firewall rules that account for
Docker's published ports; Caddy must be able to reach the manager through the
host gateway.

For Google sign-in, keep the localhost callback registered and
`GOOGLE_REDIRECT_URL` as the local fallback. Redlaunch automatically uses the HTTPS callback when login starts
at a configured public hostname. Installing Caddy alone does not publish the
management interface; you must configure **Public access** too.

If you use an external HTTPS proxy instead of Redlaunch's Public access
setting, enable secure cookies with `AUTH_COOKIE_SECURE=true`. If Google
sign-in is enabled, also set `GOOGLE_REDIRECT_URL` to that proxy's callback URL.

## Updating Redlaunch

From the installation directory, run:

```sh
cd /opt/redlaunch
make update
```

Replace `/opt/redlaunch` if you chose another directory. This pulls the latest
code and rebuilds and restarts Redlaunch. You can also use **Settings → Update
Redlaunch** in the web interface; refresh the page after the restart.

Back up your data before an update that changes managed resources, and follow
any release-specific migration instructions. Older installations that lack the
Settings SSH keys setup need the one-time host configuration in
[SSH_KEYS.md](SSH_KEYS.md).

## Local users and password recovery

Use **Settings → Users** to list users, create users with an email and password,
change passwords, and delete users after confirmation. The last user cannot be
deleted. Every user has administrator access. Google sign-in, when configured,
automatically accepts the verified Google email matching a listed user. There
is no public registration or email-based password recovery.

Upgrades preserve Google-only accounts; use **Change password** to add a password
to one. Existing username accounts can still sign in with their username and
password until you use **Set email** in Settings. Assigning an email preserves
the password and replaces username login. Google users must sign in again after
upgrading to the shared user model.

Keep SSH access for recovery. To create a user from the installation directory
in Bash (replace the example email):

```bash
read -r -s -p 'New password: ' password; printf '\n'
printf '%s\n' "$password" | docker compose run --rm --no-TTY app auth-create-user --email admin@example.com --password-stdin
unset password
```

To reset an existing user's password, replace `auth-create-user` with
`auth-reset-password`. Password changes and assigning an email invalidate all
previously issued sessions for that account, including Google sessions. Deleting
a user also revokes their access immediately. Creation refuses to overwrite an
existing email; reset refuses to create a missing user. Both commands use the
same 8–128 character policy and accept the password only through stdin.

The SQLite database is restricted to its owner's read/write access. Protect the
data volume and database backups: password hashes are sensitive. Login attempts
are limited in memory to five per email (or legacy username) and twenty per connection IP per
minute, with at most two concurrent password verifications. These limits reset
on restart. Behind a proxy, connection-IP limits are shared; forwarded headers
are not trusted. Account limits still apply independently.

To enable Google later, set both `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`
in `.env` and recreate the app using
`docker compose up -d`. Omitting both client parameters disables Google;
providing only one is a configuration error. Local login continues to work.

## Troubleshooting

Run the following commands on the VPS from your installation directory unless
stated otherwise. Avoid sharing configuration files or logs containing secrets.

### Missing host tools or Docker

Install the host tools:

```sh
sudo apt update
sudo apt install -y ca-certificates curl wget git make bash openssl
```

Install Docker Engine and the Compose plugin using the official instructions
for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/) or
[Debian](https://docs.docker.com/engine/install/debian/). Check the listed
conflicting packages if Docker is already installed.

Allow your SSH user to run Docker:

```sh
sudo usermod -aG docker "$USER"
```

The `docker` group grants root-equivalent access. Add only trusted users.
Log out and reconnect, then check:

```sh
docker info
docker compose version
```

Once both commands succeed, run the installer.

### Manual installation if the installer fails

Use this fallback only when the install script cannot complete. After preparing
the prerequisites (and optionally Google credentials), clone into a directory owned by
your SSH user:

```sh
git clone https://github.com/redbolttechnologies/redlaunch.git "$HOME/redlaunch"
cd "$HOME/redlaunch"
make setup
```

This runs the same setup as the installer. Continue with
[Open Redlaunch](#3-open-redlaunch) when it finishes.

If the installer already cloned the repository but setup failed, fix the
reported problem and use that existing checkout. If `.env` exists, do not
rerun `make setup` or delete it blindly. Setup may already have completed some
host changes. Preserve the configuration, confirm the SSH-user setup in
[SSH_KEYS.md](SSH_KEYS.md), then complete the remaining steps:

```sh
docker volume create redlaunch_app-data
docker compose build app
read -r -s -p 'Password: ' password; printf '\n'
printf '%s\n' "$password" | docker compose run --rm --no-TTY app auth-create-user --email admin@example.com --password-stdin
unset password
docker compose up -d
```

If the local user already exists, do not create it again; proceed to startup
or use `auth-reset-password`. Google sign-in automatically uses the same email.
These commands preserve the existing configuration and database volume.

### Redlaunch does not start or the browser cannot connect

Check the containers and recent application logs:

```sh
docker compose ps
docker compose logs --tail=100 app
```

Check that the SSH tunnel is still running and that you are opening
`http://localhost:8080` on your computer. If you changed `APP_PORT`, use that
port in the tunnel and update the local Google callback to match.

For custom `PROJECTS_ROOT` settings, use an absolute host path. The managed
projects directory must be mounted at the same absolute path on the host and
inside Redlaunch so Docker can find bind-mounted configuration files.

### Google login fails

- **`redirect_uri_mismatch`**: the Google client's redirect URI must match the
  callback exactly, including scheme, hostname, port, and path. There is no
  trailing slash. Register the local callback and each public hostname's HTTPS
  callback that you use.
- **Account not authorized**: sign in with the email entered during setup. If
  the Google app is in Testing status, also add that account as a test user.
  To authorize another trusted account, run:

  ```sh
  docker compose exec app redlaunch auth-add-email --email you@example.com
  ```

### Caddy cannot route traffic

Confirm Caddy is installed, the hostname resolves to the VPS, ports 80 and 443
are reachable, and the target service is running. For the management interface,
also check **Settings → Public access** and the managed HTTPS settings above.

If Caddy reports a bind-mount error for its `Caddyfile`, check the shared
projects path described above and recreate the manager after correcting it:

```sh
docker compose up -d --build
```

Then retry starting the proxy through Redlaunch.

### Scheduled backups are unavailable

The bundled deployment controls host systemd through `/usr/bin/dbus-send`.
Check that `docker-compose.yml` sets `SYSTEMD_BINARY` to that path and mounts
`/etc/systemd/system`, `/run/systemd/system`, and
`/run/dbus/system_bus_socket`. After correcting the deployment, recreate it
with `docker compose up -d --build`. Do not set `SYSTEMD_BINARY=systemctl`
inside the manager container.

### Interrupted backups and deletion recovery

Retry a failed deletion through Redlaunch after fixing the reported error;
it resumes from its saved stage. Keep the SQLite database and application
folder intact until deletion finishes. Backup files are retained separately.

A crashed backup or restore may leave a service lease that expires after
30 minutes. Do not manually remove lease rows while an operation is running.

### Before a managed-resource migration

Before a release that changes Compose project names, ownership labels, volumes,
or backup configuration:

- Record the existing container labels, Compose project names, volume mappings,
  routes, and backup schedules.
- Stop the manager before backing up `redlaunch_app-data`, then restart it.
  Also copy the managed projects tree and installation settings to a private
  backup location. These backups contain credentials.
- Back up application database volumes separately; the manager volume and
  project files do not contain their database data.
- Follow the release's migration instructions before recreating resources.
  Keep the absolute projects-root path stable because it contributes to managed
  Compose project names.

Do not remove an in-use `redlaunch-common` network or use `down --volumes` as a
migration shortcut. If ownership or volume mappings are ambiguous, resolve them
before stopping or recreating resources.
