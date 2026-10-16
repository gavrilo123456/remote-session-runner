# LANES-PREP-00 — readiness packet discovery

**State:** BLOCKED. Read-only discovery completed to the extent permitted by
current access; required provider/root access and material feasibility gates
remain unresolved. The owner separately authorized direct observations for
both PREP-00 and L0, but this failed PREP exit prevents the full L0 unit from
starting under the serial plan.

**Observed:** 2026-10-16 09:54–10:01 UTC. All observations are point-in-time; rerun the
applicable inventory immediately before any future live gate.

## Authorization and limits

The owner authorized this bounded route for `LANES-PREP-00` and `LANES-L0`:

- Direct read-only Mac shell and SSH observations across `mac-control-plane`,
  `linux-host`, `sandbox-host`, and `factory-sandbox`, using the configured
  explicit SSH identities and existing access.
- No Runner request publication or ACK, no defect probe, and no change to
  installed configuration, services, storage, or firewall rules.
- Documentation/evidence edits and their Git delivery are separately
  authorized mutations under `AGENTS.md`; those mutations are recorded as Git
  source delivery, not as read-only host observations.
- Other execution uses the normal mailbox workflow. The unsuffixed production
  Runner remains protected. Exact operational approval checkpoints A01–A10
  remain in force; this route exception waives no failed gate.

No Runner request or ACK was published, no defect probe was run, and no
installed configuration, service, storage, or firewall rule was changed in
this unit. A read-only mTLS `GET /health/ready` was issued to each existing PRD
remote endpoint. Directory listings below are metadata/counts only; no request,
ACK, response, event, or diagnostic payload was opened by this inventory.

## Pre-unit source and read inventory

- Mac authoritative checkout: clean `dev`, HEAD
  `ad77682ae9ba780d5ac672a932780fdd7c440f81`.
- Gitea `origin/dev` and automatic GitHub mirror `github/dev` both resolved to
  that exact SHA. All three Ubuntu source checkouts were clean `dev` at the
  same SHA, with `origin` set to the GitHub mirror.
- The previous lane unit is the planning ledger only; it remains NOT RUN.
- Document blob IDs at the pre-unit HEAD:

| Input | Blob ID |
| --- | --- |
| `005-initial-idea.md` | `41177ce5eefd9a063d263fd4e2875c8d1572e9a6` |
| `010-dev-qas-prd-three-machine-lanes-detailed-design.md` | `dcfd8acb56fc5154cc207dbf06e2432c87a81442` |
| `015-dev-qas-prd-three-machine-lanes-phased-implementation-plan.md` | `62473863e350de89645b1ac60967bd3cc51847e3` |
| `020-dev-qas-prd-three-machine-lanes-execution-handbook.md` | `789324119cdc0c2d733cf02560e34cc8c2674637` |
| original initial design | `308dfb82a25029a786f8d8341e6a89940a2eba0b` |
| original detailed design | `587654e9b71948519cc6a9e8d0c4813405e42e29` |
| original phased plan | `260bc76b32da215bfc8ad0f1adf05889aa6c244f` |
| `AGENTS.md` | `ec13fc2495248399cf7eebbf832faef838f95ac0` |
| `LANES-000-execution-ledger.md` | `9e4e9972c830b3375919f1f65c8441e9750da906` |
| `LANES-execution-handoff-review.md` | `d2f6448ad2f7b62aba16630886c5d39841b60fd8` |
| `LANES-plan-review.md` | `ac7f1dd86c0bda2c02d622e1a4f64fa56c8c243b` |

Read in this preflight: the complete current lane idea, detailed design,
phased plan, execution handbook, repository `AGENTS.md`, the execution ledger,
the handoff review, and plan-review evidence. The historical initial/detailed
PoC designs and plan were fully read earlier in this active implementation
session. Relevant runtime observations covered the Mac V2 config and active
LaunchAgents, Linux `runnerd.service` identity, installed component versions and
digests, configured mailbox root metadata, and the read-only PRD health route.
The PRD report and acceptance evidence were rechecked for the corrected
BUG-024 ACK classification described below.

