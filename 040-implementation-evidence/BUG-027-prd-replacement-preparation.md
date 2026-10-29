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

### Earlier 2026-10-28 checkpoint (19:05 UTC) — owner pause reaffirmed and route-validator delivery

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

### 2026-10-28 19:27 UTC — route-helper delivery

The controller/configuration correction was delivered as Mac `dev` commit
`99837926a8856f4ab4ef6ba46298619f3e8bc49d` to primary Gitea. The automatic
GitHub `dev` mirror was verified at the exact same SHA. Replacement Linux,
Sandbox and Factory worktrees were clean on `dev` at
`31717a014db2bfefb07ac23d2e9db5aa53a05c4d` before pull; each fast-forwarded
from GitHub and ended clean at `99837926a8856f4ab4ef6ba46298619f3e8bc49d`.

Mac Go 1.27.1 `go test ./src/internal/config ./src/internal/laneinstall
./src/cmd/runner-lane`, `go vet` for those packages, and `git diff --check`
passed. No native Ubuntu validation or full service-bundle gate was run for
this controller/configuration-only helper. It does not qualify a successor
service release or prove live route identity.

### 2026-10-28 19:41 UTC — exact #514 recovery completed

The owner's later exact-pair recovery authorization supersedes the historical
“not authorized” status above. The owner-confirmed Dev/QAS/PRD publisher pause
remains in force; it was not used as proof that Mac PRD was stopped. A fresh
pre-stop detailed Mac PRD inventory at `2026-10-28T19:29:19Z` (completed
`19:29:20.364234Z`, SHA-256
`f5f856897abe4d55bc06d8588f56aac16266fe3e90aef232b3228e0fc4d0aa16`)
identified exactly the authorized lost pair:

- Session `sess-dc857a66bccb38298a6b0b26485588c5`.
- Command `cmd-2872a83c6411e7838bad02ca687fbfbd`.
- Job `job-55049ec48ecff3747d09f97d96a18783`.

It reported one live command slot and one live session reservation, no active
session, queued/running command, resumable job, or pending recovery
finalization. The command and session were already `lost`, and the output was
incomplete. No other retained pair appeared in that inventory.

The supported `runner-lane prd-stop --host mac-control-plane` completed. A
fresh `prd-status` then returned `active=false`; the fresh selector still
resolved to `rr-20261024-43a4e675` (release SHA-256
`093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`, Mac
bundle SHA-256
`a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062`). A
stopped-state attempt to use `catalog-work-inventory --details` returned
`retained_work_inventory_unavailable`; no partial rows were accepted as
evidence. The recovery command independently ran its exact sealed-bundle
preflight under the Mac lifecycle lock, including LaunchAgent/socket stop
checks, complete authority and exact-pair validation, runtime ownership audit,
and guarded recovery/postflight.

A new supported consistent backup was created while PRD remained stopped:

- Directory: `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261028T194037.013280000Z`.
- Manifest SHA-256: `310a4e07449454d7e79c92a0fb31e8715e3d47465e824dd45e60e2fddbdb6723`.
- Database SHA-256: `5551b1ad34aa0c2170649ac4060e5cc2e24c2efd136fe2544c4bf71ed9352c78`.
- Schema: 40; free bytes after backup: `109143687168`; required selector reserve: `67108864`.

`catalog-prd-recover` passed for only the exact pair above, using the
`runner-locald` recovery component from the unchanged selected release. Its
report is preserved privately at
`/private/tmp/rsr-bug027-prd-recovery-20261028T1932Z/recover-514-20261028T1940Z.json`
(SHA-256
`680755f93d4bddb542d63509163899b14d5b7b3e8914017da40aa18a8bf5e883`). It
reports `recovered_lost_pairs=1`, schema 40 before and after, runtime ownership
verified, service still stopped, selector unchanged, and zero active sessions,
slots, reservations, queued/running commands, resumable jobs or pending
finalizations afterward. The postflight work inventory is empty; historical
local/remote intent records remain and are not treated as active capacity.

A private read-only comparison against the just-created consistent backup
confirms that the exact session, lost command, lost job, accepted local intent,
and all 88 command events are unchanged. The command remains `lost`, exit code
remains unknown, output remains incomplete, and final event sequence remains
88. The event-stream digest is unchanged at
`8b16b93b79f319493009528caeae3a6f162fdc18ca05ea5c0cec8a15aee6a373`. Only
the selected command slot and session reservation changed: both now carry
their supported stop/cleanup confirmation and release timestamp
`2026-10-28T19:41:04.175242Z`. The comparison record is preserved privately at
`/private/tmp/rsr-bug027-prd-recovery-20261028T1932Z/state-preservation-514.json`
(SHA-256
`a3112580a91abbca18e4d22298729f4b14b653c398fff00bd06d89cf317f9d51`). No
script was replayed, no ACK was published, no database history was edited, and
PRD was not restarted. This recovery does not constitute PRD deployment or
acceptance.

