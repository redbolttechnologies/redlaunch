# Follow-up review: implementation phases 0–5

Reviewed 2026-09-12 at `2abac67`, covering `4fd375a` through `2abac67` against the findings at `21e9cb6` in [CODE_REVIEW.md](CODE_REVIEW.md) and the acceptance criteria in [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

## Verdict

**The implementation makes substantial improvements, but phases 0–5 cannot yet be considered complete.** Several original P1 findings are only partially fixed, the deletion recovery implementation introduces a reproducible cross-application filesystem deletion, and the release gate fails on its own secret-scan rule.

The strongest completed work is removal of eager secret delivery, service-scoped database configuration, patched Linux builds, final Compose validation and transactional import metadata, bounded Docker output/log streaming, explicit routing ports, and centralized access/cookie handling. Shared worker tracking and per-project locks are also meaningful improvements.

Severity retains the original review's trusted-host-administrator model. The import restrictions and managed filesystem boundary are explicit application contracts even for authorized administrators. No unauthenticated authentication bypass was established.

## Verification and limits

| Check | Result |
| --- | --- |
| `make test` | Passed all 10 packages with Go 1.26.8, race detector, and uncached execution. |
| `make lint` | Passed `go vet`. |
| `make fmt-check` | Passed on the implementation. |
| `make compose-config` | Passed `docker compose --env-file /dev/null config --quiet` with Compose `v2.40.3-desktop.1`. |
| `make vulncheck-linux` | Built Linux/amd64 with Go 1.26.8; `govulncheck v1.8.0 -mode=binary` reported **No vulnerabilities found**. |
| `docker build .` | Passed. Resolved compiler image: `golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628`. |
| `make secret-scan` | **Failed**. A filename-only diagnostic identified `Makefile` as the sole private-key-pattern match. See R10. |
| Temporary diagnostic probes | Confirmed R01, R02, R04, R05, R06, and R07 using temporary files/SQLite databases, fake mutation adapters, and real Compose configuration resolution. The final probe run passed with `-race -count=1`. Probe source was removed afterward. |
| Linux Docker/systemd lifecycle, migration/adoption, destructive PostgreSQL restore, process-kill integration, visual QA | Not performed. The checked-in release target does not provide the planned lifecycle/recovery suite. |

Probes never started or removed Docker containers, networks, or volumes. The filesystem-deletion probe operated only on disposable test directories. Lease expiry was simulated with an advanced clock against two independently opened SQLite stores while a fake dump remained blocked; it was not a 31-minute real dump or an OS process-kill test. No local real environment files or application data were read for this review. The artifact scan establishes the tested binary's scanner result, not the state of an already deployed binary or all runtime image packages.

## Remaining findings and regressions

### R01 — P1: The managed-import policy still permits forbidden references and host mounts

**Original findings:** F01, F03; phase 1 import boundary.

**Evidence:** [compose_import_policy.go:46–114](internal/service/compose_import_policy.go#L46), [compose_import_policy.go:121–170](internal/service/compose_import_policy.go#L121), [compose_import_policy.go:221–242](internal/service/compose_import_policy.go#L221), [runner.go:1266–1347](internal/compose/runner.go#L1266).

The line-based policy does not inspect YAML flow mappings structurally, skips sequence mapping keys, and does not handle `label_file`. All three of these synthetic inputs passed the public importer and real final Compose validation:

```yaml
# Inside a normal block-mapped service:
label_file: /absolute/path/to/a/temporary-review-labels.txt
```

```yaml
# Inside a normal block-mapped service:
volumes: [{type: bind, source: /tmp/redlaunch-review-synthetic, target: /data}]
```

```yaml
services:
  web:
    image: busybox:1.36
    volumes: [reviewdata:/data]
volumes:
  reviewdata: {name: redlaunch-review-foreign}
```

The first input lets Compose process a file outside the project during import. The second bypasses the promised host-bind restriction. The third bypasses the restriction on explicit volume names. Ownership checking lists only volumes already labeled with the new project identity; it does not inspect every configured volume name. Consequently, a foreign explicitly named volume is outside that check even though `down --volumes` can target configured non-external volumes. Foreign-volume destruction was not exercised.

**Required correction:** use structural YAML validation or fail closed on every unsupported structural form before invoking Compose; include all supported file-reference fields. Independently check the actual configured resources before destructive operations. Final syntax validation is necessary but is not policy enforcement.

### R02 — P1: Custom-service creation bypasses the new filesystem/mount contract

**Original findings:** F03 and additional hardening item 3; phase 5 filesystem boundary.

**Evidence:** [application_container.go:36–115](internal/service/application_container.go#L36), [application_container.go:415–429](internal/service/application_container.go#L415), [managedfs.go:10–19](internal/service/managedfs.go#L10), [postgres.go:282–304](internal/service/postgres.go#L282).

The custom-service workflow still accepts `Source: "."`, defaults to a writable mount, and validates other relative sources only lexically. The new project-root rejection and symlink-resolution helpers are used by import, not this workflow. A probe successfully created a service definition with a writable `.:/data` mount using the production Compose validator.

This contradicts the documented prohibition on exposing the project root. Such a workload can replace its sibling Compose/environment files. A `./subdirectory` source that is a symlink outside the project is also not checked against its resolved location here. The new ancestor checks remain path-check-then-use operations; snapshots and writes still use ordinary path-based filesystem APIs.

**Required correction:** apply one project-aware mount policy to both creation and import, reject manager-owned files/project-root exposure, and enforce resolved containment. Complete directory-relative/rooted filesystem access for security-sensitive reads/writes instead of treating pre-access checks as race resistance. Cover both UI creation and import paths.

### R03 — P1: Restore remains non-transactional and has no defined partial-failure recovery

**Original finding:** F08; phase 3 restore integrity.

**Evidence:** [runner.go:756–758](internal/compose/runner.go#L756), [runner.go:790–814](internal/compose/runner.go#L790), [backup.go:326–382](internal/service/backup.go#L326), [backup_job.go:188–199](internal/handler/backup_job.go#L188).

HTTP decoupling is implemented, but the restore command remains `psql --set ON_ERROR_STOP=1` without a transaction covering the dump. The service checks the recorded filename and regular-file status, then streams the file directly into `psql`; it does not establish the planned supported-format/atomicity contract. Dumps are generated with `--clean --if-exists`, so successful destructive statements can commit before a later failure. A tracked-job timeout or shutdown can still interrupt the operation after those commits.

The error advises checking service state and trying again; there is no documented database recovery procedure for partially applied restores. Moving this command to a worker does not solve database consistency.

**Required correction:** implement and test a transactional restore for the supported dump format, or another explicitly defined atomic/recoverable strategy. Test failure after destructive SQL and cancellation against a disposable PostgreSQL database. Persist/report an interrupted restore outcome where recovery requires operator action.

### R04 — P1: A backup lease can expire while its owner is still running

**Original finding:** F07; phase 3 cross-process coordination.

**Evidence:** [backup.go:47](internal/service/backup.go#L47), [backup.go:1188–1205](internal/service/backup.go#L1188), [store.go:402–429](internal/store/store.go#L402), [main.go:259–291](cmd/redlaunch/main.go#L259), [backup.go:846–865](internal/service/backup.go#L846).

Leases last 30 minutes, with no renewal or fencing. Web jobs have a 15-minute context, but `backup-run` passes its signal context to the service without an execution deadline. The generated `Type=oneshot` systemd unit does not set a startup timeout. Thus a long scheduled dump can outlive its lease; the next backup, restore, or deletion can reclaim the row while the original dump still runs.

A probe blocked `RunScheduledBackup`, advanced acquisition time by 31 minutes in a second SQLite store, and successfully acquired a restore lease before releasing the original dump. The first operation then still completed normally.

**Required correction:** ensure every operation, including CLI and direct service callers, finishes before lease expiry, or renew leases with defined loss-of-ownership handling/fencing. Test actual process death and the lifetime of the database command inside the container, rather than assuming termination of a Docker CLI guarantees termination of its exec process.

The narrow status update and atomic no-overwrite backup filename publication are correctly implemented and should be retained.

### R05 — P1: Service deletion still leaves routing/timers behind and does not coordinate backups

**Original findings:** F07, F09; phase 3 deletion orchestration.

**Evidence:** [applications.go:1044–1155](internal/service/applications.go#L1044), [store.go:899–914](internal/store/store.go#L899).

The new durable orchestration applies to application deletion only. `DeleteServiceWithProgress` still stops/removes the container, rewrites Compose, and deletes the service row. It does not acquire the backup lease, disable its timer, remove service routes/reload Caddy, or retain a service deletion checkpoint.

A temporary SQLite/service-layer probe deleted a service successfully and confirmed that its routing row remained. The routing schema still has no service foreign key. Backup schedules/records cascade away, leaving the external systemd timer without its metadata and retained files without their UI records. Deletion can also interrupt a backup/restore of the same service because the application lock and backup lease are separate.

**Required correction:** route service deletion through the same coordinated, resumable cleanup guarantees as application deletion. Remove service routing records and apply Caddy changes, stop timers under the shared backup coordination mechanism, preserve the explicit backup-retention policy, and test failures at each step.

### R06 — P1: Retrying an old deletion tombstone can delete a replacement application's folder

**Original finding:** F09; regression in phase 3 recovery.

**Evidence:** [applications.go:1187–1224](internal/service/applications.go#L1187), [applications.go:1353–1359](internal/service/applications.go#L1353), [applications.go:1561–1589](internal/service/applications.go#L1561), [postgres.go:256–258](internal/service/postgres.go#L256).

A crash after `os.RemoveAll(directory)` and before the final checkpoint leaves a `folder` tombstone. A retry requires that same directory to exist before dispatching the saved stage, so it returns `ErrNotFound` instead of completing the already-finished cleanup.

More seriously, application creation does not reserve folder names referenced by incomplete tombstones. Reproduction with a real temporary SQLite store:

1. Retain the old application's `folder/running` tombstone, remove its metadata and folder, modeling the crash window.
2. Confirm retry fails with `ErrNotFound`.
3. Create a new application using the now-free folder name.
4. Retry deletion of the old application ID.

The retry succeeds and removes the **new application's folder**, while the new application's metadata remains. This was reproduced entirely within temporary directories.

**Required correction:** make already-absent filesystem stages idempotent, reserve folder/resource identity until deletion is durably complete, and verify filesystem ownership before removal. Mutations should also reject an active deletion intent, including across retries. Add crash-boundary tests for every checkpoint and folder reuse.

### R07 — P2: Literal dollar values still have an inconsistent editor representation

**Original finding:** F04; phase 2 environment round trips.

**Evidence:** [environment.go:194–223](internal/service/environment.go#L194), [environment.go:291–295](internal/service/environment.go#L291), [postgres.go:563–572](internal/service/postgres.go#L563).

No-op edits and raw-token moves are improved. However, the writer still doubles dollars for a literal replacement while the reader does not decode that representation. Adding the literal `$5` writes `COST="$$5"`; reopening the editor shows `$$5`. Changing only the digit to `6` writes `COST="$$$$6"`, adding a literal dollar unintentionally.

The probe also compared the generated value with the equivalent single-quoted Compose literal. Both resolved to the same serialized Compose configuration, confirming that the editor is exposing encoding rather than a consistent literal value. Compose config itself re-escapes dollars in its serialized output; that output must not be mistaken for the final container value.

**Required correction:** make displayed literal values and replacement encoding round-trip coherently while retaining separate raw-expression intent. Add semantic tests covering add → reopen → edit, single-quoted escapes, and dependency-sensitive moves between the two ordered environment files. Preserving bytes on a no-op alone does not close F04.

### R08 — P2: The new shared-network owner has no compatible legacy adoption path

**Original findings:** F01, F13; phases 0–2 migration and network ownership.

**Evidence:** [runner.go:386–431](internal/compose/runner.go#L386), [setup.go:163–205](internal/service/setup.go#L163), [INSTALL.md:501–639](INSTALL.md#L501).

Fresh setup now ensures the shared network for all four option combinations. Existing releases created `redlaunch-common` through the proxy Compose project, without the new `redlaunch.managed=true` and `redlaunch.owner=redlaunch` network labels. `EnsureNetwork` rejects that normal legacy network. `EnsureRegistry` now calls it before even checking an already-installed registry, so a previously working installation can fail when configuring the GitHub Actions integration.

The migration documentation inventories containers/volumes/timers and derives new project names, but does not inventory/adopt this network. It also does not provide the specific volume mappings for legacy imported project-scoped volumes. Its old-project `down --remove-orphans` example needs special handling for the originally reproduced colliding project identities.

**Required correction:** provide and test explicit ownership adoption/migration for legacy networks and volume mappings, including collision fixtures. Do not require operators to delete an in-use shared network blindly. Cover existing setup-complete installations, including those whose old optional setup omitted the network.

### R09 — P2: A single “manager” metrics label still conceals mixed resource scopes

**Original finding:** F16; phase 4 accurate scope.

**Evidence:** [metrics.go:27–35](internal/metrics/metrics.go#L27), [metrics.go:236–265](internal/metrics/metrics.go#L236), [metrics.go:281–293](internal/metrics/metrics.go#L281), [dashboard.html:31–69](internal/handler/templates/dashboard.html#L31).

The shared cache, single in-flight sample, age display, and stale fallback are implemented. The collection sources remain `/proc/stat`, `/proc/meminfo`, namespace-visible processes, and filesystem `statfs`. Under normal Linux Docker, the aggregate CPU/memory proc files report host-wide values; the process list is PID-namespace scoped and filesystem capacity follows the mounted filesystem. No cgroup accounting was added.

Labeling these “Current manager utilization” and “Current manager memory” does not make them one resource scope. Host activity outside the manager can determine its purported utilization. `METRICS_SCOPE=vps` is also accepted with no container host-view configuration check, although documentation tells operators to configure it deliberately.

**Required correction:** label each metric according to its actual source/scope, or implement container/cgroup accounting when claiming container utilization. Validate both deployment modes with representative fixtures. This is a source-established scope issue, not a new runtime measurement.

### R10 — P2: The secret scan matches itself and blocks the release gate

**Original area:** phase 5 repeatable release checks.

**Evidence:** [Makefile:29–31](Makefile#L29), [Makefile:64](Makefile#L64).

`git grep -l --cached 'BEGIN .*PRIVATE KEY'` matches the literal pattern in the Makefile recipe itself. `make secret-scan` fails on the reviewed commit. A filename-only search returned only `Makefile`; this failure is not evidence of a committed real private key. Since `release-check` depends on this target, the advertised gate cannot pass as written.

**Required correction:** recognize actual private-key delimiters without matching the scanner definition and verify both a clean tracked tree and synthetic prohibited material. Also wire the planned disposable Docker/systemd/recovery checks into an executable release target: the current target does not enable even the existing opt-in gateway integration tests or provide the new migration/restore/process-termination suite.

## Original finding resolution matrix

“Addressed” means the original defect has a coherent code fix and relevant passing tests/source evidence in this review; it does not claim unperformed real-daemon acceptance tests.

| Finding | Status | Assessment |
| --- | --- | --- |
| F01 — Compose identity/ownership | **Partial** | Every Compose command uses a derived installation/scope/resource project name. Configured foreign volume names and legacy adoption remain incomplete (R01, R08). |
| F02 — Eager secret delivery | **Addressed** | Secret page entries contain names/presence; raw secret attributes are removed. Replace-only/unchanged/empty semantics and absence assertions are present. |
| F03 — Import/process boundary | **Partial** | Known manager authentication keys are filtered from subprocesses, with regression coverage. Import and custom-mount policy bypasses remain (R01, R02). |
| F04 — Environment semantics | **Partial** | No-op tokens, comments, and raw moves are preserved; unsupported multiline input is documented. Literal decode/edit behavior remains inconsistent (R07). |
| F05 — Shared database credentials | **Addressed** | Additional per-service files load after the required shared files. Multiple-service tests and ambiguous legacy-state guards are present. Actual two-database recreation was not run. |
| F06 — Global locks/unmanaged jobs | **Largely addressed; acceptance incomplete** | Common bounded worker runtime, duplicate admission, deadlines, cleanup, shutdown, and cancellable per-project locks replace the principal defects. Jobs remain HTTP-owned/in-memory; full interrupted-operation and conflict coverage is incomplete, including R04–R06. |
| F07 — Backup coordination | **Partial** | SQLite leases, narrow status writes, and no-overwrite publication help. Live-owner lease expiry and service deletion conflicts remain (R04, R05). |
| F08 — Backup/restore HTTP lifetime | **Partial** | Tracked operations and explicit no-store/download deadline handling are implemented. Atomic restore and interrupted/partial-failure recovery are not (R03). |
| F09 — Deletion cleanup/recovery | **Partial, with regression** | Application tombstones, schedules, Caddy refresh, and service-layer key cleanup exist. Service cleanup is incomplete and folder reuse can delete replacement data (R05, R06). |
| F10 — Vulnerable local toolchain | **Addressed for verified artifacts** | Builds select Go 1.26.8; the newly built Linux artifact's vulnerability scan is clean. Existing deployed binaries were not inspected. |
| F11 — Invalid managed YAML rewrite | **Addressed for original defect** | Mapping aliases are rejected before mutation; final staged Compose validation and transactional import metadata are implemented. Policy parsing remains a separate R01 problem. |
| F12 — Unbounded output/log memory | **Addressed** | Capture is bounded at writers; structured output is limited; full HTTP logs stream with byte/concurrency limits. Synthetic tests pass; no production load benchmark was performed. |
| F13 — Optional setup network | **Addressed for fresh setup; upgrade gap** | Network ownership is independent of optional components. Legacy labels/setup state require migration coverage (R08). |
| F14 — Routing ports | **Addressed** | Explicit validated service port, migration default of 80, UI field, and deployment-derived manager port are implemented and tested. |
| F15 — Exposure/cookies | **Addressed at code/config level** | Loopback defaults, explicit HTTPS mode, common secure cookie policy, required valid CSRF cookies, and origin checks are present. End-to-end VPS reachability was not exercised. |
| F16 — Metrics cost/scope | **Partial** | Cache/coalescing/stale metadata are implemented; the mixed-scope interpretation remains (R09). |

## Cross-cutting phase completion gaps

- **Phase 0:** patched compiler and binary scanning work; regression fixtures exist. Fixture presence does not establish Docker ownership/adoption or volume-preserving migrations.
- **Phase 1:** eager secret and access-mode fixes are coherent. R01/R02 and migration gaps prevent closing the management boundary work.
- **Phase 2:** database scoping, staged validation, routing ports, and fresh-setup network ownership are implemented. R07/R08 and real Compose/recreation acceptance coverage remain.
- **Phase 3:** not complete while R03–R06 remain. Most worker state is still transient and orchestration remains in handlers. Only application deletion has a durable operation record; a process exit loses restore/creation job outcomes.
- **Phase 4:** bounded output, log streaming, and project lock separation are implemented. R09 and the specified representative allocation/unrelated-project-latency measurements remain.
- **Phase 5:** production authentication now fails fast through `NewWithDependencies`, but [dependencies.go:55–70](internal/handler/dependencies.go#L55) converts the struct back to `[]any` and calls the existing discovery constructor. `handler.go` remains 4,787 lines; typed domain operation/stage errors have not replaced string-based classification. New core filenames and build-copy caching improved, but R02/R10 remain.

Further original quality items are also incomplete: [writeManagedFile](internal/service/setup.go#L317) still closes/renames without syncing the replacement or parent directory; multi-file mutations have no durable journal and several compensation errors are discarded. Diagnostic redaction only reads project `secrets.env`, not the newly added service-specific secret files, and parses raw dotenv text separately from the environment editor ([runner.go:977–1006](internal/compose/runner.go#L977)). These should not be described as completed durability or comprehensive sanitized-diagnostic work.

**Recommended closure order:** fix deletion folder ownership and restore consistency; close both configuration-policy entry points; make lease lifetime and service deletion coordination sound; then repair environment round trips, legacy adoption, metrics labeling, and release gates. Add the missing failure-mode integration coverage before marking the phases complete.
