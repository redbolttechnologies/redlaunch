# Application-level VPN access

## Objective

Add an application-level **Access** setting:

- **Public** — reachable through the shared Caddy proxy by anyone.
- **VPN only** — routed traffic is accepted only from the managed WireGuard
  VPN; everything else receives `404`.

The default remains **Public**, so existing applications keep working
unchanged.

This plan assumes:

- Redlaunch installs and manages WireGuard.
- Redlaunch provides native peer management.
- Full-tunnel VPN clients are the default for v1.

## Architecture

```text
Internet ──► Caddy (redbolt-proxy, :80/:443)
                 │
                 ├──► public application routes
                 │
VPN clients ──► WireGuard (redbolt-vpn, UDP 51820)
                 │  NAT source = redlaunch-vpn subnet
                 └──► Caddy through redlaunch-vpn
                        │
                        └──► VPN-only application routes
```

Key decisions:

- Add a managed core component at `core/vpn/`, container `redbolt-vpn`.
- Create a dedicated bridge network `redlaunch-vpn` with a fixed subnet, for
  example `172.30.250.0/24`.
- Attach both `redbolt-vpn` and `redbolt-proxy` to `redlaunch-vpn`.
- WireGuard clients use `10.8.0.0/24`; v1 defaults to full tunnel
  (`AllowedIPs=0.0.0.0/0`).
- VPN traffic reaching Caddy is NATed into the `redlaunch-vpn` subnet. Caddy
  therefore sees a deterministic source network, not a spoofable client
  header.
- Caddy trusts only `172.30.250.0/24` for VPN-only application routes.
- No VPN peer or key data is stored in SQLite. `core/vpn/vars.env` and the
  generated `core/vpn/config/` directory remain the source of truth.

This avoids host networking for the VPN container and keeps the VPN container
off the application network.

## Application visibility model

Add to `internal/application`:

```go
type Visibility string

const (
    VisibilityPublic Visibility = "public"
    VisibilityVPN    Visibility = "vpn"
)
```

Add `Visibility` to `application.Application`, and validation/defaulting:

- Empty or `public` → public.
- `vpn` → VPN only.
- Any other value → validation error.

Persistence changes:

- Migration **29** adds:

  ```sql
  ALTER TABLE applications
  ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'
  CHECK (visibility IN ('public', 'vpn'));
  ```

- Update `List`, `Get`, `Create`, and related SELECTs in
  `internal/store/store.go`.
- Add `UpdateApplicationVisibility`.
- `CloneApplication` copies the source application's visibility.
- New applications and Compose imports default to `public`.

Routing rendering needs the owning app's visibility:

- Add `Visibility` to `application.Routing` as a derived field.
- Join `applications` in `ListRoutings`, `ListAllRoutings`, and `GetRouting`.
- Add `Visibility` to `application.ProxyDomain` so the Proxy page can show it.

No `vpn_peers` table is needed. Peer names live in `core/vpn/vars.env` as the
existing `PEERS` value, which is already the source of truth for the WireGuard
image.

## Caddy enforcement

The deny rule must live **inside the route's `handle` block**, not before all
handles. Otherwise a VPN-only catch-all route would also deny more-specific
public routes on the same hostname.

Generated Caddyfile shape:

```caddyfile
example.com {
    @redlaunch_route_2 path /public /public/*
    handle @redlaunch_route_2 {
        reverse_proxy redbolt-1-public:80
    }

    @redlaunch_route_1 path /*
    handle @redlaunch_route_1 {
        @redlaunch_vpn_access_1 not remote_ip 172.30.250.0/24
        respond @redlaunch_vpn_access_1 404

        reverse_proxy redbolt-1-private:80
    }
}
```

Rules:

- Only routes whose application has `visibility = vpn` get the deny block.
- Use a unique matcher name per routing ID.
- The deny returns `404`, not `403`, so VPN-only applications do not confirm
  their existence.
