# CAT-04 — Frozen candidate and live Dev qualification

## Current status — 2026-10-21 18:56 UTC: CAT-04 PASS; Dev open for normal API use

The owner-authorized admission opening is complete. Dev operation
`8fdb7e5eb058a795601a2cff9e7bde78` is `released` on all four hosts, with 14
transitions and common receipt SHA-256
`cd5e3f06290f806ea66205cc283b1f27704ce9401f73fe31a7cbe60440d1cf9d`.
The accepted `cat-20261021-ebc4446` release and per-host bundle bytes are
unchanged. All seven ordinary API/queued routes passed without maintenance
tokens. At 18:56 UTC, Dev and QAS were ready on all four hosts, with empty work
inventories and zero error counters. API admissions are open for normal use;
PRD process identities, configuration and binary hashes are unchanged.

The opening sequence, released receipts and postflight evidence are in
[Dev/QAS admission opening](CAT-Dev-QAS-admission-open-2026-10-21.md).
No external publisher control was operated; the existing publisher pause
instruction remains in force. No mailbox request or ACK was published, and
filesystem-mailbox acceptance remains **NOT RUN**. The held snapshots below
describe the earlier acceptance closeout, before this opening.

## Historical status — 2026-10-21 13:09 UTC: CAT-04 PASS; Dev cohort complete and held

The owner superseded the earlier c76bac5 retention and 740927e qualification
instructions and authorized completion of the current Dev qualification for
operation `8fdb7e5eb058a795601a2cff9e7bde78`. The sealed release remains
`cat-20261021-ebc4446`, source revision
`ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`, release-manifest SHA-256
`ede9121d5a41a85944a98d1b585aa99aa36c343244c587dfb540aa57b852c62c`.
No rebuild occurred during acceptance.

### Fresh Dev inventory, identities and capacity

The final inventory found no sessions, command slots, queued commands, or
resumable jobs on any Dev host. All four Dev services were active, schema 40,
and selected the expected release and lane-specific configuration. Capacity
was measured from the task run's existing `/tmp` paths; Sandbox remains tight
and must be rechecked before QAS backup or switch operations.

| Host | Bundle SHA-256 | Configuration SHA-256 | Work inventory | Service / capacity observation |
| --- | --- | --- | --- | --- |
| `mac-control-plane` | `942570512c160953b450839d88f7b8a79c6e5a4b59db31fb89e8f94c16fc3475` | `2a256f44cf14031b8463854aa77c190ba265a419b624a0538a6c42ba130395d9` | Empty; schema 40 | Active; LaunchAgent wrapper PIDs 4293 and 4413 at observation; `/private/tmp` 145,438,072 KiB free |
| `linux-host` | `4e7795aa8d5484d8ed77efd3977b5d5693dba6b3d4ffd56d71cf439485b4766f` | `842a17572ba218ee770b39d8cf95c442c4885204d479d5b34ef6d2e4e5ff82f5` | Empty; schema 40 | Active; PID 3236872; `/tmp` 15,905,248 KiB free |
| `sandbox-host` | `ba07943174dd7d1a7eafa49a3d9a005884ee1e9beeb11f4e10b1b12b90bd9a07` | `9a9adcd3236d8a92f2d405ef59f1199180310a03df6c9d246b784a702653d82a` | Empty; schema 40 | Active; PID 3895195; `/tmp` 1,201,412 KiB free on `/dev/sda1` (98% used) |
| `factory-sandbox` | `6f95571437db8d96bebf0d166416a4f129a55c05dce0eb706b9fa32d099f4762` | `7af6d8ea68764f703b7aebd40966bbc7ff8b86523a2cb948d5f60e68659cd727` | Empty; schema 40 | Active; PID 4130166; `/tmp` 3,606,264 KiB free on its existing tmpfs mount |

These are observation-time process IDs and capacity values, not reservations.
The Sandbox value is a limiting preflight constraint, not spare capacity for
large staging. No new backup or release bundle was staged during CAT-04.

### Required functional and lifecycle acceptance

Archived, bounded probe outputs were checked against the host-specific verify
branches already retained in the cohort receipt. All seven applicable routes
passed: Mac-local, plus direct-mTLS and queued-SSH on Linux, Sandbox, and
Factory. Every terminal result had `phase=complete`, command state
`succeeded`, exit 0, final event sequence 4, complete non-truncated output,
and teardown closed. Direct probes were accepted with HTTP 202 and readiness
HTTP 200; queued helpers exited 0 and returned a terminal `result`. Each used
the bounded five-second command limit. The evidence digests are the exact
receipt-validated hashes:

| Host and coverage | Evidence SHA-256 | Job IDs |
| --- | --- | --- |
| Mac-local | `3572e8e912e991e3b6e500614e90023b8cdae1cd4a890e32aa77e34afc79bd56` | `job-4e18d43f4ebd4cb1d1317f0e1bab6155` |
| Linux direct-mTLS + queued-SSH | `cd4517089c346ecf11c070c40893be22164001ce0144d184fafcbae7aef1dab4` | `job-a93200defcec32eeee575346f858f7ae`; `job-30741af55ca19a888e18417b51414099` |
| Sandbox direct-mTLS + queued-SSH | `55dd1b02429474cd99c3800d689c7d04429507431b45caa9b04cec1a475496c2` | `job-c171f2299ba977fbaa38b5a52d9f12e4`; `job-5aa283f3afc060198b61f1dac8baf994` |
| Factory direct-mTLS + queued-SSH | `057c2ec4af1b29b3b3fc9a525e603215d6518db9e84d3a6c95dc65b7cd6dd93d` | `job-75ae931563edf17459167377090cd234`; `job-edb81fb07a55c98ace3787a5e03b6a00` |

The evidence files are under the private task directory
`/private/tmp/cat-20261021-ebc4446-cohort/`; source files remain outside Git.
The worker verify files contain a trailing literal backslash-n in their raw
bytes; validation parsed that suffix safely and computed digests over the
original bytes, which match the receipt. Post-probe inventories again showed
no active or resumable work.

The supported completion transition was recorded after validating all four
host histories. Operation `8fdb7e5eb058a795601a2cff9e7bde78` is `complete` on
all four Dev hosts, with 13 transitions and common receipt SHA-256
`2c0e3f489cb701627c1c1e6bc9ad92bb04b31d952cceee7adf781d7ac566dc10`.
The completion-evidence digest is
`e5d1b022910e8de6bb47a864181366c8772589f263532f3cd6b8758b6e929afd`. The
supported receipt sync ran while each Ubuntu Dev service was stopped and its
work inventory empty; each service was restarted and verified active with an
empty inventory. The Mac Dev service remained active. Post-completion
admission-fence checks on all four hosts returned readiness HTTP 200 and
rejected an unscoped admission with HTTP 503 / phase `blocked`.

### Scope and next gate

QAS remains on its recorded legacy bundles; PRD remains on its unsuffixed
installed version with catalog selector unselected. Neither lane changed for
CAT-04. Dev's publisher/admission fence remains active, external project
publishers remain paused, no mailbox request or ACK was published, and
filesystem-mailbox acceptance is **NOT RUN**. There was no source change or
candidate rebuild for this closeout; the ebc4446 source gates previously
passed on Mac and all three Ubuntu hosts, as recorded below.

CAT-05 may now begin with a fresh QAS work/capacity/release preflight and
consistent backup checks. Use the exact release ID, manifest digest, and four
bundle hashes above; do not rebuild during promotion. Given Sandbox's 1.2 GiB
free-space observation, measure the actual required backup/staging footprint
and available space again before each operation. Do not proceed if the backup,
switch, and recovery needs do not fit. CAT-06 PRD remains dependent on QAS
acceptance and its own fresh work, backup, and rollback guards.

## Historical status — 2026-10-21 12:06 UTC: recovery target already superseded; live Dev divergence later resolved

The owner-requested read-only recheck used the supported `runner-lane status`,
`catalog-status`, and `catalog-cohort -action status` commands on Mac,
`linux-host`, `sandbox-host`, and `factory-sandbox`. Operation
`33b2c3a3f6399253444c6ae29f0ecb97` is already `superseded` on all four hosts;
each status command returned 18 transitions and the receipt files have the
same SHA-256 `21b1f6da486c582dd2fdaa2cd536cded34e9520ca8f2ff74c9f749a8b9cbfe78`.
The receipt records `reconcile-history` at transition 11 with four retained
verification branches, `resume` at 12, `complete` at 13, and `supersede` at 18
to operation `9940d64e28bf103bd4b53b48d06417da`. The requested recovery is
therefore already complete in the preserved history and cannot be resumed as
an active operation.

