# CAT-03 — Legacy adoption, launch paths and cohort maintenance

**Status: source, fixture, host source-check and Git delivery gates COMPLETE.**
Frozen source revision (`S`): `c381d8efc719a2cc22fc09158f2e6f8c07fccd2b`.
No release was published or selected; no installed lane, service, mailbox,
runtime configuration, database, retained work or host installation changed.

## Scope and implementation

CAT-03 implements explicit copy-and-verify adoption for qualified legacy
Dev/QAS bundles, catalog-aware launch-path validation, refusal by legacy
installers to overwrite catalog-selected binaries, and a four-host maintenance
receipt/probe model. The source audit found:

- Mac Dev/QAS LaunchAgents and Linux systemd units already execute through
  `current/bin`; Linux's generated entrypoint still accepts only relative
  `releases/<sha256>` selectors.
- The Dev/QAS forced SSH wrapper already resolves `current/bin`; its artifact
  remains in the stable lane `bin` directory. The unsuffixed production bridge
  wrapper and Linux/Mac legacy installers had separate old-layout assumptions.
- CAT-02 selectors preserve validated legacy targets, but CAT-03 had to add
  explicit bundle adoption, a whole-lane cohort receipt, and scoped probe
  admission.

The delivered source adds legacy Dev/QAS bundle adoption, catalog-aware
service-entrypoint and SSH-bridge resolution, fail-closed legacy PRD
installer/backup tools, four-host cohort receipts and switch ordering, startup
admission holds, and one-time bounded direct-mTLS maintenance probe admission.
PRD service artifacts use a separately guarded selector path; generic Dev/QAS
mutation cannot select PRD. The PRD switch acquires the catalog and PRD
lifecycle locks before stopping the service. Queued-wrapper replacement is
restricted to the current service account and exact expected prior hash.

## Validation at S

All four machines ran `make test && make vet && make build && make smoke` with
HEAD at source revision `S`. On Mac, only the CAT-03 evidence and plan/design
Markdown for the evidence-only closeout were uncommitted; source and test files
matched `S`. Mac also ran `git diff --check` on that closeout diff:

- `make test` — **PASS** (exit 0).
- `make vet` — **PASS**.
- `make build` — **PASS**.
- `make smoke` — **PASS**.
- `git diff --check` — **PASS**.

Each Ubuntu command ran from a clean `dev` checkout at exactly `S`:

| Machine | `make test` | `make vet` | `make build` | `make smoke` |
| --- | --- | --- | --- | --- |
| `linux-host` | PASS, exit 0 | PASS, exit 0 | PASS, exit 0 | PASS, exit 0 |
| `sandbox-host` | PASS, exit 0 | PASS, exit 0 | PASS, exit 0 | PASS, exit 0 |
| `factory-sandbox` | PASS, exit 0 | PASS, exit 0 | PASS, exit 0 | PASS, exit 0 |

Factory's test command used a task-owned file-backed `TMPDIR` under
`/home/ubuntu/.cache` because its `/tmp` is a small tmpfs. The task directory
was removed by the command trap after completion. Shared Go caches were kept.

The initial Mac source-test attempt could not inspect a test-owned process with
`/bin/ps` in the restricted shell. The suite passed when rerun in the authorized
Mac shell and passed again at `S` below.

Initial Ubuntu runs failed in the PRD artifact-replacement fixture. Revision
`267b1b3` had unconditional GID checks, which were narrowed at `cbd27d9` to
match the artifact's optional group declaration; the tests still failed. Stage
diagnostics at `fbe77c4` and `9cc8731` identified the actual failure: the
temporary test parent was group/world-writable (`0775`), which the production
guard correctly rejected. The fixture now explicitly uses mode `0700`; the
production guard remains intact. Source revision `S` also adds a direct
regression proving that an unspecified group is not constrained while an
explicitly declared group is still checked. Final full gates above ran after
these corrections at `S`; the earlier failed runs are preserved as history
and are not counted as acceptance.

## Git delivery

- Source revision `S`: `c381d8efc719a2cc22fc09158f2e6f8c07fccd2b` on `dev`.
- Primary Gitea `origin/dev`: exact `S` verified with `git ls-remote`.
- Automatic GitHub `github/dev`: exact `S` verified with `git ls-remote`.
- `linux-host`, `sandbox-host`, and `factory-sandbox`: clean `dev` checkouts at
  exact `S` after GitHub `pull --ff-only`; each host's final test/gate run used
  that revision.

Gitea pushes succeeded. The restricted Mac shell could not update its local
`refs/remotes/origin/dev` tracking ref; primary and mirror refs were verified
directly and agree with `S`. No source or evidence files were copied to Ubuntu.

## Live acceptance and limitations

CAT-03 live publication/adoption and service activation are **NOT RUN**. CAT-04
Dev, CAT-05 QAS and CAT-06 PRD are distinct operational units. Mailbox requests
and ACKs remain unpublished under the owner instruction, so mailbox-specific
acceptance is **NOT RUN**. External project publishers have not been resumed.

The owner has authorized the scoped CAT-06 catalog migration and Runner
restarts, conditional on successful Dev then QAS acceptance and the plan's
fresh quiescence, backup, compatibility, cohort-hold, route and postflight
guards. No PRD action is implied by CAT-03 closeout.
