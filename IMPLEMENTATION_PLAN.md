# Phased implementation plan

Based on [CODE_REVIEW.md](CODE_REVIEW.md), reviewed at `21e9cb6` on 2026-09-10. This document proposes changes; the review did not implement them.

## Sequencing and constraints

Keep the current Go → application services → infrastructure architecture, SQLite, server-rendered HTML, HTMX, and small local JavaScript interactions. Add no queue service, ORM, frontend framework, or distributed coordination service. Introduce a small abstraction only where the review identifies actual duplication or shared state.

Each numbered item below should be a focused change with its own tests and diff review. Size is relative: **S** = localized, **M** = multiple layers, **L** = migration or Linux integration work. These are scope estimates, not delivery commitments.

| Phase | Outcome | Main findings | Size |
| --- | --- | --- | --- |
| 0 | Known build/deployment baseline and regression cases | F01–F05, F10 | M |
| 1 | Protect secrets and enforce Docker project ownership | F01–F03, F10, F15 | L |
| 2 | Preserve configuration and service identity | F04, F05, F11, F13, F14 | L |
| 3 | Recoverable jobs, backups, and deletion | F06–F09 | L |
| 4 | Bounded resource use and accurate metrics | F06, F12, F16 | M |
| 5 | Simpler maintenance and release gates | Cross-cutting quality findings | M |

Security hotfixes from Phase 1 can ship individually after Phase 0. The literal-value and second-database-service guards in Phase 2 should be pulled forward if those workflows are already in use. Do not delay an independently verifiable fix until an entire phase is finished.

## Phase 0 — Establish a safe baseline

1. **Add regression cases for the reproduced defects (M).** Turn the review probes into permanent tests asserting the desired behavior: application/core project names differ; an unchanged `$` value remains unchanged; interpolation survives a move; mapping aliases remain valid or are rejected without changes; creating another database service preserves the existing service's configuration; manager-only environment values never resolve into application configuration. Change the HTTP test that currently expects `data-secret-value` to assert absence of secret values.
2. **Patch and identify the toolchain (S).** Update local/CI/compiler-image policy to a supported patched version, scan again, and record `go version -m`/image identity for release artifacts. Verify the production Linux binary separately from the local macOS scan. Do not claim that changing `go.mod` alone patches a compiled binary.
3. **Prepare migration fixtures and an operator inventory procedure (M).** Cover installations with colliding folder names, imported `name:`, existing named volumes, multiple databases, routes, and enabled backup timers. Inventory existing Docker project/container/volume labels without reading secret contents. Back up metadata/configuration using an operationally safe procedure before migrations.

**Affected files:** `Dockerfile`, `Makefile`, `go.mod` only if required by the toolchain policy, existing `internal/{compose,service,handler,store}/*_test.go`, installation documentation, and a minimal release-check workflow if desired.

**Exit gate:** current checks pass on the selected compiler; regression tests demonstrate the old failures and pass alongside their fixes; Linux artifact exposure is known. Migration tests never use personal Docker resources or real credentials.

## Phase 1 — Protect the management boundary

1. **Remove eager secret delivery (M; F02).** Introduce a secret-name/presence view model. Remove raw values from initial HTML/attributes and client initialization. Support replace-only edits with explicit unchanged/empty semantics. Implement an explicit reveal endpoint only if required, with authentication, CSRF, no-store, and limited scope. Read `DESIGN.md` before changing templates or interaction states and reuse existing controls.
2. **Assign stable Compose identities (L; F01).** Persist or deterministically derive an installation/resource identity, including distinct identities for proxy, registry, and gateway. Pass it consistently to config, up, ps, logs, exec, stop, restart, rm, and down. Verify expected ownership labels before destructive actions. Add an adoption/migration procedure for existing projects that inventories volume identities and explains any needed recreation. Never run an old ambiguous project-wide `down --volumes` to migrate.
3. **Define the import and subprocess environment policy (M; F03).** Before invoking Compose, reject unsupported external file references, remote sources, host capabilities, and paths escaping the application root. Preserve approved application interpolation inputs while excluding session/OAuth credentials from Docker subprocess environments. Document the supported import subset and error behavior. Keep these rules in application/infrastructure layers.
4. **Align listener/cookie deployment modes (M; F15).** Define SSH-only and managed-HTTPS modes, centralize cookie construction and CSRF validation, and add origin checks without trusting arbitrary forwarded headers. Coordinate listener binding with Caddy's reachability. Ensure the application-details, login, setup, job, and backup responses use the appropriate security/cache policy.

**Affected files:** `internal/handler/handler.go`, secret templates/static script, `internal/service/{applications,compose_import}.go`, `internal/compose/runner.go`, config, store migration if needed, `docker-compose.yml`, setup/install scripts, `INSTALL.md`.