## Source, installed runtime, and route observations

The source checkout SHA is not an installed-build identity. The current
unsuffixed PRD compatibility runtime remains active:

| Host | Checkout | Installed status and build identity | Safe artifact/config identity | Existing PRD route |
| --- | --- | --- | --- | --- |
| `mac-control-plane` | `ad77682...`, clean `dev` | `runner-local` and `runner-locald` LaunchAgents running; PIDs `48708` and `48627`; build revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58` | local binary SHA-256 `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`; locald `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13`; V2 config `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` | Local control services running |
| `linux-host` | `ad77682...`, clean `dev` | `runnerd.service` active/enabled; build revision `2802f00c93eb51a80f23b9380238c309545d6329` | `runnerd` SHA-256 `22f33d6cd3d429c6d91537031954222d7a648e3a3d46e1e169ddbeaa89c21104`; config `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `10.0.0.200:8443`; mTLS `GET /health/ready` returned HTTP 200, TLS verify 0 |
| `sandbox-host` | `ad77682...`, clean `dev` | `runnerd.service` active/enabled; build revision `2802f00c93eb51a80f23b9380238c309545d6329` | `runnerd` SHA-256 `b0d864b1ff285f6591868aa463e913537955bf1a0a06130f31e8fdd635004491`; config `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `10.0.0.14:8443`; mTLS `GET /health/ready` returned HTTP 200, TLS verify 0 |
| `factory-sandbox` | `ad77682...`, clean `dev` | `runnerd.service` active/enabled; build revision `2802f00c93eb51a80f23b9380238c309545d6329` | `runnerd` SHA-256 `fafb7d9ba5d343d425fa1752c21a4859a3f42c344d5eca5a43ee5f3dfc0ee374`; config `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `10.0.0.30:8443`; mTLS `GET /health/ready` returned HTTP 200, TLS verify 0 |

The Linux entrypoint digest was identical on all three Ubuntu hosts:
`83e5e56d300eeea4a56a336c13706ed7de7697f324e22278e9e3fbdf6be9f2f3`.
The existing Mac LaunchAgents reference only the unsuffixed service root and
configuration. No Dev/QAS service was observed or installed. These revision
and health results do not establish BUG-021–024 behavior or a V3 lane.

The `8443` listener is the only configured Runner HTTPS route observed on each
Ubuntu host; SSH listens on port 22. Ports `9443`, `8444`, `8445`, `2224`, and
`2225` were absent from the observed listener list and remain future routes.
The Linux and sandbox host-local UFW policies were inactive; their INPUT
policies defaulted to ACCEPT, with SSH allowed. The linux host also had an
existing TCP/9093 recovery rule. Factory UFW was absent; its INPUT chain ended
in REJECT and retained source-specific `8443` accepts for `81.95.203.65/32`
and `31.61.247.138/32`, plus port 22. These rules were only read. The Mac
outbound address observed through `api.ipify.org` was `81.95.203.65`; it is a
point-in-time observation, not an approved future source allow-list.

OCI instance metadata returned these private VNIC identities: linux host
`10.0.0.200` (`ocid1.vnic.oc1.eu-marseille-1.abwxeljrdvrbqjdmz5ooe5p5l2uncrvrvcnavvejeo5ehfin4qxzy3nu7yaa`),
sandbox host `10.0.0.14`
(`ocid1.vnic.oc1.eu-frankfurt-1.abtheljtwh24lvgbruxdvapazcuv75duo4m6jjiglk3x24e3gy3wkaaup2aq`),
and factory `10.0.0.30`
(`ocid1.vnic.oc1.uk-london-1.abwgiljtcvoxzyfmmhklrr24ur2inwhgxr64t3ilifnkoakwl7rh6owh6d7q`).
The metadata response did not supply subnet or NSG fields; that does not prove
those associations are absent.

## Access, capacity, storage, and fixture readiness