The live Dev state no longer matches the required recovery baseline. Fresh
status shows the same later operation `8fdb7e5eb058a795601a2cff9e7bde78` at
stage `verifying` on all four hosts, with common receipt SHA-256
`fe161efb19ac6cb05d1c706d0378ea2a1e80e1e4809c99663eba2855dfa1bf24`; all
four Dev services are active and all four Dev selectors point to
`cat-20261021-ebc4446`, not the required `cat-20261021-c76bac5`. The ebc4446
bundle SHA-256 values are Mac
`942570512c160953b450839d88f7b8a79c6e5a4b59db31fb89e8f94c16fc3475`, Linux
`4e7795aa8d5484d8ed77efd3977b5d5693dba6b3d4ffd56d71cf439485b4766f`, Sandbox
`ba07943174dd7d1a7eafa49a3d9a005884ee1e9beeb11f4e10b1b12b90bd9a07`, and
Factory `6f95571437db8d96bebf0d166416a4f129a55c05dce0eb706b9fa32d099f4762`.
QAS remains on the previously recorded legacy selectors and PRD remains
unselected. The requested `cat-20261021-740927e` directories are present on
all four hosts but are not selected. The previously recorded CAT-04 source
review says that candidate predates the ebc4446 correction.

No receipt, selector, service, mailbox, or retained-work state was changed by
this recheck. No fresh work inventory was taken, so the earlier empty
inventory is not treated as current proof. No mailbox request or ACK was
published; external publishers remain under the existing pause instruction.
CAT-04 cannot advance from this state without resolving the mismatch between
the required c76bac5 recovery baseline and the active ebc4446 verification
operation. No QAS or PRD action was taken. Temporary files were not created on
the hosts; `/tmp` capacity observed was Mac 139 GiB, Linux 16 GiB, Sandbox
1.3 GiB, and Factory 3.5 GiB on its existing tmpfs `/tmp` mount.

This status update was committed on Mac `dev` as
`9e9372848a90e6937ea7646a276fb6b73eda8e1b`. Primary Gitea `origin/dev` and
automatic GitHub `github/dev` both resolved to that exact SHA. Linux, Sandbox,
and Factory were clean on `dev` at `c89c645e5fb1ca4b3b1a66a4491a17b305bc6927`,
then each fast-forwarded from `origin/dev` to `9e9372848a90e6937ea7646a276fb6b73eda8e1b`
with the configured explicit GitHub identity; each post-pull worktree was
clean. `git diff --check` passed. No tests were run for this Markdown-only
update.

## Historical status — 2026-10-21 10:19 UTC: source correction delivered and all four host gates pass

The 09:39 blocker below was addressed in source without changing live host
state. A completed cohort receipt can now be superseded only by a fresh held
successor and only after each host proves a stopped service, empty work
inventory, unchanged configuration/schema, exact current selector, and one
valid completed selector journal whose digest is present in the receipt. The
successor's previous selector bundle identity must equal the completed
predecessor's target identity. Interrupted or divergent selector operations
remain ineligible and continue to require the existing recovery flow. Receipt
history is append-only; the change neither edits old receipts nor releases
admissions before all four host observations and explicit finalization.

Regressions cover exact bundle identity, wrong successor predecessor identity,
divergent current selector rejection, completed predecessor retirement only
after all four observations, and preservation of the admission hold until
finalization. The actual completed local selector-journal validator is tested
against a temporary filesystem fixture; it accepts a matching journal and
rejects selector drift. The prior interrupted-switch and unfinished-journal
recovery regressions continue to pass.

The correction is committed as `ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`,
pushed to primary Gitea `origin/dev`, and verified at the exact GitHub
`github/dev` SHA. All three Ubuntu checkouts were clean on `dev` at
`98f4da3be252dc4a50c1ab9ca7d2821d4d9517f0` before their required
`--ff-only` pulls; Mac and all three workers now pass the source gates at the
exact correction commit:

Primary Gitea `refs/heads/dev` and mirrored GitHub `refs/heads/dev` both
resolved to `ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`. Each Ubuntu checkout
was verified clean on `dev`, then pulled with the configured explicit
`/home/ubuntu/.ssh/gavrilo123456-github` identity and
`git pull --ff-only origin dev`; post-pull `HEAD` on Linux, Sandbox, and
Factory is the same `ebc4446...` and each worktree is clean. Host gates ran
from `/home/ubuntu/projects/remote-session-runner` with the Makefile's Go
1.27.1 toolchain and normal per-user Go caches. Mac gates ran from the
authoritative Mac checkout at the same commit.

| Host | `make test` | `make vet` | `make build` | `make smoke` |
| --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | PASS; host-visible rerun | PASS | PASS | PASS |
| `linux-host` | PASS | PASS | PASS | PASS |
| `sandbox-host` | PASS | PASS | PASS | PASS |
| `factory-sandbox` | PASS | PASS | PASS | PASS |

The first sandboxed Mac `make test` run failed only because an existing
`runnerlocald` cleanup test could not execute `/bin/ps` (`operation not
permitted`); the host-visible rerun passed. Factory's gates used
`TMPDIR=/tmp GOTMPDIR=/tmp SQLITE_TMPDIR=/tmp`. The mount reports `/tmp` as
`tmpfs` with user quotas; `quota` and `repquota` cannot query that mount, so the
effective per-user limit was checked read-only with `quotactl_fd`. For uid
1001 it returned 2,097,152 KiB-block soft and hard limits (2 GiB each); use after the
Factory gates was 524,980,224 bytes. No temporary test files remained and the
ordinary Go caches were preserved. Sandbox `/tmp` is on `/dev/sda1`; its free
space after the suite was 1,590,632,448 bytes.

No live receipt or selector changed during this source work. Operation
`33b2c3a3f6399253444c6ae29f0ecb97` remains complete at the synchronized SHA
`1473de3b51e5327f0f1495d7a7a9892316d63ab0df10e94047bbdffef3cb46cd`; all Dev
selectors remain on `cat-20261021-c76bac5`, all Dev services remain stopped,
and QAS/PRD remain unchanged. Existing `cat-20261021-740927e` is unchanged and
must not be promoted because it does not contain this correction. Seal a new
cohort from `ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`, then perform fresh Dev
acceptance before QAS and PRD. Any later evidence-only commits must remain
Markdown-only so the frozen source revision and bundle bytes remain pinned.
Mailbox acceptance is
**NOT RUN**; no mailbox request or ACK was published. External publishers
remain paused.

## Historical status — 2026-10-21 09:39 UTC: candidate Dev rollout blocked by receipt/runtime incompatibility

The owner-authorized recovery of operation
`33b2c3a3f6399253444c6ae29f0ecb97` completed before the following rollout
attempt. Fresh checks at 09:38 UTC found the same final receipt SHA-256
`1473de3b51e5327f0f1495d7a7a9892316d63ab0df10e94047bbdffef3cb46cd` on all
four hosts, stage `complete`, and all four Dev selectors still on
`cat-20261021-c76bac5`. Dev services were inactive with zero active session
slots, command slots, queued commands, or resumable jobs. QAS remained on its
prior legacy bundles and PRD remained unselected. No candidate preflight,
backup, selector change, QAS action, or PRD action was performed after the
blocker below was encountered. No mailbox request or ACK was published; the
external publisher pause remains in force.

The first supported Mac Dev start attempt returned
`launchctl bootstrap ... failed (exit 5): Bootstrap failed: 5: Input/output
error`. A retry under the already-authorized host-operation scope reported
`start complete`, but subsequent `launchctl print` reported both selected
LaunchAgents absent and `runner-lane status` reported inactive. The bounded
Mac Dev logs contain the exact error:
`runner-local: catalog cohort receipt is invalid; refusing work admission`
and
`runner-locald: catalog cohort receipt is invalid; refusing work admission`.
Both log files were updated at 09:35:54 UTC; the matching bounded tail entries
are local lines 154–155 and locald lines 151–152. The 09:35:45 UTC Mac status,
inventory, and selector snapshot is preserved in task-owned `/tmp` evidence
with SHA-256
`814c7ccced502e7a9f1ee85ed11c2656c7ca842850f73283e39efb7a9106d49a`. The
inventory remained empty. Source comparison identifies the incompatibility:
selected release `cat-20261021-c76bac5` was built from `c76bac5`, whose receipt
validator does not accept a reconciled `verifying` history; the supported
recovery receipt contains `reconcile-history` with
`reconciled_stage=verifying` and the four preserved host verification
branches. The source correction at `740927e` adds that validation. Thus the
currently selected runtime fails closed on a receipt accepted by the newer
operator; the `740927e` runtime is still unselected.

The subsequent supported `open` attempt did not append a transition: the
operator returned `operation not permitted`. The follow-up selected-lane stop
also returned `open selected lane lifecycle lock: operation not permitted`;
fresh service status remained inactive, the receipt stayed at its original
SHA-256, and no selector changed. These attempts are recorded as failures, not
successful transitions. Current `cat-20261021-c76bac5` and operation `33b2…`
must remain preserved. The current supersession validator also does not allow
a `complete` receipt to be superseded, and `open` requires an active service
matching the receipt. Therefore there is no supported path to retire this
completed hold while the selected service rejects the receipt. Do not bypass
the receipt guard or select `cat-20261021-740927e` under operation `33b2…`.

