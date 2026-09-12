# Code quality, security, and performance review

Reviewed 2026-09-10 at commit `21e9cb6`. Implementation sequence: [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

## Assessment and scope

The layered Go/SQLite design is appropriate for this application. The most urgent work concerns isolation of Docker operations, secret handling, and configuration/data integrity. A rewrite or additional application framework would not address the underlying problems.

This review traced authentication and HTTP entry points through application services, SQLite, Docker/Compose, systemd, configuration editing, imports, routing, backups, provisioning jobs, and deployment scripts. It also inspected template/JavaScript data handling. It is a source review with automated checks and isolated diagnostic probes, not a penetration test or a production load test. No running containers, deployed configuration, real environment files, or production data were modified. Visual design QA was outside scope.

Severity assumes the documented model: every authorized account is a trusted host administrator. An authenticated administrator's ability to run containers is intentional. Malicious imported configuration, compromised application containers, accidental cross-project operations, and unintended disclosure of manager credentials remain relevant risks. No unauthenticated authentication bypass was established.

Priority meanings: **P1** = address before expanding production use; **P2** = schedule next for reliability, security hardening, or operational correctness. Confidence distinguishes reproduced defects, direct source findings, and conditional risks.

## Verification

| Check | Result and scope |
| --- | --- |
| `make test` | Passed all 10 packages, with `-race -count=1`. |
| `make lint` | Passed; this target runs `go vet`. |
| `docker compose --env-file /dev/null config --quiet` | Passed; avoids loading the local `.env` and does not print resolved secrets. |
| Five temporary diagnostic probes | All reproduced the suspected behavior; described below. Docker calls only resolved synthetic configuration. Probe source was removed after the review. |
| `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | Completed and reported nine reachable standard-library advisories for local Go 1.26.3; detailed below. |
| Production image / Linux integration / load tests | Not performed. No production-image changes were made. |

Local tooling: Go `1.26.3 darwin/arm64`, Docker Compose `v2.40.3-desktop.1`, vulnerability scanner module `golang.org/x/vuln v1.8.0`. Existing tests use many fake Docker/systemd adapters; a passing suite does not establish real daemon behavior, cross-process coordination, or crash recovery.

## Findings

### F01 — P1: Docker project identity can collide across managed resources

**Evidence:** [runner.go](internal/compose/runner.go), `runComposeUpWithOptions` (line 138), `Down` (230); [application.go](internal/application/application.go), `ValidateFolderName` (732); [compose_import.go](internal/service/compose_import.go), `ImportDockerComposeProject` (31).

Every operation supplies `-f` but no explicit project name. Application folder names are not isolated from core directory names, and imported top-level `name:` survives import. A configuration-only probe confirmed that `applications/proxy/compose.yml` and `core/proxy/compose.yml` both resolve to project `proxy`; `proxy` is an accepted application folder name. Different folder spellings can also normalize to the same Compose identity.

`Down` uses `--volumes --remove-orphans`. A collision therefore creates a risk of removing another resource's containers, even when container names are prefixed differently. Docker uses project identity for resource grouping; the custom `redlaunch.managed` label alone does not constrain these commands. The collision was reproduced; destructive consequences were not exercised.

**Remediation:** assign an installation-scoped, stable project name to every application and core component and pass it to every Compose command. Assert ownership before destructive operations. Existing installations require an explicit migration that preserves volume identities and inventories containers before recreation. [Docker project-name precedence](https://docs.docker.com/compose/how-tos/project-name/).

### F02 — P1: Masked secrets are included in ordinary page HTML

**Evidence:** [application-details.html](internal/handler/templates/application-details.html), line 867; [handler_test.go](internal/handler/handler_test.go), line 2092; [applications.go](internal/service/applications.go), `GetEnvironmentFiles` (65).

The masked secrets panel renders each value in `data-secret-value`. The browser receives all application secrets on the initial details-page response, before any reveal/edit action. The existing HTTP test explicitly requires the synthetic secret in that attribute. Visual masking does not protect page source, browser extensions, DOM readers, or a future script compromise. This violates the repository's default restriction on sending secrets to the browser. The page already uses `Cache-Control: no-store`, which is helpful but does not prevent delivery.

**Remediation:** render secret names and presence metadata only. Make edits replace-only by default, distinguishing unchanged from intentionally empty. If reveal is a supported requirement, fetch only the requested value after an explicit authenticated, CSRF-protected action, with no-store responses and short client retention. Reverse the current test expectation so secrets must be absent from the complete HTML body and attributes.

### F03 — P1: Imported Compose configuration inherits manager credentials and lacks a preflight trust boundary

**Evidence:** [runner.go](internal/compose/runner.go), `ConfigServices` (91), command construction throughout, and `Environment` (464); [compose_import.go](internal/service/compose_import.go), lines 115–156; [docker-compose.yml](docker-compose.yml), manager authentication environment.

Docker subprocesses inherit the Redlaunch process environment. A probe using a synthetic `AUTH_SESSION_SECRET` confirmed that the real `Environment` adapter resolves `${AUTH_SESSION_SECRET}` into an application's environment. A downloaded Compose file can therefore accidentally or maliciously copy manager OAuth/session credentials into a managed service. This is separate from normal interpolation of application settings.

Import validates service names and syntax, then preserves other fields. It does not enforce a policy for host bind mounts, privileged containers, Docker sockets, `include`, `extends`, file-backed labels/configs/secrets, or remote build sources. The `--no-env-resolution` flags are not a general filesystem/network sandbox: other Compose file references can be processed during configuration loading. An administrator must currently trust the entire uploaded file, not just approve starting its services. [Docker's Compose trust model](https://docs.docker.com/compose/trust-model/).

**Remediation:** define and enforce an explicit managed-import policy before invoking Compose. Resolve allowed local references beneath the project root; reject unsupported external references and host capabilities with actionable errors. Give Docker subprocesses an intentional environment containing required Docker/host settings and approved interpolation inputs, excluding manager authentication credentials. Preserve documented application interpolation semantics and document any restricted import subset. These risks are conditional on imported/user-edited configuration; they are not evidence of unauthenticated host access.

### F04 — P1: Environment editing changes values and interpolation semantics

**Evidence:** [environment.go](internal/service/environment.go), `parseEnvironmentValue` (154), `updateEnvironmentFile` (200), `formatEnvironmentEntry` (321); [postgres.go](internal/service/postgres.go), `formatEnvironmentValue` (497); [applications.go](internal/service/applications.go), environment move operations.

The writer escapes each dollar sign as `$$`, while the reader returns that escaped representation without reversing it. A synthetic no-op edit changed `COST="$$5"` into `COST="$$$$5"`. Separately, `TARGET=${BASE}/api` became `TARGET="$${BASE}/api"`, disabling its intended interpolation. Moving entries between files passes through the same decode/re-encode path. Quoting mode and expression intent are lost. The parser is line-based, so valid multiline dotenv values also fall outside its supported format.

**Remediation:** represent an entry's raw value, quoting mode, comments, and edit intent. Preserve the original token when moving or making a no-op edit; distinguish literal replacement from Compose expressions. Validate round trips against the actual supported Compose versions. Single-quoted values and unquoted/double-quoted expressions have distinct behavior in [Docker's environment syntax](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/).

### F05 — P1: Multiple database services overwrite shared credentials

**Evidence:** [postgres.go](internal/service/postgres.go), lines 100–109 and `addPostgreSQLService` (268); [redis.go](internal/service/redis.go), lines 91–97.

Every PostgreSQL service reads the same project-level `POSTGRES_DB`, `POSTGRES_USER`, and `POSTGRES_PASSWORD`. Creating a second service overwrites those keys. A probe successfully created `db1` and `db2` and confirmed the shared settings changed to `db2`. The first container may keep its old environment until recreation; afterward its environment and persisted database credentials can disagree. Metadata still describes each service separately. Redis has the analogous shared `REDIS_PASSWORD` issue when a second password is configured.

**Remediation:** isolate service-specific settings while continuing to load the required project `vars.env` and `secrets.env`. Use a documented, Compose-compatible mapping or additional per-service environment files inside the application directory. Avoid placing credentials in Compose YAML or SQLite. A temporary validation guard can reject a second database service until isolation is implemented. Existing installations need a deliberate mapping/migration, not automatic password rotation.

### F06 — P1: Global locks and unmanaged jobs can stall the control plane

**Evidence:** [applications.go](internal/service/applications.go), `Applications.mu` (386) and `GetEnvironmentFiles` (79); [postgres.go](internal/service/postgres.go), lock at 69 through startup at 146; [application_container_job.go](internal/handler/application_container_job.go), `create` (73), `runApplicationContainerJob` (205); [github_actions_job.go](internal/handler/github_actions_job.go), `Shutdown` (346).

An application-service mutex serializes operations across every application and remains held while Docker pulls/starts containers or Caddy reloads. Even environment reads need this lock. A slow or hung deployment can block unrelated pages and mutations. Waiting on `sync.Mutex.Lock` is not canceled with the HTTP request.

Setup, PostgreSQL, Redis, application-container, and deletion workers use `context.Background()` and are not included in `Shutdown`; only GitHub Actions workers have a shared shutdown context and timeout. Most job stores have no admission cap or duplicate-operation suppression, and expired jobs are cleaned mainly on new submissions. Repeated requests can accumulate goroutines behind a blocked lock. Process exit can interrupt filesystem/metadata workflows.

**Remediation:** reuse the established tracked-worker pattern across jobs, add operation deadlines and bounded admission, and serialize conflicting operations per resource. Keep a separate narrow lock for truly shared proxy/gateway state. Do not simply remove locks or permit concurrent writes to the same Compose project.

### F07 — P1: Backup coordination does not cover the scheduler process

**Evidence:** [backup.go](internal/service/backup.go), `RunBackupNow` (215), `RunScheduledBackup` (231), `runBackup` (442), `nextBackupFileName` (883), `renderUnits` (702); [main.go](cmd/redlaunch/main.go), `runBackup`.

The web server and each systemd `backup-run` invocation create different `BackupService` instances. Their mutexes do not coordinate. Manual and scheduled backups, restore, retention, and schedule updates can overlap across processes. `runBackup` reads a schedule before the dump and later saves the whole stale schedule with status fields, potentially overwriting intervening settings. Filename allocation uses existence-check followed by rename, which is also not an exclusive reservation across processes.

**Remediation:** introduce a per-service lock/lease shared by web and CLI operations, with defined crash recovery and bounded acquisition. Update backup status independently of schedule configuration; reserve final filenames without overwrite. Test with two independently constructed services/processes against the same temporary database and directory. These races are source-established possibilities; a concurrent data-loss event was not reproduced.

### F08 — P1: Destructive restore and long backup operations remain tied to HTTP deadlines

**Evidence:** [main.go](cmd/redlaunch/main.go), HTTP server configuration (143); [handler.go](internal/handler/handler.go), `runBackupNow` (2410), `restoreBackup` (2426), `downloadBackup` (2458); [runner.go](internal/compose/runner.go), restore script and `RestorePostgreSQL`.

Backup/restore execute synchronously using the request context, while the server has a ten-second write timeout. A slow dump/restore can finish after the response deadline or be interrupted when the browser disconnects. A slow download can be truncated. A write timeout itself is not an operation-cancellation guarantee. Restore runs plain `psql --set ON_ERROR_STOP=1` without a transaction encompassing the dump, so failure after successful statements can leave a partially restored database.

**Remediation:** run backup/restore as bounded, tracked operations with progress and duplicate-submit protection. Validate the dump format and use an atomic restore strategy where supported; provide explicit recovery behavior for partial failures. Give downloads a deliberate streaming/deadline policy, and add no-store headers to backup responses. Test cancellation and failure midway through a synthetic restore on a disposable database.

### F09 — P1: Deletion leaves external state behind and can lose its recovery handle

**Evidence:** [applications.go](internal/service/applications.go), `DeleteServiceWithProgress` (849), `DeleteApplicationWithProgress` (957); [store.go](internal/store/store.go), `DeleteService` (676), `DeleteApplication` (612), routing schema (961); [application_delete_job.go](internal/handler/application_delete_job.go), line 203.

Service deletion does not remove routing records referencing the service name, and the routing schema has no service foreign key. Application deletion cascades routing metadata but neither deletion path refreshes Caddy. Both delete backup metadata through cascades without disabling systemd timers or defining what happens to retained backup files. Timers can repeatedly invoke a deleted service, and retained files become inaccessible through the UI.

Application metadata is deleted before `os.RemoveAll`; if directory removal fails, a retry cannot look up the application to finish cleanup. GitHub Actions key cleanup is correctly attempted from the HTTP job, but this business step is absent from a direct call to the application service.

**Remediation:** coordinate deletion in the application-service layer, covering timers, routing/Caddy, deployment keys, containers, files, and a documented backup-retention policy. Keep a durable tombstone/progress record until cleanup is complete and make retries idempotent. Do not silently start deleting retained backups as part of this fix.

### F10 — P1: The scanned local toolchain has reachable vulnerability advisories

The scanner reported the following under Go 1.26.3. Reachable symbols are not proof that every advisory's exploit preconditions occur in this application; for example, a template-context advisory requires the affected template context. All nine were standard-library symbol findings; the scanner also listed additional uncalled package/module findings, which are not treated as demonstrated application exploits here.

| Advisory | Area | Scanner's fixed Go 1.26 version |
| --- | --- | --- |
| [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) | `net/url`, quadratic path resolution | 1.26.6 |
| [GO-2026-6091](https://pkg.go.dev/vuln/GO-2026-6091) | `html/template`, JavaScript regexp context | 1.26.6 |
| [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) | TLS post-handshake message limits | 1.26.6 |
| [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) | HTTP header timeout handling | 1.26.6 |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | ASN.1 recursion bounds | 1.26.6 |
| [GO-2026-5856](https://pkg.go.dev/vuln/GO-2026-5856) | TLS ECH privacy | 1.26.5 |
| [GO-2026-5039](https://pkg.go.dev/vuln/GO-2026-5039) | Unescaped textproto error inputs | 1.26.4 |
| [GO-2026-5037](https://pkg.go.dev/vuln/GO-2026-5037) | X.509 hostname parsing cost | 1.26.4 |
| [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) | IDNA handling through `net/http` | 1.26.6 |

**Remediation:** use a currently supported, patched compiler in local verification and production builds, and scan the Linux artifact. The production Dockerfile uses floating `golang:1.25-alpine`; this review did not inspect its resolved image or the deployed binary. Do not assume its patch version matches local Go or infer production exposure from this scan alone. The first two official advisories also identify Go 1.25.13 as their fixed release on the 1.25 line.

### F11 — P2: Import rewrites valid YAML without validating the resulting file

**Evidence:** [compose_import.go](internal/service/compose_import.go), validation before rewriting at 131–150, `parseImportedComposeServices` (171), `ensureImportedManagedLabel` (336); [compose_file.go](internal/service/compose_file.go).

The importer uses an indentation-based partial YAML parser and validates only the original document with Compose. A probe used a valid mapping anchor and `labels: *labels`; Compose accepted the input, but the managed rewrite converted the alias into a list item containing a mapping, and Compose rejected the result. The import would already proceed toward metadata creation. Other supported YAML features, indentation choices, and service/dependency edits need equivalent scrutiny.

**Remediation:** validate the final staged configuration before committing files/metadata. Either explicitly reject syntax the editor cannot preserve or adopt a narrowly justified YAML representation with tests for anchors, merge keys, inline collections, comments, and dependencies. Batch imported metadata creation in one repository transaction instead of compensating per-row inserts with the potentially canceled request context.

### F12 — P2: Docker output and complete log downloads are unbounded in memory

**Evidence:** [runner.go](internal/compose/runner.go), `CombinedOutput` in command methods, `readLogs` (350); [applications.go](internal/service/applications.go), `GetServiceFullLogs` (752), `GetProxyFullLogs` (670).

The 8 KiB error-output limit is applied after the whole command has been captured in memory. Successful pulls/builds and failed commands can therefore allocate far more. Full logs use `command.Output()`, convert the entire byte slice to a string, and return it to HTTP. Concurrent large exports can exhaust a small VPS. Limiting the number of log lines does not bound bytes per line.

**Remediation:** bound capture at the writer, separately handle structured stdout and diagnostic stderr, stream full logs with cancellation/backpressure, and enforce sensible byte/concurrency limits. Benchmark allocations with synthetic noisy commands and large logs; no production memory or latency measurements were taken in this review.

### F13 — P2: Optional setup choices can leave every application without its required network

**Evidence:** [applications.go](internal/service/applications.go), `applicationCompose` (18); [setup.go](internal/service/setup.go), proxy network declaration and `SetupWithProgress` (182).

New application Compose files require external network `redlaunch-common`. Setup creates that network through the optional proxy project. On a clean daemon, choosing registry-only or neither core component marks setup complete without creating the required application network. The next managed application cannot start unless that network happens to exist independently. Existing fake-runner tests do not check this prerequisite.

**Remediation:** give the shared network an explicit infrastructure owner independent of optional proxy installation, or use per-application networks with deliberate proxy attachment. Cover all four first-run option combinations on a disposable Linux Docker host.

### F14 — P2: Routing assumes port 80 and hard-codes the management host port

**Evidence:** [routing.go](internal/service/routing.go), `redlaunchPublicUpstream` (15), `routingUpstream` (230), `renderCaddyfile` (237); [application.go](internal/application/application.go), routing input/model; [docker-compose.yml](docker-compose.yml), configurable `APP_PORT`.

Application upstreams contain a container hostname but no port, so the generated proxy configuration assumes the default HTTP port. Containers listening on 3000/8080 cannot express their target port through the routing model. The public management upstream is fixed at `host.docker.internal:8080`, even though the host published port is configurable. Public access therefore fails when `APP_PORT` changes.

**Remediation:** store/validate the internal upstream port explicitly, with a migration preserving existing behavior. Resolve the management endpoint from deployment configuration or use a dedicated Docker-network endpoint. Do not infer the internal port from arbitrary published-port text.

### F15 — P2: Deployment exposure and cookie policy are inconsistent

**Evidence:** [docker-compose.yml](docker-compose.yml), published app port; [config.go](internal/config/config.go), default `HTTP_ADDR`; [handler.go](internal/handler/handler.go), `secureCookie` (961), repeated CSRF cookie writes such as 4787; [INSTALL.md](INSTALL.md), SSH-tunnel and HTTPS instructions.

Compose publishes the manager on all host interfaces by default, and the standalone binary defaults to `:8080`. The documented SSH tunnel does not itself make that listener private. Session cookie security behind a proxy depends on manually setting `AUTH_COOKIE_SECURE`; CSRF cookies ignore that configuration and check only `r.TLS`. The latter remain non-Secure behind TLS termination. These are exposure/hardening issues, not a demonstrated CSRF bypass; SameSite and token checks are already present.

**Remediation:** make access mode explicit with safe bind defaults. Coordinate loopback binding with the managed Caddy path, because simply binding localhost would break the current host-gateway upstream. Centralize cookie options and CSRF validation, require a valid CSRF cookie rather than falling back to a process-wide token, and add same-origin checks appropriate to the configured proxy. Do not trust arbitrary forwarded headers.

### F16 — P2: Metrics are sampled repeatedly and mix host/container scopes

**Evidence:** [metrics.go](internal/metrics/metrics.go), defaults (19), `Collect` (132), `readSample` (170); [docker-compose.yml](docker-compose.yml); [dashboard.go](internal/handler/dashboard.go), `dashboardData` (49).

Each dashboard collection scans procfs twice with a 100 ms wait and sorts process lists. Concurrent clients duplicate this work. In the supplied container deployment, `/proc` uses the container's PID namespace and `/` is its filesystem view; the deployment does not mount a host proc/root view. The dashboard's host CPU/memory/process/disk claims therefore combine scopes and do not provide a consistent view of VPS processes/storage.

**Remediation:** choose and label the intended scope first. Cache a short-lived snapshot shared by clients, avoid overlapping samples, and serve the last valid sample with age/error metadata. If host visibility is required, configure narrowly scoped host paths deliberately and test both standalone and container modes. Do not add broad host mounts just to optimize sampling.

## Code quality and additional hardening

1. **Dependency wiring hides requirements.** `handler.New(...any)` spans roughly 300 lines of interface discovery; `handler.go` is 5,067 lines. `withAuthentication` silently permits requests when auth is absent. Production startup currently constructs auth unconditionally and fails if credentials are missing, so this is a future fail-open wiring risk, not a current production bypass. Prefer explicit required dependencies and explicit unauthenticated test construction. Split handlers by existing feature boundaries.
2. **Repeated lifecycle and CSRF logic has already diverged.** Seven job implementations and repeated form/cookie handling explain why only GitHub Actions jobs have timeout/shutdown tracking. Extract a small shared lifecycle mechanism and HTTP helpers once behavior is covered; keep domain work in services.
3. **Filesystem checks are not race-resistant containment.** `managedApplicationDirectory`, `snapshotManagedFile`, and backup input/output helpers use `Lstat` followed by path-based access. Relative bind sources are checked lexically, not against symlink targets. Risk requires a local actor or container able to modify these trees; allowing writable `.` mounts makes that precondition relevant. Consider rooted filesystem operations and directory-relative no-follow access. Preserve checks on all ancestors, not just the final component.
4. **Recovery is mostly in memory.** Atomic file rename is a useful baseline, but `writeManagedFile` does not sync the file/directory, multi-file updates have no durable recovery record, and several rollback errors are ignored or use the canceled request context. `writeManagedFileInPlace` correctly preserves bind-mounted inodes but truncates before writing. Add narrowly scoped recovery where crashes would corrupt configuration; avoid pretending SQLite transactions atomically cover Docker and files.
5. **Diagnostics lack a consistent safe structure.** User messages are often generic, while error classification and redaction inspect strings from Docker. Raw command errors can still reach logs through other paths. Use typed operation/stage errors and bounded redacted diagnostics, with synthetic-secret assertions. Do not redact by dumping whole environment maps or make operational failures completely invisible.
6. **Resource rules are not uniformly applied.** Core generators write `compose.yaml` despite the requested `compose.yml` convention. The top-level manager container has a prefix but no `redlaunch.managed=true` label and no `env_file` pair. Decide explicitly whether the manager is included in the universal managed-container rule, then align deployment/tests/docs. The existing SQLite WAL, busy timeout, parameterized queries, foreign keys, and single connection are reasonable defaults; no connection-pool expansion is justified by this review.
7. **Registry keys intentionally share registry-wide authority.** `GITHUB_ACTIONS.md` explicitly documents that a key can push any registry repository. The restricted SSH gateway is not repository-level registry authorization. Treat all connected repositories as one trust domain until authenticated namespace authorization is a product requirement. Do not present this documented limitation as a newly discovered authentication bypass.
8. **Build hygiene can improve.** The Dockerfile copies the entire context before building, invalidating dependency/source cache together. `.dockerignore` excludes primary environment filenames but does not match the broader secret/credential patterns in `.gitignore`, or exclude `.git`; local sensitive files can enter a builder/cache even though they are not copied into the final runtime stage. Prefer a minimal source context and a patched, identifiable build toolchain. Add reproducible Linux integration and vulnerability gates before considering the release pipeline complete.

## Existing strengths to preserve

The application has clear service/repository boundaries for most HTTP operations, parameterized SQL, explicit migrations, cryptographic session signatures and OAuth state, current allowlist checks on each session, escaped HTML and restrictive CSP, guarded names/paths, private environment-file permissions, streamed PostgreSQL dumps to files, and atomic replacement of most managed files. The SSH gateway restricts shell/forwarding capability and handles key revocation with recreation. These are useful foundations; targeted fixes and better failure-mode coverage are the appropriate next steps.