| Area | Observation | Readiness result / prepared correction |
| --- | --- | --- |
| Ubuntu SSH/root access | Explicit configured SSH identities reached all three `ubuntu` accounts. `sudo -n -l` showed `NOPASSWD: ALL`, and `sudo -n id -u` returned `0` on each host. | Access verified for the observed SSH/root route. Provider ownership/API access is separate. |
| Mac root/admin access | Mac account is UID 502 and not a member of `admin`; `sudo -n -l` returned `sudo: a password is required`. | Mac root execution is unavailable in this shell. A named, authorized Mac admin route/operator is required before privileged bootstrap A03; do not infer it from current LaunchAgent access. |
| OCI provider/network access | OCI CLI 3.94.2 is installed on the Mac, but `~/.oci/config` and `~/.oci` are absent, `OCI_CLI_AUTH`, `OCI_CLI_PROFILE`, and `OCI_CLI_CONFIG_FILE` are unset, and a read-only `oci iam region list` stopped at the missing-config prompt; it was aborted without creating a config or sending an API request. The CLI is absent on the Ubuntu hosts. | Actual provider security-list/NSG/NAT rule IDs, owners, and rollback evidence are unavailable. Supply an authorized read-only provider route or identify the authorized network operator before A02 can be materialized. |
| Mac capacity | 14 logical CPUs and 48 GiB RAM. APFS Data container disk3 capacity `494384795648` bytes, in-use `316459319296`, unallocated `177925476352` (36%). `tmutil listlocalsnapshots /` listed no local snapshots; Runner-managed snapshot accounting is unknown. | The §6 unallocated floor is `max(20 GiB, 15%)`, currently about `74157719347` bytes. The remaining margin before quotas/budgets is about `103767757005` bytes. Lane quotas, shared-cache budget/use, and distinct cache-backup volume identity/quota/use are not established, so the full equation cannot pass. Current Mac service state uses the shared Data volume. |
| `linux-host` capacity/enforcement | `/dev/sda1` ext4 is 48,274,628,608 bytes with 22,706,483,200 available (47%); 5% of inodes used. The host is hybrid: cgroup2 is mounted at `/sys/fs/cgroup/unified` with an empty `cgroup.controllers`; the default tree is tmpfs with separate cgroup-v1 controllers. `runnerd.service` is in `/system.slice/runnerd.service`; systemd reports CPUAccounting and IOAccounting disabled, and there is no readable service `io.max`. `go` is not installed for `ubuntu`. | The selected cgroup-v2 lane slice and per-device-I/O contract is unsupported by the observed current systemd hierarchy. This is a hard eligibility stop until an approved host-level correction and fresh proof; do not change the production boot/cgroup setup here. Normal Go-cache paths/budget are also unknown. |
| `sandbox-host` capacity/enforcement | `/dev/sda1` ext4 is 54,958,841,856 bytes with 1,751,961,600 available (3%); 12% of inodes used. `/sys/fs/cgroup` is cgroup2fs; `cpuset`, `cpu`, `io`, `memory`, `pids`, and other controllers are present, `io` is enabled through `system.slice` to `runnerd.service`, and its `io.max` file exists but is empty. `go` is not installed for `ubuntu`. | The plan requires at least 30% free bytes and inodes after the full projection; current free bytes are 3%, a hard stop. Do not delete or reclaim retained data. An approved capacity/storage correction and fresh L0 are required. The full envelope, project quotas, and normal Go-cache budget remain unmeasured. |
| `factory-sandbox` capacity/enforcement | `/dev/sda1` ext4 is 50,839,412,736 bytes with 46,624,546,816 available (91%); 3% of inodes used. `/sys/fs/cgroup` is cgroup2fs and advertises `io`, but `io` is not enabled for `system.slice` or `runnerd.service`; their enabled controllers are `cpu`, `memory`, and `pids`, and root confirmed the service `io.max` file is absent. `go` is not installed for `ubuntu`. | Capacity bytes/inodes clear the simple 30% observation, but cgroup I/O delegation/enforcement, the complete PRD/Dev/QAS/management/cache/backup envelope, and normal Go-cache budget need approved L0/L2 validation. No current lane I/O cap is claimed. |
| Native fixtures | No disposable storage or service fixture was provisioned. No fixture cleanup manifest or exact test-device quota was approved. | Prepare exact A01 fixture identity, quota, test namespace/loopback ports, root operator, and cleanup/rollback manifest from a fresh eligible inventory; provision only after A01 approval. |