Storage was not the guard that stopped this attempt. A fresh Sandbox read at
09:39 UTC measured 1,696,821,248 bytes free on `/`; the already-published
Sandbox bundle occupies 42,159,445 bytes and the retained prior Dev backup
occupies 676,085 bytes. No new backup was made, so this is not a backup or
selector preflight PASS.

The exact next dependency is a supported compatibility/finalization path that
preserves the completed receipt and keeps admissions fenced while allowing the
selected `c76bac5` runtime to validate its recovered history, or safely
replaces the completed hold while all services are stopped and inventories are
empty. Any source correction needs its required tests, Git delivery, a new
sealed candidate, and fresh Dev acceptance before QAS and PRD. Until that
dependency is delivered, candidate `cat-20261021-740927e` remains unselected;
Dev functional acceptance is **BLOCKED**, QAS and PRD remain unchanged, and
mailbox acceptance remains **NOT RUN**.

## Historical status — 2026-10-21 09:24 UTC: operation 33b2 reconciled and complete; candidate acceptance remains pending

The owner authorized recovery of Dev operation
`33b2c3a3f6399253444c6ae29f0ecb97`. Using the supported operator built from
`740927e93306dec8a3a9487a5aca6bc7be5d9819`, I validated the four preserved
receipt copies against their live hashes and status reads. They shared the
same 10-transition prefix and carried one distinct `verify` transition per
host. The owner-only `/tmp` snapshots were mode `0600`; their hashes were Mac
`69d6d5ddc28f4e706e8fde7b7dd0758461eba4b99e1d08bc526ca5cee81a071a`, Linux
`4a8d64839888330ce2f7c7890291a692684378e53b266d85ec9fb001207a646c`, Sandbox
`64200b559cb70fdd401b3d716b6e4258867a157b7e45d3908e23c9a171bb799d`, and
Factory `c66ddcd32d5d2bb2ebbda1ae71f3e2060bc88e46fb431eca04dc62cd13cb825f`.
The host-specific verify evidence digests are preserved in the four branches:

| Host | Verify evidence SHA-256 |
| --- | --- |
| `mac-control-plane` | `1a5fd8946bdec5b900082ad6625b90a13ca6a83fa47bb1d8094d57e403437f4c` |
| `linux-host` | `06a96c62675f34ce3bd23ddf4e1056fc3a273b363a4d82b7ef6320b60044904c` |
| `sandbox-host` | `f8171981e99803c94ff2d18551cf4cfaf285b35309cef549650e5e198fd0a8cc` |
| `factory-sandbox` | `861e980a7c3a5ce98924fbb28c628ad3ced6f2b4acbe32f1342489206e9961db` |

Supported `reconcile-history` on Mac produced a blocked receipt with
`reconciled_stage=verifying`, all four original histories retained, and
reconciliation evidence SHA-256
`e350f6e1970e591bec63be983aae4b6c4c4539b0a1aa6628a596941cef85dab5`. The
reconciled receipt SHA-256 was
`8265bd994fd1a1b2129940c3f810aa41ec3907ea61acccceafc22b7ae277b5b2`; it was
synchronized to Linux, Sandbox, and Factory through `catalog-cohort sync`,
with each local guard passing. Fresh all-host evidence at 09:11:45 UTC
(`ca399b610eecbd7faa366ef0a644fb53c1bc81767fce738fc94e3e6c69b2550f`) showed
the same receipt, c76 selectors, zero work, unchanged QAS bundles, and
unselected PRD. The supported `resume` transition returned the receipt to
`verifying`; its synchronized SHA-256 was
`85eb937c9286cbd2ef1e767664e5c10e4189d23af2599ff1e3cb5a7256090aa2`.

Before completion, a fresh single-run per-host collector validated all four
receipt copies and selector/work inventories, enumerated 67 receipts on each
host, and found exactly one admission-holding receipt everywhere: this
operation at `verifying`. It retained all four host verify evidence digests.
Mac Dev was started only because the supported `complete` action requires its
selected-lane service to be active; the verifying hold continued to block
ordinary admissions and the work inventory remained zero. Completion evidence
SHA-256 was
`9123f61d41f18cbd5919fd5c01c19c3de290a890d9d3e6f68d4b8041237f82cd`.
Supported completion produced stage `complete` with 13 transitions. Mac Dev
was then stopped, and the final receipt was synchronized to Linux, Sandbox,
and Factory. All four final copies are byte-identical at SHA-256
`1473de3b51e5327f0f1495d7a7a9892316d63ab0df10e94047bbdffef3cb46cd`.

Final supported checks show all four Dev services stopped and work inventories
empty. Dev remains on `cat-20261021-c76bac5` (release SHA-256
`4e03f8c7e18beacf50f5478bcae0e9a0d83e88ca866a8c1f5a94d8da466e40fc`) with
these selected bundle hashes:

| Host | Dev bundle SHA-256 |
| --- | --- |
| `mac-control-plane` | `17349ead4f2b0bf631bae4300a3aedaa048403baf7532aa9cd4060cdd148989b` |
| `linux-host` | `006640d0b045264f90f16c2f8ba8e37cc386b370f153c35c74f1598bb61ac30f` |
| `sandbox-host` | `7047be682acd5d28df26751aa377d582748cb5d3bfb3b1d5f8d03ac946c09908` |
| `factory-sandbox` | `62942404849e14f5788ba32dc4b81f07aa7438844c751f9dfe1f74e42e498850` |

The `complete` stage continues to hold admissions by design; the exact
operation above is still the sole hold on each host. QAS remains on the four
legacy bundle hashes recorded below, and PRD remains `unselected`. External
project publishers remain paused. No mailbox request or ACK was published.
This receipt recovery does not claim fresh direct-mTLS, queued-SSH, Mac-local,
or mailbox functional acceptance. Candidate `cat-20261021-740927e` (manifest
SHA-256 `ab01036a2824b680dd37e90e043d744f817db724a4e233b7916e2bad6ca36382`,
source `740927e`) remains unselected; its own Dev acceptance is the next
rollout gate before QAS or PRD.

The first resume attempt was rejected by automatic review because its evidence
digest was deemed stale; no receipt changed. A new same-run four-host capture
was accepted. The first completion attempt was likewise rejected as stale;
no receipt changed. Source inspection and its regression confirm that
`complete` continues to hold admissions. A new single-run four-host capture
then passed review and the supported completion succeeded. The longer
read-only receipt census attempt was interrupted before finishing; it made no
state changes. A later per-host local census completed successfully.

The recovery evidence and plan update were committed on Mac `dev` as
`2dfbd44e6d8179adaa6d5c75eeba4e6e8c1e3f86`. The commit was pushed with the
configured Gitea identity using `git -c core.sshCommand='ssh -i
/Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o
BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev`; the push
succeeded. A read-only `git ls-remote github refs/heads/dev` using the
configured GitHub identity returned the exact same SHA. Before pulling, Linux,
Sandbox, and Factory were each on clean `dev` worktrees at
`2750eeae03ea0c600679e8ac4f823f7e0e0e5b0c`. Each ran the required
`git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o
IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull
--ff-only origin dev`; all three fast-forwarded to
`2dfbd44e6d8179adaa6d5c75eeba4e6e8c1e3f86` and reported clean `dev` worktrees.
`git diff --check` passed on Mac. Runtime tests were not rerun for this
documentation-only commit; `740927e` source gates remain the recorded Mac and
three-worker `make test`, `make vet`, `make build`, and `make smoke` PASS.

## Historical status — 2026-10-21 08:36 UTC: corrected release sealed; current Dev receipt awaited authorization

The verification-history reconciliation correction is committed as
`740927e93306dec8a3a9487a5aca6bc7be5d9819` and delivered on Mac `dev`,
primary Gitea `origin/dev`, the exact GitHub mirror `dev`, and all three clean
Ubuntu `dev` checkouts. Mac `origin/dev` was refreshed from primary Gitea before
the bundle build; every checkout reported `HEAD` and `origin/dev` at this SHA.

At this exact source revision, all four required gates passed on all four
machines: `make test`, `make vet`, `make build`, and `make smoke` (each exit 0).
Linux and Factory full tests passed on their first current-revision run. Sandbox
has one preserved earlier `make test` attempt that I interrupted to prevent
root exhaustion when only 166,896 KiB remained; that attempt is not PASS. The
owner had directed me to try within the tight capacity limit, not to interrupt
the test. The later full rerun passed: `/dev/sda1` had 1,909,124 KiB
available before it and 1,780,112 KiB after it. Sandbox `make vet`, `make build`,
and `make smoke` then passed; the last measured free space during smoke was
1,530,828 KiB. No shared cache or host data was removed. All Go commands set
`TMPDIR`, `GOTMPDIR`, and `SQLITE_TMPDIR` to `/tmp`; on Factory, `/tmp` is
currently mounted as `tmpfs`, and no `/dev/shm` path was used. On Mac, the
supported bundle writer rejected `/tmp` because it is a symlink to
`/private/tmp`; the Mac build artifacts therefore use the canonical
disk-backed `/private/tmp` path.

