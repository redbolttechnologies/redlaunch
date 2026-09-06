# GitHub Actions image deployment

This guide shows a sample workflow that builds the Docker image from the
repository-root `Dockerfile` and pushes it to the Redlaunch local registry
through an SSH tunnel. The registry stays bound to the target server's
loopback interface; it is not exposed to the public internet.

The example publishes an image tagged with the commit SHA. It does not update
or restart a Redlaunch application automatically. After the push, use the
image reference `localhost:5000/redlaunch:<commit-sha>` in the managed service
that should run it.

The target server must already have Redlaunch's Docker Registry component
installed and running. Redlaunch configures that registry as
`127.0.0.1:5000:5000` in Compose, so it is reachable only on the target
server's loopback address.

## Configure GitHub

Create these repository variables under **Settings → Secrets and variables →
Actions → Variables**:

| Variable | Example value | Purpose |
| --- | --- | --- |
| `REDLAUNCH_SERVER_IP` | `203.0.113.10` | Public IP address of the target server |
| `REDLAUNCH_SERVER_USERNAME` | `redlaunch-deploy` | SSH username used by the workflow |

Create these repository secrets under **Settings → Secrets and variables →
Actions → Secrets**:

| Secret | Value |
| --- | --- |
| `REDLAUNCH_DEPLOY_SSH_KEY` | The complete private Ed25519 key for the deploy user |
| `REDLAUNCH_DEPLOY_KNOWN_HOSTS` | A verified `known_hosts` entry for the target server |

The host key is not a credential, but storing the multiline `known_hosts` entry
as a secret keeps it out of normal workflow output. Verify the host key
fingerprint before adding it to GitHub; do not blindly trust a key collected
during a workflow run.

## Sample workflow

Save the following as `.github/workflows/push-image.yml` in the repository:

```yaml
name: Build and push Redlaunch image

on:
  push:
    branches:
      - master

permissions:
  contents: read

env:
  IMAGE: localhost:5000/redlaunch:${{ github.sha }}

jobs:
  build-and-push:
    runs-on: ubuntu-latest
    steps:
      - name: Check out repository
        uses: actions/checkout@v4

      - name: Build image
        run: docker build --file ./Dockerfile --tag "$IMAGE" .

      - name: Open SSH tunnel and push image
        shell: bash
        env:
          SERVER_IP: ${{ vars.REDLAUNCH_SERVER_IP }}
          SERVER_USERNAME: ${{ vars.REDLAUNCH_SERVER_USERNAME }}
          SSH_PRIVATE_KEY: ${{ secrets.REDLAUNCH_DEPLOY_SSH_KEY }}
          SSH_KNOWN_HOSTS: ${{ secrets.REDLAUNCH_DEPLOY_KNOWN_HOSTS }}
        run: |
          set -euo pipefail

          test -n "$SERVER_IP" || { echo "REDLAUNCH_SERVER_IP is not set" >&2; exit 1; }
          test -n "$SERVER_USERNAME" || { echo "REDLAUNCH_SERVER_USERNAME is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "REDLAUNCH_DEPLOY_SSH_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "REDLAUNCH_DEPLOY_KNOWN_HOSTS is not set" >&2; exit 1; }
          [[ "$SERVER_IP" =~ ^[0-9.]+$ ]] || { echo "REDLAUNCH_SERVER_IP must be an IPv4 address" >&2; exit 1; }
          [[ "$SERVER_USERNAME" =~ ^[A-Za-z_][A-Za-z0-9._-]*$ ]] || { echo "REDLAUNCH_SERVER_USERNAME contains invalid characters" >&2; exit 1; }

          ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
          install -d -m 700 "$ssh_dir"
          printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/id_ed25519"
          printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
          chmod 600 "$ssh_dir/id_ed25519"
          chmod 644 "$ssh_dir/known_hosts"

          tunnel_pid=""
          cleanup() {
            if [ -n "$tunnel_pid" ]; then
              kill "$tunnel_pid" 2>/dev/null || true
              wait "$tunnel_pid" 2>/dev/null || true
            fi
          }
          trap cleanup EXIT

          ssh \
            -N -T \
            -i "$ssh_dir/id_ed25519" \
            -o IdentitiesOnly=yes \
            -o BatchMode=yes \
            -o ConnectTimeout=15 \
            -o ExitOnForwardFailure=yes \
            -o StrictHostKeyChecking=yes \
            -o UserKnownHostsFile="$ssh_dir/known_hosts" \
            -o ServerAliveInterval=30 \
            -o ServerAliveCountMax=3 \
            -L "127.0.0.1:5000:127.0.0.1:5000" \
            "$SERVER_USERNAME@$SERVER_IP" &
          tunnel_pid=$!

          for attempt in $(seq 1 30); do
            if ! kill -0 "$tunnel_pid" 2>/dev/null; then
              echo "SSH tunnel exited before it was ready" >&2
              exit 1
            fi

            if curl \
              --fail --silent --show-error \
              --connect-timeout 2 --max-time 5 \
              http://127.0.0.1:5000/v2/ >/dev/null; then
              break
            fi

            if [ "$attempt" -eq 30 ]; then
              echo "The registry did not respond through the SSH tunnel" >&2
              exit 1
            fi
            sleep 1
          done

          docker push "$IMAGE"
```