The remaining PRD replacement work is unchanged: implement and test the
supported first-install/bootstrap and paired route/trust transition for
replacement Linux, verify candidate acceptance for any service-byte change,
prepare the release-bound PRD approval and backups, deploy through the
promotion toolkit, and pass all eight PRD acceptance gates before reopening
admissions or clients.

### 2026-10-28 20:39 UTC — accepted release and pause refreshed

The owner confirmed that Dev, QAS, and PRD clients/publishers are paused. This
is recorded as an owner confirmation only; it does not prove service state,
admission fences, or that no publishers are configured.

Read-only `runner-release status` on Mac Go 1.27.1 reconfirmed sealed release
`rr-1e6c47721d-f83e3be7`, source
`1e6c47721d98ba5af01145ff2e2ed44eb8639030`, digest
`c21b2e07763d05b5ffa77b26c0fc30ef0918135c995248df8d7374eb9adc9acd`, and
matching captured primary/mirror refs. The current route-changing candidate
`rr-a38cb0057e-877d49e3` remains sealed at digest
`ce7350c7f338f1679b5f67ae9c7531acbe80174894a4946aa6eee87366f14b74`.
Its Dev acceptance `74dffd3837e1e8f4f7183306bec7fa40` and QAS acceptance
`ff2f40e39ff26b5bcab74e259fd4323a` each report PASS for all eight gates and
bind the same four bundle hashes. The sealed bytes remain unchanged.

The current closeout, retained-work issue, promotion guide, and operations
runbook were read. No live PRD operation, admission hold, route/configuration
change, service action, mailbox publication, or ACK publication was performed
for this checkpoint.

### 2026-10-28 20:59 UTC — owner confirms all-lane client pause

The owner confirmed that Dev, QAS, and PRD are paused. Treat this as the
current owner confirmation for client/publisher activity only. It does not
establish current service stop state, catalog admission holds, empty work, or
that no publishers are configured. Fresh host and work observations remain
required immediately before each dependent operation. QAS state and all
mailbox ownership remain unchanged.

### 2026-10-28 21:22 UTC — current release acceptance rechecked

Using the pinned Go 1.27.1 toolchain and the local promotion store, read-only
`runner-release status` reconfirmed both the historical release named in the
989 starting point and the route-changing candidate currently required for
the replacement route:

- Historical `rr-1e6c47721d-f83e3be7`, digest
  `c21b2e07763d05b5ffa77b26c0fc30ef0918135c995248df8d7374eb9adc9acd`: Dev
  operation `db7efa3c78056440d580f8671f9c18fc` and QAS operation
  `a353cb72e3f72df38cb3e64e74349841` each report all eight gates passed.
- Current route candidate `rr-a38cb0057e-877d49e3`, digest
  `ce7350c7f338f1679b5f67ae9c7531acbe80174894a4946aa6eee87366f14b74`:
  Dev operation `74dffd3837e1e8f4f7183306bec7fa40` and QAS operation
  `ff2f40e39ff26b5bcab74e259fd4323a` each report all eight gates passed and
  bind the same four per-host bundle hashes recorded above.

The current candidate remains sealed and unchanged. These are source/release
acceptance records, not current PRD installation or acceptance evidence. No
PRD service, selector, configuration, admission, mailbox, or retained-work
state was changed during this read-only verification. The owner's latest
confirmation says Dev, QAS, and PRD clients/publishers remain paused; live
service state, admissions, and inventories still require fresh observation.

### 2026-10-28 21:43 UTC — selected replacement absence planning correction

The owner again confirmed Dev, QAS, and PRD are paused. Record that as the
client/publisher pause only; no inference is made about services, admission
holds, or installed publishers.

Controller work now lets the read-only PRD planner record the replacement
Linux namespace as absent only after the exact host-local absence observer
passes. It does not synthesize a selector, configuration, schema, database,
or backup identity. The absent-selected-lane path is limited to planning;
ordinary snapshots and peer-lane rechecks still reject it. The first-install
operation itself is not implemented by this change.

Focused checks passed on the Mac with Go 1.27.1:

- `go test ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config`
- `go vet ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config`
- `make build smoke`
- `git diff --check`