**Acceptance tests:** raw secret absent from all ordinary responses; empty vs unchanged edits; rejected imports leave disk/SQLite unchanged and never resolve external dependencies; synthetic manager credentials absent from Compose environment and diagnostics; project collisions cannot target sibling resources; missing/invalid CSRF rejected; secure cookie flags correct behind configured TLS termination; both access modes remain usable.

**Release gate:** `make test`, `make lint`, safe Compose validation, production image build, and isolated Docker tests for project ownership/migration. Deploy identity changes only with a reviewed inventory and recovery procedure.

## Phase 2 — Preserve configuration and service correctness

1. **Repair environment round trips (M; F04).** Preserve raw tokens and quote/interpolation intent during no-op edits and moves. Treat literal replacement as a distinct operation. Consolidate the existing environment reader/writers around this representation; document any unsupported multiline syntax. Use Compose resolution as the compatibility oracle with synthetic inputs.
2. **Isolate per-service credentials (L; F05).** First reject a second PostgreSQL/Redis service if isolation is not yet available. Then implement scoped keys/mappings or additional service-specific environment files inside the application directory, while every service continues to load both required project files. Migration must preserve each existing database's actual credentials and mounted data; flag ambiguous legacy state for operator resolution instead of guessing or rotating secrets.
3. **Validate staged Compose changes (M; F11).** Validate after adding managed fields, before committing files/metadata. Cover import, service creation, and deletion edits. Choose either a documented restricted editor or a YAML parser that handles the needed syntax; a parser dependency is justified only with a clear maintenance/security assessment and removal of equivalent brittle code. Register imported services with one repository transaction. Use a fresh bounded context for necessary compensation after cancellation.
4. **Own required networks independently (M; F13).** Ensure the application network exists for all first-run choices, with explicit management ownership. Preserve the existing simple topology unless isolation requirements justify changing it. Keep network operations behind the Docker adapter.
5. **Make routing targets explicit (M; F14).** Add a validated upstream port to routing input/model/schema, defaulting existing records to 80. Resolve the management endpoint from deployment configuration. Test non-default host ports and application listeners. Update the routing UI using existing design-system fields.

**Affected files:** `internal/service/{environment,environment_import,postgres,redis,application_container,compose_import,compose_file,setup,routing}.go`, domain models/validation, store migrations, routing templates, Docker adapter, relevant documentation.

**Acceptance tests:** byte/semantic preservation of comments, quotes, dollars, expressions, CRLF/BOM where supported; two services retain distinct credentials after recreation; failed final validation produces no partial import; anchors/merge keys/inline collections rejected or preserved intentionally; setup succeeds for proxy-only, registry-only, both, and neither; routes work for container ports 80/3000/8080 and a non-default manager host port.

**Release gate:** focused tests followed by `make test`, `make lint`, generated `docker compose config` checks, and disposable Linux smoke tests. Every schema change uses a new migration; do not edit previously deployed migrations.

## Phase 3 — Make operations bounded and recoverable

1. **Unify tracked job lifecycle (M; F06).** Extend the existing GitHub Actions context/WaitGroup pattern to setup, database creation, application creation, and deletion. Add bounded admission, per-resource duplicate suppression, execution deadlines, cancellation, periodic expiry, and shutdown draining. Store only the minimal operation state needed for recovery; do not persist secret input payloads. Move orchestration out of HTTP handlers.
2. **Coordinate backup writers across processes (L; F07).** Use a per-service filesystem lock or SQLite lease with explicit expiry/crash semantics, shared by CLI and web. Prevent conflicting backup/restore/retention/deletion operations. Separate schedule-setting updates from completion/status updates. Reserve output filenames without overwrite and recover abandoned temporary files conservatively.
3. **Decouple backup/restore from HTTP lifetime (M; F08).** Reuse the tracked-operation mechanism, return progress immediately, and keep browser disconnects from determining database consistency. Define a bounded transaction/recovery strategy for the supported SQL dump format. Add safe streaming downloads with explicit cache and timeout behavior.
4. **Implement resumable deletion (L; F09).** Record deletion intent before side effects. Disable timers and coordinate running backups; remove/update routing state and reload Caddy; revoke deployment keys; remove owned Docker resources; finalize files and metadata with retryable stages. Preserve retained backups according to an explicit policy and retain the lookup/tombstone until cleanup is complete. Route every caller through the same application service.
5. **Harden recovery contexts and file durability (M).** Report compensation failures and keep enough state for retry. Use bounded cleanup contexts after request cancellation. Sync critical file replacements and directories where durability matters. Preserve bind-mount inode requirements deliberately; do not replace every in-place write mechanically.

**Affected files:** `internal/handler/*_job.go`, handler backup actions, `cmd/redlaunch/main.go`, `internal/service/{backup,applications,github_actions,routing,setup}.go`, `internal/store`, `internal/systemd/manager.go`, file helpers.

