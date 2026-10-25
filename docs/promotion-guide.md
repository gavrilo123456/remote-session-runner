# Build and promote a sealed Runner release

`build-release` freezes one verified source commit, builds the four native host
bundles, seals one immutable release identity and publishes those bytes to the
existing catalogs. It does not stop services or select a lane. `promote-to`
uses that same release without rebuilding. The routine order is **Dev → QAS →
owner-approved PRD**. Source checks, catalog publication, selector activation,
whole-cohort functional acceptance and mailbox ownership are separate states.

The commands run from the Mac coordinator checkout. Ubuntu build and control
helpers run on their matching existing hosts over pinned SSH. The default
owner-only host profile is
`~/Library/Application Support/RemoteSessionRunner/runner-release-hosts.json`
(mode `0600`); workflow records are outside Git at
`~/projects-runtime/runner-release-operations/`. Profile entries bind each
canonical host ID to its repository, Go 1.27.1 binary, runtime root, SSH
identity and verified known-hosts file. PRD profiles also name the reviewed
legacy rollback compatibility input. Never commit that file or its credentials.
SSH private-key files may use mode `0400` or `0600`; group/world-readable or
writable keys are rejected, and existing key permissions should be preserved.

## Freeze and publish once

On the Mac, inspect a source plan first. `--branch` captures a primary Gitea
tip and requires the GitHub mirror to match; adding `--commit` requires that
full SHA to be in the captured branch history. `--commit` alone requires a
matching published primary/mirror branch as its reachability witness.

```sh
# Mac — from the clean canonical dev checkout
build-release --branch dev --plan --json
build-release --branch dev --commit <full-commit-sha> \
  --source-test-receipt <mac-owner-only-receipt.json> \
  --source-test-receipt <linux-owner-only-receipt.json> \
  --source-test-receipt <sandbox-owner-only-receipt.json> \
  --source-test-receipt <factory-owner-only-receipt.json> \
  --release-id <immutable-release-id> --json
runner-release status --build <build-id> --json
runner-release status --release <immutable-release-id> --json
```

A non-plan build requires four canonical mode-`0600` source-test receipts, one
per native host. Each binds the full source SHA, host, GOOS/GOARCH, Go 1.27.1
binary hash, test-evidence hash and timestamp; all four gates (`test`, `vet`,
`build`, `smoke`) must pass under `native-full-v1`. The build checks the receipt
platform and toolchain hash against the bundle it actually builds, then stores
receipt digests in the immutable build/release provenance. Keep receipt files
and their referenced logs outside Git if they contain host-specific details.
Example record shape (replace every placeholder with measured values):

```json
{
  "format_version": 1,
  "source_commit": "<40-hex-source-commit>",
  "host_id": "factory-sandbox",
  "goos": "linux",
  "goarch": "amd64",
  "toolchain": "go1.27.1",
  "toolchain_binary_sha256": "<64-hex-toolchain-hash>",
  "coverage_profile": "native-full-v1",
  "gates": {"test": "passed", "vet": "passed", "build": "passed", "smoke": "passed"},
  "evidence_sha256": "<64-hex-captured-gate-evidence-hash>",
  "tested_at_utc": "<RFC3339-UTC-time>"
}
```

`--plan` resolves the published refs without creating a build or requiring
receipts. `--resume <build-id>` reuses the receipts already frozen in that
build; if receipt files are supplied again, they must hash to the same refs.

The build record pins the captured source, controller, toolchain settings,
four native bundle manifests, artifact hashes, provenance and publication
progress. `--resume <build-id>` resumes the same source/build identity and
verifies existing bytes; an immutable release ID cannot be rebuilt with
different bytes. A successful command says **ready without activation**.

## Plan and advance a lane

Run a read-only plan before the first mutation. Even a named-host request
prepares and holds the entire selected four-host cohort, stops that lane across
all four machines, captures inventories and consistent backups, and
synchronizes one cohort receipt. Only the next selector step is limited to the
named host. The documented switch order is Linux → Sandbox → Factory → Mac;
peer lanes remain unchanged.

