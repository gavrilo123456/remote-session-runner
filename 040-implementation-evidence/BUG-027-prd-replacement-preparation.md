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

## 2026-10-28 — replacement PRD engineering checkpoint

### Source delivery and sealed candidate

The replacement-address correction is delivered at
`a38cb0057ed2c4ee7782473b8131ac95af4deab4`; primary Gitea and the automatic
GitHub mirror both report that exact `dev` SHA. The Mac worktree is at that
commit. The previously accepted `rr-1e6c47721d-f83e3be7` remains unchanged.

Native source gates for `a38cb0057ed2c4ee7782473b8131ac95af4deab4` completed:

| Host | Result |
| --- | --- |
| Mac (`darwin/arm64`, Go 1.27.1) | Full `make test`, vet, build and smoke passed on the final post-commit run. An earlier sandbox-restricted attempt failed and remains **NOT PASS**; its capture is preserved separately. |
| Sandbox (`linux/arm64`, Go 1.27.1) | Full `make test`, vet, build and smoke passed. |
| Factory (`linux/amd64`, Go 1.27.1) | Full `make test`, vet, build and smoke passed. |
| Replacement Linux (`linux/amd64`, Go 1.27.1) | At that earlier checkpoint, focused changed-package tests, P025/P101 regressions, vet, build and smoke passed; the full Linux suite was **NOT PASS**. See the 2026-10-28 native full-suite rerun below. |

Private captures and source receipts remain outside Git in
`/private/tmp/bug027-prd-source-evidence-20261028/`. The four-host release
build is `772fbc26a2470590b58d4d4a91685d20`; it is sealed, all four bundles
are published, and its status is ready without activation:

- Release `rr-a38cb0057e-877d49e3`, digest
  `ce7350c7f338f1679b5f67ae9c7531acbe80174894a4946aa6eee87366f14b74`.
- Provenance SHA-256:
  `74e0bdec901f8959d5ac6a0c7ed548b0051d2b784951b7183e6454cb37144d76`.
- `linux-host`: `043d74e48724c0f14090c667b648bc94c08e30a0f73f4726d54962bda7697dfa`.
- `sandbox-host`: `7be317e4ad63d1890b8e39123264eec6dbfa85b98a988c7e1f792030d618008b`.
- `factory-sandbox`: `d0b7f9864ad923188dac12d86149e15a293cf544e356942c45ccdc9d14a7e8ee`.
- `mac-control-plane`: `16971e20e9b8179c581aa68a5169a4530dd706134a6cfc52ad8bc1f5bc8e1d99`.

Build provenance records Linux CGO disabled, Mac CGO enabled, and the full
source-revision linker argument for each platform. `runner-release status`
returns no Dev or QAS acceptance record for this release. No selector or
service changed. Since `a38cb...` changes startup code, these bundles still
require Dev then QAS acceptance before PRD use.

The public-command fixture sequence passed for legacy PRD adoption,
completion, interrupted switching/reconciliation, and reverse rollback.
Focused replacement-route and config-validation tests also passed. Those
fixtures cover an existing PRD namespace and the paired address validator;
they do **not** exercise first installation of an absent PRD namespace on the
replacement worker.

### Old host and replacement observations

An explicit-key SSH attempt to old PRD Linux `ubuntu@129.151.232.40` returned
`Permission denied (publickey)`. The Mac key fingerprint is
`SHA256:ocEk05Q1tTyNNyIKFc9G/s63510slZ05jGLWd1qBktg` (RSA 4096). It has not
been installed or changed by this checkpoint. This access failure does not
establish that the old service is stopped, fenced, empty, backed up, or free
of retained work.

SSH to replacement `ubuntu@79.72.94.59` succeeded. The read-only host check
found `runnerd.service` not found/inactive, zero MainPID and ControlPID, and
no PRD configuration, database, `current` selector, selector-operation
directory, cohort-receipt directory, or PRD backup directory. The service
root itself exists for the replacement's non-PRD setup. The earlier complete
namespace-absence observation classified its permitted toolchain-only state;
that is not an installed or usable PRD baseline. The direct spot query found
no PRD 8443 listener. A complete process and unit scan remains required
immediately before any later write.

The read-only command
`promote-to prd all --release rr-a38cb0057e-877d49e3 --hosts-file <prepared-profile> --plan --json`
did not produce a plan. It failed on replacement Linux when the existing
`catalog-status --host linux-host --lane prd` command could not establish the
PRD identity without its absent PRD config. No preflight, backup, hold,
receipt, service, or selector was changed.