The new unit regression confirms that selected PRD absence is represented
only for the verified replacement Linux profile and only through the
explicit absence report. An initial test run exposed that the peer-lane
recheck helper could enter that selected-absence path; the guard was narrowed
to reject `peer == selectedLane`, and the complete focused package suite then
passed. No live state was changed.

Before these changes reach the replacement checkout, its current
`catalog-namespace-absence` command rejects selected-PRD scope, so the
read-only `promote-to prd all --plan` attempt exited nonzero at that explicit
guard. This is a source-delivery prerequisite, not evidence that the
replacement namespace is partially installed. No release was activated and
no PRD hold, backup, selector, configuration, or service was changed.

Remaining bootstrap work is still required: a tested supported first install
must create the replacement's PRD configuration, fresh authority database,
TLS identity, catalog selection, and stopped service artifacts with rollback
to an absent/stopped state. Then obtain the current release-bound PRD
approval, recheck all host inventories and capacity, and only afterward use
the toolkit for PRD mutation and the eight-gate acceptance. Mailbox
acceptance remains `NOT RUN`.

### 2026-10-28 21:48 UTC — absent-namespace capacity observation

After delivery of `ef37b5bb6727e7cbe666791b2cf5de0e7c67e358`, the read-only
PRD plan passed replacement namespace absence and failed at capacity because
the backup directory correctly does not exist yet. The correction keeps the
fixed canonical backup path in the report, but for the exact absent
replacement Linux PRD namespace measures the existing `/home/ubuntu/.local/share`
filesystem parent and binds that measurement to the repeated absence evidence
digest. It does not create the PRD root or backup directory. Existing lanes
still measure their actual backup roots.

Mac Go 1.27.1 checks for this controller correction passed:

- `go test ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config`
- `go vet ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config`
- `make build smoke`
- `git diff --check`

The capacity regression accepts only the exact parent path with the same
replacement-absence digest and rejects an unbound path or digest. The route
config builder and fixed-identity TLS CSR/signing helpers are also fixture
tested; they produce bytes only and do not install or activate files. No
service bundle is changed by these controller-only changes.

The remaining blocker is now beyond read-only planning: the repository still
has no supported PRD first-install operation that writes the validated config,
fresh database, trust identity, service artifacts, and catalog selector as a
single recoverable, admission-held sequence. The PRD plan is not an execution
or acceptance result. No PRD files, services, admissions, or selectors were
changed, and release-bound approval has not been prepared or approved.

### 2026-10-28 21:51 UTC — absent-namespace admission check correction

The delivered read-only PRD plan passed replacement absence and capacity,
then failed because `catalog-cohort discover` cannot run without a PRD
configuration on the replacement. The planner now treats admission as empty
only after a fresh second absence report validates the exact selected PRD
namespace, including the absent receipt root, and matches the original
evidence digest. Partial or changed namespace evidence remains a hard failure;
the planner does not infer empty admission from a command error.

Mac Go 1.27.1 checks passed:

- `go test ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config`
- `go vet ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config`
- `make build smoke`
- `git diff --check`

No live host or service state changed. The next read-only plan attempt must
wait for this controller commit to reach all three clean worker checkouts.
The end-to-end PRD first-install operation remains absent and blocks any
cohort preparation, backup, selector change, service start, or acceptance.

### 2026-10-28 21:57 UTC — owner pause reconfirmed; fresh PRD plan

The owner confirmed that Dev, QAS, and PRD are paused. This records the
client/publisher pause assertion only. It does not prove service stop state,
catalog admission holds, zero accepted work, or that no publisher is
configured.

After controller delivery at `0d3792d1d58097ea4e1ede41db546ff56168e9e6`, the
read-only command
`promote-to prd all --release rr-a38cb0057e-877d49e3 --plan --json` completed
with `planned_no_effects`. It collected a fresh Linux absence report at
`2026-10-28T21:57:04.515018796Z`; the replacement namespace remains absent,
the service unit is not found, the direct listener is absent, the process scan
is complete with zero unexpected Runner processes, and its work inventory is
empty. Measured free space is 40,426,082,304 bytes against the plan's
567,108,864-byte minimum.

The same plan reports current PRD work inventories empty on all four hosts.
Sandbox and Factory PRD services are active; Mac PRD is stopped. No selected
host reports a backup or maintenance hold yet, and `can_switch` is false on
all four hosts. Current measured space is 1,631,944,704 bytes on Sandbox,
34,868,895,744 on Factory, and 112,964,091,904 on Mac, each above the
567,108,864-byte preflight minimum. Capacity must be measured again after any
backup and immediately before writes.