```sh
# Mac — the JSON plan lists all four affected service groups and lane effects
promote-to dev linux-host --release <immutable-release-id> --plan --json

# Mac — after the selected lane's actual publisher owner confirms its pause
promote-to dev linux-host --release <immutable-release-id> \
  --operation-id <new-32-hex-operation-id> \
  --publisher-owner <owner> --publisher-control <specific-pause-control> \
  --publishers-paused --no-new-accepted --json

# Mac — continue the exact operation through the remaining ordered hosts,
# whole-cohort acceptance, supported release and source-marker closeout
promote-to dev all --release <immutable-release-id> --resume <operation-id> \
  --publisher-owner <owner> --publisher-control <specific-pause-control> \
  --publishers-paused --no-new-accepted --json
```

After every host selector and journal is verified, the controller first
advances and synchronizes the cohort receipt to `verifying`, then records the
per-host verification transitions with a sync after each host. It starts the
selected services under the still-held admission fence only after those
receipt checks pass, and only then creates one-time functional probe scopes.
Probe preparation is intentionally rejected while the synchronized receipt is
still `switching`; do not hand-edit receipts or bypass this transition.

For a new QAS operation, the same release must already have a passed Dev
acceptance receipt binding its digest, profile and all four bundle hashes.
PRD similarly requires the QAS acceptance and a current owner approval file
bound to the exact release digest, host set, predecessor hashes, measured
backup/recovery package and valid operational window:

```sh
# Mac — read-only package inputs; --plan never creates backups or stops services
promote-to qas all --release <immutable-release-id> --plan --json
promote-to prd all --release <immutable-release-id> \
  --approval-file <reviewed-owner-approval.json> --plan --json
```

The release-bound approval digest covers the release identity, predecessor
selectors and bundle hashes, configuration/schema/work-inventory identities,
backup and rollback procedures, and the required capacity policy. It excludes
the exact free-byte count because that value changes during normal operation;
the read-only plan and every fresh preflight still measure and enforce actual
headroom immediately before writes.

If every host reports the same held `complete` receipt from an earlier
promotion, a new operation may replace that hold only after the coordinator
validates the receipt history and rechecks each selected release/bundle,
configuration, schema, stopped service, empty work inventory and backup. The
new receipt is held across the cohort first. The old receipt remains an
admission fence until each host records its supported supersession evidence
and that history is synchronized. Partial, divergent, switched-but-incomplete,
or otherwise unverified holds remain blocked for their dedicated recovery
path. Never delete or edit the old receipt to make a new plan pass.

Execution additionally requires the named publisher owner/control and explicit
pause/no-new-accepted assertions. The wrapper verifies those flags against live
catalog/host evidence; a command-line assertion does not substitute for real
owner control. Acceptance checks direct mTLS, queued SSH, Mac-local execution,
exit/event/output/teardown, limits, capacity and peer-lane identities. A missing
gate leaves the cohort held and is not a pass.

PRD's initial host preflight is online: it takes the consistent database
backup while the selected service remains active, under the catalog publication
lock. It must not acquire the lane lifecycle lock, which the running service
holds for its lifetime. The preflight compares service state, empty-work
inventory, selector, configuration and schema before and after the backup. The
later synchronized cohort fence acquires both catalog and lifecycle locks,
revalidates the evidence, and only then stops the selected service. A lifecycle
lock conflict during online preflight is a controller defect to fix; do not
work around it by stopping PRD before its admission hold.

## Inspect, resume, or recover

```sh
# Mac — all commands below inspect owner-only workflow records
runner-release status --lane dev --operation <operation-id> --json
runner-release status --acceptance-lane dev --release <immutable-release-id> --json
runner-release branch-sync --lane qas --operation <accepted-operation-id> --json
runner-release rollback --lane dev --operation <failed-operation-id> \
  --publisher-owner <owner> --publisher-control <specific-pause-control> \
  --publishers-paused --no-new-accepted --json
```