### Bootstrap and route/trust boundary

The current `catalog-prd-backup` command creates a consistent host-local
backup for an already configured PRD. It includes an online SQLite snapshot,
binary/configuration/secret files, and the systemd artifact. It is not a
cross-host importer or initial namespace installer; it does not snapshot the
workspace tree. `PRDServiceController.Status` also refuses a missing systemd
unit, while standard PRD preflight expects a valid config and existing
database. No current command can safely prepare the replacement namespace
from the old host's state.

The intended paired V2 address change is exact: Mac's `linux-host` queued
host and direct URL become `79.72.94.59` and
`https://79.72.94.59:8443`; Linux's direct bind and public URL become
`10.0.0.228:8443` and `https://79.72.94.59:8443`. Service/database paths,
mailbox identities, credential references, trust paths, policy, and other
fields stay fixed. The replacement server certificate must chain to the
currently trusted PRD CA and contain the exact replacement IP SAN. The
replacement SSH host key must use its independently verified pin. No
certificate, config, `known_hosts` entry, or service file changed here. The
existing route-rebind commands are Dev/QAS V3-only and are not a PRD route
transition.

Before importing anything, old-host access is required to measure the actual
source schema, config/secrets, service artifacts, workspace contents,
selector, work inventory, and ownership. A supported bootstrap must use a
verified consistent online backup, preserve history and workspace data,
validate the new TLS/SSH identities, initialize only the proved-absent
replacement namespace, leave it stopped, and verify imported schema/data
without starting a worker or migrating the source. It must record
interruption/resume/rollback and never delete source state or expose copied
secrets. Those actual source facts are unavailable while SSH is rejected; no
bootstrap implementation or PASS claim is recorded.

### Old-host SSH restoration action

On the Mac, derive the public half in a pipeline and verify its full SHA-256
fingerprint before copying the one-line public key to the provider console.
The private key is never printed or sent:

```sh
# Mac — verify first; only then print the public-key line for the console
set -eu
pub=$(/usr/bin/ssh-keygen -y -f /Users/tomasz.walczuk/.ssh/remote-session-runner)
fingerprint='SHA256:ocEk05Q1tTyNNyIKFc9G/s63510slZ05jGLWd1qBktg'
actual=$(printf '%s\n' "$pub" | /usr/bin/ssh-keygen -lf - -E sha256 | /usr/bin/awk 'NR == 1 {print $2}')
test "$actual" = "$fingerprint"
printf '%s\n' "$pub"
```

The expected complete fingerprint is
`SHA256:ocEk05Q1tTyNNyIKFc9G/s63510slZ05jGLWd1qBktg`. Copy the public-key
line from the first command only after its fingerprint matches exactly.

At the old instance provider console, append that exact line for `ubuntu`;
preserve all existing authorized keys and host-key files. The console helper
verifies the pasted key's fingerprint before touching `authorized_keys`, and
backs up an existing regular file with metadata before appending. Replace the
placeholder with the single line printed on the Mac:

```sh
# Old PRD Linux provider console — root or authorized sudo shell
set -eu
key='<PASTE-EXACT-PUBLIC-KEY-LINE>'
fingerprint='SHA256:ocEk05Q1tTyNNyIKFc9G/s63510slZ05jGLWd1qBktg'
file=/home/ubuntu/.ssh/authorized_keys
actual=$(printf '%s\n' "$key" | ssh-keygen -lf /dev/stdin -E sha256 | awk 'NR == 1 {print $2}')
if [ "$actual" != "$fingerprint" ]; then
  echo 'Pasted public key does not match the full expected fingerprint.' >&2
  exit 1
fi
sshdir=/home/ubuntu/.ssh
sudo test ! -L "$sshdir"
if sudo test -e "$sshdir"; then
  sudo test -d "$sshdir"
else
  sudo install -d -o ubuntu -g ubuntu -m 0700 "$sshdir"
fi
sudo test ! -L "$file"
if sudo test -e "$file"; then
  sudo test -f "$file"
  if sudo ssh-keygen -lf "$file" -E sha256 | awk -v fp="$fingerprint" '$2 == fp {found=1} END {exit found ? 0 : 1}'; then
    echo 'This fingerprint is already present; inspect its authorized_keys options and do not append a duplicate.' >&2
    exit 1
  fi
  backup="${file}.pre-bug027-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  sudo test ! -e "$backup"
  sudo cp -a -- "$file" "$backup"
else
  sudo install -o ubuntu -g ubuntu -m 0600 /dev/null "$file"
fi
if sudo test -s "$file"; then
  last_byte=$(sudo tail -c 1 "$file" | od -An -t x1 | tr -d '[:space:]')
  if [ "$last_byte" != 0a ]; then
    printf '\n' | sudo tee -a "$file" >/dev/null
  fi
fi
printf '%s\n' "$key" | sudo tee -a "$file" >/dev/null
```