The all-host lane-runtime capacity proof is therefore not green. The sandbox
free-byte gate fails, linux-host's active systemd hierarchy does not expose the
required cgroup-v2 controls, and factory-sandbox has not delegated `io` to its
Runner service. The Mac and other Linux policy equations also remain incomplete;
no substitute limits or reduced host cohort are proposed.

## Mailbox and producer readiness

The active Mac V2 configuration lists eight mailbox roots. Each observed root
was owned by UID 502, mode `0700`, and on device `16777229`. The table records
root descriptor IDs and child-directory entry counts only; counts do not
classify active work or ACK state. `inbox`, `outbox`, `events`, `acks`, and
`diagnostics` child descriptors were inspected without opening their contents.

| Configured inbox | Root path | Root dev:inode | Entry counts (`inbox/outbox/events/acks/diagnostics`) |
| --- | --- | --- | --- |
| `default` | `~/Library/Application Support/RemoteSessionRunner/mailbox` | `16777229:16789911` | `0/0/0/0/0` |
| `analytics` | `~/Library/Application Support/RemoteSessionRunner/mailboxes/analytics` | `16777229:16809306` | `0/0/0/0/0` |
| `slidestud-io` | `~/projects/slidestud.io/tmp/mailbox-` | `16777229:16721423` | `0/1284/1256/104/33` |
| `agents-work-dispatcher` | `~/projects/agents-work-dispatcher/tmp/mailbox-` | `16777229:16770872` | `5/21/21/23/0` |
| `agentic-software-factory-runner-direct` | `~/projects/agentic-software-factory/runner/mailbox-` | `16777229:23180710` | `0/12/11/0/1` |
| `agentic-factory-test-project-01-runner` | `~/projects/agentic-factory-test-project-01/runner/mailbox-` | `16777229:24339425` | `0/21/18/0/0` |
| `agentic-factory-test-project-02-runner` | `~/tmp/agentic-factory-test-project-02/runner/mailbox-` | `16777229:24339431` | `0/12/12/0/0` |
| `remote-session-runner-workspace` | `~/projects/remote-session-runner/runner/mailbox-` | `16777229:25287946` | `0/4/4/0/0` |

Observed extra top-level entries include `.DS_Store` under `slidestud-io`,
`.runner-mailbox-ingress.lock` under the three Agentic Factory roots, and a
`status` directory under `slidestud-io`. Their required type/descriptor/owner/
mode inventory remains for the full L0 record. The roots' configured aliases
identify integrations, not verified human owners or every process that can
publish. No producer owner, pause/fence capability, or maintenance window was
verified for the external roots. Tomasz remains the plan's default coordinator;
that default does not prove a repository owner's authority or a working pause
control. The first Dev/QAS rollout has no external producer; it uses its
internal acceptance inbox and preserves every existing PRD root.