`--resume` inspects operation history, host receipts, selectors, journals,
services and inventories before repeating an uncertain effect. Continuation
evidence is four-hour bounded, separate from the unchanged host receipt, and
does not extend a PRD maintenance authorization. Rollback uses supported
reverse selector journals; it does not restore an older database. If runtime
acceptance passed but branch synchronization failed, retry only
`runner-release branch-sync`; do not redeploy or roll back a healthy cohort.

### Exact retained-lost recovery before PRD promotion

If a fresh PRD inventory contains only an exact set of terminal-lost command
pairs, recover those pairs before the zero-work promotion preflight. This is
not a force switch. Confirm the selected-lane publishers are paused and no
new work is being accepted; preserve the complete inventory and command/event
history. Every live command slot and session reservation must correspond to
one supplied pair, with no active session, queued/running command, resumable
job, or pending recovery finalization. Do not include uncertain intents or
other lanes.

For an already catalog-selected PRD service, bind recovery to the exact
selected predecessor identity. Create a fresh consistent backup first, then
use the supported lane service stop and the candidate's sealed recovery
component. The service must remain stopped; the selector and database schema
must remain unchanged. The candidate recovery component verifies runtime
ownership and releases only the listed lost pairs while preserving their lost
results and events. It neither installs nor selects a service binary, starts
services, replays scripts, or restores a database.

```sh
# Mac — current synchronized controller checkout; exact selected Mac PRD identity
go run ./src/cmd/runner-lane catalog-prd-backup \
  --host mac-control-plane --release-id <accepted-release-id>
go run ./src/cmd/runner-lane prd-stop --host mac-control-plane
go run ./src/cmd/runner-lane catalog-prd-recover \
  --host mac-control-plane \
  --release-id <accepted-release-id> \
  --release-sha256 <accepted-release-digest> \
  --bundle-sha256 <sealed-mac-bundle-digest> \
  --expected-selected-release-id <currently-selected-release-id> \
  --expected-selected-release-sha256 <currently-selected-release-digest> \
  --expected-selected-bundle-sha256 <currently-selected-mac-bundle-digest> \
  --backup-dir <backup-directory> \
  --backup-manifest-sha256 <backup-manifest-digest> \
  --lost-pair 'sess-EXACT-LOST-1:cmd-EXACT-LOST-1' \
  --lost-pair 'sess-EXACT-LOST-2:cmd-EXACT-LOST-2'
```

For an intentionally unselected legacy selector, omit all three
`--expected-selected-*` flags; the recovery command continues to require the
selector to remain unselected. Partial identity arguments, selector drift,
changed backup targets, schema drift, extra work, a live service, or failed
runtime ownership checks refuse recovery. If the command fails or its result
is interrupted, keep the service stopped and inspect the fresh inventory,
selector, backup, and recovery markers before any retry. Once recovery is
verified, collect new all-host preflights and proceed through the normal
release-bound PRD approval, hold, switch, and acceptance sequence.

## Current scope and further reading

PROMO-01 through PROMO-07 cover source implementation, fixture qualification,
documentation and Git delivery. They do not claim live Dev/QAS/PRD acceptance.
PROMO-08 is a separately instructed learning walkthrough. No service restart,
selector activation, project mailbox request or ACK publication is implied by
these source commands or examples.

See the [PROMO implementation plan](../040-improvments/040-dev-qas-prd-three-machine-lanes/030-build-release-and-promotion-cli-implementation-plan.md),
[catalog design](../040-improvments/070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md),
[CAT implementation plan](../040-improvments/070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md),
[lane runbook](lanes.md), and
[Git delivery contract](../AGENTS.md#commit-to-primary-verify-the-mirror-then-fast-forward-the-test-hosts).