For an existing file, the helper leaves its ownership and mode unchanged and
stores a metadata-preserving backup beside it before append. If the fingerprint
is already present, stop and inspect the existing authorized-key options; do
not append a duplicate. Do not overwrite `authorized_keys`, remove an existing
key, change server host-key files, or repoint the working replacement alias.
After the provider change, verify the old host against its existing pins and
retry a bounded noninteractive SSH metadata command.

### 2026-10-28 — replacement Linux full source gates and SSH helper

The BUG-027 handoff in `060-messages/990-message.md` explicitly requires
Linux's own full tests, vet, build and smoke and says Factory substitution was
declined. The later `AGENTS.md` Factory substitution is explicitly scoped to
CAT-06; it does not amend this BUG-027 requirement. No later BUG-027-specific
approval replacing Linux's full suite was found. Therefore the suite was run
on replacement `linux-host` (`79.72.94.59`), using the exact verified `dev`
checkout at `f692e98bc6f2652c9c40992ce260fc0d3286764b`, a clean worktree, and
the pinned Go 1.27.1 Linux/AMD64 toolchain. That commit has no `src/` or
`Makefile` diff from the sealed source commit
`a38cb0057ed2c4ee7782473b8131ac95af4deab4`.

| Gate | Command / settings | Result |
| --- | --- | --- |
| Full suite | `GOTOOLCHAIN=local make test`; `GOMAXPROCS=2`, `GOFLAGS=-p=1` | **PASS**, exit 0; 317.41 seconds real. |
| Vet, build, smoke | `GOTOOLCHAIN=local make vet build smoke`; same settings | **PASS**, exit 0; 52.46 seconds real. The final capacity query ran after the gates. |

Captured output is outside Git in
`/private/tmp/bug027-prd-source-evidence-20261028/`:

- Full suite SHA-256:
  `2e06e2f2f309cd0f4a526f70be84660e743ed97fa4097b57ff1b66ccfe3c882c`.
- Vet/build/smoke SHA-256:
  `2a247e183a6d76c518e96bf4c1777a07da5da202bfda4b571e31421052a845ee`.
- Post-gate `/` free bytes: `40,606,142,464`.

The prior interrupted Linux suite remains **NOT PASS** in its original
evidence. This successful rerun does not erase or relabel that history. The
Mac key's public half was freshly fingerprinted through a pipeline; it
returned the expected full fingerprint
`SHA256:ocEk05Q1tTyNNyIKFc9G/s63510slZ05jGLWd1qBktg`. The provider-console
procedure above now checks the pasted key before any write, rejects duplicate
key fingerprints, and preserves a metadata-bearing copy of an existing
`authorized_keys` before append. It also rejects symlink paths and ensures a
pre-existing file ends on a line boundary before appending. Both documented
shell snippets pass `bash -n`; neither was executed. No provider-console
action or old-host authorization change has occurred.

### Historical checkpoint superseded by the 2026-10-28 owner decision

The following bootstrap/import notes and next steps record the earlier
assumption that the retired Linux worker's source data was needed. The owner
later retired `129.151.232.40` from all lanes and authorized a fresh PRD
namespace on replacement `79.72.94.59`; no old-host SSH, data import, or old
work transfer is required. These notes remain historical and are not current
gates.

### PRD bootstrap/import work remaining

The Linux source-gate and disposable public-command suite are now complete
for the current source snapshot. Existing fixtures cover PRD cohort adoption,
interrupted switching, receipt reconciliation, rollback, replacement-address
validation, and complete-absence refusal. They do not prove a state-preserving
import from the retired PRD worker. The source-specific import contract still
depends on facts that only the old host can provide: its exact installed
binary and service hashes, configuration and schema, complete retained-work
inventory, workspace contents and file types, and TLS/SSH identities.
Without those facts, a fixture can test a guessed package shape but cannot
prove that the package preserves the real PRD state or that the installed
predecessor can use the replacement route. Keep the old namespace and #514
untouched. Once old-host read access returns, capture those facts and backups,
then bind the supported export/import fixture to the actual source manifest
before implementing or activating the corresponding importer.