- If the trusted VPN source constant is empty, emit unconditional
  `respond 404` for those routes. Fail closed.
- Keep the deny before rewrites and `reverse_proxy`.
- The existing path precedence, rewrites, proxy policies, and
  management-domain fallback remain unchanged.
- `SetApplicationVisibility` acquires the application project lock, then the
  proxy lock, updates SQLite, regenerates Caddy, and rolls back both on reload
  failure. This matches the existing `SaveProxySettings` pattern.
- If the proxy is not installed yet, allow the setting to be stored and skip
  the Caddy refresh; when the proxy is installed, the generated configuration
  already includes the deny rules.

Add a shared constant for the trusted source, e.g.:

```go
const managedVPNProxySource = "172.30.250.0/24"
```

Add a test asserting the VPN Compose template's network subnet and this
constant stay aligned.

## Managed WireGuard component

Create `core/vpn/` with:

- `compose.yml`
- `vars.env`
- `secrets.env`
- `config/` (runtime, mode `0700`)

Compose shape:

- Image: pinned `lscr.io/linuxserver/wireguard` image.
- Container name: `redbolt-vpn`.
- Label: `redlaunch.managed=true`.
- `env_file`: both `vars.env` and `secrets.env`.
- `cap_add`: `NET_ADMIN`; optionally `SYS_MODULE`.
- Sysctl: `net.ipv4.conf.all.src_valid_mark=1`.
- Port: `51820:51820/udp`.
- Volume: `./config:/config`.
- Network: `redlaunch-vpn`, static IPv4 address `172.30.250.2`.
- No Docker socket, no host networking, no published management UI.

`vars.env` contains:

```env
PUID=1000
PGID=1000
TZ=Etc/UTC
SERVERURL=<public hostname or IP>
SERVERPORT=51820
PEERS=phone,laptop
PEERDNS=auto
INTERNAL_SUBNET=10.8.0.0
ALLOWEDIPS=0.0.0.0/0
LOG_CONFS=false
```

Notes:

- `ALLOWEDIPS=0.0.0.0/0` provides the full-tunnel default. Do not include
  `::/0` in v1; IPv6 VPN support is a follow-up.
- The image generates server and peer keys under `config/`. Never store those
  keys in SQLite or log them.
- `secrets.env` exists as required by project convention and can remain empty.

`redlaunch-vpn` network creation:

- Extend the compose/network layer to create a network with a fixed subnet, or
  add a dedicated `EnsureNetworkWithSubnet` helper.
- Verify an existing `redlaunch-vpn` network has the expected subnet; refuse a
  mismatched network with a clear error.
- Add the network to the proxy Compose file via an idempotent helper similar
  to `addProxyHostGateway` and `addProxyCaddyfileMount`.
- The proxy joins both `redlaunch-common` and `redlaunch-vpn`.

Installation:

- Add VPN as an optional selection in first-run setup.
- Add an idempotent **Install VPN** action on the VPN page for existing
  installations.
- Installing after setup must:
  1. Ensure the `redlaunch-vpn` network.
  2. Write the VPN project files and empty environment files.
  3. Add the VPN network to the proxy Compose project and recreate the proxy.
  4. Start the VPN container.
- Warn that recreating the proxy briefly interrupts routed traffic.

## Native peer management

Add a `VPN` page and service. Peer data is derived from `vars.env` and
`config/`.

Operations:

- **List peers** — parse `PEERS`, list
  `config/peer_<name>/peer_<name>.conf`.
- **Create peer** — validate the name, append it to `PEERS` using the existing
  dotenv parser/writer, recreate the VPN container, wait for the generated
  config file, then show a download action.
- **Download config** — serve the generated `.conf` file with:
  - authenticated access,
  - `Cache-Control: no-store`,
  - `X-Content-Type-Options: nosniff`,
  - `Content-Disposition: attachment`,
  - no config contents in logs.
