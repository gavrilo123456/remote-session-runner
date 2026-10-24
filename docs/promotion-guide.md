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

Execution additionally requires the named publisher owner/control and explicit
pause/no-new-accepted assertions. The wrapper verifies those flags against live
catalog/host evidence; a command-line assertion does not substitute for real
owner control. Acceptance checks direct mTLS, queued SSH, Mac-local execution,
exit/event/output/teardown, limits, capacity and peer-lane identities. A missing
gate leaves the cohort held and is not a pass.

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