One new immutable release was built from source `740927e` and sealed as
`cat-20261021-740927e`, release-manifest SHA-256
`ab01036a2824b680dd37e90e043d744f817db724a4e233b7916e2bad6ca36382`. Supported
`catalog-inspect` passed on all four hosts with this identical release digest
and these per-host bundle hashes:

| Host | Bundle SHA-256 | Catalog publication/inspection |
| --- | --- | --- |
| `mac-control-plane` | `3a48ac5f852adab5ee10eb054b8cedf373e63d687be31975275efd64c16b98f0` | PASS |
| `linux-host` | `81971e45af48fad539d89d682848fa47dfe83ceb002c139751a828ff2122c07b` | PASS |
| `sandbox-host` | `7ee54e47f57fb6117607b027e871487319fc03673f19f2860c4836b1140b9577` | PASS |
| `factory-sandbox` | `4300444e0d1a421514fe0201d947464a57654390f2da9c7e28035d9f441f511e` | PASS |

Publication added this release only; no selector, Runner service, configuration,
database, mailbox, or receipt was changed by publication. It remains unselected.

Fresh supported read-only checks at 2026-10-21 08:34 UTC found Dev stopped on
all four hosts with zero active session slots, command slots, queued commands,
and resumable jobs. All four selectors still resolve to the previous
`cat-20261021-c76bac5` bundles; schema is 40. The current operation is
`33b2c3a3f6399253444c6ae29f0ecb97` at stage `verifying`. Its four existing
receipt copies each contain the common hold/switch/verify-start prefix and one
distinct, host-specific `verify` transition:

| Host | Existing receipt SHA-256 | Verify host |
| --- | --- | --- |
| `mac-control-plane` | `69d6d5ddc28f4e706e8fde7b7dd0758461eba4b99e1d08bc526ca5cee81a071a` | Mac |
| `linux-host` | `4a8d64839888330ce2f7c7890291a692684378e53b266d85ec9fb001207a646c` | Linux |
| `sandbox-host` | `64200b559cb70fdd401b3d716b6e4258867a157b7e45d3908e23c9a171bb799d` | Sandbox |
| `factory-sandbox` | `c66ddcd32d5d2bb2ebbda1ae71f3e2060bc88e46fb431eca04dc62cd13cb825f` | Factory |

The corrected `reconcile-history` command was attempted on Mac with all four
preserved receipt copies. It returned `operation not permitted` before any
receipt write; the four hashes above remained unchanged. The follow-up request
for host-level execution was rejected by automatic approval review because the
owner's latest exact recovery authorization names `5c9d691cc33016b1ef5bb7b741dea0de`,
whereas the current operation is `33b2c3a3f6399253444c6ae29f0ecb97`. No
alternate path was used. This live receipt reconciliation/finalization is
blocked pending explicit owner authorization for `33b2…` as the current
operation. The selected release remains `cat-20261021-c76bac5`; the new
`cat-20261021-740927e` candidate has not been activated or accepted in Dev.
Read-only selector checks at 2026-10-21 08:38 UTC show QAS remains on its
legacy bundles: Mac `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310`,
Linux `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863`,
Sandbox `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459`,
and Factory `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687`.
PRD catalog status remains `unselected` on all four hosts; this candidate did
not alter the unsuffixed PRD installation. Mailbox requests and ACKs remain
unpublished; mailbox acceptance is **NOT RUN**; external publishers remain
paused.

## Historical status — 2026-10-21 06:10 UTC: recovery correction delivered; Dev rollback complete

The receipt rollback/synchronization correction is delivered at source revision
`c76bac5e15b3c1d9ac004024f8062f43f0e6a4f4`. Mac `dev`, primary Gitea
`origin/dev`, automatic GitHub `dev`, and all three clean Ubuntu `dev`
checkouts resolve to this revision. The required source gates passed at this
revision on Mac and all workers: `make test`, `make vet`, `make build`, and
`make smoke`. Mac's first sandboxed test run could not inspect processes; its
host-visible rerun passed. Linux passed all gates. Sandbox's first full run
using a custom `/tmp/catc76` wrapper failed two `laneroot` fixture tests; the
two focused tests then passed using normal `/tmp`, and the full `make test`
plus `make vet build smoke` passed without the wrapper. Factory's full test,
vet, build, and smoke gates passed using its normal `/tmp`. On Factory,
`/tmp` is mounted as `tmpfs`; no `/dev/shm` path or custom temporary wrapper
was used. These failed attempts remain recorded and were not counted as PASS.

The regression in
[`catalog_cohort_rollback_sync_test.go`](../src/internal/laneinstall/catalog_cohort_rollback_sync_test.go)
exercises reconciliation, Sandbox rollback, receipt synchronization to all
four fixture hosts, Linux rollback, synchronization again, and finalization
through the actual local selector validation path. It accepts a rolled-back
host only when its completed journal, rollback evidence, and `PreviousTarget`
agree; the existing stopped-service, empty-work, schema, and configuration
guards remain in force.

Fresh supported Dev inventories on 2026-10-21 at 06:04–06:10 UTC showed all
four services stopped, schema 40, and zero active session slots, active
command slots, queued commands, or resumable jobs. The only receipt that held
the current operation was `5c9d691cc33016b1ef5bb7b741dea0de`; the other
historical nonterminal-looking entries were `2ae3b253be9c4c211d00fff162e1dfdd`
and `da3b42aefd53e1a529efed575d1044e3`, and supported status reads show both
already `superseded`. Operation `7a30271dab023394bdd9b43116e5979e` remains
superseded and was not changed.

The four-host receipt copies initially diverged: Mac, Linux, and Factory held
the `blocked` history (SHA-256
`8ffea32af5764b8d1e4cd324154f3fcba8694e8f98fe8c92d8381590abe7d9ce`), while
Sandbox held the `switching` history (SHA-256
`926e6a442f4450f2f7701883e9b6deb73d2b3c77976219f31087e23bdf49f07e`). The
supported Mac `reconcile-history` operation produced a blocked receipt with
both histories retained as branches. Its reconciliation evidence digest is
`1b7a928f5133634ab8362a11174ca260b6f1cdd2825eb399ea8c9d58e177f7ef`; the
resulting receipt SHA-256 was `a68fb97a8ee3c7d8a904462db227e3080a1d4cd47d089d2373181f93207d5b5e`
on all four hosts.

`rollback-start` used fresh all-host inventory, selector, journal, and measured
Sandbox-capacity evidence SHA-256
`777c2b0107913dad8f37823a68c77ae5fe08b2c0767e59a7990053958eeea812`. The
receipt was synchronized to all hosts at SHA-256
`44ad504c48aba8241bfa9f6dd927b1c80261338019637f4c776b0b36ff3328af` before
the first selector rollback.

Sandbox was rolled back first using journal
`81bac94cd52f3a8fb8a4b8da867e639e`. Its completed journal SHA-256 is
`4cf568eb1b2ce983aa25e938d50242afb36372676fafd30fbfde2f6fe84fdae6`; it
returned to legacy bundle `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459`.
The supported operation created a fresh rollback backup at
`/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-408777429`
with manifest SHA-256
`a5b8db816ab1e180103ecb51c0812b1ba6580e92fe27ec44163ba729a05c357c`. The
Sandbox rollback receipt was synchronized to all four hosts at SHA-256
`b844a9e7ef58b4f93452afbad9243c7a60362aab32817f7445d4b50e74a3a27f`.

Linux was rolled back second using journal
`f647cb04c9c03cc6e254408f45d949c6`. Its completed journal SHA-256 is
`8818fcec8c3bf7b569c2367d10055d25b9083f684cb51185571b9e21c278c104`; it
returned to legacy bundle `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863`.
The supported operation created a fresh rollback backup at
`/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-1583166030`
with manifest SHA-256
`127f46172874c9d32336723ce630e18c0bbf6b073a5332fc5f7a68df48f091c3`. The
Linux rollback receipt was synchronized to all four hosts at SHA-256
`4112891635fc9ce499a07a6adc497f548df1a3ac13bf5dc7865b87ba8da1751f`.

`rollback-complete` used fresh post-rollback state and backup-manifest
evidence SHA-256
`89234bc5849ce43a599d0e3dbcbcdf15000a7f50a8ca64e79a195b9d5c26a679`. The
final rolled-back receipt is byte-identical on Mac, Linux, Sandbox, and
Factory: SHA-256
`201deb4ee39c40812b7aa937fd6c11be8248214a8c99356cf47a1182c0b90f9e`.
Final selectors are back on their prior bundles: Mac
`6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310`, Linux
`f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863`,
Sandbox `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459`,
and Factory `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687`.
All four Dev services remain stopped and all inventories remain empty. Mac and
Factory did not switch and were not rolled back. The databases remain at
schema 40; no older database was restored. Existing and newly created backups,
mailboxes, held ACKs, retained work, and shared caches were preserved.

