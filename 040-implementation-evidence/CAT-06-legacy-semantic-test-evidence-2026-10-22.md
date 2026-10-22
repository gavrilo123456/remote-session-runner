# CAT-06 legacy semantic test evidence — 2026-10-22

## Source identity and scope

The fixture and public-command harness results below are bound to source
revision `9113b44fc436014c4017522ca2c2432ed66c79b9`. This record supports the
typed legacy rollback compatibility inputs; it does not establish a production
backup, live rollback, selector change, retained-work recovery, or PRD
deployment acceptance. All fixture databases, receipts, journals, binaries,
and configurations were disposable test data. No retained production script
was started, and no mailbox request or ACK was published.

The installed PRD components on all four hosts report source revision
`00c1ca6e7694842c35d398583b3f26fea59e3613`. The earlier read-only semantic
check built from that exact source revision used
`store.OpenExistingRestartPreflightReadOnly` against each actual schema-40 PRD
database and passed without migration or database-byte changes; the full host
identities and results are in the [compatibility record](CAT-06-legacy-compatibility-2026-10-22.md#read-only-check-using-the-installed-source-revision).
On Linux, Sandbox, and Factory the installed 00c1ca6 services also remain
active against their schema-40 databases. Mac's unsuffixed services remain
stopped because its current disabled-ingress/QAS handoff configuration must
stay in place. The Mac fixture verifies rollback keeps that configuration and
stopped state; it does not claim that the old Mac service can start with the
current configuration.

## Rollback semantics exercised

The suite `catalog-prd-legacy-semantics-v1` covers these exact checks:

1. `current-database-read-without-migration` — the schema-40 fixture is read
   through the compatibility read path; the schema version and database bytes
   remain unchanged.
2. `retained-history-unchanged` — rollback leaves the database sentinel and
   existing receipt/journal history intact.
3. `launcher-resolves-preserved-legacy-binaries` — restored legacy launcher
   bytes execute the preserved fixture binary and resolve its expected marker.
4. `queued-wrapper-resolves-preserved-legacy-binaries` — restored queued SSH
   wrapper bytes execute the preserved fixture bridge and resolve its expected
   marker.
5. `rollback-preserves-current-database` — rollback restores launch artifacts
   without restoring or replacing a database.

`TestPRDLegacyRollbackRestoresLauncherAndQueuedWrapperWithoutChangingDatabase`
was included in passing current-revision suites on Mac, Sandbox, and Factory.
`TestPRDLegacyCompatibilityReadsSchemaWithoutMigration` and
`TestPRDMacLegacyRollbackKeepsDisabledQASHandoffConfigAndStoppedService` also
passed on Mac. `TestCatalogPublicCommandSequenceSubprocess` drives the public
`runner-lane` command grammar through a test-only backend over disposable
PRD-shaped state; it covers initial legacy adoption, successful completion,
an interrupted selector switch, public reconciliation, and reverse rollback
with synchronization after each host. The fixture checks database sentinels,
receipt/journal retention, and stopped-service state.

## Exact-revision platform results

- **Mac:** the focused CAT-06 tests listed above passed at `9113b44` using the
  repository's shared Go 1.27.1 caches. The same source revision's full
  `make test`, vet, build, and smoke gates had passed in the preceding Mac
  validation run.
- **Sandbox:** from 2026-10-22 07:27:44 UTC to 07:32:17 UTC, Go 1.27.1 ran
  `TMPDIR=/tmp GOTMPDIR=/tmp SQLITE_TMPDIR=/tmp GOMAXPROCS=2 GOFLAGS=-p=1
  make test`, followed by `make vet`, `make build`, and `make smoke`; all passed
  at `9113b44`. The updated `runner-lane` package passed in 2.808 seconds and
  `laneinstall` in 40.939 seconds. `vmstat` showed 1% steal near start and 0%
  at completion. `/dev/sda1` free space was 1,020,657,664 bytes before and
  948,707,328 bytes after; caches were preserved and no cleanup was attempted.
- **Factory:** from 2026-10-22 07:23:08 UTC to 07:26:20 UTC, Go 1.27.1 ran
  `TMPDIR=/var/tmp GOTMPDIR=/var/tmp SQLITE_TMPDIR=/var/tmp GOMAXPROCS=2
  GOFLAGS=-p=1 make test`, followed by `make vet`, `make build`, and
  `make smoke`; all passed at `9113b44`. `runner-lane` passed in 1.834 seconds
  and `laneinstall` in 32.626 seconds. The sampled final CPU steal was 0%.
- **Linux:** the corrected targeted regressions pass at `9113b44`. The earlier
  `no tests to run` result used the nonexistent `DoesNotSourceOnce` suffix; the
  actual test name is `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`.
  Rerun with `GOMAXPROCS=2 GOFLAGS=-p=1 TMPDIR=/tmp GOTMPDIR=/tmp
  SQLITE_TMPDIR=/tmp` passed in 0.421 seconds. The exact prior slow case,
  `TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`, passed all
  three subtests in 20.001 seconds with a 5-minute timeout. CPU steal was 1%
  near start and rose to 88% at completion. The one current-revision full
  `make test` attempt started at 07:46:04 UTC with restricted concurrency. It
  reported the cached `catalog-probe-once` and `lane-preflight` packages and
  `runner-lane` passed in 38.402 seconds, then made no further package progress
  before host load rose to 19.67 and CPU steal reached 93% at 07:48:59 UTC.
  The exact task-owned Go test PID 4133338 was sent SIGINT. `make test` exited
  2; this is **INTERRUPTED / NOT PASS**, not an assertion failure. At
  07:50:13 UTC no test process remained, all three Runner services were active,
  the checkout was clean at `9113b44`, and `/tmp` had 15,610,396,672 bytes
  available. Vet, build, and smoke did not run because the test step stopped
  the `set -e` sequence. Earlier full-suite interruption at
  `6ff1d0252358d2fb1d46d2a887b5bd4d7832631c` and timeout at
  `4ec62835c4c341e82a31fd68921e8e64d790d54e` remain separate non-passing
  results.

## Interpretation

These passing isolated semantics support the format and contents of the
host-specific compatibility inputs. The inputs still must be checked against
each host's actual installed PRD binary-set, configuration, and schema using
`runner-lane catalog-compatibility-check`. That read-only check does not create
a backup. `catalog-host-preflight` is an operational preflight that creates a
backup and is not a dry run; it remains gated.