The runner's Docker daemon connects to `localhost:5000`, while SSH forwards
that connection to `127.0.0.1:5000` on the target server. Docker treats a
loopback registry as a local registry; if the runner uses a custom Docker
daemon, make sure that daemon is configured for the registry's HTTP or trusted
TLS setup.

The private key and known-hosts contents are passed to commands through
environment variables and are never put on a command line. The tunnel is
closed by the `trap` after `docker push` completes.

## Create a trusted SSH tunnel

Complete these steps once for the target server. The examples assume an
Ubuntu or Debian server and an existing administrator account with `sudo`.

### 1. Create a dedicated deploy user

On the target server:

```sh
sudo adduser --disabled-password --gecos "" --shell /usr/sbin/nologin redlaunch-deploy
```

Do not add this user to the `docker` group. The workflow only needs to forward
the registry port, and membership in the Docker group grants root-equivalent
access to the server.

Create an SSH configuration block that permits only local TCP forwarding to
the registry:

```sh
sudoedit /etc/ssh/sshd_config.d/90-redlaunch-deploy.conf
```

Add:

```text
Match User redlaunch-deploy
    AllowTcpForwarding local
    PermitOpen 127.0.0.1:5000
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    ForceCommand /usr/bin/false

Match all
```

Validate and reload SSH:

```sh
sudo sshd -t
sudo systemctl reload ssh
```

If the server uses a different SSH service name, reload that service instead.

### 2. Create a dedicated SSH key

Run this on a trusted administrator workstation, not on the target server:

```sh
ssh-keygen -t ed25519 \
  -f ~/.ssh/redlaunch-github-actions \
  -C "github-actions-redlaunch" \
  -N ""
```

This creates:

- `~/.ssh/redlaunch-github-actions`: the private key, which becomes a GitHub secret;
- `~/.ssh/redlaunch-github-actions.pub`: the public key, which is installed on the server.

The sample uses an unencrypted key because the workflow is non-interactive.
Use this key only for the restricted deploy account and protect the GitHub
secret carefully.

### 3. Install the public key on the target server

On the target server, create the key directory and open the authorized-keys
file:

```sh
sudo install -d -m 700 -o redlaunch-deploy -g redlaunch-deploy \
  /home/redlaunch-deploy/.ssh
sudoedit /home/redlaunch-deploy/.ssh/authorized_keys
```

On the workstation, display the public key:

```sh
cat ~/.ssh/redlaunch-github-actions.pub
```

Paste the complete one-line output into `authorized_keys`, prefixed with the
restrictions below:

```text
no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc,permitopen="127.0.0.1:5000" ssh-ed25519 AAAA... github-actions-redlaunch
```

Replace only the `ssh-ed25519 AAAA...` portion and comment with the exact
contents of the generated `.pub` file. Then fix ownership and permissions:

```sh
sudo chown redlaunch-deploy:redlaunch-deploy /home/redlaunch-deploy/.ssh/authorized_keys
sudo chmod 600 /home/redlaunch-deploy/.ssh/authorized_keys
```

The SSH server configuration and the key restrictions both limit this key to
the registry forwarding destination. The account cannot open an interactive
terminal through this key.

### 4. Verify and record the server host key

Host-key verification protects the workflow from connecting to an impostor
server. First obtain the server's Ed25519 host-key fingerprint through a
trusted channel, such as the VPS console:

```sh
sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

On the trusted workstation, collect the key for the exact IP used by the
workflow:

```sh
ssh-keyscan -t ed25519 -H 203.0.113.10 > redlaunch-known_hosts
ssh-keygen -lf redlaunch-known_hosts
```

Compare the two fingerprints. Only after they match should the complete
contents of `redlaunch-known_hosts` be stored as the
`REDLAUNCH_DEPLOY_KNOWN_HOSTS` GitHub secret.

### 5. Add the GitHub secrets

In **Settings → Secrets and variables → Actions → Secrets**, create:

1. `REDLAUNCH_DEPLOY_SSH_KEY`: paste the complete contents of
   `~/.ssh/redlaunch-github-actions`, including the `BEGIN` and `END` lines.
2. `REDLAUNCH_DEPLOY_KNOWN_HOSTS`: paste the verified contents of
   `redlaunch-known_hosts`.

In **Settings → Secrets and variables → Actions → Variables**, create:

1. `REDLAUNCH_SERVER_IP`: the same IP used for the host-key entry;
2. `REDLAUNCH_SERVER_USERNAME`: `redlaunch-deploy`.

Keep the private key out of the repository, workflow YAML, issues, and build
logs. The workflow uses strict host-key checking and fails if either the
server variables or SSH secrets are missing.

## References

- [GitHub Actions variables](https://docs.github.com/en/actions/concepts/workflows-and-actions/variables)
- [Using secrets in GitHub Actions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)
- [Docker daemon insecure registries](https://docs.docker.com/reference/cli/dockerd/#insecure-registries)
- [Docker Compose push](https://docs.docker.com/reference/cli/docker/compose/push/)