**Acceptance tests:** a hanging fake Docker process times out; duplicate submissions do not duplicate work; shutdown tracks all worker types; two processes cannot concurrently restore/back up the same service; completed backup status cannot revert schedule edits; failed deletion at each stage can resume; Caddy/timers/keys agree with deletion state; restore failure has the documented recovery behavior. Include process termination tests, not just the Go race detector.

**Release gate:** Linux integration suite on disposable Docker/systemd fixtures plus normal tests/lint. Provide an operator-visible interrupted-operation state and a concise recovery procedure before rollout.

## Phase 4 — Bound resource consumption and improve responsiveness

1. **Bound command/log memory (M; F12).** Capture diagnostics into a bounded tail buffer while preserving exit status; impose explicit limits on structured output; stream log downloads with backpressure, cancellation, and concurrency limits. Keep credentials out of diagnostic output.
2. **Replace global application contention carefully (M; F06).** Use per-project serialization for configuration and Docker mutations and narrow shared locks for proxy/gateway updates. Add cancellable admission. Preserve consistency against deletion and backup operations. Measure unrelated-project latency while one deployment is stalled.
3. **Cache correctly scoped metrics (M; F16).** Define whether each metric describes the manager or the VPS. Share a short-lived snapshot, avoid overlapping procfs sampling, and report age/unavailable state. Make host visibility an explicit deployment choice. Avoid adding a metrics service for this small application.
4. **Reduce redundant inspections only after measurement (S).** Service details currently request runtime, logs, and resolved Compose environment sequentially, including environment data the template does not currently display. Stop resolving unused sensitive data, and use a short-lived cache or bounded parallel reads only where profiling demonstrates benefit. Keep cache invalidation after mutations explicit.

**Affected files:** `internal/compose/runner.go`, service detail interfaces, `internal/service/applications.go`, metrics collector, dashboard/service handlers, deployment docs if metrics scope changes.

**Acceptance tests/measurements:** synthetic multi-megabyte command output does not grow memory with total output; concurrent log downloads remain within defined limits; a slow application A does not block reads/mutations of unrelated application B; concurrent dashboard requests reuse one sample; stale/error states are observable. Capture representative allocation and latency baselines before setting numeric budgets; do not invent production targets from this source review.

## Phase 5 — Reduce maintenance risk and establish release checks

1. **Make required dependencies explicit (M).** Replace the large `...any` constructor wiring with a small explicit dependency struct. Require authentication for production routes; provide explicit test-only unauthenticated construction. Split the 5,067-line handler along existing feature boundaries without changing behavior.
2. **Consolidate proven duplication (M).** Share CSRF/form helpers and job lifecycle code; keep feature-specific validation and progress descriptions local. Use typed operation errors instead of parsing strings. Preserve sanitized operator diagnostics.
3. **Harden filesystem boundaries (M/L).** Prefer rooted/directory-relative operations for managed trees and reject symlink escapes in bind sources. Cover ancestor replacement and a container-writable project path in isolated tests. The project data-directory contract should state which paths workloads may modify.
4. **Align resource conventions and build context (S/M).** Standardize new core Compose filenames and migrate old names compatibly; explicitly resolve whether the manager follows the universal env-file/label rule. Restrict Docker build inputs, exclude repository history and local credential variants, and separate dependency download from source compilation for caching.
5. **Add repeatable release gates (M).** Race tests, vet, formatter check, supported-version Compose fixtures, Linux build, patched-toolchain vulnerability scan, safe secret scan, and a small Docker/systemd integration suite. Test migrations from prior schema versions. Document registry-wide key authority and operational recovery limits.

**Exit gate:** required dependencies fail fast; normal build/test commands remain simple; all release checks are reproducible on a clean host; no unrelated refactor or framework/dependency expansion is bundled with the fixes. Review the final diff and update behavior/configuration/installation documentation with every shipped change.

## Decisions that must be explicit before dependent implementation

- **Import contract:** support a restricted managed subset by default; permitting arbitrary host-level Compose capabilities would require an explicit product decision and documentation.
- **Multiple databases:** preserve the feature with scoped configuration, or enforce one service of each database type; never silently overwrite another service's credentials.
- **Existing project adoption:** preserve named-volume/data identity and define the required recreation window before migrating Compose project names.
- **Backup deletion:** choose retain/export/delete behavior and reflect it in the UI; cleanup must not introduce unexpected data removal.
- **Management access and metrics:** align SSH-only versus HTTPS routing and host-versus-container visibility with installation defaults.

These choices do not block independent secret-handling, toolchain, regression-test, output-bounding, or job-tracking work. Follow the smallest coherent implementation for each accepted contract.
