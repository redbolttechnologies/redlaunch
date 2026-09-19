# Server SSH keys

The **Settings → SSH keys** tab creates Ed25519 key pairs for external
automation, for example GitHub Actions, to SSH into this server itself.

Each key has a display name used only in the Redlaunch list, for example
`GHA migrator workflow access`. Redlaunch generates the pair, installs the
public key in the dedicated `redlaunch` user authorized keys file, and shows
the private key once. Revoking a key removes its public key from
`authorized_keys`.

Keys grant shell access as the dedicated `redlaunch` user. That user owns no
Redlaunch files and has no sudo privileges. `make setup` adds the user to
the `docker` group so one-off Compose commands can run through
`docker exec redbolt-redlaunch ...`. The `docker` group is root-equivalent:
treat keys as full host administrator credentials.

## Prerequisites

`make setup` creates the dedicated user with password login locked and adds
it to the `docker` group:

```sh
id redlaunch
sudo grep "redlaunch-ssh-key:" /home/redlaunch/.ssh/authorized_keys
```

`id redlaunch` must list the `docker` group. Group membership applies to
new SSH sessions only: reconnect after the user is added. Installations from
before this grant need it once:

```sh
sudo usermod -aG docker redlaunch
id redlaunch
```

Then reconnect and verify `docker exec` works before running migrations:

```sh
ssh -i ~/.ssh/redlaunch-key redlaunch@your-server 'docker exec redbolt-redlaunch true'
```

The manager container mounts the host `.ssh` directory at the identical path
(the directory, not the file: the manager runs with a read-only root
filesystem and manages the directory itself):

```yaml
volumes:
  - "/home/redlaunch/.ssh:/home/redlaunch/.ssh"
```

The manager preserves unmanaged lines and comments in the file and only
rewrites lines ending in `redlaunch-ssh-key:<id>`. The file is created with
mode `0600` when missing and must not be a symlink.

Existing installations from before this tab existed need the user once:

```sh
sudo useradd --create-home --shell /bin/bash --user-group redlaunch
sudo install -d -m 700 -o redlaunch -g redlaunch /home/redlaunch/.ssh
sudo touch /home/redlaunch/.ssh/authorized_keys
sudo chown redlaunch:redlaunch /home/redlaunch/.ssh/authorized_keys
sudo chmod 600 /home/redlaunch/.ssh/authorized_keys
sudo passwd -l redlaunch
```

Existing installations from before host-key display existed also need the
public host keys published once (public `*.pub` only, never private keys):

```sh
sudo install -d -m 700 -o redlaunch -g redlaunch /home/redlaunch/.ssh/host_keys
sudo install -m 600 -o redlaunch -g redlaunch /etc/ssh/ssh_host_*_key.pub /home/redlaunch/.ssh/host_keys/
```

Then restart the stack so the new mount applies:

```sh
docker compose up -d
```

## Create a key

1. Open **Settings → SSH keys**.
2. Enter a display name and select **Create SSH key**.
3. Copy the private key or use **Download private key**. The key is shown only
   in this response (`Cache-Control: no-store`) and is never stored by
   Redlaunch.

Use the private key from the external system:

```sh
install -m 600 redlaunch-ssh-key-1.key ~/.ssh/redlaunch-key
ssh -i ~/.ssh/redlaunch-key redlaunch@your-server
```

Verify the installed key on the server:

```sh
grep "redlaunch-ssh-key:" /home/redlaunch/.ssh/authorized_keys
```

## Use a key from CI

The creation response shows both the private key and the server's public
host keys for `SSH_KNOWN_HOSTS`. The known-hosts value is mandatory for
non-interactive SSH with `StrictHostKeyChecking=yes` and `BatchMode=yes`:
the private key authenticates CI to the server, while `known_hosts`
authenticates the server to CI and blocks machine-in-the-middle attacks.
Do not disable strict checking to work around a host-key error.

1. Replace `YOUR_SERVER_HOST` in the displayed entry with the same IP or DNS
   used as `SERVER_HOST` in CI (port 22, for example `87.229.84.149`).
2. Save the resulting `YOUR_SERVER_HOST ssh-ed25519 ...` line as the CI
   secret mapped to `SSH_KNOWN_HOSTS`.
3. Save the complete private key, including the `BEGIN` and `END` lines, as
   the CI secret mapped to `SSH_PRIVATE_KEY`.
4. Verify the displayed fingerprint out of band before trusting it:

```sh
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

A minimal CI step writes both secrets under `$RUNNER_TEMP` only and keeps
strict checking enabled:

```sh
ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
install -d -m 700 "$ssh_dir"
printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/id_ed25519"
printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
chmod 600 "$ssh_dir/id_ed25519"
chmod 644 "$ssh_dir/known_hosts"
ssh -p 22 -i "$ssh_dir/id_ed25519" \
  -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 \
  -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$ssh_dir/known_hosts" \
  redlaunch@"$SERVER_HOST" 'bash -s' <<'REMOTE_SCRIPT'