- **Rotate peer** — remove the peer's generated config directory, recreate the
  container, and let the image generate new keys.
- **Delete peer** — remove the name from `PEERS`, recreate the container, then
  remove the config directory.
- **Public endpoint** — edit `SERVERURL`; recreate the container. Warn that
  existing peers must re-download configs.
- **Zero peers** — handle the empty `PEERS` case explicitly and test the chosen
  sentinel against the pinned image.

Peer names:

- Restrict to `^[a-z][a-z0-9]{0,31}$`.
- Use lower-case and case-insensitive uniqueness.
- Reject duplicates and path-like values.
- Serially protect peer mutations with a VPN project lock.

Container operations:

- Reuse the existing tracked-job pattern for install/recreate operations so
  the browser does not time out during a Docker image pull or container
  recreation.
- Add Start/Stop/Restart controls for the VPN container for operational parity
  with the Proxy page.

## UI

Application details:

- Add an **Access** tab (recommended) with:
  - **Public** — “Anyone who can reach the proxy can access this application.”
  - **VPN only** — “Only clients connected to the managed WireGuard VPN can
    access this application. Other requests receive a 404.”
  - Warning when WireGuard is not installed: VPN-only applications will be
    unreachable.
  - Warning about direct published ports and CDN proxying.
- Add a lock/globe badge next to the application title and on application cards
  in `/applications`.
- Add a VPN-only notice on the routing page.
- Add an **Access** column to the Proxy page's associated-domains table.

VPN page:

- Add a `VPN` sidebar item and `/vpn` page.
- Show:
  - container status,
  - public endpoint and UDP port,
  - peer count,
  - install card when the component is not installed,
  - peers table with download/rotate/delete actions,
  - create-peer dialog,
  - endpoint settings.
- If the VPN is not installed, the page should explain the feature and offer
  **Install VPN**.

Setup page:

- Add a **VPN — WireGuard** option with a `51820/udp` badge.
- Add setup progress stages: `vpn-files`, `vpn-start`.
- Update `SetupWithProgress`, `setupSteps`, `setupStageLabel`, and the setup job
  tests.

Design constraints:

- Reuse existing buttons, tables, dialogs, fields, and status badges.
- Use existing semantic tokens; no new arbitrary colors, spacing, or standalone
  visual pattern.
- Use text plus icon/text for access state, not color alone.

## Security requirements

- Caddy enforcement is fail-closed: no VPN source means `404`.
- The trusted source is the dedicated `redlaunch-vpn` subnet, not a request
  header or `X-Forwarded-For`.
- Peer configs and server keys are secrets:
  - `config/` is `0700`, generated key files restricted,
  - no secret values in HTML, logs, errors, database, or test snapshots,
  - downloads are authenticated and never cached.
- Validate peer names and prevent path traversal using the existing managed-path
  checks.
- The VPN container gets only the capabilities it needs and never mounts the
  Docker socket.
- Do not publish a VPN web UI.
- Document that VPN-only applies to Caddy-routed hostnames. Directly published
  container ports remain reachable unless removed or firewalled; the Access tab
  should warn when the application has published ports.
- Document that a CDN or external reverse proxy in front of the hostname masks
  the VPN source address and will make VPN-only apps unreachable.

## Affected files and layers

Domain:

- `internal/application/application.go`
- new `internal/application/visibility.go` or `vpn.go`

Persistence:

- `internal/store/store.go`
- new/updated store tests

Service:

- `internal/service/routing.go`
- `internal/service/applications.go`
- `internal/service/applications_clone.go`
- new `internal/service/application_access.go`
- new `internal/service/vpn.go`
- `internal/service/setup.go`
- `internal/compose/runner.go` for fixed-subnet network creation
- service tests

HTTP:

