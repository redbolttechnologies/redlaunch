# API tokens

API tokens let external automation, for example GitHub Actions, trigger a
one-off service run (such as a database migration) without SSH shell access,
Docker access, or knowledge of any secrets.

A token authorizes exactly one action: starting one-off runs (`docker compose
run --rm` semantics) in **one pinned application**. It cannot read
environment files, run arbitrary commands (the command comes from the
admin-managed `compose.yml`), reach other applications, or open a shell.

## Create a token

1. Open **Settings → API tokens**.
2. Select **Create API token**, enter a display name, pick the application,
   and pick an expiry (30/90/180/365 days, or never).
3. Copy the token from the one-time dialog. It is shown only there
   (`Cache-Control: no-store`) and Redlaunch stores only its SHA-256 hash.

Use the token from the external system as a `Bearer` credential:

```sh
curl --fail -X POST \
  -H "Authorization: Bearer $REDLAUNCH_RUN_TOKEN" \
  "$REDLAUNCH_URL/api/v1/applications/7/services/migrate/run"
```

The response is `202 Accepted` with a pollable job (runs execute
asynchronously so long migrations never hold the dispatch connection):

```json
{"status":"running","job_id":"...","status_url":"/api/v1/applications/7/services/migrate/run/status?id=..."}
```

Poll the status URL with the same `Authorization` header until it reports
`complete` or `failed` (failures include the redacted Compose tail in
`detail`). Unknown or expired job IDs report `404`.

## Example: Drizzle migration from GitHub Actions

```yaml
name: Migrate DB

on:
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: migrate-talenthunt
  cancel-in-progress: false

jobs:
  migrate:
    runs-on: ubuntu-latest
    env:
      REDLAUNCH_URL: ${{ vars.REDLAUNCH_URL }}
      APPLICATION_ID: "7"
      SERVICE_NAME: migrate
    steps:
      - name: Dispatch migration run
        id: dispatch
        shell: bash
        env:
          REDLAUNCH_RUN_TOKEN: ${{ secrets.REDLAUNCH_RUN_TOKEN }}
        run: |
          set -euo pipefail
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

- Never pass `-v`/`--verbose` to curl with the token header, and never echo
  the token: GitHub masks `secrets.*` values, but only when they appear
  verbatim.
- The `concurrency` group keeps manual dispatches from queueing duplicate
  runs. Redlaunch additionally serializes runs per application and returns
  the already-running job when a duplicate dispatch arrives.
- Keep the migration idempotent (Drizzle `migrate` is journal-based), since a
  retried workflow dispatches again.

## Reachability

The API is served by the management interface, which binds to loopback by
default. GitHub Actions can reach it via either supported path:

- **Managed HTTPS (recommended):** publish Redlaunch through the managed Caddy
  proxy (Settings → Domains, `MANAGEMENT_ACCESS_MODE=managed-https`) and use
  `https://redlaunch.example.com` as `REDLAUNCH_URL`. The token always travels
  over TLS.
- **SSH tunnel:** forward the loopback port with an existing Settings SSH key
  (`ssh -f -N -L 8080:127.0.0.1:8080 redlaunch@server`) and use
  `http://127.0.0.1:8080` as `REDLAUNCH_URL`. The SSH key only provides
  transport here; the Bearer token remains the authorization.

Do not expose the management port to the network without TLS: Bearer tokens
sent over plain HTTP can be intercepted.

## Rotation and revocation

- Prefer the shortest expiry the automation tolerates. Rotate by creating a
  replacement token, updating the external secret, then revoking the old
  token. Overlapping validity avoids downtime.
- **Revoke token** on the Settings tab deletes the token immediately:
  authentication looks the token hash up on every request, so there is no
  grace period or cache to wait out. Remove the value from the external
  system separately.
- Expired tokens are rejected with an explicit "expired" error so CI failures
  point at rotation instead of Redlaunch itself.
- Token use is logged with the token ID and prefix (`rlr_…`), application,
  service, and result. Token values and hashes are never logged.

## Status codes

| Code | Meaning |
| --- | --- |
| `202` | Run dispatched (or the already-running job for a duplicate dispatch); poll `status_url`. |
| `200` | Job status (`running`, `complete`, or `failed` with redacted `detail`). |
| `400` | Invalid application ID or service name. |
| `401` | Missing, unknown, malformed, or expired token. |
| `403` | Valid token, but not scoped to this application. |
| `404` | Unknown application, service, or job ID. |
| `503` | The operation system is busy; retry shortly. |

## References

- [GitHub Actions variables](https://docs.github.com/en/actions/concepts/workflows-and-actions/variables)
- [Using secrets in GitHub Actions](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-secrets)