The plan binds release `rr-a38cb0057e-877d49e3`, digest
`ce7350c7f338f1679b5f67ae9c7531acbe80174894a4946aa6eee87366f14b74`, and
backup/recovery package digest
`457b24fa236dfae29c298b954c398d21d0c9f49b2e730cd2679e0e2572de73f0`. The
release remains Dev/QAS accepted for all eight gates with unchanged bundle
hashes; this plan is neither PRD acceptance nor a live PRD operation.

No service, selector, configuration, database, admission, backup, mailbox,
request, or ACK was changed. Remaining gates before PRD mutation are the
supported first-install/bootstrap and paired route/trust transition for
replacement Linux, the current release-bound owner approval and operational
window, followed by fresh backups, holds, stop/process proofs, and all eight
PRD acceptance gates. Clients remain paused.

### 2026-10-29 06:56 UTC — checkpoint reconciliation

Re-read `060-messages/989-message.md` in full before continuing. The existing
sealed candidate `rr-a38cb0057e-877d49e3` remains unchanged at source
`a38cb0057ed2c4ee7782473b8131ac95af4deab4`, release digest
`ce7350c7f338f1679b5f67ae9c7531acbe80174894a4946aa6eee87366f14b74`.
Its Dev operation `74dffd3837e1e8f4f7183306bec7fa40` and QAS operation
`ff2f40e39ff26b5bcab74e259fd4323a` both record `passed=true`, the same four
bundle hashes and all eight functional gates passed. These records are
preserved; any source successor still needs its own Dev then QAS acceptance.

A fresh detailed Mac PRD inventory completed at
`2026-10-29T06:56:27.288544Z`, report SHA-256
`97b15d37a97a7a7ef1d3a585593170fed9ca291917d1f337d47630dcd20ffdf0`.
It is retained privately at
`/private/tmp/bug027-prd-inventory.RnCShb/mac-prd.json`. It identifies the
selected `rr-20261024-43a4e675` Mac artifact, schema 40, stopped service, and
complete inventory. Sessions, commands, jobs, reservations, command slots,
retained pairs, and recovery finalizations are empty. The report separately
contains 1,440 local-intent and 9 remote-intent metadata rows; these are not
execution-capacity records and were not modified.

The exact #514 local intent remains in that metadata inventory. The Oct 28
supported recovery evidence remains authoritative for its lost result and
history: it records release of only the session reservation and command slot,
with the lost result, incomplete output, all 88 events, and event-stream digest
preserved. No second recovery was attempted. The Mac PRD service remains
stopped; no PRD state changed during this checkpoint. The fresh detailed
inventory is a Mac-only observation, not the all-host PRD preflight needed
before deployment.

### 2026-10-29 07:39 UTC — #514 inventory refreshed; no repeat recovery

A fresh read-only detailed inventory was captured with the qualified native
Mac diagnostic at
`/private/tmp/bug027-prd-inventory-refresh-20261029.json` (SHA-256
`14c1423e8bf8acf00ef08a83925197fbcc1d649a97abd57c3957b3591a296ee4`). The
observation completed at `2026-10-29T07:39:20.546861Z`; the report is complete,
schema 40, and bound to selected Mac artifact
`rr-20261024-43a4e675` / bundle SHA-256
`a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062`. The
service is stopped. Sessions, commands, jobs, session reservations, command
slots, retained pairs, pending recovery finalizations, and mailbox bindings
are all zero. The report still says `safe_to_resume=not_established` and
`recovery_authorized=false`.

The exact #514 session/command/job is absent from the active work and capacity
collections. Its accepted local-intent metadata record remains present and
unchanged; local/remote intent records are routing history, not retained
execution capacity. The Oct 28 supported recovery evidence remains the
authoritative record of the lost result and preserved event history. No
recovery, ACK, replay, or other PRD mutation was repeated.

### 2026-10-29 08:31 UTC — resumed checkpoint and #514 refresh

After rereading the full current assignment, the historical Dev/QAS result
`rr-1e6c47721d-f83e3be7` remains accepted with operations
`db7efa3c78056440d580f8671f9c18fc` and
`a353cb72e3f72df38cb3e64e74349841`; those immutable outcomes are retained.
The later sealed route candidate `rr-a38cb0057e-877d49e3` is a distinct
candidate and any successor service bytes still require their own Dev then
QAS acceptance. The Oct 28 exact-pair #514 recovery remains complete; no
second recovery was attempted.