Immediately before Sandbox rollback, `/dev/sda1` had 1,224,752 KiB available;
the Dev database, WAL, SHM, and config measured 634,880, 0, 32,768, and 3,835
bytes respectively. A conservative reserve of 73,823,694 bytes (ten times
those measured inputs plus the 64 MiB floor) left 1,180,322,354 bytes of
headroom. After both new backups, Sandbox still had 1,223,376 KiB available.
No existing backup or shared cache was removed.

No mailbox request or ACK was published. Mailbox acceptance, direct
functional acceptance, queued-SSH acceptance, and Mac-local acceptance are
**NOT RUN**. No new successor has been created, no selector has been changed
after rollback, and QAS/PRD have not been operated in this recovery. Next:
publish a new sealed candidate from the corrected revision, prepare fresh
four-host preflights and backups, supersede the fully rolled-back operation
through the supported host-by-host flow, verify one exact admission hold on
every host, then proceed through Dev → QAS → PRD. The prior sealed candidate
`cat-20261021-147138f` remains unchanged and unselected.

## Historical status — 2026-10-21 04:26 UTC: CAT-04 blocked before recovery

The CAT-04 switched-history recovery correction is committed as
`147138f3a82774db8713a677e365c2c40156154f` (`fix(catalog): reconcile
interrupted cohort switches safely`). The commit was pushed to primary Gitea,
the automatic GitHub mirror resolved to the exact same SHA, and the three
clean Ubuntu `dev` checkouts were fast-forwarded to that SHA before validation.
Mac and all three workers passed the required source gates at this revision:
`make test`, `make vet`, `make build`, and `make smoke`. Mac's first sandboxed
full-test attempt was blocked by `/bin/ps` access; its host-visible rerun passed.
Sandbox's initial `/dev/shm`-only test attempt failed two `laneroot` fixture
fallback cases, then the ordinary-temp rerun passed. Its first build failed at
100% root usage; build and smoke passed with task-owned Go temporary output in
`/dev/shm`. Factory's original quota failure remains historical; the rerun
after its 2 GiB `/tmp` quota change passed all four gates. No shared Go cache,
retained work, or mailbox data was cleaned.

The correction adds supported `reconcile-history` handling for valid divergent
receipt copies with real selector switches, validates each host's stopped
service, empty work inventory, schema, configuration and selector journal
before writing/synchronizing reconciled history, and retains historical
branches. Regressions cover interrupted switched histories, inconsistent host
evidence, restart hold retention, reverse rollback order, and supersession
eligibility. The scoped `catalog-probe-once` helper and its bounded JSON digest
tests are included in the source revision.

The candidate `cat-20261021-147138f` is frozen at source revision
`147138f3a82774db8713a677e365c2c40156154f`; its release manifest SHA-256 is
`97c6dcf9ca634c4656f792f165a640f188ecbaccef3a9e9900836e2ce140080f`. Supported
`catalog-inspect` on all four hosts validated the same release identity and
these native bundle hashes:

| Host | Bundle SHA-256 | Candidate publication |
| --- | --- | --- |
| `mac-control-plane` | `8820476ef7e0161b6055f3af464cc759da0ef0aa3cf8a078464c0e0fd3491e3f` | PASS |
| `linux-host` | `5a10463e490ad4baf07470c3835b0922334bcbb039b17dbe6eec9db690ed1c18` | PASS |
| `sandbox-host` | `56494835d7752ccc34498bb0621eb0c615e1c8c1d2530bdd08a7a035e17c6f25` | PASS |
| `factory-sandbox` | `0cba389804562efa63a96858518fb133c7c2fadd29923743b9a37d58068d1afb` | PASS |

Publication and inspection did not change a selector or start a service. Fresh
supported Dev inventories at 04:24–04:25 UTC found all four services stopped,
schema 40, and zero active session slots, command slots, queued commands, or
resumable jobs. Linux and Sandbox still select `cat-20261020-5a77768`; Factory
and Mac still select their legacy bundles. No QAS or PRD operation was made.

The requested operation `7a30271dab023394bdd9b43116e5979e` is already
`superseded` on all four hosts. Its synchronized receipt SHA-256 is
`da532b48cd158cb957e1706c2dbdcd7c99faaea8dfad28fe5532baaf5f8c3d2b`; its
history already records switches on Linux, Sandbox, Factory and Mac followed
by rollbacks in reverse order (Mac, Factory, Sandbox, Linux). It cannot safely
receive another `block` or `rollback-start` transition.

The only non-superseded receipt found in each Dev receipt directory is instead
`5c9d691cc33016b1ef5bb7b741dea0de`. Mac, Linux and Factory copies are at
`blocked` (SHA-256
`8ffea32af5764b8d1e4cd324154f3fcba8694e8f98fe8c92d8381590abe7d9ce`); the
Sandbox copy is at `switching` (SHA-256
`926e6a442f4450f2f7701883e9b6deb73d2b3c77976219f31087e23bdf49f07e`). The
Sandbox history includes the Linux and Sandbox switches. Their completed
selector journals are Linux `f647cb04c9c03cc6e254408f45d949c6` and Sandbox
`81bac94cd52f3a8fb8a4b8da867e639e`; Factory and Mac have no selector switch
for this receipt. The supported source rules classify `blocked` and
`switching` as admission-holding stages. The prior authorization names `7a…`,
not this different live operation `5c9…`; no receipt reconciliation, rollback,
successor creation, or supersession was attempted pending confirmation of the
correct operation ID.

Sandbox root has only 46,808 KiB available (100% used). Its Dev database is
634,880 bytes, WAL 0 bytes, SHM 32,768 bytes, and configuration 3,835 bytes.
The recorded cohort preflight used a 67,108,864-byte free-space requirement,
which the current filesystem does not meet; do not attempt a fresh successor
preflight or selector change until capacity is restored and measured again.
The current database/configuration sizes are not permission to remove retained
backups or shared caches.

The exact `CatalogCohortAdmissionHeld` pre-switch check for a new successor,
Dev direct-MTLS acceptance, queued-SSH acceptance, and Mac-local acceptance are
**NOT RUN**. No selector was switched and no service was started. Mailbox
acceptance is **NOT RUN** by instruction; no request or ACK was published.
QAS and PRD promotion have not begun. CAT-04 remains **BLOCKED** on
confirmation of whether to recover the actual held operation `5c9…` and on
Sandbox capacity restoration; CAT-05 through CAT-07 remain **NOT STARTED**.

## Source correction and Factory gate rerun — 2026-10-20

The delayed-reconciliation correction is committed as
`2f0e72eb2896427ab79e5a5f58a90b60be5d4769`. It was pushed to primary Gitea,
verified at the same full SHA on the automatic GitHub mirror, and fast-forwarded
to the clean `dev` checkouts on Linux, Sandbox and Factory. On Mac and all three
workers at that exact source revision, `make test`, `make vet`, `make build` and
`make smoke` passed. Mac's first sandboxed test attempt could not execute `/bin/ps`;
the authorized host-visible rerun passed. Factory's original quota-related
failure remains preserved: linker output reported `disk quota exceeded`, and
temporary configuration/SQLite tests reported disk-I/O errors.

Factory's effective `/tmp` per-user quota was read with `quotactl_fd` for UID
1001: both soft and hard limits were 2,097,152 KiB (2 GiB), with usage
387,293,184 bytes and 538 inodes. `/tmp` is a 4 GiB tmpfs mounted with
`usrquota`; `df` alone did not establish the UID limit. The Factory rerun used
the task-owned short path `/home/ubuntu/.cache/c04r` with `TMPDIR`, `GOTMPDIR`
and `SQLITE_TMPDIR` exported. All four gates passed at `2f0e72e`; the temporary
directory, log and status marker were removed after processes exited. Shared Go
caches and retained data were preserved.

## Latest status — 2026-10-20 17:06 UTC: CAT-04 BLOCKED after the Dev selector switch

The same sealed candidate was published and inspected on all four hosts:

| Host | Dev selector | Bundle SHA-256 |
| --- | --- | --- |
| `mac-control-plane` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261020-2f0e72e/mac-control-plane` | `998caace471e798c0d1228acde9d3b631f805079c2a473da15905e4d6ded304b` |
| `linux-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-2f0e72e/linux-host` | `2d4487ff47fe4e07adb9307d9b68ca2e48c5fce137a549b900f63559ddb18fa1` |
| `sandbox-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-2f0e72e/sandbox-host` | `7d20c9e0f29e3a24ec8ba8def558025d0136e9d87b6e07d1167d6b11039f6524` |
| `factory-sandbox` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-2f0e72e/factory-sandbox` | `d46a836f938886f284f157893f67b0b2b340178a5808464a917a7a29837e50b2` |

The sealed release ID is `cat-20261020-2f0e72e`, source revision
`2f0e72eb2896427ab79e5a5f58a90b60be5d4769`, and `release.json` SHA-256
`96d43c0e7013be80e0c26602538d05021062ec725f9b5163cc81dccc6d433638`. All
four selectors point to those exact bundles; all report schema 40. The
selector operations and pre-switch backup manifest digests are recorded in the
current cohort receipt and host journals. The cohort operation is
`7a30271dab023394bdd9b43116e5979e`, still at `switching` with nine transitions.