set -euo pipefail
# remote commands here
REMOTE_SCRIPT
```

## Run one-off Compose commands (migrations)

A bare `docker compose run --rm migrate` inside
`projects/applications/<name>/` fails against a managed project with:

- `volume "redbolt-<id>-..." already exists but was created for project
  "redlaunch-app-<name>-<hash>" (expected "<name>")`,
- a new `<name>_default` network being created, and
- `Conflict. The container name "/redbolt-<id>-..." is already in use`.

The bare command defaults the Compose project to the directory basename
(`talenthunt`), while Redlaunch always runs with its derived identity
(`--project-name redlaunch-app-<name>-<hash>`) and interpolation files
(`--env-file .env --env-file vars.env --env-file secrets.env`). The second
identity tries to recreate the explicitly named containers and volumes that
already belong to the managed project.

Resolve the managed identity on the server with the bundled helper and run
Compose inside the manager container, where the 0600 `vars.env`/`secrets.env`
files are readable as root. The calling host user only needs `docker exec`
access (the `docker` group) and no direct read access to `secrets.env`.
Direct host-side `docker compose --env-file secrets.env` fails as the
`redlaunch` user because those files are `0600` owned by root.

```sh
set -euo pipefail
DEPLOY_DIR=/opt/redlaunch/projects/applications/<name>
# DEPLOY_DIR must be the absolute managed application directory: the manager
# mounts the projects root at the identical host path, so this path resolves
# both on the host and inside the manager container.
if [ -f "$DEPLOY_DIR/compose.yml" ]; then
  compose_file="compose.yml"
elif [ -f "$DEPLOY_DIR/compose.yaml" ]; then
  compose_file="compose.yaml"
else
  echo "no compose.yml or compose.yaml found in $DEPLOY_DIR" >&2
  exit 1
fi
project_name=$(docker exec redbolt-redlaunch redlaunch compose-project-name --directory "$DEPLOY_DIR")
test -n "$project_name"
# Managed projects always contain vars.env and secrets.env; .env exists only
# for some imported projects. Existence of .env is checked inside the manager
# container so no host-side secrets read is needed.
env_args=()
if docker exec redbolt-redlaunch test -f "$DEPLOY_DIR/.env"; then
  env_args+=(--env-file .env)
fi
env_args+=(--env-file vars.env --env-file secrets.env)
docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" run --rm migrate
```

Check for the Compose file by absolute path: the Compose file is world-readable
while `vars.env`/`secrets.env` intentionally stay `0600`. Keep the explicit
`--project-name` on every manual Compose invocation in a managed directory
(`up`, `run`, `exec`, `logs`), whether run on the host or through
`docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose ...`;
omitting it recreates the duplicate project described above.

`No ED25519 host key is known` with strict checking means the `known_hosts`
content does not contain the system host key for the configured host.

`make setup` copies host `/etc/ssh/ssh_host_*_key.pub` files (public only)
into `/home/redlaunch/.ssh/host_keys/`, which the manager reads through its
existing `.ssh` directory mount. No private host key ever enters the
container. If the host keys change, re-copy them and rotate the CI secret.
When no host keys are provisioned, creation shows `ssh-keyscan` fallback
instructions instead.

## Revoke a key

Select the delete action next to a key and confirm **Revoke key**. Redlaunch
removes the matching `redlaunch-ssh-key:<id>` line and deletes the stored
metadata, then redirects back to **Settings → SSH keys**. Remove the private
key from the external system separately.

## Push images to the local registry

The local registry listens on host loopback `127.0.0.1:5000`. Forward it over
the same Settings SSH key, then push as `localhost:5000/...`:

```sh
ssh -N -L 127.0.0.1:5000:127.0.0.1:5000 -i ~/.ssh/redlaunch-key redlaunch@your-server
docker build -t localhost:5000/myapp/web:<sha> .
docker push localhost:5000/myapp/web:<sha>
```

See the [recipes guide](RECIPES.md) for complete GitHub Actions workflows
using only Settings SSH keys.

## Troubleshooting

- `No ED25519 host key is known ... Host key verification failed` in CI means
  `SSH_KNOWN_HOSTS` does not contain the system sshd key for the configured
  `SERVER_HOST:22`. Re-check that the secret holds the `Settings → SSH keys`
  host entry (plain `host ssh-ed25519 ...`) and that the host string matches
  exactly.
- `set SSH directory permissions: ... read-only file system` in the container
  logs means the stack still uses the old file mount instead of the directory
  mount above. Pull the latest Compose file and recreate the container with
  `docker compose up -d`. If `/home/redlaunch/.ssh/authorized_keys` on the
  host is a directory (Docker creates one when the file is missing at first
  start), stop the stack, remove that directory, create the real file per the
  existing-installation steps, and start again.
- A creation failure leaves no stored metadata; the database row is removed
  when the `authorized_keys` write fails.
- A revoke failure keeps both the file and the metadata unchanged, except when
  the metadata delete fails after the file write, in which case Redlaunch
  restores the previous file contents on a best-effort basis.