A fresh detailed, read-only Mac PRD work inventory completed at
`2026-10-29T08:31:17.181652Z`. The private JSON is at
`/private/tmp/bug027-mac-prd-refresh-20261029.json`, SHA-256
`bac3b39bf289b78ddb26fe15ed3b196675df0db2161a8362a4e57fe9c2cfe7c6`.
It reports schema 40, service stopped, selected artifact
`rr-20261024-43a4e675`, and zero sessions, commands, jobs, held reservations,
held command slots, retained pairs, pending finalizations, or mailbox
bindings. It still reports `safe_to_resume=not_established` and
`recovery_authorized=false`. The diagnostic was run from the current source
checkout (`go run`); its `diagnostic_build_revision` is `unattested`, so this
is inventory evidence only, not a qualification of the diagnostic binary or
proof of runtime cleanup. Historical intent rows and lost result evidence
remain preserved.

### 2026-10-29 07:53 UTC — public replacement bootstrap source path

The replacement bootstrap is now connected to the public `runner-release
prd-replacement-bootstrap` coordinator command and the public
`runner-lane catalog-prd-replacement-bootstrap` host command. The coordinator
stores a mode-0600 operation record before the first remote CSR or namespace
mutation. The replacement worker stores its own journal before creating the
PRD namespace, and exact retries use that journal to resume directory,
configuration, authorized-key, database, and fresh-baseline steps. Conflicting
request bytes or partial state without the matching journal are rejected.
The command verifies the pinned replacement SSH host key and immutable
release/approval identities. The Mac route step is backed up and journaled;
the command leaves replacement Linux PRD unselected and stopped for the normal
promotion toolkit. Operator sequencing and rollback limitations are now in
[`docs/operations.md`](../docs/operations.md#fresh-prd-bootstrap-on-the-replacement-linux-worker).

The bootstrap operation is not a substitute for cohort backup: the later
held `promote-to prd all` preparation must still back up the current Mac,
Sandbox, and Factory authorities and recheck the replacement baseline. The
retired Linux endpoint is not used, imported, or enabled by this flow.

Focused regression results on Mac, Go 1.27.1, current uncommitted source:

```text
go test ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config ./src/cmd/runner-lane -count=1
releaseflow: PASS
laneinstall: PASS
config: PASS
runner-lane: PASS
```

This run included public-command subprocess coverage, interruption/resume for
each durable bootstrap step, exact/conflicting retries, authorized-key
preservation, database staging/install retry, fresh-install receipt parsing
and rollback, and strict fresh PRD entrypoint creation. One initial test run
failed because three new fixture subcases used the `0755` `t.TempDir()` root;
the installer correctly refused it. The fixture now creates a private `0700`
child directory, and the complete affected-package rerun passed without
weakening production checks. `git diff --check` also passed.

The service-artifact owner/group mismatch is corrected at construction, and
`TestFreshPRDEntrypointCreationUsesCanonicalOwnerBranch` invokes the actual
fresh-user-artifact creation and verifier path. This is source-level evidence
only. Full host source gates, Git delivery, a compatible successor release,
Dev/QAS acceptance, live bootstrap, PRD installation, and all eight PRD gates
remain pending. The current candidate is not sealed and no PRD mutation was
made in this checkpoint.

### Entrypoint owner/group creation regression

`BuildPRDCatalogServiceArtifacts("linux-host")` now emits the canonical Linux
account/group for the entrypoint, matching the strict fresh-creation
validator. `TestFreshPRDEntrypointCreationUsesCanonicalOwnerBranch` validates
the canonical artifact against the production allowlist, then invokes the
actual file creation helper under the Mac test account with the equivalent
current-user identity and confirms the production verifier accepts the
result. The test ran as part of the passing `laneinstall` package suite above.
This tests the real creation branch while preserving the production check for
the deployed Linux owner/group; it is not live replacement-host installation
evidence.

### 2026-10-29 08:00 UTC — Mac full source gates

On the canonical Mac checkout, the full source gates passed with the pinned Go
1.27.1 toolchain, `GOTOOLCHAIN=local`, and the shared Makefile caches:

| Gate | Result | Evidence note |
| --- | --- | --- |
| `make test` | PASS on host-level rerun | All repository packages completed successfully. |
| `make vet` | PASS | No diagnostics. |
| `make build` | PASS | All command packages built. |
| `make smoke` | PASS | Every command built and passed `--help` / `--version`. |

The first sandboxed `make test` attempt is preserved as **NOT PASS**: the
unrelated Darwin regression
`TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` was unable to
run `/bin/ps` against its test-owned process (`operation not permitted`). The
isolated `runnerlocald` rerun and the full `make test` rerun were then executed
with native process inspection available and both passed. No test, assertion,
product code, or machine-wide process policy was changed to obtain the pass.
The linker emitted its existing duplicate `-lproc` warning; it did not fail
the gates.

These results apply to the current Mac working tree derived from base
`30324a5af8bc735de4846bd246e0384236c5f498`; the source revision is not yet
committed or frozen. Sandbox and Factory full gates, Linux's assigned focused
and native checks, Git delivery, successor sealing, Dev/QAS acceptance, and
all live PRD work remain outstanding.

### 2026-10-29 08:07 UTC — exact systemd-unit creation retry

The fresh replacement path now handles an interruption after the Linux PRD
systemd unit has been linked but before its staged name is removed. An exact
retry accepts only the canonical unit target with matching root ownership,
0644 mode, and content digest; it removes only a matching hash-scoped staged
file. Conflicting installed bytes remain untouched. The production installer
routes a preexisting fresh-install unit through this guard instead of
mistaking it for an absent predecessor.

`TestFreshPRDSystemdUnitCreateScriptResumesAtomicInstallAndRejectsConflict`
exercises the generated production script on Linux-shaped disposable paths,
including interruption after link, an exact retry, a pre-link stage retry,
and conflicting-unit rejection. `TestFreshPRDEntrypointCreationUsesCanonicalOwnerBranch`
continues to exercise the actual fresh-user-file creation helper. The focused
Mac rerun passed for `releaseflow`, `laneinstall`, `config`, and `runner-lane`
with Go 1.27.1; the Linux-only systemd script test is skipped on Mac and still
requires native Linux validation. These results are source tests only. No
candidate was sealed and no live service, selector, configuration, database,
mailbox, or admission state changed.

### 2026-10-29 12:12 UTC — operation-specific controller amendment

The read-only coordinator journal for operation
`02bfdc603d2ea3360ecd32501fe347d1` remains at
`prepared → server_csr_ready → server_material_ready`. It is bound to release
`rr-309de02fca-ddc84f50` and digest
`4d650244e8dd9f08390cdb1619399ba40eb948f4db1adcc14f24ad6ff0bcf651`.
Its original `host_control_sha256` is
`c4c7aed5b9fefe6270a8804e5b99ad77c7e1c6193cfaacb2910bda915cf8db0f`.
The retained CSR, server-certificate, and CA hashes are unchanged; no Linux or
Mac namespace journal has been recorded. The new controller-amendment sidecar
was absent at this checkpoint.

The original digest reconstructs exactly with controller revision
`309de02fca7bc8dcd608834df76e831a96d413d7`; current delivered controller
`3e0e9ad536b20fd6dc8b49afcef099663f1eef01` computes
`26d4fbcc48d103b471a06b25b59d3f33f93edb0255706e4b0da907440c95f4c9` from the
same profile, SSH identity, pinned host key and trust files. This establishes a
controller provenance mismatch. The old CLI collapsed that mismatch into the
same conflict text used for a busy lane lock. The prior default-sandbox attempt
failed before lock acquisition because the protected runtime path could not be
opened; the prior elevated attempt returned only the ambiguous conflict string.
No root descriptor inspection is required unless the corrected command reports
an actual `acquire prd lane lock` failure.

The controller-only correction adds the one-time public command
`prd-bootstrap-controller-update`, restricted to this operation and its exact
`server_material_ready` stage. It recomputes the original digest, binds the
existing request/release/approval/predecessor/CSR/certificate identities and
current SSH profile/trust inputs, then atomically records old/new controller
revision and host-control hashes in a separate owner-only sidecar. It leaves
the original coordinator journal and sealed service release untouched. Exact
amendment retries are idempotent; changed request, approval, key, trust, or
profile inputs are rejected. Lock acquisition errors now name the acquisition
stage; resume conflicts name the mismatched fields.

Mac Go 1.27.1 checks used the shared
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`:

| Check | Result |
| --- | --- |
| `go test ./src/internal/releaseflow ./src/cmd/runner-lane ./src/cmd/runner-release -count=1` | PASS |
| `go vet ./src/internal/releaseflow ./src/cmd/runner-lane ./src/cmd/runner-release` | PASS |
| Build `runner-release` and `runner-lane` to task-owned `/private/tmp` paths | PASS |
| `gofmt -l` for modified Go files; `git diff --check` | PASS |

One earlier chained attempt omitted the shared cache environment on its final
build commands and was **NOT PASS** because the sandbox denied writes to the
default Go caches. The same tests passed, and vet/build/format/diff checks then
passed with the shared cache paths exported. This source is controller-only;
the sealed service release remains unchanged. Git delivery and native Linux
validation of this correction remain pending. No live bootstrap retry or PRD
mutation has been made in this checkpoint.

### 2026-10-29 12:23 UTC — controller correction delivered and Linux-validated

The controller correction and its tests were committed on Mac `dev` as
`43bedc7dd0a08ac4775a218c9b5570c9d0423dd7`. Primary Gitea accepted that commit;
the automatic GitHub `dev` mirror resolved to the exact same SHA. The clean
Ubuntu `dev` worktrees for replacement `linux-host` (`79.72.94.59`),
`sandbox-host`, and `factory-sandbox` each fast-forwarded from
`3e0e9ad536b20fd6dc8b49afcef099663f1eef01` and verified clean at
`43bedc7dd0a08ac4775a218c9b5570c9d0423dd7` before validation.

On replacement `linux-host`, Go 1.27.1 reported `linux/amd64` with
`GOTOOLCHAIN=local`. At the synchronized commit, native checks passed:

```text
go test ./src/internal/releaseflow ./src/cmd/runner-lane ./src/cmd/runner-release -count=1  PASS
go vet  ./src/internal/releaseflow ./src/cmd/runner-lane ./src/cmd/runner-release       PASS
build runner-release and runner-lane to ~/.cache/remote-session-runner/controller-update-43bedc7 PASS
both binaries --help and --version smoke                                               PASS
```

The remote checkout remained clean and on `dev`. No service, selector,
configuration, database, mailbox, or receipt was changed. The native check is
controller tooling validation, not service release acceptance. The separate
sealed service release `rr-309de02fca-ddc84f50` remains unchanged. The
controller amendment has not yet been written, and the existing bootstrap
operation has not yet resumed.

### 2026-10-29 08:10 UTC — final Mac source gates after unit-retry change

The new unit-creation test and installer retry branch changed source after
the prior 08:00 full-suite record. The affected packages passed on the Mac
with Go 1.27.1:

```text
go test ./src/internal/releaseflow ./src/internal/laneinstall ./src/internal/config ./src/cmd/runner-lane -count=1
PASS (all four packages)
```

The latest full `make test` sandbox run remains **NOT PASS** for the same
Darwin test-only `/bin/ps` permission restriction. The identical full suite
then passed on the Mac with host-level process inspection. The latest
`make vet`, `make build`, and `make smoke` also passed. `git diff --check`
passed. The Linux-only unit-script test skips on Darwin, so it still needs a
native Linux run. These checks are against the uncommitted tree based on
`30324a5af8bc735de4846bd246e0384236c5f498`; the source is not yet frozen.
No candidate was sealed and no service, selector, configuration, database,
mailbox, or admission state changed.

### 2026-10-29 12:41 UTC — bootstrap directory guard diagnosis and correction

The first supported retry of operation
`02bfdc603d2ea3360ecd32501fe347d1` reached the replacement host and failed at
the `directories` step with the sanitized result:

```text
replacement PRD bootstrap step directories: invalid or inconsistent catalog selector operation
```

This was not a lane-lock acquisition failure. The current replacement-host
checkout and pinned SSH connection were verified; the host's canonical PRD
configuration, database, selector, and systemd unit remained absent. The
bootstrap journal retained `completed_steps: []`, and the original CSR,
certificate, CA, and coordinator journal hashes were unchanged. The fresh
absence evidence had classified the existing runtime root as `toolchain-only`.
The operation lock creates the expected `run/runnerd-lifecycle.lock` before
the first apply step. On inspection, the shared root was owner/group
`ubuntu:ubuntu`, mode `0775`, with the recorded Go toolchain and that lock
directory. The directory installer had incorrectly sent this existing shared
root through the helper for a newly created private directory, which requires
`0700`.

The correction preserves the existing shared root and its mode. It validates
the absence receipt, the exact recorded Go toolchain and binary hash, the
operation-owned lifecycle lock, and only the canonical empty or precisely
partial bootstrap directory set. It rejects unknown root entries, changed
toolchain bytes, unsafe root permissions, symlinks, changed ownership, and
unexpected contents before further writes. Every new PRD directory remains
owner-only `0700`; exact retries cover both a genuinely absent root created
for its lock and a validated toolchain-only root. Tests also exercise a
partially completed directory step without relaxing the production path
guard.

The controller amendment mechanism now preserves its first sidecar and can
append a bounded chain of revision-named successor records. Each transition
binds the preceding controller revision and host-control digest, the same
non-controller identity inputs, and the unchanged coordinator bindings.
Repeated transitions are idempotent; gaps, forks, mismatched inputs, and
modified predecessor records are rejected. This is limited to the same
`server_material_ready` bootstrap operation. It does not modify the original
coordinator journal, CSR, server certificate, CA, or sealed service release.

The first full Mac suite attempt after these source edits was **NOT PASS** for
two separately diagnosed reasons: the existing legacy-client certificate
fixture used a fixed `2026-10-28` issuance time and was expired against the
validator's actual wall clock on `2026-10-29`; the sandbox also prevented the
unrelated Darwin process-ownership test from executing `/bin/ps`. The fixture
now uses a fresh test clock; this changes test data only. The full
`make test` rerun with native process inspection passed across all packages
with exit code `0`. `make vet build smoke` also completed with exit code `0` on
the same Mac Go 1.27.1 toolchain. Focused directory, public-bootstrap,
amendment-chain, and runner-lane subprocess tests passed. No service release was rebuilt or
changed. This latest directory correction still requires Git delivery and
native Linux validation before retrying the live bootstrap; no further PRD
namespace write has yet been attempted.

### 2026-10-29 13:17 UTC — directory correction source gates

The source revision is `d9363e7f339a00407ccef698ff172b50a70ca6b3`; it is
committed on Mac `dev`, pushed to primary Gitea, mirrored to the exact same
GitHub SHA, and fast-forwarded to the three required Ubuntu `dev` checkouts.
The sealed service release `rr-309de02fca-ddc84f50` and its digest
`4d650244e8dd9f08390cdb1619399ba40eb948f4db1adcc14f24ad6ff0bcf651` are
unchanged.

Source gates on this revision:

| Host | Result |
| --- | --- |
| Mac (`darwin/arm64`, Go 1.27.1) | Full `make test`, `make vet build smoke`, focused bootstrap/amendment tests, formatting, and `git diff --check`: PASS. The first full test attempt was NOT PASS because a stale dated certificate fixture expired and the sandbox denied `/bin/ps`; the fixture was corrected and the full run passed with host-level process inspection. |
| Factory (`linux/amd64`, Go 1.27.1, CGO=1) | Serialized `go test -p=1 ./... -count=1`: PASS. `make vet build smoke`: PASS. Full-test log SHA-256 `6f98bdd88b5dd9b9ff112d0bc75cd7fc382bed405af31e2581c3c7d6cfe0e7ca`; vet/build/smoke log SHA-256 `a852bfb96177adebe19e5196a18ead37b16ed1feccdbb8d53049d59bb011e230`. `TMPDIR`, `GOTMPDIR`, and `SQLITE_TMPDIR` were `/tmp`; it was a 4 GiB tmpfs by explicit owner instruction. A post-run observation measured 3,365,302,272 bytes free in `/tmp`. |
| Sandbox (`linux/arm64`, Go 1.27.1) | Serialized `go test -p=1 ./... -count=1`: PASS, log SHA-256 `d0a1e02e34fd9029aefc301fff03e439db845d644323e4315b56fbd21fe113f3`. A separate `make vet build smoke` attempt was intentionally stopped by the 600,000,000-byte safety guard during `go build` at 554,979,328 bytes free. That sequence is NOT PASS; no cleanup of caches or host data was attempted. Latest measured root free space is 554,565,632 bytes. |
| Replacement Linux (`linux/amd64`, Go 1.27.1) | Focused `laneinstall`, `releaseflow`, and `runner-lane` tests: PASS; exact P025 and P101 regression filters: PASS; native vet, `runner-release`/`runner-lane` builds, and help/version smoke: PASS. These are focused native checks, not a full Linux suite. |

Two overlapping Factory full-suite attempts shared one log and were terminated
after that overlap was detected; both are **NOT PASS**. Their preserved
interleaved log copy is outside Git at
`~/.cache/remote-session-runner/bug027-prd-bootstrap-d9363e7/full-test-overlap-not-pass.log`,
SHA-256 `c0cbf35df425f78635b26467cfe436906344b56e3e9039410176110599370e98`.
The successful serialized Factory run uses a separate clean log and is the only
Factory full-suite PASS claimed above.

These checks qualify the directory/controller source correction only. They do
not constitute live Dev/QAS acceptance, PRD bootstrap, service installation,
mailbox acceptance, or client resumption. The latest Sandbox vet/build/smoke
result remains NOT PASS because its guard protected the remaining disk
headroom; the historical Linux full-suite interruption remains NOT PASS.