| Host | Selector operation | Pre-switch backup manifest SHA-256 |
| --- | --- | --- |
| `linux-host` | `6fe85937d8df01d7b781005bf03bd855` | `e4143a8c0da232f29c207328722a2d3f7e456247b99b0a2d453a89dd3247146e` |
| `sandbox-host` | `e232ac57d274938a0d2d54843c2e7d43` | `63a8864a8d69033b88a13fca8c52937c723c26b3a2dbef9aa944fca1cbb541be` |
| `factory-sandbox` | `d198ecac5f486117177e3d1eb41242d2` | `cc00c5d6ce603f4cb6728fb16587ee3e13682d53096fd90a87361421d441651f` |
| `mac-control-plane` | `83825c4e0619b38105ae79d7d65dcf93` | `54c695bc621cb3a1b3b7a348714c6ffb2cc28de99bac7b2cc7f23922e4f8845c` |

Fresh supported status and work-inventory reads found all four Dev services
inactive and zero active session slots, active command slots, queued commands
or resumable jobs. QAS and PRD service status remained active on all four hosts;
this attempt changed only Dev selectors. No QAS or PRD promotion began.

The guarded Linux Dev start returned `complete`, but the service did not become
ready: `runnerd-dev.service` exited with `ExecMainStatus=78` at 16:58:14 UTC,
and ten private-socket readiness attempts returned connection refused. The
service log reported `runnerd: catalog cohort receipt is invalid; refusing
work admission`. No request was accepted or executed.

Diagnosis: `CatalogCohortAdmissionHeld` validates every receipt and fails
closed when more than one receipt holds admissions. On each of the four hosts,
supported status reads show the current operation `7a30271dab023394bdd9b43116e5979e`
at `switching` (nine transitions) and old operation
`cbf3c2318d5702a0134ce8cd486d2b5f` still at `held` (one transition). Those two
simultaneous holds explain the startup refusal. Another old operation,
`7b310f3b76834ca3adb0a755e5483653`, remains `prepared` (zero transitions); it
does not hold admission yet but is still an abandoned pre-switch receipt that
needs supported disposition. Linux's Dev receipt directory contained 37
records: these three plus 34 `superseded` records. Supported status reads on
the other three hosts confirmed the same stages for these three operation IDs.
This is incomplete receipt reconciliation, not evidence of a release-binary
hash mismatch.

Read-only SHA-256 checks confirmed byte-identical copies of these three
receipts on Mac, Linux, Sandbox and Factory:

| Receipt operation | Stage | Receipt SHA-256 |
| --- | --- | --- |
| `7a30271dab023394bdd9b43116e5979e` | `switching` | `c317edff862631d632ba6aed86056c8794c66091c053203550046c65fe45f017` |
| `7b310f3b76834ca3adb0a755e5483653` | `prepared` | `7dbef8fd15ae81e1ccbb655c9ca3be36b5195c11546257f49a2a5fea4d9216d7` |
| `cbf3c2318d5702a0134ce8cd486d2b5f` | `held` | `5d3d9838c02cdd6032b84fe4e7d54341e859ef367d655c73e405c14b7110d114` |

Current state is preserved: Dev selectors remain on the sealed candidate,
services remain stopped (Linux failed closed), and all recorded work inventories
are empty. No rollback or receipt mutation was attempted after the failed gate.
The next recovery must use the supported cohort path to reconcile these two
abandoned pre-switch receipts and the already-switched current operation before
any service starts again; do not ignore ambiguous receipts or edit receipt
files. Dev runtime/direct acceptance is **NOT PASS**; queued/mailbox acceptance
is **NOT RUN**. No mailbox requests or ACKs were published, and external
publishers remain paused.

The 2026-10-20 14:55 UTC status below is retained as historical evidence.
The older 2026-10-19 failure remains historical evidence as well.

**Historical status (2026-10-20 14:55 UTC): BLOCKED during recovery of a partial Linux Dev selector switch.** Release `cat-20261020-7786fbc`, source revision `7786fbc3e5c78aa42e898d74cfceaa265da8df99`, release manifest SHA-256 `7f44d5896b7fc068b7266124ace4c4ff3f279cffd779cde02cd6117ac720d4f7`, has only its Linux Dev selector active. The selected bundle hash is `bd2537669aee2bbf8408c87d916704970f2ce990fe50032c72c14cedcb338edd`. Mac remains on legacy bundle `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310`, sandbox on `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459`, and factory on `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687`. All four Dev services were observed inactive with zero active session slots, active command slots, queued commands, and resumable jobs between 14:55:23Z and 14:55:35Z. No QAS or PRD operation occurred in this attempt.

Linux selector journal `a500acf4cdcb03a2a2e5ecb1e16a6136` is `complete`; it records cohort `cebd9ec7e0b62a26cdbdba2c4569d2cc`, predecessor legacy bundle `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863`, the candidate path, and verified backup manifest `a629b7d41fd4db7813928120cca1b3cdf17731dc2d4c96b54e3fd029e00311be`. The supported `catalog-reconcile` attempt returned `invalid or inconsistent catalog selector operation` while trying to write its `switch-reconciled` receipt. A temporary read-only Go overlay test against the synchronized Linux checkout at `7786fbc3e5c78aa42e898d74cfceaa265da8df99` validated the journal fields, sealed release identity, predecessor, and backup. Source inspection then identified the exact rejection: the receipt writer required the original selector operation creation timestamp to be within two minutes of the most recent cohort preflight (`14:30:44.489942149Z` versus `14:33:53.401368469Z`). The host-local `Reconcile` path separately rechecks service stopped, work empty, schema, configuration, and a fresh backup before finalizing the durable selector journal. Applying the new-switch limit to its later receipt was therefore inconsistent.

The Mac source correction then in progress retains the two-minute limit for new selector switches and permits a delayed `switch-reconciled` event only when its journaled selector operation follows the original preflight and the supported reconciliation path has completed its fresh guards. A regression covers late reconciliation, rejects a stale ordinary switch, and rejects an operation timestamp before preflight. The claims in this historical snapshot are superseded by the delivered source correction and latest status above. Factory `/tmp` measured 3,825,668 KiB available (4 GiB total) and root `/` 36,315,128 KiB available at 14:55 UTC. Sandbox `/` measured 1,383,724 KiB available (98% used). Treat these as snapshots and recheck immediately before each backup, publication, or switch. Project publishers remained paused; mailbox acceptance was **NOT RUN** and no request or ACK was published.

The previous 13:04 UTC status is retained below as a historical snapshot; the
older 2026-10-19 failure remains historical evidence as well.

## Prior status — 2026-10-20 13:04 UTC

## Fresh read-only recheck — 2026-10-20 13:04 UTC

Supported `catalog-status`, `catalog-cohort status`, and
`catalog-work-inventory` reads found the same Dev selectors and sealed bundle
hashes listed below. At 12:55 UTC, Linux, sandbox, and Factory each reported
the Dev service inactive and zero active session slots, command slots, queued
commands, and resumable jobs. Mac reported the same at 13:03 UTC. Each receipt
copy remains at `verifying` with 10 transitions; no receipt was modified.

Linux's Dev `staging/catalog-cohorts` directory contains 30 receipt records:
28 `superseded`, one `prepared` operation (`7b310f3b76834ca3adb0a755e5483653`),
and the current `verifying` operation. The historical records are preserved;
the old-attempt blocker no longer prevents the current single held operation.
On Linux, a temporary read-only Go test against the clean source checkout at
`57e3775e8580f49c3a262db7c52fbdacf081d879` called
`CatalogCohortAdmissionHeld` using the live Dev plan and receipt directory. It
returned `held=true`, operation `b33462e2631356c319d3e14fc22397c7`, with no
error. The temporary test file was removed and the checkout remained clean.
This establishes the source-level hold result against the current host state;
it does not explain the earlier HTTP 400 from the service.

The direct admission-fence discrepancy remains unresolved. No Dev service was
started again after that HTTP 400. The source change now emits a bounded startup
log with the evaluated hold boolean and operation ID, so the next supported
start can show whether the daemon recognized this receipt before any functional
probe. The direct API regression also asserts that a no-token malformed `{}`
POST is rejected with HTTP 503 while held, before request-body validation.

Factory `/tmp` is a 2 GiB tmpfs with 1,728,516 KiB available at 13:04 UTC.
Sandbox `/` reported 1,961,640 KiB available (97% used) at 13:04 UTC, compared
with 520,128 KiB available (100% used) at 12:55 UTC; no files or caches were
removed by this work. Recheck the sandbox filesystem immediately before any
backup or selector write. Mailbox requests and ACKs remain **NOT RUN**.

## Latest CAT-04 attempt — 2026-10-20

The sealed candidate is `cat-20261020-57e3775`, source revision
`57e3775e8580f49c3a262db7c52fbdacf081d879`, release manifest SHA-256
`c2c2084e0e9d651e3b5863472895c1c11b3bafcf83e8e2818dece173620a7271`.
Per-host sealed bundle SHA-256 values are:

| Host | Dev selector | Bundle SHA-256 |
| --- | --- | --- |
| `mac-control-plane` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261020-57e3775/mac-control-plane` | `d8c9f6fb502848fe98cebd204d2f8d4f5f75a07d48a77e3800c61a319bc08ee5` |
| `linux-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-57e3775/linux-host` | `f236285b0a78fbbb2c4225ebcaffaaa1bf852580041d394e6b59994b61102567` |
| `sandbox-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-57e3775/sandbox-host` | `ed852a2a143b8fceec32470187d5cd95c0ff62fc297892026e571104624d22aa` |
| `factory-sandbox` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-57e3775/factory-sandbox` | `6c45d101dda4d989e8d8c0da299c0a13d9c0ae449d2faaef30c7bbf8fdda6188` |

The supported `catalog-cohort` status read on Linux confirmed receipt stage
`verifying`, release identity and all four host bundle identities. Fresh
`catalog-work-inventory` observations at 12:26 UTC showed zero active session
slots, active command slots, queued commands and resumable jobs on every Dev
host. The same inventories were zero again at 12:37 UTC and 12:48 UTC. At
12:48 UTC the protected cohort receipt SHA-256 was
`83d7de500cabcc41a145833b62c56ce5a36709a3d5c8627a106287d3fde34260` on Mac,
Linux, sandbox and factory; every copy reported operation
`b33462e2631356c319d3e14fc22397c7`, stage `verifying`, and 10 transitions.
QAS selectors remain on their previous legacy bundle identities, and their
Ubuntu services remain active. PRD remains `unselected` in the catalog; its
Ubuntu services remain active, and the Mac QAS and PRD LaunchAgents were
observed running. The owner instruction to keep external project publishers
paused remains in force.

The bounded Linux direct-mTLS check used the configured Go TLS path with the
lane CA, normal hostname validation, configured server fingerprint and URI
SAN; `/health/ready` returned HTTP 200 and build revision
`57e3775e8580f49c3a262db7c52fbdacf081d879`. Native Python rejected this
certificate with `Missing Authority Key Identifier`; native curl rejected it
with `unsupported or invalid name syntax`. Those client attempts stopped
before submitting work; no insecure TLS option was used.

The supported one-time probe was prepared for Linux with controller
`runner-controller-dev-linux-host`, idempotency key
`catalog-probe-b33462e2631356c319d3e14fc22397c7-linux-host`, and request-body
SHA-256 `928274f138b9e1ae9a49c779871a9af191b04d4c489a767938e628bd9ac0dc79`.
The accepted job identity was `job-50057e7e639a912c938dabe66d51abf7`, with
session identity `sess-d4cec60d8ad63484b85166ebbafae5cb` and command identity
`cmd-6d87fc2be2d852f04404a87335490973`. The direct API returned HTTP 202;
the authority read returned HTTP 200 with phase `failed`, no command state or
exit code, incomplete output and teardown `not_created`. The worker's
authorization log at `2026-10-20T12:31:24.402039298Z` records
`reason_code=environment_denied`. The probe body used `linux-dev`, while the
installed Dev configuration registers `linux-host-dev`. The fixed probe
environment was therefore wrong and the harmless command did not execute.
The failed job/session/command history remains retained; no replay occurred.

The probe scope on Linux remained `ready` with no bound job after that
request; the token expired and its temporary capability file was removed.
After all Dev services had been stopped, Linux Dev was started once through
the supported operator to verify the admission fence. A direct-mTLS POST of
only `{}` with no probe token returned HTTP 400, not the expected HTTP 503
`maintenance_admission_closed`; this malformed request created no job. That
response shows the fence did not protect the observed request. The later
read-only `CatalogCohortAdmissionHeld` evaluation returned true against the
same host's current receipt and configuration, so the mismatch between that
source-level result and the observed HTTP response remains unestablished. The
service was stopped and no additional start or scoped probe has occurred.

Following the failed fence check, supported `runner-lane stop` operations
stopped Linux, sandbox and factory Dev services; Mac's first stop attempt
returned `Operation not permitted` from the restricted shell, then the same
supported stop succeeded in the authorized elevated shell. A final status
read reported Mac Dev inactive; systemd reported the three Ubuntu Dev
services inactive. Dev selectors, database state, configuration, mailboxes,
workspaces and shared caches were not changed by the stops. The failed probe
job remains recorded. Sandbox `/` had 527,244 KiB available (about 515 MiB)
at 12:37 UTC. Factory `/tmp` is now a 2 GiB tmpfs, with 1,728,516 KiB
available at 12:36 UTC. Recheck sandbox headroom immediately before any later
backup or selector operation.

Source corrections prepared in the Mac worktree: `CatalogMaintenanceProbeRequestBody` now uses the lane's configured `<host>-<lane>` environment (for example, `linux-host-dev`) instead of the invalid generic `linux-dev`; Linux `runnerd` logs the evaluated maintenance hold and receipt identity at startup; and the direct API regression asserts that a no-token malformed request is rejected by the hold before body validation. The probe-body regression covers all three worker profiles in Dev, QAS and PRD while asserting the fixed script, remote target, empty source and five-second timeout remain unchanged. On Mac with Go 1.27.1, the full `make test`, `make vet`, `make build`, `make smoke`, and `git diff --check` gates passed. Git delivery, required Ubuntu host gates, and publication of a new sealed release remain pending. Candidate `cat-20261020-57e3775` remains immutable and unchanged.

Mailbox requests and ACKs: **NOT RUN**. No mailbox files, held ACKs or
publisher states were altered. No QAS or PRD promotion began. CAT-04 cannot
pass until a new corrected candidate is delivered, Dev is fully accepted on
all four hosts, and the maintenance-fence discrepancy is resolved.

## Preserved historical evidence — 2026-10-19

The following records describe the prior candidate and prior startup failure;
they are retained without being presented as the current Dev state.

### Candidate and source delivery

The immutable release is `cat-20261019-f0e2843`, built natively on each host
from source revision `f0e2843c5c92874ae817e7653fef01dd1311e5f6` with Go 1.27.1.
The exact shared `release.json` SHA-256 is
`aa3398575c46950c41e5279beb96b7c872bb8988e9203b34642584928ef4bba0`.
Compatibility declares reads for schemas 1–40 and writes for schema 40.

| Host | Immutable catalog path | Bundle SHA-256 | `catalog-inspect` |
| --- | --- | --- | --- |
| `mac-control-plane` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261019-f0e2843/mac-control-plane` | `ff99ba5a3930a9c4d3e6b132426400405b7d89705920c40ed018f93c916576be` | PASS |
| `linux-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/linux-host` | `3da44d9b136e65c17f78326ebc0faf59e6053cdbb1df940b485b9b46035e1cb1` | PASS |
| `sandbox-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/sandbox-host` | `c46b888411cfc6aff4064807edfeeb27088ab0279bbbf6daeec66504c002c66b` | PASS |
| `factory-sandbox` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/factory-sandbox` | `46950532a9efb9983937e0808ccfe1f9b4be3366d1bae9706e5437806d4d82fd` | PASS |

Each host's `catalog-inspect --host <host> --release-id cat-20261019-f0e2843`
returned the same release digest, the expected local host identity, source
revision and bundle digest. The live Dev selectors and three Ubuntu launcher
entrypoints were changed through the supported operations. Configuration,
databases, mailboxes, credentials, workspaces, logs, and retained state stayed
in their existing lane locations.

Candidate source revision `f0e2843c5c92874ae817e7653fef01dd1311e5f6` is the
sealed runtime input. A supported launcher-refresh correction was delivered
afterward as source commit
`daf9978fa754e7b47fe93f13c5ac28603b1b7d1d`; it changes only operator behavior,
not the sealed runtime bundles. Mac and all three Ubuntu checkouts passed
`make test`, `make vet`, `make build`, and `make smoke` at `daf9978`. Gitea
`origin/dev` and the automatic GitHub mirror both resolve to that exact commit.
All three Ubuntu checkouts were clean on `dev`, fast-forwarded from GitHub, and
verified at `daf9978` before their gates. At `f0e2843`, the same gates had also
passed on Mac and all three workers. Earlier failures remain history and are
not acceptance.

### Historical pre-CAT-04 live baseline

The four current Dev selectors remain legacy. Their bundle identities are:

| Host | Dev and QAS legacy bundle SHA | Running Dev/QAS build revision |
| --- | --- | --- |
| `mac-control-plane` | `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `linux-host` | `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `sandbox-host` | `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `factory-sandbox` | `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |

Read-only `catalog-status` confirmed PRD remains catalog-unselected on all
hosts. The existing unsuffixed PRD services are still running from their
legacy paths. The Mac's six Dev/QAS/PRD LaunchAgents and all nine Ubuntu
`runnerd`, `runnerd-dev`, and `runnerd-qas` services were observed active.
No service lifecycle or selector operation was attempted.