Dev acceptance for the current route-changing service candidate remains
unrecorded; QAS acceptance is consequently also unrecorded. The prior owner
confirmation said all PRD clients were stopped but did not identify a Dev
publisher pause/control. No Dev or QAS service operation has begun on the
strength of that statement.

The evidence/helper update was delivered in `76b2021909fcc6a541289092e58e16f8810e6c59`
and `ca659b3f11b8920f0451b7c0ef7d8ee1891f8723`. Both primary Gitea
`origin/dev` and the automatic GitHub `github/dev` mirror resolve to
`ca659b3f11b8920f0451b7c0ef7d8ee1891f8723`. The Mac checkout and clean
Ubuntu checkouts on replacement Linux (`79.72.94.59`), Sandbox
(`132.226.205.205`), and Factory (`145.241.255.66`) were fast-forwarded and
verified at that exact commit. The first primary push succeeded remotely but
its local tracking-ref write was sandbox-denied; both remote refs were verified
at `76b2021`, then the tracking ref was corrected with a narrow authorized
Git ref update. The follow-up push and all final refs then completed normally.

### #514 proposal — unchanged and not authorized for execution

The exact recorded identities remain session
`sess-dc857a66bccb38298a6b0b26485588c5`, command
`cmd-2872a83c6411e7838bad02ca687fbfbd`, job
`job-55049ec48ecff3747d09f97d96a18783`. Its result is lost, with incomplete
output and unconfirmed teardown. Do not recover it, release capacity, replay
work, or publish an ACK in this checkpoint. Before any exact-pair owner
decision, refresh the complete Mac PRD inventory, prove authority and runtime
ownership/stop state, and take a fresh consistent backup. Current evidence
still says `safe_to_resume=not_established` and
`recovery_authorized=false`.

### Next operations

1. Restore old-host SSH using the provider-console procedure above. Until
   then, the old host's admission, service, database, retained work and
   backup state remain **unknown**, not empty or stopped.
2. Capture the old host's fresh inventory and consistent backups, then
   resolve only work required by the supported guards.
3. Implement and fixture-test the missing operator-only PRD replacement
   bootstrap and PRD-specific route/trust activation against the actual
   source format. Keep the old host state and sealed
   `rr-a38cb0057e-877d49e3` unchanged.
4. Obtain Dev publisher pause/no-new-work control before qualifying this
   service-code candidate in Dev; pause and drain QAS separately before its
   acceptance. Do not use those qualifications as PRD work/backup evidence.
5. Prepare release-bound PRD approval, backup/rollback and recovery only
   after the old-host and bootstrap gates pass. No PRD deployment, mailbox
   reroute, recovery, request, or ACK has occurred.

### 2026-10-28 — owner pause confirmation and fresh PRD checkpoint

At 18:47:33 UTC, Tomasz Walczuk confirmed that Dev, QAS and PRD clients and
publishers are paused and remain inactive until he explicitly resumes them.
This is owner-confirmed pause evidence, supplemented by the fresh inventories
below; it is not evidence that no publishers are configured. All services
remain active, and no lane admission holds had been applied at this checkpoint.

Fresh `catalog-work-inventory` observations at 18:52:21–18:52:46 UTC used the
clean controller revision `864697eda8685d50ca4a700153eb40b9248122fc` on Mac
and clean worker checkouts at that same revision. Every Dev and QAS inventory
on all four machines was empty. Sandbox and Factory PRD inventories were
empty. Mac PRD retained exactly one active command slot and one active
session reservation, with no active session, queued/running command, resumable
job, or pending recovery finalization. This is the already-recorded #514 pair;
its lost result, incomplete output and unconfirmed teardown are unchanged.

Replacement Linux PRD was freshly confirmed absent at
`2026-10-28T18:54:45.125341893Z`, evidence SHA-256
`b148a0a9ee71fe0d277049f2e2b399837b3dc780cbe3889fe9917c56ee14cc56`. The
service unit is not found, no listener or unexpected Runner process exists,
and the config, database, selector journals, cohort receipts, backups and
lifecycle lock are absent. Replacement Dev/QAS remain installed and unchanged.
The fresh PRD admission discovery showed no current cohort hold on Mac,
Sandbox or Factory; Linux PRD has no namespace from which a PRD receipt could
be read.

