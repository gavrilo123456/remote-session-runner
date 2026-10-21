# Reversible mailbox handoff — 2026-10-21

## Assignment and current status

The owner stopped mailbox clients and requested a backup, removal of PRD
mailbox configuration, and activation of the same paths in QAS. After installed
validation proved that a configuration-only transfer was unsupported, the owner
explicitly selected **keep existing paths; implement supported handoff**.

**IN PROGRESS: Dev/QAS route qualification complete; actual mailbox activation
remains outstanding.** Dev and QAS select `cat-20261021-5bc6f43` on all
four hosts. PRD and both mailbox
configurations remain unchanged; no mailbox request or ACK has been published.
PRD upgrade/CAT-06 remains a separate task.

## Verified starting point and backups

Source checkout: Mac `dev`, starting at
`c12ba16ae79b947cb90d3af041e79714b7affdeb`. The unrelated untracked
`src/cmd/catalog-holdcheck/` is preserved and not used as an operational tool.

Persistent owner-only raw configuration backups were created and rehashed at:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/mailbox-routing-to-qas-20261021T192543Z/
```

| File | SHA-256 |
| --- | --- |
| `prd-mac.yaml` | `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` |
| `qas-mac.yaml` | `e00766f638ceb1f6102989ac1b2325128fda54daf6cabe63e87fd8beeb74c89a` |

Both source files still matched those hashes after read-only inspection.
Private non-active candidate validation by the installed binaries refused:

- Removing the workspace mailbox from PRD: `configured inbox set is not safe
  to install` (exit 1, retained-state read-only check).
- Adding that external path with QAS target contexts: `configuration is invalid`
  (exit 1, static configuration check).

Read-only lifecycle queries covered all eight PRD mailboxes. None had accepted
work or actionable unaccepted request pairs. Historical inputs still exist:
SlideStud had six terminal unacknowledged exchanges and Factory had three,
along with retained request/ACK shapes. Those files cannot safely become fresh
QAS ingress merely by copying configuration. Full safe lifecycle aggregates and
validation results are retained in `readiness-and-blocker.json` beside the
backups. No original artifacts were edited or acknowledged.

## Implementation boundary

The [runbook](../docs/mailbox-handoff.md) defines the supported operation.
The implementation adds explicit disabled ingress, receipt-qualified QAS roots,
temporary ownership leases and replay exclusions, scoped environment aliases,
private backup/CAS activation, restartable archive/return operations, and a
separate `runner-mailbox-handoff` operator command. It keeps the general PRD
catalog-install restriction and does not migrate either authority database.

Historical PRD files are preserved in private epochs at their original root.
The PRD default moves under the QAS ID `prd-default`; QAS's native default stays
unchanged. All original client filesystem paths are retained. Existing explicit
PRD environment names resolve through approved QAS policies only on transferred
mailboxes. PRD agents stay stopped while the old binary cannot parse the explicit
disabled-ingress configuration; original bytes are retained for supported return.

## Validation record

Focused config, ownership, importer, runtime, archive/return and CLI regressions
passed. Transaction fixtures cover five activation and six return interruption
boundaries, reload/resume, preserved directory bytes/inodes, configuration CAS,
exclusive ownership, replay exclusions and acknowledged-return guards. Focused
race checks, vet and Linux amd64/arm64 test cross-compilation passed. Their
fixture results are source evidence, not live activation evidence.

The first Mac full suite failed solely in
`TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`: the sandbox denied
`/bin/ps` against the test-owned process (`operation not permitted`). All other
packages passed. A focused JSON rerun reproduced that environmental restriction;
its log is `/private/tmp/runner-mailbox-handoff.HwGhu9/mac-locald-rerun.jsonl`.
The failed run is retained and will not be relabeled PASS. The required gate
was rerun through the already authorized Mac host shell with process-inspection
access and passed. No behavioral test or production guard was weakened.

The new operator initially failed smoke's standard help/version contract; both
surfaces now use the existing command stub and have side-effect-free regression
coverage. The final `make test`, `make vet`, `make build`, and `make smoke` all
passed on Mac. Exact logs are `mac-release-{test,vet,build,smoke}.log` in the
same private work directory. The earlier failed smoke logs are preserved.

Source S1 `65e746f46789d54915a60a22ec7a0e6eb018cd4b` was committed on Mac,
pushed to primary Gitea `origin/dev`, verified at the exact automatic GitHub
mirror SHA and fast-forwarded to all three initially clean Ubuntu checkouts.
Native source validation found a test-fixture permission problem: Go's numbered
`t.TempDir` child inherits the host umask; under `0002` it was group writable,
so the production ancestor guard correctly refused it. The failures remain in
`/tmp/runner-mailbox-handoff-gates.hNKe5i/test.log` on Linux,
`/tmp/runner-mailbox-handoff-gates.2MGHdx/test.log` on Sandbox, and
`/tmp/runner-mailbox-handoff-gates.Z5WGT8/test.log` on Factory. The correction
makes only the new fixture roots explicitly 0700 and adds a regression that
still rejects a group-writable parent. Production path guards are unchanged.
The S1 Mac bundle was built but never published or selected.

The corrected handoff suite passed on Mac under explicit `umask 0002` (7.340s).
The full Mac `make test`, `make vet`, `make build`, and `make smoke` rerun also
passed; logs are `mac-s2-{test,vet,build,smoke}.log` in the private work directory.

## Frozen source and publication — before any lane switch

Source S2 is `5bc6f4352bffd634c48297c98f283b3727d38d17`. It was pushed to
primary Gitea, verified at the exact automatic GitHub mirror SHA and pulled
with `--ff-only` into all three clean Ubuntu `dev` checkouts. All four required
gates (`make test`, `make vet`, `make build`, `make smoke`) passed on Mac and
every worker. Native worker logs and qualified controller/bundle artifacts are:

| Host | Private evidence directory |
| --- | --- |
| Linux | `/tmp/runner-mailbox-handoff-s2.1cAHxC/` |
| Sandbox | `/tmp/runner-mailbox-handoff-s2.ZzIloF/` |
| Factory | `/tmp/runner-mailbox-handoff-s2.2bOdbg/` |

The immutable release `cat-20261021-5bc6f43` was published, without activation,
on all four hosts. Common release SHA-256:
`540f4f2b75a7d2b6031dd478f41a3fe847bc52041e828de2c45aa9a2ec56ab47`.

| Host | Native bundle SHA-256 |
| --- | --- |
| Mac | `188f89d88ee426a7b73ede34d7a178c5be92191b6c29412223056216924b7cf7` |
| Linux | `6640cc2490898d69f0a0280b9ece1bcac75905ace163c83f05a3fdf8cae62265` |
| Sandbox | `e59bcc2a23dfa6c09c9e911f9e54c6efb79bfa187598e4b52ae6fb088a6980e6` |
| Factory | `bf0fe031c0dd2911fc65e718bb74879b48cb93dc3e9eb887df1fd0322b9022b1` |

Mac native artifacts were built from a clean detached S2 worktree, preserving
the unrelated canonical untracked helper. The qualified source-attested Mac
controller SHA-256 is `a50acdebac0ff4f129027cb1d8e36e36033e3dbc0660de9692940e59ff0ea660`;
the handoff operator is `15ae9b54142ed5fb5b300b7a858bb44c513d2f2d436c977e76866b4c1424b60a`;
the fixed scoped-probe tool is `98c24b67daca62068ab683430fa8b33ce8d0fc8516d2b75e21c4f55dd5d37dd9`.
Dev/QAS selection and live handoff remain outstanding at this publication point.

## Dev handoff-release qualification — 2026-10-21 20:29 UTC

Dev operation `106ea6517ec280e7fb909ee9c6fb98f7` is `released`, with 16
transitions and common final receipt SHA-256
`6242a2a53a190106eee02a2e5d2fb6ca6b9e07c6bf4db015933f8bbcdba0a996`.
All four Dev services are active on the frozen S2 release and schema 40. The
supported preflights created consistent selected-lane backups and observed
empty slots, queues and resumable jobs before switching Linux, Sandbox,
Factory, then Mac. The single protected receipt prefix was extended and synced
only onto stopped services; no receipt was deleted, rewritten by hand or forked.

All seven fixed scoped checks passed: Mac-local and each worker's direct mTLS
and queued SSH route. Every accepted probe ended with exit 0, complete output
and closed teardown; ordinary admissions were separately observed fenced while
held. Result aggregates and their digest are `dev-seven-routes.json` and
`dev-seven-routes.sha256` in the private work directory. Final released receipt
copies were synchronized before the supported worker/Mac starts.

Ordinary API verification also covered all seven routes. The first harness
stopped on Linux queued CLI's `event_history_complete_this_read=false`, despite
successful output and closed teardown. A read-only fetch of the same command
proved contiguous events 1–4, exact output and terminal success; it was not
resubmitted. The corrected harness allows bounded read-only convergence, and
three fresh queued verification commands passed. Original failure and follow-up
remain separate in `dev-normal-routes.log`, `dev-normal-queued-followup.log`,
`handoff-native-route-dev-0918bad2e5ea/`, and
`handoff-queued-followup-dev-5525d65fbd07/`. New timeout/output-cap enforcement,
managed-session and fault probes were NOT RUN for this focused handoff route
qualification; no new live claim is made for those unchanged execution paths.
Source/transaction/interruption regressions are recorded separately above.

At 20:29 UTC all four supported Dev inventories were empty; readiness reported
S2, and storage/cleanup counters were zero. Captured PRD/QAS process identities
and configuration hashes matched the pre-qualification baseline. Selected-lane
restarts and receipt synchronization did not restart peer lanes. Host postflight
files are `dev-HOST-postflight.json` and `dev-mac-final-{work,ready}.json` in the
private work directory. No actual filesystem mailbox request or ACK was used;
mailbox acceptance and normal project load remain NOT RUN. Clients stay stopped.

## QAS handoff-release qualification — 2026-10-21 20:41 UTC

The Dev closeout `c492eee5bb42b72a222261a13f453e0106bdff52` was pushed to
Gitea. The first mirror check still observed `30a31c0`; remote progress stopped
until the automatic mirror matched `c492eee` exactly. All three clean workers
then fast-forwarded before QAS preflights. No rebuild or dependency resolution
occurred during QAS promotion: release and native bundle hashes above are
identical to Dev.

QAS operation `28e49a5f0a4edb6a35ed73f3944ee907` is `released`, with 16
transitions and common final receipt SHA-256
`4069f1b93c55eba7f7c87e3d25cb9a2a5ed75c26953b009558559054d826e546`.
Fresh consistent backups, full empty-work reports and at least 536,870,912
bytes remaining headroom preceded the ordered Linux/Sandbox/Factory/Mac
switches. Scoped admissions stayed fenced through seven fixed route checks;
all had exit 0, complete output and closed teardown. The single receipt prefix
was extended serially and synchronized through supported stopped-service
operations. No historical receipt was removed or manually edited.

After release, all seven ordinary QAS API routes also passed, with bounded
read-only event follow-up where needed. Evidence is `qas-seven-routes.json`,
`qas-normal-routes.log`, and `handoff-qas-normal-qas-a93af90a9b91/`. As in Dev,
this is focused handoff route qualification; additional live limit enforcement,
managed-session and fault probes were NOT RUN, and no new claim is made for
those unchanged paths. The source/transaction regressions remain separate.

At final pre-handoff postflight, every Dev/QAS service was active on S2 and
schema 40, QAS inventories were empty, and storage/cleanup counters were zero.
Dev process IDs captured before QAS switching were unchanged; PRD process IDs
and configuration hashes matched the starting baseline. Both Mac mailbox raw
configurations still match their original backups. Host evidence is
`qas-HOST-postflight.json` and `qas-mac-final-{work,ready}.json`. The next action
is the supported initial Mac PRD/QAS stop, private handoff package preparation,
and guarded activation. No filesystem mailbox request or ACK has been published;
clients remain stopped and mailbox/normal-project acceptance is NOT RUN.

Fresh PRD read-only inspection distinguished three local terminal-lost retained
session/command pairs from three older uncertain SlideStud remote intents.
The latter have terminal `indeterminate` mailbox responses but no proven remote
delivery; Sandbox PRD has no matching jobs by their IDs or exact idempotency
keys and no active execution work. Mac has no active sessions, queued/running
commands, resumable jobs, pending finalizations or accepted mailbox exchanges.
The operation preserves these uncertainties and unreleased reservations; it
does not recover them, claim clean reconciliation or require zero historical
`queued_intents` metrics. Original files and identities will be archived/fenced.

Before live activation: complete source review, source gates and the required
Gitea-primary/exact-GitHub-mirror/clean-worker delivery; qualify the exact native
candidate through the applicable Dev/QAS release gates; repeat fresh stopped
client, work, configuration and filesystem-identity checks; run only the supported
operator path. Record actual release/bundle/tool hashes and terminal postflight
before calling the mailbox switch complete. No mailbox request or ACK is to be
published by maintenance.

An early preparation attempt while both installed executors were still active
refused with `selected lane lifecycle lock is held`, before producing a package
or changing anything active. The runbook now makes the existing lock contract
explicit: fresh read-only idle checks, supported initial Mac PRD/QAS stops,
process/listener stop proof, then preparation under both lifecycle locks. No lock
is deleted or bypassed. Consistent backups still precede all configuration and
mailbox changes. QAS must first receive the qualified handoff runtime.