Supported private-socket `/health/ready` snapshots showed Dev and QAS ready
with build revision `00c1ca6e7694842c35d398583b3f26fea59e3613` on every
host. Reported active session slots, active command slots, queued commands,
queued intents, and mailbox backlog were zero. Mac dispatch-attempt baselines
were Dev 20 and QAS 13. `storage_errors_total` was zero except sandbox Dev and
QAS, both 2; `cleanup_failures_total`, event gaps, event lag, and truncation
counters were zero. The health response does not expose `resumable_jobs`, so
these snapshots are not the full restart guard and do not establish a fresh
quiescence PASS.

Sandbox `/` has 81 MiB available; `/dev/shm` has 2.9 GiB. Existing Dev and QAS
SQLite files are each below 1 MiB, but no maintenance backup has been created.
The small root-filesystem headroom must be rechecked immediately before any
backup or selector switch. No cleanup, cache deletion, or retained-work change
was made.

### Historical Dev cohort and qualification gates

At approximately 20:51 UTC on 2026-10-19, the protected Dev receipt and current
selectors were rechecked. The receipt was at `switching` for release
`cat-20261019-f0e2843`; its four host fence proofs record the owner's publisher
pause instruction and stopped local Dev ingress/dispatch services. No external
client census is claimed. Fresh work inventories on Mac, Linux, sandbox and
factory reported zero active session slots, active command slots, queued
commands, and resumable jobs. Mailbox requests and ACKs were not published.

The first Linux launcher refresh correctly refused a cohort backup bound to
the old legacy bundle. Source/operator correction `daf9978` then made the
supported refresh capture a consistent backup bound to the selected catalog
release and reuse that exact backup on retry. The refresh completed on all
three Ubuntu workers, with old launchers retained as backups:

| Host | Old launcher SHA-256 | Refreshed launcher SHA-256 | Selected-release backup | Backup manifest SHA-256 |
| --- | --- | --- | --- | --- |
| `linux-host` | `68b10a8d28ada7d75753037b9944b053e08c0227d322523f4af2f10db603f88b` | `6ad403a72c87988f1d722352c7415f9a2880dd1b6e0291dfdadf54cf7d6bc5c0` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-368157258` | `2b9cd6d91d867c1382fff10322fc50b3b90f875f9ee56a94c9c880ebf25328f4` |
| `sandbox-host` | `68b10a8d28ada7d75753037b9944b053e08c0227d322523f4af2f10db603f88b` | `096347de91d8c9e0f199a1634fca6666d69764e8a8d7ed566b0f3f6931b119de` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-469300006` | `f1ce1716140e1f79ad287b6e1c066cd2717ce55663a87eae33575fac1fded1cc` |
| `factory-sandbox` | `68b10a8d28ada7d75753037b9944b053e08c0227d322523f4af2f10db603f88b` | `1d213d9e6025971b206bf6ca532e877914bebfee82541e18123079e7991b1a2e` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-3665589844` | `7e0bf384ea0a3e031b06d0b256170a3815111cfc675a2b5b45239a80d73ace8a` |

The Mac Dev LaunchAgents resolve through the selected catalog path, so no Mac
launcher refresh was needed before start. Linux, sandbox, factory, and Mac
Dev services were all stopped after the refresh. The Linux free-space check
showed 188,121,088 bytes on sandbox `/` before refresh. The sandbox Dev backup
occupies 643,318 bytes by recursive `du -sb`, the retained old launcher backup
is 2,451 bytes, and the sealed sandbox bundle occupies 42,068,658 bytes. The
backup and launcher refresh completed; at 21:04 UTC a fresh `df -B1` showed
159,035,392 bytes available (`df` rounds the filesystem to 100% used). This
confirms the completed Dev operations fit; it does not establish backup
headroom for later QAS/PRD database sizes. No files, databases, retained work,
backups, or shared caches were deleted.

The supported start command was run only for Linux Dev:
`runner-lane start --host linux-host --lane dev`. It initially reported
`complete`, but systemd immediately reported `runnerd-dev.service` failed,
`MainPID=0`, `ExecMainStatus=78`; the Dev socket refused connections. The
bounded service log gives the exact startup error:
`runnerd: catalog cohort receipt is invalid; refusing work admission`.

Inspection found five preserved Dev cohort receipts on Linux:

| Receipt ID | Stage | Relevant recorded transitions |
| --- | --- | --- |
| `2ae3b253be9c4c211d00fff162e1dfdd` | `held` | hold, Linux preflight; no switch |
| `da3b42aefd53e1a529efed575d1044e3` | `held` | hold, Linux preflight; no switch |
| `12a4be798fe39fec658142eab29c9f9f` | `blocked` | hold, block; no switch |
| `627c78a2bccecd80d1d7f0c405723895` | `rolled_back` | Linux switch, Linux rollback, rollback complete |
| `9ae78b3bca5c6d6c178db524971ec37e` | `switching` | current four-host selector switch |

The candidate's admission guard refuses startup when more than one receipt
holds admissions. The old
`held`/`blocked` receipts have no supported closeout path that can safely
release them (the supported open action requires a completed or rolled-back
receipt and an active service). Renaming, deleting, editing, or bypassing
these receipts would violate the retained evidence and admission guards. This
is a proven candidate/receipt guard incompatibility, not evidence of corrupt
state or a route failure. The frozen runtime candidate cannot be changed
without rebuilding and repeating Dev then QAS qualification.

Fresh read-only service inspection at 21:04 UTC showed Mac Dev LaunchAgents
unloaded, Mac QAS/PRD LaunchAgents running, Linux Dev failed (exit 78) with
Linux QAS/PRD active, and sandbox/factory Dev inactive with QAS/PRD active.
No Mac Dev service was started. The sandbox root had 159,035,392 bytes
available at that check.

Therefore CAT-04 installed acceptance is **FAILED/BLOCKED**, not PASS: Linux
Dev did not remain active; no four-host readiness, mTLS route, bounded
execution, lifecycle/teardown, capacity, or peer-preservation acceptance could
be completed. QAS promotion (CAT-05) and PRD promotion (CAT-06) were not
started. Mailbox request/ACK acceptance is **NOT RUN** by instruction. All
mailbox contents, held ACKs, retained requests/work, configuration, secrets,
and databases remain preserved; no replay or manual capacity release occurred.

### Historical commands and machine scope

- **Mac and each Ubuntu source checkout:** fast-forward to `daf9978`, then run
  `make test`, `make vet`, `make build`, and `make smoke`; all passed. Operator
  executables were built from this revision for the supported refresh/start
  operations and their hashes recorded outside the checkout.
- **Ubuntu workers:** supported Dev launcher refresh completed over SSH using
  explicit identities and verified host keys. It created consistent selected-
  release backups and retained previous launchers. No tracked host files were
  edited.
- **Temporary operator tools built from `daf9978`:** Mac
  `/private/tmp/runner-lane-cat-daf9978-mac`, SHA-256
  `0b56aec174ab3a05772c1cf3c3d37c95ac29b2295b953e27fcfda27e54e92f58`; Linux
  `/dev/shm/runner-lane-cat-daf9978-linux`, SHA-256
  `57db13120e0847aa590638e9dc95c36024283a31d9a3cd4607b4cb06e9c28b9c`;
  sandbox `/dev/shm/runner-lane-cat-daf9978-sandbox`, SHA-256
  `4d4574af169ecaa3c59108291bba6a5b6ec0b0c709e77123fa2dc2f8a7586fb1`;
  factory `/dev/shm/runner-lane-cat-daf9978-factory`, SHA-256
  `77b2ed7c625609341a7996ff59389bdc1cdca43c3b53f38577479966edc5159a`.
  These operator executables do not alter the sealed runtime bundle hashes.
- **Linux Dev:** supported start was attempted once and failed with exit 78;
  the exact log message and receipt states are recorded above. No other Dev
  service was started, and QAS/PRD service state and selectors were not changed.
- **All hosts:** mailbox acceptance is **NOT RUN**. No mailbox request/ACK,
  retained work, held ACK, configuration, secret, or database was changed.

### Historical delivery and next gate

The operator correction source commit is `daf9978fa754e7b47fe93f13c5ac28603b1b7d1d`; Mac `dev`, Gitea `origin/dev`, and GitHub `dev` were verified at that exact SHA. All three Ubuntu checkouts were clean on `dev`, fast-forwarded from GitHub, and verified at the same SHA before host gates. This evidence/plan update is documentation-only and does not change the sealed runtime candidate or its source revision.

CAT-04 is blocked on a supported way to reconcile the preserved historical
cohort receipts with the frozen candidate admission guard, or on a corrected
runtime candidate. No such correction is authorized by these evidence edits.
Do not begin CAT-05 QAS until all four Dev services pass selector identity,
readiness, direct and queued route, lifecycle/teardown, capacity, and peer-
preservation acceptance. Do not prepare or execute CAT-06 PRD until CAT-05
QAS passes. CAT-07 remains unstarted. The sealed `f0e2843` candidate remains
unchanged; any runtime correction requires a new sealed release and renewed
Dev then QAS acceptance.