`promote-to prd all --release rr-a38cb0057e-877d49e3 --plan --json` remains
blocked at the replacement host: the existing `catalog-status --host
linux-host --lane prd` command rejects the absent PRD configuration. The
configuration-level fresh-route validator has since been delivered at
`3c58f18e65e1e1b3b021a61cd2bfbcc5fb7f41f1`; the fresh-install bootstrap and
supported paired Mac route/SSH-pin transition are not yet implemented or
live. No PRD service was stopped, no admission hold was written, and no
configuration, selector, database or mailbox path was changed.

Release `rr-a38cb0057e-877d49e3` remains sealed and unchanged (digest
`ce7350c7f338f1679b5f67ae9c7531acbe80174894a4946aa6eee87366f14b74`). Its
Dev acceptance `74dffd3837e1e8f4f7183306bec7fa40` and QAS acceptance
`ff2f40e39ff26b5bcab74e259fd4323a` both remain PASS for the same four bundle
hashes. The delivered configuration-level fresh-route validator passed
focused tests and `go vet ./src/internal/config`; this is source validation
only, not bootstrap or PRD acceptance.

The exact #514 recovery remains separately unauthorized. A supported retry
requires a new complete inventory, fresh consistent backup, stopped-service
and process-ownership proof, then the sealed host bundle's `catalog-prd-recover`
with `--lost-pair sess-dc857a66bccb38298a6b0b26485588c5:cmd-2872a83c6411e7838bad02ca687fbfbd`.
The tool preserves its lost result and history, does not replay or ACK, and
must release only this pair's reservation. Any additional work, selector,
configuration, schema, backup, process or runtime mismatch must halt. The
exact lost-pair recovery decision is not granted by the pause confirmation and
remains a separate owner decision if the pair blocks deployment.

### 2026-10-28 19:24 UTC — all-lane pause reaffirmed and route candidate helper

The owner reaffirmed that Dev, QAS and PRD clients and publishers are paused.
This confirms the requested publisher pause; it does not establish that the
services are stopped, that catalog admission holds are active, or that no
publishers are configured. The latest measured inventory remains the
19:17–19:19 UTC observation above. Refresh the complete inventories and
operational guards immediately before any live write or restart.

Added `BuildFreshPRDReplacementMacRoute`, an in-memory V2 configuration helper
that changes only `remote_hosts.linux-host.queued_bridge.host` and
`remote_hosts.linux-host.direct_endpoint.url`. It validates the exact retired
route before building and validates the resulting V2 PRD document and full
semantic diff. The helper edits only those two YAML scalar nodes; it does not
write the configuration, change `known_hosts`, restart services, or certify
the replacement SSH/TLS identities.

The first focused regression exposed that marshaling the whole typed V2
document materializes omitted zero-value structs and produces an invalid
configuration. The helper was corrected to edit the validated YAML syntax
tree, then parse and compare the complete typed document. Focused config tests
and the affected `config`, `laneinstall`, and `runner-lane` test packages pass
on Mac Go 1.27.1. This is controller/configuration source validation only.

The supported replacement PRD bootstrap, host-local fresh config/identity
installation, paired Mac configuration and SSH-pin transition, release-bound
approval, and PRD deployment are still incomplete. No service, config, route,
selector, certificate, key, database, mailbox path, reservation or ACK was
changed. #514 remains the exact separately unauthorized Mac PRD retained pair.

### 2026-10-28 — owner pause reaffirmed and route-validator delivery

At 19:05 UTC, the owner reiterated that Dev, QAS, and PRD clients and
publishers are paused. This supplements the earlier owner/control/effective-
time record; it remains a publisher-state confirmation, not proof that
services are stopped, admissions are fenced, or no publishers are configured.
The 18:52 UTC inventory and replacement namespace evidence remain the latest
measured runtime observations; they were not refreshed by this message.

The V2 fresh-route configuration validator and its focused regressions were
delivered at `3c58f18e65e1e1b3b021a61cd2bfbcc5fb7f41f1` through primary Gitea,
exact GitHub mirror verification, and clean fast-forward validation on
replacement Linux, Sandbox, and Factory. Those checks do not implement the
replacement PRD namespace bootstrap, issue/install TLS or SSH identities, or
perform the paired Mac route transition. No service/runtime mutation followed
the commit.