BUG-024 ACK classification follows the owner's correction and the evidence in
[`BUG-024.md`](BUG-024.md#approved-live-execution-ledger) and
[`BUG-024/acceptance.json`](BUG-024/acceptance.json): all four fresh
verification IDs (`req-bug024-20261016-mac-01`, `req-bug024-20261016-linux-01`,
`req-bug024-20261016-sandbox-01`, `req-bug024-20261016-factory-01`) are
recorded `ack_consumed: true`. Retained response/event files are archival and
do not call for another ACK. Preserve the historical
`req-asf-ext-20261015-harness-01` response and deliberately held ACK; no
disposition or ACK action was taken. Directory-entry counts above are not used
to override those recorded ACK facts.

## Prepared approval packages and exact open inputs

The fixed approval checkpoints remain those in handbook §3; no broad
authorization or placeholder is treated as a signer mandate.

- **A01 storage/fixtures:** future packages must name each exact host/device/
  path, maximum quota/budget, retention, owner/mode, cleanup/rollback plan,
  current descriptor/capacity evidence, and distinct same-host cache-backup
  volume. Mac Dev/QAS service-root APFS volumes and Linux lane/cache/staging
  quota scopes are not created here. Present capacity data is insufficient to
  choose quotas safely; sandbox is already below the floor.
- **A02 networking:** preserve current PRD `:8443` and port-22 behavior. The
  fixed future maps are release transport `:9443`, Dev direct `:8444`, QAS
  direct `:8445`, Dev queued SSH `:2224`, and QAS queued SSH `:2225` on all
  three private binds/public endpoints in design §5. Existing host rules were
  read above, but cloud/provider rule IDs, authorized source policy, actual
  network owner, and rollback handles remain unavailable without provider
  access. No route change is authorized here.
- **A03 bootstrap cohort / root work:** record the fixed four-host sequence and
  failure-disable-all contract. Ubuntu root access is observed. Mac admin/root
  access is not available in this shell and must be established before its
  privileged bootstrap step. No key, CA, helper, listener, or service was
  installed.
- **A04–A07:** retain the per-host one-use bootstrap PKI, exact authority-body,
  finite renewal-grant, and normal PKI-rotation review schemas. All dependent
  public pins, digests, receipts, and mandates must be generated from approved
  preceding evidence at their declared checkpoints; none exists yet.
- **A08 PRD/producer maintenance:** list the eight configured roots above, then
  obtain actual owners, writer/direct-client inventory, tested pause/fence
  procedures, retained-work disposition, fresh inventory, and a scoped window
  before any affected baseline maintenance or L6 fencing. No pause or window is
  claimed.
- **A09/A10:** retain only for their named registry-loss or authority/anchor-loss
  incidents. No such incident is asserted or rehearsed against production.

The exact fixed namespace and approval package schemas are prepared; packages
requiring current provider inventory, capacity feasibility, or an authorized
Mac root operator cannot yet be made actionable. The next applicable correction
is to establish approved read-only OCI network access or name the authorized
provider operator, make an authorized Mac admin/root route available for A03,
and submit an A01 capacity/storage correction for sandbox and linux-host
enforcement. Then rerun this readiness unit and take a fresh complete L0; do not
infer that a historical measurement remains valid.

## Observation routes and commands

The SSH connections used the existing aliases and explicit identities:

| Canonical host | SSH alias | Identity file |
| --- | --- | --- |
| `linux-host` | `remote-session-runner` | `/Users/tomasz.walczuk/.ssh/remote-session-runner` |
| `sandbox-host` | `sandbox.env` | `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` |
| `factory-sandbox` | `factory-sandbox` | `/Users/tomasz.walczuk/.ssh/factory-sandbox` |

Each SSH invocation used the Mac SSH config and
`IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`, and
`UpdateHostKeys=no`. Read-only commands included `id`, `uname -srm`, Git
branch/status/HEAD/remote reads, `systemctl is-active`, `systemctl is-enabled`,
`systemctl show`, `ss -lnt`, `findmnt`, `df -B1 /`, `df -i /`, `go env`,
`sha256sum`, `sudo -n -l`, `sudo -n id -u`, UFW/nftables/iptables listing,
cgroup mount/controller/file reads, and selected OCI VNIC metadata fields.
The exact provider probe was `oci iam region list --query 'data[*].name'
--raw-output`; it stopped before an API request because the local config was
missing.

Mac reads included `git status`, `git rev-parse`, `launchctl print`, `id`,
`dseditgroup -o checkmember`, `sudo -n -l`, `sysctl`, `ps`, `diskutil apfs
list`, `tmutil listlocalsnapshots /`, `shasum -a 256`, installed binary
`--version`, and descriptor-only mailbox root enumeration. PRD readiness used
`curl` GET `/health/ready` with the existing configured CA/client certificate
and private-key paths and printed only HTTP status, remote IP, and TLS verify
result. The public egress observation was a GET to `https://api.ipify.org`.
No credential content or request/response payload was emitted.

## Gate verdict and sequence stop

`LANES-PREP-00` does not pass: necessary provider API/network ownership access
was not verified, Mac privileged access required by the declared bootstrap
operator role was not available, and the current cohort already exposes a
hard sandbox capacity failure plus an unproven linux-host cgroup-v2 gate.
Producer/pause ownership and actionable A01/A02 packages are also incomplete.
Per the plan's stop rule, the full `LANES-L0` unit and all later units are not
started. Preliminary observations that overlap L0 are preserved here only as
PREP discovery; they are not a complete or green L0 inventory. A fresh L0 is
required after PREP's access and capacity prerequisites are corrected.

No source tests, installed tests, defect probes, mailbox requests, ACKs, or
service changes were run. Documentation validation was
`git diff --cached --check` before source S and passed. The source and evidence
delivery checks for S are recorded below. This blocked outcome does not
authorize L0 or any later unit to start.

## Source S and evidence delivery

Source S is `44241e8199df7e3882b34194e10d23cbd9e1c468`
(`phase(LANES-PREP-00): record readiness discovery`). It contains only this
PREP record and the blocked/not-started `LANES-L0.md` record. This documentation
delivery is a Git mutation authorized separately from the read-only host
observations; it did not change an installed checkout's service or runtime.

| Check | Command or guard | Result |
| --- | --- | --- |
| Mac source commit | `git status --short --branch`; `git rev-parse HEAD`; `git show --stat --oneline HEAD` | `dev`, HEAD S; worktree clean; exactly the two unit-evidence files above |
| Primary Gitea | `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev`; `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote origin refs/heads/dev` | Push was issued; the primary ref check returned S. |
| GitHub mirror | `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev` | Returned exact S before Ubuntu pulls |
| `linux-host` | Verified clean `dev`, origin `git@github.com:gavrilo123456/remote-session-runner.git`, pre-pull HEAD `ad77682ae9ba780d5ac672a932780fdd7c440f81`, and mirror S; ran `git -c core.sshCommand="ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes" pull --ff-only origin dev` | Exit 0; post-pull HEAD S; clean `dev` |
| `sandbox-host` | Same branch, clean-worktree, origin, pre-pull HEAD and mirror-S guards; same explicit-identity `pull --ff-only origin dev` command | Exit 0; post-pull HEAD S; clean `dev` |
| `factory-sandbox` | Same branch, clean-worktree, origin, pre-pull HEAD and mirror-S guards; same explicit-identity `pull --ff-only origin dev` command | Exit 0; post-pull HEAD S; clean `dev` |

Mac-to-host SSH used the existing explicit host identities and verified host
keys: `129.151.232.40` (`linux-host`) with
`/Users/tomasz.walczuk/.ssh/remote-session-runner`, `132.226.205.205`
(`sandbox-host`) with `/Users/tomasz.walczuk/.ssh/dev.slidestud.io`, and
`145.241.255.66` (`factory-sandbox`) with
`/Users/tomasz.walczuk/.ssh/factory-sandbox`; each used
`IdentitiesOnly=yes`, `BatchMode=yes`, and `StrictHostKeyChecking=yes`. The
linux-host SSH invocation also emitted a non-fatal warning that the sandboxed
Mac shell could not update `~/.ssh/known_hosts`; the remote guard, pull and
post-pull checks all succeeded. Later host invocations set
`UpdateHostKeys=no`. Each Ubuntu Git
fetch/pull used `/home/ubuntu/.ssh/gavrilo123456-github` with
`IdentitiesOnly=yes`, `BatchMode=yes`, and `StrictHostKeyChecking=yes`. The
pre-pull guards and resulting exact HEAD/clean-worktree checks were executed
on each host in sequence. No source was copied with `scp` or `rsync`.
