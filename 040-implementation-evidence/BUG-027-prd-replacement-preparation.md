# BUG-027 PRD replacement preparation checkpoint

**Status:** preparation only; no PRD deployment, service change, selector
change, configuration activation, mailbox mutation, request publication, ACK,
or recovery has occurred.

## Existing accepted release

The already accepted service release remains unchanged:

- Release: `rr-1e6c47721d-f83e3be7`
- Source: `1e6c47721d98ba5af01145ff2e2ed44eb8639030`
- Digest: `c21b2e07763d05b5ffa77b26c0fc30ef0918135c995248df8d7374eb9adc9acd`
- Dev acceptance: `db7efa3c78056440d580f8671f9c18fc`
- QAS acceptance: `a353cb72e3f72df38cb3e64e74349841`

Those acceptances do not apply to the replacement-route source changes below.

## Replacement route source correction

The controller/runtime configuration resolver previously accepted only the
retired Linux public/private addresses. The source correction adds a narrowly
bound replacement identity: Linux/AMD64, account `ubuntu`, host ID
`linux-host`, private address `10.0.0.228`, and public endpoint
`https://79.72.94.59:8443`. Other canonical PRD worker routes remain unchanged.

The paired V2 configuration validator allows only these route changes:

- Mac PRD: `linux-host` queued host and direct HTTPS URL.
- Linux PRD: direct HTTPS bind and public URL.

It rejects a one-sided transition and changes to mailbox paths, identities,
credentials, trust paths, service/database paths, policies, or any other
decoded configuration field. It does not install or activate either
configuration and does not assert that a replacement TLS certificate or SSH
host key is suitable.

This code is in `laneinstall`, which is imported by the Runner service startup
path (`runnerd` calls `CatalogPlanFromConfiguration`). Therefore it changes
service bundle bytes and requires a new release accepted in Dev and then QAS
before any PRD use. The accepted release above must remain immutable.

## Source checks

At the current Mac source worktree:

- Focused `config` and `laneinstall` tests: PASS.
- Full repository `make test`: PASS with the pinned Go 1.27.1 toolchain.
- The initial sandboxed full test attempt was **NOT PASS** because an existing
  `runnerlocald` test could not execute `/bin/ps` (`operation not permitted`).
  The same full test target passed when run outside that workspace restriction;
  no test was skipped and no process-control setting was changed.
- `make vet build smoke`: PASS.
- `gofmt -d` and `git diff --check`: clean.

Native Ubuntu checks, Git delivery, release sealing, Dev/QAS acceptance of the
successor, and all live PRD work remain pending.

## Old PRD host access and prepared profile

The old PRD Linux endpoint remains `129.151.232.40`; a fresh explicit-key SSH
attempt returned:

```text
ubuntu@129.151.232.40: Permission denied (publickey).
```

This is an access failure, not evidence of an empty inventory or stopped
service. Its database, configuration, binaries, backups, selectors, service
state, work inventory, and retained work remain unverified.

The existing Mac promotion host profile still targets
`ubuntu@129.151.232.40`. A private mode-`0600` candidate profile was prepared
at:

`/private/tmp/bug027-prd-replacement-prep.kiWuy0/runner-release-hosts.json`

Its SHA-256 is
`5a4d239fc0839ceeb4a7dc0e56d04fe2b56ceda097fe1b67ef4ba10c1c1cff58`. The
only change from the active profile is `linux-host.ssh_target` to
`ubuntu@79.72.94.59`; the active profile, aliases, known-hosts files, and SSH
authorizations were not changed. The Mac known-hosts file already contains
verified replacement-host entries; the current replacement endpoint must
still be freshly verified as part of the later operational preflight.

## #514 recovery proposal — not executed

Exact recorded identities:

- Session: `sess-dc857a66bccb38298a6b0b26485588c5`
- Command: `cmd-2872a83c6411e7838bad02ca687fbfbd`
- Job: `job-55049ec48ecff3747d09f97d96a18783`

The preserved result is `lost`, with incomplete output and unconfirmed
teardown. The held reservation/slot and history remain intact. The prior
read-only report did not establish `safe_to_resume` and set
`recovery_authorized=false`. No recovery or capacity release is authorized by
this preparation checkpoint.

Before proposing the supported exact-pair recovery for execution, refresh the
complete Mac PRD inventory, prove exact authority and runtime ownership,
confirm process exit/cleanup under the supported recovery guard, and take a
fresh consistent backup. The required owner decision is whether to authorize
that exact lost-pair disposition after those checks, or retain the hold and
defer cutover. Do not infer success or replay the command.

## Remaining gates

1. Restore explicit SSH access to old host `129.151.232.40` without replacing
   its host keys or removing existing authorized keys.
2. Obtain its fresh service/work/ownership inventory and consistent config,
   database, binary, and service-definition backups; verify admission
   fencing/stopping before cutover.
3. Inspect the actual old and replacement PRD configuration, database schema,
   server certificate SAN/chain, and SSH host key. Prepare the supported
   state-preserving bootstrap and route/trust transition; no certificate or
   configuration change has been made.
4. Complete source implementation and native checks, deliver through primary
   Gitea and the exact GitHub mirror, seal a successor, then accept the same
   bytes in Dev → QAS.
5. Prepare fresh PRD approval, backup/rollback inputs, and exact #514 recovery
   disposition before using the promotion toolkit.
