# Reversible mailbox handoff — 2026-10-21

## Assignment and current status

The owner stopped mailbox clients and requested a backup, removal of PRD
mailbox configuration, and activation of the same paths in QAS. After installed
validation proved that a configuration-only transfer was unsupported, the owner
explicitly selected **keep existing paths; implement supported handoff**.

**IN PROGRESS: implementation and validation. No installed configuration,
service, selector, mailbox request or ACK has changed for this handoff yet.**
The accepted Dev/QAS release remains `cat-20261021-ebc4446`; source work does not
qualify or deploy a replacement. PRD upgrade/CAT-06 remains a separate task.

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