- `internal/handler/handler.go`
- new `internal/handler/application_access.go`
- new `internal/handler/vpn.go`
- `internal/handler/templates/application-details.html`
- `internal/handler/templates/application-name.html`
- `internal/handler/templates/applications.html`
- `internal/handler/templates/application-routing.html`
- `internal/handler/templates/proxy.html`
- new `internal/handler/templates/vpn.html`
- `internal/handler/templates/setup.html`
- `internal/handler/templates/layout.html`
- `internal/handler/static/app.css`
- optional `internal/handler/static/vpn.js`

Wiring and samples:

- `cmd/redlaunch/main.go`
- `projects/core/vpn/` sample project files

Documentation:

- `README.md`
- `INSTALL.md`
- `RECIPES.md`
- new `VPN.md` help guide, added to `documentation.go` and
  `internal/handler/help.go`

## Testing and verification

Add tests for:

- Visibility validation and defaulting.
- Migration 29: existing applications become `public`; update/list/get works.
- Clone copies visibility.
- Caddyfile rendering:
  - public route has no deny,
  - VPN route has the deny inside its `handle`,
  - a VPN-only catch-all does not deny a more-specific public route,
  - no trusted source emits unconditional `404`,
  - management domain is unaffected,
  - rewrites and proxy policies remain intact.
- Visibility update rolls back SQLite and Caddyfile on reload failure.
- Handler tests for the Access tab, badge, CSRF, invalid values, and redirects.
- VPN service tests with temp dirs and a fake runner:
  - install writes compose/env files/label/network,
  - peer create/delete/rotate updates `PEERS` and preserves comments,
  - peer name and path validation,
  - config download headers,
  - zero-peer case,
  - rollback when container recreation fails.
- Setup tests for the new VPN option and progress stages.
- Fixed-subnet network creation and mismatched-subnet refusal.

Validation commands:

```sh
make test
make lint
make fmt-check
make secret-scan
```

Also:

- `docker compose config` for generated `core/vpn` and `core/proxy` projects.
- `caddy validate` against a generated Caddyfile fixture.
- Manual end-to-end on a Linux host:
  1. Install VPN.
  2. Create a peer and connect.
  3. Add a routed app and set it to **VPN only**.
  4. Confirm public requests get `404`.
  5. Confirm VPN requests reach the app.
  6. Switch to **Public** and confirm public access returns.
  7. Stop WireGuard and confirm VPN-only apps remain denied.
  8. Restart Caddy and confirm the configuration persists.

Docker Desktop on macOS cannot fully validate WireGuard kernel behavior; the
end-to-end test should run on a Linux VPS or VM.

## Implementation order

1. **Enforcement spike** — validate on a Linux Docker host that VPN traffic to
   the published Caddy port arrives from the `redlaunch-vpn` subnet. If Docker's
   networking behaves differently, fall back to host networking for the VPN
   container and trust the WireGuard client subnet.
2. **Visibility model and persistence** — migration, domain type, store queries,
   clone behavior.
3. **Caddy enforcement** — routing join, deny rendering, fail-closed behavior,
   rollback, tests.
4. **Application UI** — Access tab, badges, routing notice, Proxy table column.
5. **VPN core component** — Compose template, network creation, proxy network
   integration, setup option, existing-install action.
6. **Peer management and VPN page** — create/download/rotate/delete, endpoint
   settings, tracked jobs.
7. **Machine API/MCP** — add visibility to application responses and an
   `update_application_access` operation if included in v1.
8. **Documentation and full verification**.

## Out of scope for v1

- Per-domain or per-routing visibility.
- Per-user or per-peer application authorization.
- Split-tunnel clients as the default.
- IPv6 VPN routing.
- VPN access to the Redlaunch management interface.
- Supporting external VPN providers or arbitrary trusted CIDRs through the UI.
- Enforcing VPN-only access for directly published container ports.
- Multiple VPN servers or high availability.

The main limitation to communicate clearly is that v1 controls **Caddy-routed
web access**. Direct host port mappings and CDN-fronted hostnames are outside
that guarantee.
