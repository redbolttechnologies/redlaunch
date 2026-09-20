# Redlaunch recipes

Practical, copy-pasteable guides for the most common Redlaunch workflows with
GitHub Actions.

 companion guides:

- [Server SSH keys](SSH_KEYS.md) — `redlaunch` shell user, managed Compose identity,
  registry pushes over SSH
- [API tokens](API_TOKENS.md) — machine-triggered one-off runs without SSH
- [Installation](INSTALL.md) — VPS setup, first-run screen, ports, domains

## Contents

- [Conventions and shared prerequisites](#conventions-and-shared-prerequisites)
- [Recipe 1: Push Docker images to the local container registry](#recipe-1-push-docker-images-to-the-local-container-registry)
- [Recipe 2: Trigger a Drizzle migration from GitHub Actions](#recipe-2-trigger-a-drizzle-migration-from-github-actions)
- [Recipe 3: Deploy an application from GitHub Actions via the container registry](#recipe-3-deploy-an-application-from-github-actions-via-the-container-registry)
- [Recipe 4: Deploy an application from GitHub Actions by copying the image directly](#recipe-4-deploy-an-application-from-github-actions-by-copying-the-image-directly)
- [Shared utilities](#shared-utilities)
- [Security checklist](#security-checklist)
- [Troubleshooting quick reference](#troubleshooting-quick-reference)

## Conventions and shared prerequisites

Examples below use documentation placeholders. Replace them with your values;
do not paste them literally.

| Placeholder | Example | Meaning |
| --- | --- | --- |
| `your-user@your-server` | `deploy@example.com` | Your normal SSH admin account |
| `203.0.113.10` | `203.0.113.10` | Public IP or DNS name GitHub runners use to reach the VPS |
| `/opt/redlaunch` | `/opt/redlaunch` | Redlaunch installation directory on the VPS |
| `myapp` | `myapp` | Application folder name under `projects/applications/` |
| `app` | `app` | Long-lived application Compose service |
| `migrate` | `migrate` | One-off migration Compose service |
| `myapp/web` | `myapp/web` | Registry image repository (no `localhost:5000`, no tag) |

All automation below uses Redlaunch SSH keys from **Settings → SSH keys**.

| Identity | User and port | Purpose | Capabilities |
| --- | --- | --- | --- |
| Server shell | `redlaunch@<host>:22` | Push images through a loopback tunnel, run managed Compose commands, copy images | Shell as the dedicated `redlaunch` user (in the `docker` group, which is root-equivalent) |

Keep strict host-key checking enabled in CI; a host-key error means the wrong
entry or the wrong host string was used.

General rules applied by every workflow below:

- `permissions: contents: read` on the workflow.
- Repository values in `vars.*`, secrets only in `secrets.*`.
- SSH key and `known_hosts` material lives only under `$RUNNER_TEMP`.
- Never `echo` a secret and never pass `-v`/`--verbose` to `curl` or `ssh`
  with a secret header or key.
- Never construct remote shell commands by concatenating untrusted input.
  Service names, image names, and directories are validated in Redlaunch;
  workflows pass them as fixed values or validated `vars.*`.
- Variables marked **optional** in the tables below fall back to Redlaunch
  defaults inside the scripts (`${VAR:-default}`), so define them only to
  override. Required variables have no safe default; the workflows fail early
  with a message naming the missing variable.

---

## Recipe 1: Push Docker images to the local container registry

Use this when your image is built in GitHub Actions and should land in
Redlaunch's private registry for later use.

How it works:

```text
GitHub runner (docker build)
  -> SSH -L 127.0.0.1:5000:127.0.0.1:5000 redlaunch@server:22
  -> docker push localhost:5000/<repository>:<commit-sha>
  -> visible on Redlaunch's Registry page
```

The registry listens on host loopback `127.0.0.1:5000` and is not exposed
publicly. The same Settings SSH key used for deploys forwards to it.

### 1.1 Prerequisites

1. Install Redlaunch and complete first-run setup. Select **Docker Registry**
   on the **Set up your server** screen and click **Complete setup**.

   Verify afterwards:

   - **Registry** page loads (empty before the first push is normal).
   - `docker ps` shows `redbolt-registry`.
   - Projects exist under:

     ```text
     projects/core/registry/
     ```

2. Create the application if you do not have one yet:

   - **Applications → Create application**, for example name `My app`,
     folder `myapp`.
   - Create at least one **Application** service (for example `app`). The
     service image does not need to exist yet; leave **Automatically start
     container** off until the first push succeeds.

3. Create a Settings SSH key:

   - **Settings → SSH keys → Create SSH key**, for example display name
     `myapp push and deploy via GHA`.
   - Copy the private key and the system host-key entry for port `22`.
   - Verify the fingerprint out of band:

     ```sh
     ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
     ```

4. Allow inbound TCP `22` to the VPS from GitHub-hosted runners (VPS
   firewall and cloud security group / network policy). A connection timeout
   in CI almost always means this port is blocked.

5. In the GitHub repository, open **Settings → Secrets and variables →
   Actions** and create:

   Variables:

    | Variable | Value | Description |
    | --- | --- | --- |
    | `SERVER_HOST` | Public host of the VPS (required) | SSH target for the registry tunnel (`redlaunch@SERVER_HOST`) |
    | `REDLAUNCH_IMAGE_REPOSITORY` | Registry repository, no `localhost:5000`, no tag (required) | Repository pushed as `localhost:5000/<repository>:<commit-sha>` and listed on the Registry page |
    | `REDLAUNCH_DOCKERFILE` | Dockerfile path in the repository (optional, defaults to `Dockerfile`) | Dockerfile passed to `docker build --file` |
    | `REDLAUNCH_BUILD_CONTEXT` | Build context in the repository (optional, defaults to `.`) | Build context directory passed to `docker build` |
    | `DEPLOY_SSH_PORT` | `22` (optional, defaults to `22`) | SSH port used for the `redlaunch` user connection |

   Secrets (paste complete values, including `BEGIN`/`END` lines for the key):

   | Secret | Value | Description |
   | --- | --- | --- |
   | `SSH_PRIVATE_KEY` | Complete `redlaunch` private key | Authenticates as the `redlaunch` user to open the registry SSH tunnel |
   | `SSH_KNOWN_HOSTS` | System host-key line for `SERVER_HOST` | Verifies the VPS host key with `StrictHostKeyChecking=yes` |

### 1.2 Reusable push workflow

```yaml
name: Build and push Redlaunch image

on:
  push:
    branches:
      - main
  workflow_dispatch:

permissions:
  contents: read

env:
  IMAGE_REPOSITORY: ${{ vars.REDLAUNCH_IMAGE_REPOSITORY }}
  DOCKERFILE: ${{ vars.REDLAUNCH_DOCKERFILE }}
  BUILD_CONTEXT: ${{ vars.REDLAUNCH_BUILD_CONTEXT }}

jobs:
  build-and-push:
    runs-on: ubuntu-latest
    steps:
      - name: Check out repository
        uses: actions/checkout@v4

      - name: Build image
        run: |
          set -euo pipefail
          DOCKERFILE="${DOCKERFILE:-Dockerfile}"
          BUILD_CONTEXT="${BUILD_CONTEXT:-.}"
          test -n "$IMAGE_REPOSITORY" || { echo "REDLAUNCH_IMAGE_REPOSITORY is not set" >&2; exit 1; }
          IMAGE="localhost:5000/${IMAGE_REPOSITORY}:${GITHUB_SHA}"
          echo "IMAGE=$IMAGE" >> "$GITHUB_ENV"
          docker build --file "$DOCKERFILE" --tag "$IMAGE" "$BUILD_CONTEXT"

      - name: Open SSH tunnel and push image
        shell: bash
        env:
          SERVER_HOST: ${{ vars.SERVER_HOST }}
          DEPLOY_SSH_PORT: ${{ vars.DEPLOY_SSH_PORT }}
          SSH_PRIVATE_KEY: ${{ secrets.SSH_PRIVATE_KEY }}
          SSH_KNOWN_HOSTS: ${{ secrets.SSH_KNOWN_HOSTS }}
        run: |
          set -euo pipefail

          DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"

          test -n "$SERVER_HOST" || { echo "SERVER_HOST is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "SSH_PRIVATE_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "SSH_KNOWN_HOSTS is not set" >&2; exit 1; }
          [[ "$DEPLOY_SSH_PORT" =~ ^[0-9]+$ ]] && (( DEPLOY_SSH_PORT >= 1 && DEPLOY_SSH_PORT <= 65535 )) || { echo "DEPLOY_SSH_PORT is invalid" >&2; exit 1; }
          [[ "$SERVER_HOST" =~ ^([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|[0-9A-Fa-f:]+)$ ]] || { echo "SERVER_HOST contains invalid characters" >&2; exit 1; }

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
            -p "$DEPLOY_SSH_PORT" \
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
            "redlaunch@$SERVER_HOST" &
          tunnel_pid=$!

          for attempt in $(seq 1 30); do
            if ! kill -0 "$tunnel_pid" 2>/dev/null; then
              echo "SSH tunnel exited before it was ready" >&2
              exit 1
            fi
            if curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:5000/v2/ >/dev/null; then
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

Notes:

- The pushed reference is always `localhost:5000/<repository>:<commit-sha>`.
  The workflow does not restart any service. After a successful run, set the
  Redlaunch service to that exact SHA image when you are ready to run it.
  Pushed tags are listed on the **Registry** page.
- Keep `StrictHostKeyChecking=yes` and `ExitOnForwardFailure=yes`. Do not
  work around a host-key error by disabling checking.
- The tunnel is closed by the `trap` after `docker push` completes. Key files
  exist only under `$RUNNER_TEMP`.
- Defaults used by the workflow: SSH user `redlaunch`, SSH port `22`,
  Dockerfile `Dockerfile`, build context `.`. `SERVER_HOST` and
  `REDLAUNCH_IMAGE_REPOSITORY` are always required.

### 1.3 Verify and rotate

- Run the workflow manually once from the repository's **Actions** tab.
- Open Redlaunch's **Registry** page and confirm
  `myapp/web:<commit-sha>` appears.
- To rotate, create a replacement key in **Settings → SSH keys**, update the
  GitHub secrets, then revoke the old key. To revoke, use **Revoke key** in
  Redlaunch, then remove the GitHub variables/secrets separately.

---

## Recipe 2: Trigger a Drizzle migration from GitHub Actions

Use this when your application needs a database migration (for example
Drizzle `migrate`) to run server-side as a one-off container.

Redlaunch runs the migration with `docker compose run --rm` semantics: it
uses the service's configured image and command with the managed `vars.env` /
`secrets.env`, removes the container afterwards, and keeps secrets on the
server. The caller never sees secret values.

### 2.1 Create the `migrate` service in Redlaunch

Create a second **Application** service next to your long-lived `app`
service. It uses the same image repository but a different entrypoint or
command.

Example settings for a Node.js + Drizzle application:

| Field | `app` service | `migrate` service |
| --- | --- | --- |
| Service name | `app` | `migrate` |
| Image | `myapp/web` with **Use Docker Registry** off → stored as `localhost:5000/myapp/web:...` | Same image reference as `app` |
| Automatically start container | On (after the image exists) | Off |
| Custom entrypoint command | Empty (image default, for example a web server) | `npm run db:migrate` (or `pnpm db:migrate`, `bun run db:migrate`) |
| Depends on | `db` as needed | `db` with `healthy` condition |
| Restart policy | `unless-stopped` | `no` |
| Ports | As needed | None |
| Volumes | As needed | Same data access as `app` if migrations need files |

How the entrypoint maps to your project:

- `package.json` should expose a non-interactive migration script, for
  example:

  ```json
  {
    "scripts": {
      "db:migrate": "drizzle-kit migrate"
    }
  }
  ```

- `DATABASE_URL` (or `POSTGRES_*` values) comes from the managed environment
  files, not from GitHub. Set it under the application's **Variables** /
  **Secrets** tabs. The one-off run loads `vars.env` and `secrets.env`
  automatically.
- Keep migrations idempotent. Drizzle's journal-based `migrate` is safe to
  retry; a retried workflow dispatches the migration again.
- Test locally first with Redlaunch's **Run once** action on the `migrate`
  service before wiring CI.

Important: the migration runs whatever image is currently configured for the
`migrate` service in `compose.yml`. If you just pushed a new commit SHA, set
both `app` and `migrate` to that SHA before migrating, or use the deploy
recipes below that push, migrate, then restart in order. Do not expect the
migration to implicitly use the runner's freshly built image.

### 2.2 Choose a trigger method

| Method | Access | Best for | Secrets leave the server? |
| --- | --- | --- | --- |
| A. API token (recommended) | HTTPS Bearer token, scoped to one application | Scheduled or post-deploy migrations, least privilege | No |
| B. SSH shell key | `redlaunch@server:22` key + `docker exec` | Full control, existing SSH automation, no HTTPS exposure | No (commands run server-side) |

Both methods keep `vars.env` / `secrets.env` on the server. The API token
additionally cannot read environment files, run arbitrary commands (the
command comes from the admin-managed `compose.yml`), reach other
applications, or open a shell.

### 2.3 Method A: API token (recommended)

1. Open **Settings → API tokens → Create API token**.
2. Enter a display name (for example `myapp migrate via GHA`), pick the
   application, and pick the shortest expiry the automation tolerates.
3. Copy the token from the one-time dialog. Redlaunch stores only its
   SHA-256 hash.

In GitHub, create:

| Variable | Example | Description |
| --- | --- | --- |
| `REDLAUNCH_URL` | `https://redlaunch.example.com` (managed HTTPS) | Redlaunch base URL called to dispatch the migration run and poll its `status_url` |

| Secret | Value | Description |
| --- | --- | --- |
| `REDLAUNCH_RUN_TOKEN` | The one-time token value | Bearer token authorizing the migration dispatch and status polls, scoped to one application |

Set these as workflow or job environment values:

```text
APPLICATION_ID: "7"
SERVICE_NAME: ${{ vars.MIGRATE_SERVICE_NAME }}  # optional, defaults to "migrate"
```

Find the numeric application ID in the Redlaunch URL
(`/applications/<id>`) or inventory. `SERVICE_NAME` is the exact Compose
service name; when the `MIGRATE_SERVICE_NAME` variable is unset or empty the
workflow below falls back to `migrate`.

If Redlaunch is SSH-only (loopback bind), GitHub cannot reach the API
directly. Either publish Redlaunch through the managed Caddy proxy
(`MANAGEMENT_ACCESS_MODE=managed-https`, Settings → Public access) and use
the HTTPS URL, or forward the port over SSH with an existing Settings SSH
key and use `http://127.0.0.1:8080`. Never send Bearer tokens over plain
HTTP across untrusted networks.

Reusable workflow:

```yaml
name: Migrate DB

on:
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: migrate-myapp
  cancel-in-progress: false

jobs:
  migrate:
    runs-on: ubuntu-latest
    env:
      REDLAUNCH_URL: ${{ vars.REDLAUNCH_URL }}
      APPLICATION_ID: "7"
      SERVICE_NAME: ${{ vars.MIGRATE_SERVICE_NAME }}
    steps:
      - name: Dispatch migration run
        id: dispatch
        shell: bash
        env:
          REDLAUNCH_RUN_TOKEN: ${{ secrets.REDLAUNCH_RUN_TOKEN }}
        run: |
          set -euo pipefail
          SERVICE_NAME="${SERVICE_NAME:-migrate}"
          response=$(curl --fail --silent --show-error --max-time 30 -X POST \
            -H "Authorization: Bearer $REDLAUNCH_RUN_TOKEN" \
            "$REDLAUNCH_URL/api/v1/applications/$APPLICATION_ID/services/$SERVICE_NAME/run")
          echo "$response"
          status_url=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status_url"])')
          echo "status_url=$status_url" >> "$GITHUB_OUTPUT"

      - name: Wait for migration to finish
        shell: bash
        env:
          REDLAUNCH_RUN_TOKEN: ${{ secrets.REDLAUNCH_RUN_TOKEN }}
        run: |
          set -euo pipefail
          for _ in $(seq 1 60); do
            response=$(curl --fail --silent --show-error --max-time 30 \
              -H "Authorization: Bearer $REDLAUNCH_RUN_TOKEN" \
              "$REDLAUNCH_URL${{ steps.dispatch.outputs.status_url }}")
            echo "$response"
            status=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
            case "$status" in
              complete) exit 0 ;;
              failed) exit 1 ;;
            esac
            sleep 10
          done
          echo "migration did not finish in time" >&2
          exit 1
```

Notes:

- The dispatch returns `202 Accepted` with a pollable `status_url`. Runs
  execute asynchronously so long migrations never hold the dispatch
  connection.
- Poll until `complete` or `failed`. Failures include the redacted Compose
  tail in `detail`.
- The `concurrency` group prevents duplicate manual dispatches from queueing.
  Redlaunch also serializes runs per application and returns the
  already-running job on duplicate dispatch.
- Rotate by creating a replacement token, updating the GitHub secret, then
  revoking the old token. Revocation is immediate.

### 2.4 Method B: SSH shell key

Use this when you already automate over SSH or cannot reach the API over
HTTPS.

1. Open **Settings → SSH keys → Create SSH key** (for example
   `myapp migrate via GHA`).
2. Copy the private key and the system host-key entry for port `22`.
3. Verify the fingerprint out of band:

   ```sh
   ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
   ```

4. In GitHub, create:

    | Variable | Example | Description |
    | --- | --- | --- |
    | `SERVER_HOST` | `203.0.113.10` (required) | SSH target that runs the migration command server-side |
    | `REDLAUNCH_APP_DIR` | `/opt/redlaunch/projects/applications/myapp` (required) | Managed application directory where `docker compose run --rm` executes |
    | `DEPLOY_SSH_PORT` | `22` (optional, defaults to `22`) | SSH port used for the `redlaunch` user connection |
    | `MIGRATE_SERVICE_NAME` | `migrate` (optional, defaults to `migrate`) | One-off Compose service executed with `docker compose run --rm` |

   | Secret | Value | Description |
   | --- | --- | --- |
   | `SSH_PRIVATE_KEY` | Complete `redlaunch` private key | Authenticates as the `redlaunch` user to run the migration over SSH |
   | `SSH_KNOWN_HOSTS` | System host-key line with `SERVER_HOST` as the host field | Verifies the VPS host key with `StrictHostKeyChecking=yes` |

5. Confirm prerequisites on the server (once, as an admin):

   ```sh
   id redlaunch
   sudo grep "redlaunch-ssh-key:" /home/redlaunch/.ssh/authorized_keys
   ssh -i ~/.ssh/redlaunch-key redlaunch@your-server 'docker exec redbolt-redlaunch true'
   ```

   `id redlaunch` must list the `docker` group. Reconnect SSH sessions after
   group changes.

Reusable workflow step (add to your deploy or migration workflow):

```yaml
permissions:
  contents: read

concurrency:
  group: migrate-myapp
  cancel-in-progress: false

jobs:
  migrate:
    runs-on: ubuntu-latest
    env:
      SERVER_HOST: ${{ vars.SERVER_HOST }}
      DEPLOY_DIR: ${{ vars.REDLAUNCH_APP_DIR }}
      DEPLOY_SSH_PORT: ${{ vars.DEPLOY_SSH_PORT }}
      MIGRATE_SERVICE: ${{ vars.MIGRATE_SERVICE_NAME }}
    steps:
      - name: Run Drizzle migration on server
        shell: bash
        env:
          SSH_PRIVATE_KEY: ${{ secrets.SSH_PRIVATE_KEY }}
          SSH_KNOWN_HOSTS: ${{ secrets.SSH_KNOWN_HOSTS }}
        run: |
          set -euo pipefail
          DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
          MIGRATE_SERVICE="${MIGRATE_SERVICE:-migrate}"
          test -n "$SERVER_HOST" || { echo "SERVER_HOST is not set" >&2; exit 1; }
          test -n "$DEPLOY_DIR" || { echo "REDLAUNCH_APP_DIR is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "SSH_PRIVATE_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "SSH_KNOWN_HOSTS is not set" >&2; exit 1; }

          ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
          install -d -m 700 "$ssh_dir"
          printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/id_ed25519"
          printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
          chmod 600 "$ssh_dir/id_ed25519"
          chmod 644 "$ssh_dir/known_hosts"

          DEPLOY_DIR="$DEPLOY_DIR" MIGRATE_SERVICE="$MIGRATE_SERVICE" \
          ssh -p "$DEPLOY_SSH_PORT" -i "$ssh_dir/id_ed25519" \
            -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 \
            -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$ssh_dir/known_hosts" \
            redlaunch@"$SERVER_HOST" 'bash -s' <<'REMOTE_SCRIPT'
          set -euo pipefail
          DEPLOY_DIR="${DEPLOY_DIR:?}"
          MIGRATE_SERVICE="${MIGRATE_SERVICE:-migrate}"
          # DEPLOY_DIR must be the absolute managed application directory.
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
          env_args=()
          if docker exec redbolt-redlaunch test -f "$DEPLOY_DIR/.env"; then
            env_args+=(--env-file .env)
          fi
          env_args+=(--env-file vars.env --env-file secrets.env)
          docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" run --rm "$MIGRATE_SERVICE"
          REMOTE_SCRIPT
```

The remote snippet receives `DEPLOY_DIR` and `MIGRATE_SERVICE` as fixed
remote values via the `VAR="..."` prefix on the `ssh` invocation (SSH does
not forward the local environment automatically). The pattern above keeps the
explicit `--project-name` and `--env-file` files on every invocation and runs
Compose inside the manager container where the
`0600` `vars.env` / `secrets.env` files are readable. A bare
`docker compose run --rm migrate` in the application directory creates a
duplicate project, fails with container-name and volume conflicts, and must
not be used.

---

## Recipe 3: Deploy an application from GitHub Actions via the container registry

Use this for the standard pipeline: build once in CI, push immutably to the
local registry, migrate, then restart the server-side service.

```text
GitHub runner
  1. docker build -t localhost:5000/myapp/web:<sha> -t localhost:5000/myapp/web:latest
  2. push via redlaunch@:22 tunnel to 127.0.0.1:5000
  3. optionally trigger migrate service (Recipe 2)
  4. ssh as redlaunch@:22 -> docker compose pull + up -d in managed identity
```

### 3.1 Redlaunch setup

1. Create the application (**Applications → Create application**), for
   example folder `myapp`.
2. Add supporting services first if needed (for example PostgreSQL `db` with
   `pg_isready` healthcheck, Redis `redis`). Set their credentials; they
   create `db.vars.env` / `db.secrets.env` in addition to the project files.
3. Create the long-lived service (**Services → Create service →
   Application**):

   - Service name: `app`
   - Image name: `myapp/web` (repository only; the tag is managed by the
     pipeline)
   - **Use Docker Registry switch: off (unchecked).** Unchecked adds the
     `localhost:5000/` prefix; checked uses the reference exactly as entered
     (for external registries such as `ghcr.io/...`). For this recipe the
     switch must stay off.
   - **Automatically start container:** off until the first image is pushed,
     then on.
   - Entrypoint: empty unless the image needs an override.
   - `depends_on`: `db` with `healthy` when the app needs the database.
   - Ports, volumes, healthcheck, restart policy as needed.
   - Routing: add domains and routes only after the service runs.

   The stored image becomes `localhost:5000/myapp/web:latest` initially.
   The pipeline pushes both the SHA tag and a moving tag (`latest`); the
   service tracks the moving tag for automatic deploys. Keep the SHA tag for
   traceability and rollback (see Registry page).

4. Create the `migrate` service from Recipe 2 if the application needs
   migrations. It must use the same repository (`myapp/web`, switch off).
5. Create one Settings SSH key for push and deploy
   (`Settings → SSH keys`, for example `myapp push and deploy via GHA`).

### 3.2 GitHub setup

You need one credential set for both push and deploy:

GitHub variables/secrets:

| Variable | Example | Description |
| --- | --- | --- |
| `SERVER_HOST` | `203.0.113.10` (required) | SSH target for the registry tunnel and the deploy connection (`redlaunch@SERVER_HOST`) |
| `REDLAUNCH_APP_DIR` | `/opt/redlaunch/projects/applications/myapp` (required) | Managed application directory where `docker compose pull` and `up -d` run |
| `REDLAUNCH_IMAGE_REPOSITORY` | `myapp/web` (required) | Repository built and pushed as `localhost:5000/<repository>:<sha>` and `:latest` tags |
| `REDLAUNCH_DOCKERFILE` | `Dockerfile` (optional, defaults to `Dockerfile`) | Dockerfile passed to `docker build --file` |
| `REDLAUNCH_BUILD_CONTEXT` | `.` (optional, defaults to `.`) | Build context directory passed to `docker build` |
| `DEPLOY_SERVICE` | `app` (optional, defaults to `app`) | Long-lived Compose service restarted with `pull` and `up -d` |
| `DEPLOY_SSH_PORT` | `22` (optional, defaults to `22`) | SSH port used for the `redlaunch` user connection |
| `REDLAUNCH_URL` | `https://redlaunch.example.com` (optional, only for API migration) | Redlaunch base URL called to dispatch the optional migration run |
| `APPLICATION_ID` | `"7"` (optional, only for API migration) | Numeric application selected in the migration API path (`/api/v1/applications/<id>/...`) |
| `MIGRATE_SERVICE_NAME` | `migrate` (optional, defaults to `migrate`, only for API migration) | One-off Compose service executed by the optional migration run |

| Secret | Value | Description |
| --- | --- | --- |
| `SSH_PRIVATE_KEY` | Complete `redlaunch` private key | Authenticates as the `redlaunch` user for the registry tunnel and deploy SSH session |
| `SSH_KNOWN_HOSTS` | System host-key line for `SERVER_HOST` | Verifies the VPS host key with `StrictHostKeyChecking=yes` |
| `REDLAUNCH_RUN_TOKEN` | API token value (optional, only for API migration) | Bearer token authorizing the optional migration dispatch and status polls |

`REDLAUNCH_APP_DIR` must be the absolute managed application directory. It
resolves both on the host and inside the manager container because the
projects root is mounted at the identical path. Adjust `/opt/redlaunch` when
the installation lives elsewhere.

### 3.3 Reusable deploy-via-registry workflow

This workflow pushes the SHA and moving tag through the Settings SSH tunnel,
then pulls and restarts only the `app` service over the same key.

```yaml
name: Deploy via registry

on:
  push:
    branches:
      - main
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: deploy-myapp
  cancel-in-progress: false

env:
  IMAGE_REPOSITORY: ${{ vars.REDLAUNCH_IMAGE_REPOSITORY }}
  DOCKERFILE: ${{ vars.REDLAUNCH_DOCKERFILE }}
  BUILD_CONTEXT: ${{ vars.REDLAUNCH_BUILD_CONTEXT }}
  DEPLOY_HOST: ${{ vars.SERVER_HOST }}
  DEPLOY_DIR: ${{ vars.REDLAUNCH_APP_DIR }}
  DEPLOY_SERVICE: ${{ vars.DEPLOY_SERVICE }}
  DEPLOY_SSH_PORT: ${{ vars.DEPLOY_SSH_PORT }}

jobs:
  build-push-deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Check out repository
        uses: actions/checkout@v4

      - name: Build image
        run: |
          set -euo pipefail
          DOCKERFILE="${DOCKERFILE:-Dockerfile}"
          BUILD_CONTEXT="${BUILD_CONTEXT:-.}"
          test -n "$IMAGE_REPOSITORY" || { echo "REDLAUNCH_IMAGE_REPOSITORY is not set" >&2; exit 1; }
          IMAGE_SHA="localhost:5000/${IMAGE_REPOSITORY}:${GITHUB_SHA}"
          IMAGE_LATEST="localhost:5000/${IMAGE_REPOSITORY}:latest"
          echo "IMAGE_SHA=$IMAGE_SHA" >> "$GITHUB_ENV"
          echo "IMAGE_LATEST=$IMAGE_LATEST" >> "$GITHUB_ENV"
          docker build --file "$DOCKERFILE" \
            --tag "$IMAGE_SHA" --tag "$IMAGE_LATEST" \
            "$BUILD_CONTEXT"

      - name: Push image through registry tunnel
        shell: bash
        env:
          SSH_PRIVATE_KEY: ${{ secrets.SSH_PRIVATE_KEY }}
          SSH_KNOWN_HOSTS: ${{ secrets.SSH_KNOWN_HOSTS }}
        run: |
          set -euo pipefail
          DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
          test -n "$DEPLOY_HOST" || { echo "SERVER_HOST is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "SSH_PRIVATE_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "SSH_KNOWN_HOSTS is not set" >&2; exit 1; }
          [[ "$DEPLOY_SSH_PORT" =~ ^[0-9]+$ ]] && (( DEPLOY_SSH_PORT >= 1 && DEPLOY_SSH_PORT <= 65535 )) || { echo "DEPLOY_SSH_PORT is invalid" >&2; exit 1; }
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

          ssh -N -T -p "$DEPLOY_SSH_PORT" -i "$ssh_dir/id_ed25519" \
            -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 \
            -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=yes \
            -o UserKnownHostsFile="$ssh_dir/known_hosts" \
            -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
            -L "127.0.0.1:5000:127.0.0.1:5000" \
            "redlaunch@$DEPLOY_HOST" &
          tunnel_pid=$!

          for attempt in $(seq 1 30); do
            if ! kill -0 "$tunnel_pid" 2>/dev/null; then
              echo "SSH tunnel exited before it was ready" >&2
              exit 1
            fi
            if curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 5 http://127.0.0.1:5000/v2/ >/dev/null; then
              break
            fi
            if [ "$attempt" -eq 30 ]; then
              echo "The registry did not respond through the SSH tunnel" >&2
              exit 1
            fi
            sleep 1
          done

          docker push "$IMAGE_SHA"
          docker push "$IMAGE_LATEST"

      - name: Pull and restart app service
        shell: bash
        env:
          SSH_PRIVATE_KEY: ${{ secrets.SSH_PRIVATE_KEY }}
          SSH_KNOWN_HOSTS: ${{ secrets.SSH_KNOWN_HOSTS }}
        run: |
          set -euo pipefail
          DEPLOY_SERVICE="${DEPLOY_SERVICE:-app}"
          DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
          test -n "$DEPLOY_HOST" || { echo "SERVER_HOST is not set" >&2; exit 1; }
          test -n "$DEPLOY_DIR" || { echo "REDLAUNCH_APP_DIR is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "SSH_PRIVATE_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "SSH_KNOWN_HOSTS is not set" >&2; exit 1; }

          ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
          install -d -m 700 "$ssh_dir"
          printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/deploy_ed25519"
          printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/deploy_known_hosts"
          chmod 600 "$ssh_dir/deploy_ed25519"
          chmod 644 "$ssh_dir/deploy_known_hosts"

          # DEPLOY_DIR and DEPLOY_SERVICE travel as fixed remote values.
          # Do not interpolate untrusted input into the remote script.
          DEPLOY_DIR="$DEPLOY_DIR" DEPLOY_SERVICE="$DEPLOY_SERVICE" \
          ssh -p "$DEPLOY_SSH_PORT" -i "$ssh_dir/deploy_ed25519" \
            -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 \
            -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$ssh_dir/deploy_known_hosts" \
            redlaunch@"$DEPLOY_HOST" 'bash -s' <<'REMOTE_SCRIPT'
          set -euo pipefail
          DEPLOY_DIR="${DEPLOY_DIR:?}"
          DEPLOY_SERVICE="${DEPLOY_SERVICE:?}"
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
          env_args=()
          if docker exec redbolt-redlaunch test -f "$DEPLOY_DIR/.env"; then
            env_args+=(--env-file .env)
          fi
          env_args+=(--env-file vars.env --env-file secrets.env)
          docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" pull "$DEPLOY_SERVICE"
          docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" up -d "$DEPLOY_SERVICE"
          REMOTE_SCRIPT
```

To include migrations, insert the API-token dispatch/poll steps from Recipe
2 between the push and the `Pull and restart` step, or run the SSH migration
snippet in the same remote session before `up -d`. Migrate before restarting
the long-lived service so the new code never runs against an old schema.

Verify:

- Workflow pushes two tags; **Registry** page shows the new SHA.
- Application **Services** tab shows `app` running with the new image.
- `docker compose ... pull` output confirms the moving tag was refreshed.
- Domain route serves the new version.

Rollback: set the `app` (and `migrate`, if any) image back to the previous
SHA in Redlaunch's service editor, then re-run only the `Pull and restart`
logic (or click **Restart** in the UI). The immutable SHA tags make the
previous image addressable.

---

## Recipe 4: Deploy an application from GitHub Actions by copying the image directly

Use this when you do not want to run the local registry, the image is small
enough to transfer over SSH, or you need a registry-independent path (for
example bootstrapping, air-gapped reviews, or debugging a registry outage).

Pros and cons vs. Recipe 3:

|  | Registry (Recipe 3) | Direct copy (this recipe) |
| --- | --- | --- |
| Server prerequisites | Registry + shell key (`:22`) | Shell key (`:22`) only |
| Transfer | `docker push` through tunnel (efficient layer reuse) | `docker save` → SSH → `docker load` (full image each time) |
| Image naming | Must be `localhost:5000/...` (switch off) | Keep `localhost:5000/...` (switch off) for consistency, or any exact reference with switch on |
| Best for | Frequent deploys, larger images, shared base layers | Small images, infrequent deploys, no-registry setups |

This recipe keeps the same `localhost:5000/myapp/web` naming and the switch
off, so the Compose file is identical to Recipe 3 and only the transport
changes. If you prefer a registry-free name (for example `myapp:abc123`),
turn **Use Docker Registry** on and use that exact reference in both the
service and the workflow's `IMAGE` variables.

### 4.1 Redlaunch and GitHub setup

Redlaunch setup is identical to Recipe 3, Section 3.1, except the registry is
optional. You still need:

- Application `myapp` with service `app` (`myapp/web`, switch off,
  autostart as appropriate).
- Optional `migrate` service from Recipe 2.
- One shell key (`Settings → SSH keys`) for `redlaunch@server:22`.

GitHub variables/secrets:

| Variable | Example | Description |
| --- | --- | --- |
| `SERVER_HOST` | `203.0.113.10` (required) | SSH target receiving the copied image (`docker load`) and running `up -d` |
| `REDLAUNCH_APP_DIR` | `/opt/redlaunch/projects/applications/myapp` (required) | Managed application directory where `docker compose up -d` runs |
| `REDLAUNCH_IMAGE_REPOSITORY` | `myapp/web` (required) | Repository built as `localhost:5000/<repository>:<sha>` and `:latest` tags for `docker save` |
| `REDLAUNCH_DOCKERFILE` | `Dockerfile` (optional, defaults to `Dockerfile`) | Dockerfile passed to `docker build --file` |
| `REDLAUNCH_BUILD_CONTEXT` | `.` (optional, defaults to `.`) | Build context directory passed to `docker build` |
| `DEPLOY_SERVICE` | `app` (optional, defaults to `app`) | Long-lived Compose service restarted with `up -d` after `docker load` |
| `DEPLOY_SSH_PORT` | `22` (optional, defaults to `22`) | SSH port used for the `redlaunch` user connection |

| Secret | Value | Description |
| --- | --- | --- |
| `SSH_PRIVATE_KEY` | Complete `redlaunch` private key | Authenticates as the `redlaunch` user for the image copy and deploy SSH session |
| `SSH_KNOWN_HOSTS` | System host-key line for `SERVER_HOST` | Verifies the VPS host key with `StrictHostKeyChecking=yes` |

### 4.2 Reusable copy-and-deploy workflow (streaming)

Streaming avoids a temporary tarball on the runner and is sufficient for most
images. The image is piped directly into the server's Docker daemon, then the
managed Compose project is pulled up by service name (no registry pull
needed because the image is already local).

```yaml
name: Deploy via image copy

on:
  push:
    branches:
      - main
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: deploy-copy-myapp
  cancel-in-progress: false

env:
  IMAGE_REPOSITORY: ${{ vars.REDLAUNCH_IMAGE_REPOSITORY }}
  DOCKERFILE: ${{ vars.REDLAUNCH_DOCKERFILE }}
  BUILD_CONTEXT: ${{ vars.REDLAUNCH_BUILD_CONTEXT }}
  DEPLOY_HOST: ${{ vars.SERVER_HOST }}
  DEPLOY_DIR: ${{ vars.REDLAUNCH_APP_DIR }}
  DEPLOY_SERVICE: ${{ vars.DEPLOY_SERVICE }}
  DEPLOY_SSH_PORT: ${{ vars.DEPLOY_SSH_PORT }}

jobs:
  build-copy-deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Check out repository
        uses: actions/checkout@v4

      - name: Build image
        run: |
          set -euo pipefail
          DOCKERFILE="${DOCKERFILE:-Dockerfile}"
          BUILD_CONTEXT="${BUILD_CONTEXT:-.}"
          test -n "$IMAGE_REPOSITORY" || { echo "REDLAUNCH_IMAGE_REPOSITORY is not set" >&2; exit 1; }
          IMAGE_SHA="localhost:5000/${IMAGE_REPOSITORY}:${GITHUB_SHA}"
          IMAGE_LATEST="localhost:5000/${IMAGE_REPOSITORY}:latest"
          echo "IMAGE_SHA=$IMAGE_SHA" >> "$GITHUB_ENV"
          echo "IMAGE_LATEST=$IMAGE_LATEST" >> "$GITHUB_ENV"
          docker build --file "$DOCKERFILE" \
            --tag "$IMAGE_SHA" --tag "$IMAGE_LATEST" \
            "$BUILD_CONTEXT"

      - name: Copy image to server and restart app service
        shell: bash
        env:
          SSH_PRIVATE_KEY: ${{ secrets.SSH_PRIVATE_KEY }}
          SSH_KNOWN_HOSTS: ${{ secrets.SSH_KNOWN_HOSTS }}
        run: |
          set -euo pipefail
          DEPLOY_SERVICE="${DEPLOY_SERVICE:-app}"
          DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
          test -n "$DEPLOY_HOST" || { echo "SERVER_HOST is not set" >&2; exit 1; }
          test -n "$DEPLOY_DIR" || { echo "REDLAUNCH_APP_DIR is not set" >&2; exit 1; }
          test -n "$SSH_PRIVATE_KEY" || { echo "SSH_PRIVATE_KEY is not set" >&2; exit 1; }
          test -n "$SSH_KNOWN_HOSTS" || { echo "SSH_KNOWN_HOSTS is not set" >&2; exit 1; }
          test -n "$IMAGE_SHA" || { echo "built image reference is missing" >&2; exit 1; }
          test -n "$IMAGE_LATEST" || { echo "built image reference is missing" >&2; exit 1; }

          ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
          install -d -m 700 "$ssh_dir"
          printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/deploy_ed25519"
          printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/deploy_known_hosts"
          chmod 600 "$ssh_dir/deploy_ed25519"
          chmod 644 "$ssh_dir/deploy_known_hosts"

          ssh_opts=(
            -p "$DEPLOY_SSH_PORT" -i "$ssh_dir/deploy_ed25519"
            -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15
            -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$ssh_dir/deploy_known_hosts"
            -o ServerAliveInterval=30 -o ServerAliveCountMax=3
          )

          # Stream the image into the host daemon. The tag includes
          # localhost:5000/ so it matches the managed service image.
          docker save "$IMAGE_SHA" "$IMAGE_LATEST" \
            | ssh "${ssh_opts[@]}" redlaunch@"$DEPLOY_HOST" 'docker load'

          DEPLOY_DIR="$DEPLOY_DIR" DEPLOY_SERVICE="$DEPLOY_SERVICE" \
          ssh "${ssh_opts[@]}" redlaunch@"$DEPLOY_HOST" 'bash -s' <<'REMOTE_SCRIPT'
          set -euo pipefail
          DEPLOY_DIR="${DEPLOY_DIR:?}"
          DEPLOY_SERVICE="${DEPLOY_SERVICE:?}"
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
          env_args=()
          if docker exec redbolt-redlaunch test -f "$DEPLOY_DIR/.env"; then
            env_args+=(--env-file .env)
          fi
          env_args+=(--env-file vars.env --env-file secrets.env)
          # No pull here: the image was just loaded locally.
          docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" up -d "$DEPLOY_SERVICE"
          REMOTE_SCRIPT
```

Variant for large images or flaky networks: save to a file, copy with
`scp`, then load. This allows resuming the copy without rebuilding:

```sh
DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
docker save "$IMAGE_SHA" "$IMAGE_LATEST" -o /tmp/myapp.tar
scp -P "$DEPLOY_SSH_PORT" -i "$ssh_dir/deploy_ed25519" \
  -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o UserKnownHostsFile="$ssh_dir/deploy_known_hosts" \
  /tmp/myapp.tar redlaunch@"$DEPLOY_HOST":/tmp/myapp.tar
ssh "${ssh_opts[@]}" redlaunch@"$DEPLOY_HOST" 'docker load -i /tmp/myapp.tar && rm /tmp/myapp.tar'
```

Then run the same `up -d` remote snippet as above.

Notes and limits:

- Transfer size matters. `docker save` sends every layer in the tagged
  images. Keep images small (multi-stage builds, `.dockerignore`) and prefer
  the registry recipe for large or frequent deploys where layer reuse helps.
- The remote `up -d` intentionally omits `pull`: the image is already present
  from `docker load`. Adding `pull` for a `localhost:5000/...` reference
  without a running registry would fail.
- Prune old images cautiously on the server (`docker image prune`) and never
  as part of the deploy itself; a failed `load` followed by a prune could
  remove the last good image. Verify the new service is healthy first.
- Insert the Recipe 2 migration before `up -d` when needed, same as in
  Recipe 3.

Verify:

- `docker load` output lists `Loaded image: localhost:5000/myapp/web:<sha>`.
- Redlaunch shows the service restarted; domain serves the new version.
- Rollback is the same as Recipe 3: point the service at the previous SHA
  (still present as a local image unless pruned) and `up -d` again.

---

## Shared utilities

### Managed Compose identity (required for every SSH `up` / `run`)

Never run a bare `docker compose` command inside
`projects/applications/<name>/`. Redlaunch derives the project name from the
absolute projects root, scope, and folder
(`--project-name redlaunch-app-<name>-<hash>`) and loads
`--env-file .env --env-file vars.env --env-file secrets.env`. A bare command
defaults to the directory basename, creates a second project, and fails with
`already exists but was created for project ...`, a new `<name>_default`
network, and `Conflict. The container name ... is already in use`.

Always resolve the identity on the server and run Compose inside the manager
container, where the `0600` environment files are readable as root:

```sh
set -euo pipefail
DEPLOY_DIR=/opt/redlaunch/projects/applications/myapp
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
env_args=()
if docker exec redbolt-redlaunch test -f "$DEPLOY_DIR/.env"; then
  env_args+=(--env-file .env)
fi
env_args+=(--env-file vars.env --env-file secrets.env)
# Examples:
docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" pull app
docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" up -d app
docker exec -w "$DEPLOY_DIR" redbolt-redlaunch docker compose --project-name "$project_name" "${env_args[@]}" -f "$compose_file" run --rm migrate
```

Keep the explicit `--project-name` on every manual invocation (`up`, `run`,
`exec`, `logs`), whether run on the host or through `docker exec`.

### CI SSH setup snippet

```sh
DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
ssh_dir="$RUNNER_TEMP/redlaunch-ssh"
install -d -m 700 "$ssh_dir"
printf '%s\n' "$SSH_PRIVATE_KEY" > "$ssh_dir/id_ed25519"
printf '%s\n' "$SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
chmod 600 "$ssh_dir/id_ed25519"
chmod 644 "$ssh_dir/known_hosts"
ssh -p "$DEPLOY_SSH_PORT" -i "$ssh_dir/id_ed25519" \
  -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 \
  -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$ssh_dir/known_hosts" \
  redlaunch@"$SERVER_HOST" 'bash -s' <<'REMOTE_SCRIPT'
set -euo pipefail
# remote commands here
REMOTE_SCRIPT
```

For the registry tunnel, add `ExitOnForwardFailure=yes` and
`-L "127.0.0.1:5000:127.0.0.1:5000"` as shown in Recipe 1.

## Security checklist

- Firewall: TCP `22` for administration and deploys; TCP `80`/`443` only
  when Caddy serves public traffic. The registry itself stays on loopback
  plus the private Docker network.
- Least privilege: one shell key per automation purpose; one API token per
  application with the shortest workable expiry. Revoke separately in
  Redlaunch and in GitHub.
- The `redlaunch` shell user is in the `docker` group, which is
  root-equivalent. Treat SSH keys as full host administrator credentials.
- The registry has no authentication. Anyone with SSH access can push to any
  repository path. Add registry authentication before treating separate
  repositories as mutually untrusted.
- Never expose the Docker socket or Docker API over HTTP. Use the dedicated
  Compose service layer (`docker exec redbolt-redlaunch ...`).
- Never print secrets to logs, include them in error messages, or expose
  environment-file contents through HTTP. Redlaunch masks secret values in
  the UI and stores only API-token hashes.

## Troubleshooting quick reference

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `SERVER_HOST is not set` | Repository variable missing at repository scope | Create variables from the tables above |
| `No ED25519 host key is known` on port `22` | Wrong known-hosts entry or host string | Use the plain `host ssh-ed25519 ...` system entry for `SSH_KNOWN_HOSTS` |
| Registry readiness failure | Registry stopped | Start Registry component; check `docker ps` |
| `volume ... already exists but was created for project ...` | Bare `docker compose` without managed identity | Use the managed Compose identity snippet with `--project-name` and `--env-file` files |
| `Conflict. The container name ... is already in use` | Same as above (duplicate project) | Same fix; never omit `--project-name` in managed directories |
| `permission denied` reading `secrets.env` as `redlaunch` user | Expected (`0600` root) | Run Compose via `docker exec redbolt-redlaunch ...`, not host-side `--env-file secrets.env` |
| API `401` expired/unknown | Expired or revoked token | Rotate token; CI failure explicitly says expired |
| API `403` | Token scoped to another application | Create a token for the target application ID |
| Migration runs old code | `migrate` service still points at old SHA | Set `app` and `migrate` to the new SHA before dispatching, or chain push → migrate → deploy |
| Compose `up -d` does not pick up new SHA | Service tracks `latest`, only SHA was pushed | Push both `:sha` and `:latest` (or update the service image), then `pull` + `up -d` |
| `docker load` + `up -d` fails with pull error | `pull` attempted for `localhost:5000/...` without registry | Omit `pull` in the direct-copy flow; the image is already local |

## References

- [GitHub Actions variables](https://docs.github.com/en/actions/concepts/workflows-and-actions/variables)
- [Using secrets in GitHub Actions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)
- [Docker daemon insecure registries](https://docs.docker.com/reference/cli/dockerd/#insecure-registries)
- [Docker Compose push](https://docs.docker.com/reference/cli/docker/compose/push/)
