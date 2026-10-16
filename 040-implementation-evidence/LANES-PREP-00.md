# LANES-PREP-00 — readiness packet discovery

**State:** BLOCKED. Read-only discovery completed to the extent permitted by
current access; required provider/operator access and material feasibility
gates remain unresolved. The owner separately authorized direct observations
for both PREP-00 and L0, but this failed PREP exit prevents the full L0 unit
from starting. The later incremental-readiness decision permits independent
source-only implementation/testing while PREP and L0 remain blocked.
PREP records measurements and proposals without waiting for future volumes,
generated UUIDs, or L2 pressure-test results; those are checked at the later
operations that need them.

**Observed:** initial route and runtime observations 2026-10-16 09:54–10:01 UTC;
read-only remediation follow-up 10:42–10:48 UTC. All observations are
point-in-time; rerun the applicable inventory immediately before any future
live gate.

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
- The later owner authorization sets Runner as the preferred execution route;
  when it cannot run a required implementation command, use normal SSH on the
  three Ubuntu hosts for builds, tests and setup. That authorization covers
  execution route, not approval to change production services, boot settings,
  storage, firewall rules or credentials. Keep the original PREP/L0 direct
  observation route read-only, with no Runner request/ACK or defect probe. The
  unsuffixed production Runner remains protected; exact A01–A10 approval
  checkpoints and maintenance procedures remain in force.

No Runner request or ACK was published, no defect probe was run, and no
installed configuration, service, storage, or firewall rule was changed in
this unit. A read-only mTLS `GET /health/ready` was issued to each existing PRD
remote endpoint. Directory listings below are metadata/counts only; no request,
ACK, response, event, or diagnostic payload was opened by this inventory.

## 2026-10-16 owner decision: incremental readiness and SSH fallback

The owner explicitly replaced the earlier requirement to resolve every
readiness prerequisite before implementation. PREP remains BLOCKED and L0
remains NOT STARTED until their actual prerequisites pass. Keep the prescribed
unit, test, acceptance and delivery order. Resolve access, storage and
infrastructure dependencies before the operation that needs them; an
unrelated failed gate does not block independent source-only implementation
and testing. Preserve every failed gate and do not claim an unobserved PASS.

Use Runner where it can execute a required command. When it cannot, the owner
authorized normal SSH commands on `linux-host`, `sandbox-host` and
`factory-sandbox` for implementation builds, tests and setup throughout the
rollout. Use the configured explicit identities and verified host keys, and
record the fallback reason, execution machine, source revision, exact command
and result in that unit's evidence. This execution-route authorization does
not approve production changes or waive any operational checkpoint. Preserve
the unsuffixed production Runner, normal host work, retained evidence and held
ACKs.

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

| Area | Fresh observation | Readiness result |
| --- | --- | --- |
| Ubuntu SSH/root route | The three configured SSH identities still reach the `ubuntu` accounts; `sudo -n id -u` returns `0` on each. | Host-local administrative execution is available on the observed route. It does not grant OCI API or network ownership access. |
| Ubuntu Go toolchain | `/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go` exists on all hosts and reports `go1.27.1 linux/amd64` on linux-host and factory-sandbox, `go1.27.1 linux/arm64` on sandbox-host. `go env` reports the normal `/home/ubuntu/.cache/go-build` and `/home/ubuntu/go/pkg/mod` paths. | The earlier “Go is not installed” inference from `go` not being on interactive PATH was wrong. The explicit Makefile toolchain is present; source gates must invoke the pinned path/Makefile. |
| Mac administrator route | Account UID is `502`, groups do not include `admin`; `sudo -n -l` reports `sudo: a password is required`. | This proves only that unattended elevation is unavailable and this account is not a local admin. An authorized interactive Mac administrator/operator can satisfy privileged operations. No such named operator/session is yet evidenced. |
| OCI provider access | OCI CLI 3.94.2 is installed on the Mac, but `~/.oci`/`~/.oci/config` and auth/profile/config environment variables are absent. The read-only region-list probe stopped at its missing-config prompt and was cancelled before writing a config or making an API request. OCI CLI is absent on Ubuntu. | There is no verified read-only OCI identity or named operator evidence. This does not prove provider resources are absent. VNIC OCIDs/IPs are known from instance metadata, but the VCN/subnets, NSG/security-list rules, route/gateway/NAT, attached volume IDs/performance, backup policy and provider rollback handles are not. |
| Mac APFS and snapshots | At follow-up, container `disk3` capacity is `494384795648` B; in use `316379525120` B; unallocated `178005270528` B. It is backed by internal physical `disk0s2`. Data volume `disk3s1` consumes `289262825472` B; no Data-volume snapshots and no Time Machine local snapshots were listed. The sealed system snapshot consumes about 12.6 GB and is already included in the container baseline. `diskutil list` found no separate physical backup disk; `disk4` is a 152 MB disk image. | The calculated floor and proposed full envelope are below. Future Runner snapshots have a 10 GiB budget; none can be measured yet because no lane volumes exist. A distinct same-host cache-backup device remains unidentified. |
| Linux filesystems, inodes and devices | Root is ext4 on `/dev/sda1` (`8:1`), on OCI `BlockVolume` disk `/dev/sda` (`8:0`) for all three hosts. Current `project` ext4 feature and project-quota mount option are absent on all roots. Root block-device I/O/VPU performance is not in host metadata. | Do not try to apply project quotas to the existing PRD root in this package. Use new, separately approved quota-capable volumes. OCI performance metadata is required to set numerical per-device I/O caps. |
| `linux-host` capacity and cgroups | Root C=`48274628608` B, U=`25560256512` B, available=`22697594880` B (47%); 288,498/5,999,616 inodes used (5%). Kernel `5.15.0-1081-oracle`, systemd `245.4`; v2 is only `/sys/fs/cgroup/unified` with empty controllers while v1 trees are active. `/proc/cmdline` has no cgroup mode argument; GRUB defaults are `quiet splash`. `docker.service`, `containerd.service`, `runnerd.service`, and `snapd.service` are active. `runnerd.service` has `Delegate=no`, CPUAccounting/IOAccounting off. | Hybrid cgroups are incompatible with the planned v2 lane controls. A conversion needs a tested compatibility package and separately approved production maintenance/reboot. This is not an `io.max` verdict about current PRD. |
| `sandbox-host` capacity and cgroups | Root C=`54958841856` B, U=`53209952256` B, available=`1732112384` B (3%); 799,699/6,872,192 inodes used (12%). Unified v2 root, `system.slice` and existing `runnerd.service` expose `cpuset cpu io memory pids`; systemd `249.11`. The PRD `runnerd.service` has `Delegate=no`, `IOAccounting=no`, and its `io.max` is empty. | The current root is below the 30% floor. The empty PRD service `io.max` is not itself a lane failure; Dev/QAS and management scopes need their own verified caps. Preserve the current PRD service. |
| `factory-sandbox` capacity and cgroups | Root C=`50839412736` B, U=`4198699008` B, available=`46623936512` B (92%); 170,484/6,146,560 inodes used (3%). Unified v2 root advertises `io`, but `system.slice` and `runnerd.service` expose only `cpu memory pids`; systemd `259.5`. Existing PRD unit has `Delegate=no`, `IOAccounting=no`. | This is a new-scope controller-propagation task, not an existing PRD lane failure. Enable/verify `io` only through the approved new management/Dev/QAS hierarchy; do not modify `runnerd.service` or claim a numeric cap before measuring volume performance. |
| Native fixtures | No disposable storage, fixture service or isolated test volume was provisioned. | A01 may approve separate test devices/quotas and loopback namespace; L2 proves the controls there. No fixture is a PREP prerequisite and none exists now. |

### Current disk/cache accounting and proposed Linux A01 envelope

Go cache sizes below are `du -sB1` observations; inode counts are `du -s
--inodes`. Other selected totals are bounded reads of known cache/log/Docker
directories, not a root-wide `du` decomposition. The earlier sandbox root-wide
walk exceeded the safe observation window and was stopped without mutation;
unclassified bytes remain in the measured root baseline. All retained work and
current caches stay in the accounting and are preserved.

Snapshot accounting: Mac reported no Data-volume snapshots and no Time Machine
local snapshots; the sealed system snapshot's approximately 12.6 GB is included
in the measured container baseline. The proposed 10 GiB aggregate Runner
snapshot ceiling is future budget, not existing consumption. The Linux roots
are ext4; host observations showed no local snapshot subvolume/snapshot
inventory. OCI-managed boot/block-volume backups and their retention/cost are
not exposed by the host metadata and remain unknown until the OCI operator
returns that inventory.

| Host | Current root C / U / free | Go build / modules / toolchain (B) | Go cache inodes build / modules | Other selected root use (B) |
| --- | --- | --- | --- | --- |
| `linux-host` | `48274628608 / 25560256512 / 22697594880` | `682254336 / 364761088 / 295497728` | `6552 / 7966` | journal `4303667200`; apt `161435648`; Docker `2692218880`; containerd `323584` |
| `sandbox-host` | `54958841856 / 53209952256 / 1732112384` | `639860736 / 364556288 / 290099200` | `6441 / 7863` | journal `839299072`; apt `155574272`; Docker `1448550400`; containerd `1241088` |
| `factory-sandbox` | `50839412736 / 4198699008 / 46623936512` | `326692864 / 364343296 / 295510016` | `4154 / 7863` | journal `97533952`; apt `110231552` |

The following is the concrete **engineering proposal**, not an A01 approval or
configured policy. Each Linux host receives a new 96 GiB lane/management
filesystem and a separate 20 GiB cache-backup block volume. Format the new
lane/management filesystem as ext4 with `project,quota`, initialize the
project quota type, mount with `prjquota`, and verify byte/inode hard limits
before any service uses it.
Existing roots lack that feature; do not reformat or retrofit the production
root. ext4 requires the `project` feature for `prjquota` ([ext4 manual](https://www.man7.org/linux/man-pages/man5/ext4.5.html)).

| Project | Canonical mount/bind path | Hard byte budget | Hard inode budget | Contents |
| --- | --- | ---: | ---: | --- |
| Dev (`project_id=1001`) | Volume child bound to `/home/ubuntu/.local/share/remote-session-runner-dev` | 16 GiB | 524,288 | Dev root, workspace, DB/WAL, inbox, outputs and logs; the 576 MiB log cap is included |
| QAS (`project_id=1002`) | Volume child bound to `/home/ubuntu/.local/share/remote-session-runner-qas` | 16 GiB | 524,288 | QAS root, workspace, DB/WAL, inbox, outputs and logs; the 576 MiB log cap is included |
| Management staging (`project_id=1003`) | `/var/lib/remote-session-runner/management` | 8 GiB | 131,072 | Builder, staging, release transport, anchor/helper and bounded management logs |
| Go build cache (`project_id=1004`) | Bind at existing `/home/ubuntu/.cache/go-build` | 6 GiB | 196,608 | Copy current cache with owner/mode verification; preserve the old directory under the mount for rollback |
| Go module cache (`project_id=1005`) | Bind at existing `/home/ubuntu/go/pkg/mod` | 2 GiB | 65,536 | Copy current cache with owner/mode verification; preserve the old directory under the mount for rollback |
| Release cache (`project_id=1006`) | `/var/lib/remote-session-runner-release-cache` | 8 GiB | 262,144 | Root-owned immutable digest-addressed cache; stop admission at cap, never evict accepted artifacts automatically |
| Cache backup (`project_id=1007`, separate device) | `/var/lib/remote-session-runner-release-cache-backup` | 8 GiB of at least 20 GiB | 262,144 | Latest and immediately previous verified release-cache backups; require at least 8 GiB measured free after filesystem metadata; on limit, refuse the new backup and request owner disposition, never auto-delete |

Mount the 96 GiB filesystem at `/var/lib/remote-session-runner-capacity` and
create `dev`, `qas`, `management`, `go-build`, `go-modules`, and `release-cache`
project directories beneath it. Bind those directories to the exact canonical
service, management, Go-cache and release-cache paths in the table. The
separate 20 GiB-or-larger backup device mounts at
`/var/lib/remote-session-runner-release-cache-backup`. The proposed
`/etc/projid` names are `rsr-dev:1001`, `rsr-qas:1002`, `rsr-management:1003`,
`rsr-go-build:1004`, `rsr-go-modules:1005`, `rsr-release-cache:1006`, and
`rsr-cache-backup:1007`; each
`/etc/projects` entry maps that ID to its project directory. Mark each root
directory with its project ID and project-inherit flag, then set the hard byte
and inode limits from the table. The persistent mount entry uses the generated
UUID and `ext4,prjquota,nofail`; bind entries expose only the approved project
directories. New Dev/QAS and management units must have `RequiresMountsFor=` on
their exact roots/cache paths so a missing optional mount leaves the lane
stopped while the host and unsuffixed PRD continue booting. The actual IDs and
quota-device performance must be matched to
OCI metadata before these templates become host instructions.

For each new Linux data volume, the guarded operation package is: (1) match the
attached unused block device to its OCI volume OCID/serial; verify with
`lsblk`, `blkid` and `findmnt` that it is the expected new empty device and not
the root or any mounted filesystem; (2) create ext4 with the `project` feature
and a volume label, then verify `tune2fs -l` reports `project` and `quota`. Set
`LANE_DEVICE` only after verifying the unused device against the OCI volume
OCID. The guarded command shape for the new 96 GiB filesystem is:

```sh
: "${LANE_DEVICE:?set only to the OCI-verified new 96 GiB lane volume}"
test -b "$LANE_DEVICE"
sudo wipefs -n "$LANE_DEVICE"
```

Only after the read-only output confirms there is no signature and the OCI
operator independently confirms the exact device OCID, the approved create
command is:

```sh
sudo mkfs.ext4 -O project,quota -E quotatype=prjquota -i 16384 \
  -L RSR-LANES "$LANE_DEVICE"
```

For the distinct backup device, set `BACKUP_DEVICE` only after matching its
OCI OCID/serial, confirm it is a new empty device, and use this guarded sequence:

```sh
: "${BACKUP_DEVICE:?set only to the OCI-verified new backup volume of at least 20 GiB}"
test -b "$BACKUP_DEVICE"
sudo wipefs -n "$BACKUP_DEVICE"
```

After the empty-device output and OCID are independently verified under A01,
format it with the same ext4 project-quota features:

```sh
sudo mkfs.ext4 -O project,quota -E quotatype=prjquota -i 16384 \
  -L RSR-CACHE-BACKUP "$BACKUP_DEVICE"
```

Never set either device variable to `/dev/sda`, `/dev/sda1`, a mounted
filesystem, or an unknown/nonempty volume. This creates internal project-quota
metadata; confirm both ext4 features before mounting with `prjquota`. (3) create the
project-directory tree, copy the current Go caches with metadata preserved,
and compare file counts, byte totals and read integrity; (4) configure the
UUID `prjquota` mount and bind mounts, project ID inheritance, and hard byte/
inode limits; (5) run `findmnt`, `df -B1`, `df -i`, project-quota reports,
`lsattr`, and a bounded disposable quota-refusal check; (6) verify PRD
`:8443` health and that the unsuffixed `runnerd.service` is untouched. The
backup device follows the same empty-device guard and separate filesystem
setup, with an 8 GiB hard project budget and at least 8 GiB measured free after
filesystem metadata. Use a 20 GiB-or-larger device to leave room for metadata
and reserve; verify `df -B1` after formatting and reject any size that cannot
hold the 8 GiB hard budget and 8 GiB low-water simultaneously.
Formatting and mounts run only after A01 approval; no current root, PRD data,
or retained work is reformatted, moved out of scope or deleted.

The proposed persistent mount lines (with only the generated UUIDs substituted
after the approved attach) are:

```fstab
UUID=<lane-volume-uuid> /var/lib/remote-session-runner-capacity ext4 defaults,prjquota,nofail,x-systemd.device-timeout=30s 0 2
UUID=<backup-volume-uuid> /var/lib/remote-session-runner-release-cache-backup ext4 defaults,prjquota,nofail,x-systemd.device-timeout=30s 0 2
/var/lib/remote-session-runner-capacity/dev /home/ubuntu/.local/share/remote-session-runner-dev none bind,nofail 0 0
/var/lib/remote-session-runner-capacity/qas /home/ubuntu/.local/share/remote-session-runner-qas none bind,nofail 0 0
/var/lib/remote-session-runner-capacity/management /var/lib/remote-session-runner/management none bind,nofail 0 0
/var/lib/remote-session-runner-capacity/go-build /home/ubuntu/.cache/go-build none bind,nofail 0 0
/var/lib/remote-session-runner-capacity/go-modules /home/ubuntu/go/pkg/mod none bind,nofail 0 0
/var/lib/remote-session-runner-capacity/release-cache /var/lib/remote-session-runner-release-cache none bind,nofail 0 0
```

The project-map content is:

```text
# /etc/projid
rsr-dev:1001
rsr-qas:1002
rsr-management:1003
rsr-go-build:1004
rsr-go-modules:1005
rsr-release-cache:1006
rsr-cache-backup:1007
```

The `/etc/projects` content is:

```text
1001:/var/lib/remote-session-runner-capacity/dev
1002:/var/lib/remote-session-runner-capacity/qas
1003:/var/lib/remote-session-runner-capacity/management
1004:/var/lib/remote-session-runner-capacity/go-build
1005:/var/lib/remote-session-runner-capacity/go-modules
1006:/var/lib/remote-session-runner-capacity/release-cache
1007:/var/lib/remote-session-runner-release-cache-backup
```

After the UUID mounts are verified, set project ID/inheritance and hard
byte/inode caps using these proposed commands:

```sh
sudo chattr -p 1001 +P /var/lib/remote-session-runner-capacity/dev
sudo setquota -P rsr-dev 0 16G 0 524288 /var/lib/remote-session-runner-capacity
sudo chattr -p 1002 +P /var/lib/remote-session-runner-capacity/qas
sudo setquota -P rsr-qas 0 16G 0 524288 /var/lib/remote-session-runner-capacity
sudo chattr -p 1003 +P /var/lib/remote-session-runner-capacity/management
sudo setquota -P rsr-management 0 8G 0 131072 /var/lib/remote-session-runner-capacity
sudo chattr -p 1004 +P /var/lib/remote-session-runner-capacity/go-build
sudo setquota -P rsr-go-build 0 6G 0 196608 /var/lib/remote-session-runner-capacity
sudo chattr -p 1005 +P /var/lib/remote-session-runner-capacity/go-modules
sudo setquota -P rsr-go-modules 0 2G 0 65536 /var/lib/remote-session-runner-capacity
sudo chattr -p 1006 +P /var/lib/remote-session-runner-capacity/release-cache
sudo setquota -P rsr-release-cache 0 8G 0 262144 /var/lib/remote-session-runner-capacity
sudo chattr -p 1007 +P /var/lib/remote-session-runner-release-cache-backup
sudo setquota -P rsr-cache-backup 0 8G 0 262144 /var/lib/remote-session-runner-release-cache-backup
```

These are proposed host operations, not commands executed in PREP. Verify
project IDs and inheritance on each root and new child, plus a disposable L2
hard-quota refusal. Do not test quota refusal against PRD or production lane
data.

The six main projects total 56 GiB on a 96 GiB volume, leaving 41.67% of its
bytes unallocated by project limits before filesystem metadata. At full
project budgets, `df -B1` must still show at least 30% available on that
filesystem; enlarge it before use if measured metadata/reserve leaves less.
Set inode density to at least one inode per 16
KiB (about 6.29 million on 96 GiB); the proposed 1,703,936 project inode caps
leave about 72.9% unassigned. The backup filesystem is a second block
device, not a second partition on the same device. Its 8 GiB quota uses at most
40% of a 20 GiB device; require at least 8 GiB measured free after metadata at
its hard cap. The stable Go cache paths remain the ones returned
by the explicit Go binary. Bind mounting is a proposed authorized operation;
it is not installed.

For sandbox-host, expand its backing boot BlockVolume so `/dev/sda1` grows to
at least 80 GiB. Current use is 49.55 GiB, so that root target leaves 38.06%
free; the minimum root size from the present use alone is 70.79 GiB. Then the
96 GiB lane/management volume and distinct 20 GiB backup device give this
full-envelope calculation (current root use already includes PRD, Docker,
journal, toolchain and old caches; the new 56 GiB project budget includes the
8 GiB release cache, and the separate backup budget adds 8 GiB):

```text
sandbox capacity = 80 + 96 + 20 = 196 GiB
sandbox peak use = 49.55 + 56 + 8 = 113.55 GiB
30% free-space floor = 58.80 GiB
projected free = 82.45 GiB = 42.07% (23.65 GiB above floor)
```

The other proposed aggregate envelopes also clear 30%: linux-host has about
160.96 GiB capacity / 87.80 GiB peak use / 73.16 GiB free / 45.45% free, and
factory-sandbox about 163.35 GiB / 67.91 GiB / 95.44 GiB free / 58.42% free.
The proposed 80 GiB sandbox root itself leaves 30.45 GiB (38.06%) against its
24 GiB 30% floor. Each existing root must also remain above 30%; each new
filesystem must pass its own measured floor. These calculations are
capacity proposals based on the observed current roots, not fresh L0 proof.
They must be recomputed from exact OCI-attached sizes, inode capacity and use,
PRD growth/retention and the approved volume performance before A01 approval.

**Linux production impact and rollback.** The new volumes isolate lane and
management writes from the unsuffixed PRD service. The sandbox boot-volume
expansion is additive and preserves its existing ext4, retained data, caches,
Docker, journal and PRD state; it has provider cost and possible device-rescan
or filesystem-grow interruption, which the OCI operator must establish. New
mounts can make a lane fail closed if missing; no current PRD unit or root mount
is to be changed. To roll back before a lane is accepted, disable only the new
lane/management units, unmount the new devices after confirming no open handles,
and expose the preserved old cache directories again. Keep all volumes and data
for review; do not shrink the enlarged root, purge caches or delete retained
work. Restore only newly added mount entries after proving the unsuffixed PRD
health route remains available. Exact device IDs, VPU/performance settings and
provider rollback handles require the bundled OCI read-only/operator input.

### Cgroup correction package

The fixed new Linux unit names are `remote-session-runner-dev.slice`,
`remote-session-runner-qas.slice`, `runnerd-dev.service`, `runnerd-qas.service`,
and the corresponding queued-SSH `runner-sshd-dev.service` /
`runner-sshd-qas.service`. The proposal is to place the new units in their
matching lane slices, configure `CPUQuota`, `MemoryMax`, `TasksMax`,
`IOAccounting=yes`, and per-device `IOReadBandwidthMax` /
`IOWriteBandwidthMax` / IOPS limits, and request the runtime controllers on the
new delegated services (`Delegate=cpu cpuset io memory pids`). Set exact limits
only after OCI volume performance and L2 workload evidence are available.
systemd delegation applies to services/scopes, not slices; `Delegate=` exposes
the service's child tree and does not itself enable controllers
([systemd delegation guide](https://systemd.io/CGROUP_DELEGATION/)).

| Host | Exact correction proposal | Production impact, verification and rollback |
| --- | --- | --- |
| `linux-host` | Current v2-only mount has no controllers; v1 controllers are mounted separately. Kernel 5.15 supports `cgroup_no_v1=all`; installed systemd's own manual confirms `systemd.unified_cgroup_hierarchy=1` selects unified v2. After containerd/Docker compatibility review and an approved window, append `systemd.unified_cgroup_hierarchy=1 cgroup_no_v1=all` to the existing `GRUB_CMDLINE_LINUX_DEFAULT="quiet splash"`, regenerate GRUB, preserve the old boot entry, then reboot through console/out-of-band access. Do not alter `runnerd.service`. | This affects every v1 consumer; Docker/containerd are active, so their versions, cgroup driver and workloads must pass a clone/staging test first. A reboot and service interruption are required. Verify a single `/sys/fs/cgroup` hierarchy, required controllers, Docker/containerd, SSH, existing PRD `:8443` mTLS readiness, and a new isolated slice's `io.max`/descendant membership. Roll back by booting the preserved prior entry, restoring the exact original GRUB default, regenerating the menu and rechecking PRD health. No boot change or reboot is authorized yet. |
| `sandbox-host` | Keep unified-v2. Apply accounting and hard resource/I/O controls only to new management and lane scopes. Do not interpret the existing empty PRD `runnerd.service/io.max` as a failure and do not change that unit. | Staged L2 fixture must prove controller propagation, per-device caps, hard byte/inode quotas and PRD `:8443` health under saturation. Roll back only new test units and mounts under their A01 fixture manifest; existing service stays untouched. |
| `factory-sandbox` | Keep unified-v2. Root advertises `io`, but `system.slice` does not currently expose it. First configure I/O accounting on only the new management/Dev/QAS slices and services. If `system.slice` still does not propagate `io`, the exact conditional proposal is `/etc/systemd/system/system.slice.d/20-runner-lanes-io.conf` containing `[Slice]` then `IOAccounting=yes`; reload systemd only in the separately approved change window. Do not change PRD `runnerd.service`. | Enabling `system.slice` I/O accounting makes the controller available across that parent and may add accounting overhead for unrelated services; inspect their unit workload and current limits first. Confirm new services expose `io`, actual per-device `io.max` appears and is enforced in L2, descendants stay within their lane, unrelated service limits remain unchanged, and PRD health stays green. Roll back only after new scopes stop: remove the exact drop-in, reload systemd, verify the prior controller view and PRD service remain active. |

Kernel v2 controllers cannot safely be moved between hierarchies after boot;
the kernel documentation recommends selecting hierarchy/controller placement
before service startup and documents `cgroup_no_v1=` as the boot control
([kernel cgroup-v2 guide](https://kernel.org/doc/html/v5.19/admin-guide/cgroup-v2.html),
[kernel parameters](https://docs.kernel.org/6.4/admin-guide/kernel-parameters.html)).
For all hosts, `io.max` limits are keyed to the volume's actual major/minor
device and require an observed performance baseline; current OCI `BlockVolume`
labels and host-visible devices do not provide VPU throughput or IOPS. No
numeric I/O cap is guessed in this package.

## Proposed Mac A01 capacity and privileged-operation package

The Mac's measured `GOCACHE` and `GOMODCACHE` are respectively `1725832` and
`354328` KiB (combined `2130083840` B, 1.984 GiB). Proposed APFS Data-container
volumes, all on `disk3`, are:

| Purpose | Mountpoint | APFS quota/budget |
| --- | --- | ---: |
| Dev service root | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev` | 16 GiB |
| QAS service root | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas` | 16 GiB |
| Go build cache | Existing `/private/tmp/remote-session-runner-gocache` path | 6 GiB |
| Go module cache | Existing `/private/tmp/remote-session-runner-gomodcache` path | 2 GiB |
| Host-management staging | `/Library/Application Support/RemoteSessionRunner-host-management` (root:wheel, `0700`) | 8 GiB |
| Immutable release cache | `/Library/Application Support/RemoteSessionRunner-release-cache` (root:wheel, `0700`) | 8 GiB |
| Runner-managed snapshots | Aggregate across approved lane volumes | 10 GiB total |
| Release-cache backup | `/Library/Application Support/RemoteSessionRunner-release-cache-backup/` on a separate physical device, not in `disk3` | At least 20 GiB device; 8 GiB budget and at least 8 GiB measured free after APFS metadata; root:wheel `0700` |

The six Data-container quota groups plus the future snapshot ceiling consume
66 GiB at maximum. Using full maxima in addition to current use conservatively
preserves both the existing cache trees under the new mountpoints and every
other current Data/System/Preboot/Recovery/VM byte:

```text
Data container C = 494384795648 B; current unallocated F = 178005270528 B
floor = max(20 GiB, ceil(0.15 * C)) = 74157719348 B
future maximum = (16 + 16 + 6 + 2 + 8 + 8 + 10) GiB = 70,866,960,384 B
projected unallocated = F - future maximum = 107,138,310,144 B
margin over floor = 32,980,590,796 B
```

The APFS volumes should be mounted at the same cache paths the Makefile exports.
Initially mount each cache volume at a separate empty staging directory; copy
and verify its contents there. At the approved switchover, preserve the old
cache directory under a dated `.pre-volume` name, then mount the new volume at
the canonical path. Rollback unmounts the new volume and restores the preserved
directory. Do not run `go clean` or delete shared cache contents. `diskutil
list` showed only the internal physical store for `disk3`;
the mounted MyTE disk image is not a distinct backup device. The separate 16
GiB backup device is therefore still an input, and the Mac equation does not
count it against Data-container capacity.

After the exact A01 storage package and interactive administrator route are
approved, use the current verified APFS container reference (observed as
`disk3` in this PREP snapshot) and create six Data-container mountpoints. Dev,
QAS and both Go-cache paths are UID:GID `502:20`, mode `0700`; host-management
and the immutable release cache are root:wheel `0:0`, mode `0700`. The host
admin can use:

```sh
sudo install -d -o 502 -g 20 -m 0700 -- \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev"
sudo install -d -o 502 -g 20 -m 0700 -- \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas"
sudo install -d -o 502 -g 20 -m 0700 -- \
  "/private/tmp/remote-session-runner-gocache.apfs-staging"
sudo install -d -o 502 -g 20 -m 0700 -- \
  "/private/tmp/remote-session-runner-gomodcache.apfs-staging"
sudo install -d -o 0 -g 0 -m 0700 -- \
  "/Library/Application Support/RemoteSessionRunner-host-management"
sudo install -d -o 0 -g 0 -m 0700 -- \
  "/Library/Application Support/RemoteSessionRunner-release-cache"
sudo diskutil apfs addVolume disk3 APFS RSR-Dev \
  -quota 16g -mountpoint "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev"
sudo diskutil apfs addVolume disk3 APFS RSR-QAS \
  -quota 16g -mountpoint "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas"
sudo diskutil apfs addVolume disk3 APFS RSR-HostManagement \
  -quota 8g -mountpoint "/Library/Application Support/RemoteSessionRunner-host-management"
sudo diskutil apfs addVolume disk3 APFS RSR-ReleaseCache \
  -quota 8g -mountpoint "/Library/Application Support/RemoteSessionRunner-release-cache"
sudo diskutil apfs addVolume disk3 APFS RSR-GoBuildCache \
  -quota 6g -mountpoint "/private/tmp/remote-session-runner-gocache.apfs-staging"
sudo diskutil apfs addVolume disk3 APFS RSR-GoModuleCache \
  -quota 2g -mountpoint "/private/tmp/remote-session-runner-gomodcache.apfs-staging"
```

Before running those commands, the authorized admin must recheck the container
reference, capacity equation, paths and approval; the observed `disk3` name is
not a durable identity. Record each generated volume UUID and device. Enable
ownership on each new volume by setting `APFS_VOLUME_DEVICE` to one recorded
device at a time, verifying it against the generated APFS volume UUID, then
running `sudo diskutil enableOwnership "$APFS_VOLUME_DEVICE"`;
and apply the table's required owner and `0700` mode at the mounted root: UID:GID
`502:20` for Dev, QAS and Go caches; root:wheel `0:0` for host-management and
release cache. The persistent `/etc/fstab` entries use the generated UUIDs (not
mutable APFS device names), type `apfs`, and the exact approved mountpoints;
create one entry for each of the six volumes. For example:

```fstab
UUID=<dev-volume-uuid> /Users/tomasz.walczuk/Library\040Application\040Support/RemoteSessionRunner-dev apfs rw,nobrowse 0 0
UUID=<qas-volume-uuid> /Users/tomasz.walczuk/Library\040Application\040Support/RemoteSessionRunner-qas apfs rw,nobrowse 0 0
UUID=<host-mgmt-volume-uuid> /Library/Application\040Support/RemoteSessionRunner-host-management apfs rw,nobrowse 0 0
UUID=<release-cache-volume-uuid> /Library/Application\040Support/RemoteSessionRunner-release-cache apfs rw,nobrowse 0 0
UUID=<go-build-volume-uuid> /private/tmp/remote-session-runner-gocache apfs rw,nobrowse 0 0
UUID=<go-module-volume-uuid> /private/tmp/remote-session-runner-gomodcache apfs rw,nobrowse 0 0
```

After the volumes are mounted, set their roots explicitly:

```sh
sudo chown 502:20 "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev"
sudo chmod 0700 "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev"
sudo chown 502:20 "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas"
sudo chmod 0700 "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas"
sudo chown 502:20 /private/tmp/remote-session-runner-gocache
sudo chmod 0700 /private/tmp/remote-session-runner-gocache
sudo chown 502:20 /private/tmp/remote-session-runner-gomodcache
sudo chmod 0700 /private/tmp/remote-session-runner-gomodcache
sudo chown 0:0 "/Library/Application Support/RemoteSessionRunner-host-management"
sudo chmod 0700 "/Library/Application Support/RemoteSessionRunner-host-management"
sudo chown 0:0 "/Library/Application Support/RemoteSessionRunner-release-cache"
sudo chmod 0700 "/Library/Application Support/RemoteSessionRunner-release-cache"
```

The authorized admin seeds the Go volumes at their staging mountpoints and
verifies them before cutover:

```sh
sudo ditto --rsrc --extattr --acl \
  /private/tmp/remote-session-runner-gocache/ \
  /private/tmp/remote-session-runner-gocache.apfs-staging/
sudo ditto --rsrc --extattr --acl \
  /private/tmp/remote-session-runner-gomodcache/ \
  /private/tmp/remote-session-runner-gomodcache.apfs-staging/
```

Then pause only lane build activity for the approved cutover. Rename each
original cache directory to a dated `.pre-volume` path without deleting it;
create an empty canonical mountpoint and mount the new volume there using its
recorded device, for example
`sudo diskutil mount -mountPoint /private/tmp/remote-session-runner-gocache
"$GOCACHE_VOLUME_DEVICE"` (with the analogous module-cache command). Verify
`go env GOCACHE GOMODCACHE` and a bounded read/write check before resuming
builds. Keep prior cache trees and new volumes intact. The backup volume is a
separate operation after its physical device is supplied and approved. Require
a device of at least 20 GiB and verify the resulting APFS container has at
least 8 GiB free after its 8 GiB backup-volume quota is fully allocated. If it
is blank, the authorized admin may use this sequence only after confirming the
exact empty physical device and A01 approval:

```sh
: "${BACKUP_DEVICE:?set only to the approved empty physical backup device}"
sudo diskutil apfs createContainer "$BACKUP_DEVICE"
# Record the returned container identifier before continuing.
: "${BACKUP_CONTAINER:?set to the recorded new APFS container identifier}"
sudo install -d -o 0 -g 0 -m 0700 -- \
  "/Library/Application Support/RemoteSessionRunner-release-cache-backup"
sudo diskutil apfs addVolume "$BACKUP_CONTAINER" APFS RSR-CacheBackup \
  -quota 8g \
  -mountpoint "/Library/Application Support/RemoteSessionRunner-release-cache-backup"
```

Then enable ownership on its recorded APFS volume device, verify/set its root
to `0:0`, `0700`, and confirm the independent 8 GiB free-space low-water. Never
guess its device/container ID or erase a disk with unknown contents. Verify
`diskutil apfs list`, every UUID, mount/quota, `df`, owner/mode, cache
integrity, Data unallocated floor, snapshot total, and separate-device
low-water. Stop on any identity or headroom
mismatch. Actual UUIDs and backup-device IDs are generated/provided only at
these approved steps; no such operation ran in PREP.

**Mac production impact and rollback.** New quota-limited service and cache
volumes consume shared APFS container headroom; full-cache or staging volumes
must refuse further build/staging writes rather than spill into PRD. This does
not provide CPU/RAM/PID isolation. The copy-and-mount cache approach leaves
existing contents underneath the mount. Before service use, rollback is to
unmount only the new volume and restore the old mount view; leave the new volume
and its contents intact for review. Do not delete or shrink a used volume.
Snapshot cleanup is not an automatic rollback: preserve snapshots and require
the exact A01 retention/disposition before any removal. A separate device is
required for the backup floor.

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
publish. The following producer/control discovery is from repository guidance
and source-level inspection; none of the controls was exercised or used to
pause a producer.

| Root | Producer/control evidence found | Remaining owner/action before affected maintenance |
| --- | --- | --- |
| `default`, `analytics` | Active Mac V2 config roots; all five child counts are zero at observation time. No producer was established by the root listing. | Identify any scheduled job, UI or direct client that can publish; identify its owner and tested pause/zero-work procedure. |
| `slidestud-io` | `AGENTS.md` documents a direct workspace publisher that writes request JSON then `.ready` last; it prohibits shell redirection, temporary publisher scripts, SSH and direct Runner API for publication. | The human/automation writer and its stop control are not identified. Get the repository/operator owner, exact writer schedule/process, pause/fence action and paired root snapshots. |
| `agents-work-dispatcher` | File-only mailbox policy. Its Kafka reinstallation plan documents pausing task producers, dispatcher services, and automatic cleanup/retry through an approved mailbox operation, then keeping affected components paused through the approved recovery workflow. | This is a documented candidate procedure, not a tested pause receipt for this Runner root. Identify operator/service labels, exact mailbox request and retained-work zero-state checks; obtain approval before use. |
| `agentic-software-factory-runner-direct` | Runner guide identifies the exact root, defaults to `ubuntu-factory-sandbox`, and records durable orphan cleanup disabled. For an unhealthy queue/bridge it says stop creating work and do not restart services or alter Runner state without authorization. | No dedicated tested pause/fence interface was found. Identify all writers and owner, then define an approved stop-new-work and quiescence procedure without changing the mailbox or service state. |
| `agentic-factory-test-project-01-runner`, `agentic-factory-test-project-02-runner` | Configured mailbox roots and retained entry counts are visible. A targeted search found no repo-specific `AGENTS.md` or source-defined pause API in the searched project files. | This does not prove no control exists. Ask each project/automation owner to identify the writer, actual owner and exact pause/fence plus retained-work verification. |
| `remote-session-runner-workspace` | Workspace mailbox root is configured with retained entries; listing alone does not identify a human or automatic publisher. | Identify any local automation/direct writer and the supported pause/fence and zero-work procedure. |

### A08 producer and control revalidation — 2026-10-16, 16:02 CEST

The owner assigned Tomasz Walczuk as accountable coordinator. The following
read-only pass maps the currently configured Mac roots, documented direct
writers and available service controls. It did not publish requests or ACKs,
pause a producer, alter a root, stop/restart a service, or inspect request
bodies. Counts are direct child entries, not counts of active work.

| Root | Current root path | Inbox/outbox/events/acks/diagnostics | Direct-writer evidence and pause control |
| --- | --- | --- | --- |
| `default` | `~/Library/Application Support/RemoteSessionRunner/mailbox` | `0/0/0/0/0` | Runner's owner-local route; no external direct publisher identified. Owner confirmation is needed for any local automation. |
| `analytics` | `~/Library/Application Support/RemoteSessionRunner/mailboxes/analytics` | `0/0/0/0/0` | Configured route; no direct publisher identified. Owner confirmation is needed for any analytics integration. |
| `slidestud-io` | `~/projects/slidestud.io/tmp/mailbox-` | `0/1317/1288/103/34` | `slidestud.io/AGENTS.md` specifies direct native workspace publication of JSON plus a marker-last `.ready`. Outbox files show `req-codex-logger-gate-watch-20261016-b134` through `b142` at one-minute intervals through 16:02 CEST, plus producer-spool review/validation requests. This is active production use. The AGENTS workflow has no mailbox-client lock or service pause control; pause means the owner suspends the publishing agent/workflow. |
| `agents-work-dispatcher` | `~/projects/agents-work-dispatcher/tmp/mailbox-` | `5/21/21/23/0` | `agents-work-dispatcher/AGENTS.md` specifies manual native file publication and no direct API/SSH publisher. Four stale marker/request identities remain in `inbox/`: three marker-only `.ready` files and `req-awd-kafka-pki-prep-20261005-03.json` (12,528 bytes) with a one-byte `.ready`. Preserve every file; do not consume, repair or remove them during maintenance. The Kafka plan's task-producer/dispatcher/cleanup pause is a candidate procedure for that separate service workflow, not a tested fence of this Mac mailbox. |
| `agentic-software-factory-runner-direct` | `~/projects/agentic-software-factory/runner/mailbox-` | `0/12/11/0/1` | `runner/how-to-use-runner.md` requires native workspace file publication, defaults to `ubuntu-factory-sandbox`, and says stop creating new work on an unhealthy route. The manual publishing workflow has no tested pause receipt; the owner suspends any active publisher. Durable orphan cleanup is disabled. |
| `agentic-factory-test-project-01-runner` | `~/projects/agentic-factory-test-project-01/runner/mailbox-` | `0/21/18/0/0` | Prior feasibility-run artifacts identify this as a manual workspace mailbox. No root-level AGENTS/README publisher procedure, scheduled producer, or service pause API was found in the checkout. Owner must confirm no agent is publishing during the window. |
| `agentic-factory-test-project-02-runner` | `~/tmp/agentic-factory-test-project-02/runner/mailbox-` | `0/12/12/0/0` | Prior feasibility-run artifacts identify this as a manual workspace mailbox. No root-level AGENTS/README publisher procedure, scheduled producer, or service pause API was found in the checkout. Owner must confirm no agent is publishing during the window. |
| `remote-session-runner-workspace` | `~/projects/remote-session-runner/runner/mailbox-` | `0/5/5/0/0` | This owner-facing workspace root has no inbox work. It retains the four BUG-024 route responses/events and the new B021-01 response/events. The failed B021-01 response remains unACKed and must not be altered. |

All eight roots are on device `16777229`, UID:GID `502:20`, mode `0700`; no
`.runner-mailbox-retired` sentinel was present. The cooperative ingress lock file
exists on the agents-work-dispatcher and two Agentic Factory project roots, but
its presence does not establish a held lock or fence. Source tests prove that
`mailboxclient` respects `AcquireShared` and a retirement sentinel. That
retirement sentinel is persistent and the lock is cooperative; it does not
fence the raw workspace writers above. No safe, non-destructive global pause
for direct file publishers is implemented.

The Mac installer's existing service control is concrete: it boots out
`com.remote-session-runner.local` first and
`com.remote-session-runner.locald` second, waits for the captured old processes
to be inert and both private sockets to disappear, then validates retained
mailbox state before candidate activation. The previous BUG-024 installed
refresh exercised that service boundary. Stopping these services prevents new
Runner intake but does not prevent a same-user workspace writer from creating
new inbox markers while they are stopped. The factory bridge refresh has an
installer lock, staged binary/wrapper/manifest replacements and rollback on a
failed refresh; it does not restart `runnerd`.

**A08 preparation is complete; the operational pause gate remains blocked.**
The only live producer activity observed was SlideStud's one-minute logger-gate
stream, so its owner must suspend that producing workflow and any other
SlideStud mailbox publishers for the maintenance interval. The accountable
owner must also confirm no workspace agent, integration or scheduled producer
is active for the remaining seven roots. These are real stop controls for the
documented manual writers, but they are not yet paused or live-tested. The
agents-work-dispatcher residue and all retained output/ACK state are preserved.
The A08 package proposes one 30-minute owner-selected maintenance window; the
exact start time and approval must be supplied by the owner after the final
candidate revision and current snapshots are frozen.

The first Dev/QAS rollout has no external producer; it uses its internal
acceptance inbox and preserves every existing PRD root.

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

- **A01 storage/fixtures:** concrete numerical proposals are recorded above.
  On each Ubuntu host, provision one new 96 GiB ext4 `project`/`prjquota`
  lane-management volume and one independent 20 GiB backup device; on
  `sandbox-host`, also expand its boot BlockVolume so root is at least 80 GiB.
  Apply the listed project byte/inode budgets and preserve existing cache trees
  during verified copy/bind. On Mac, create six quota-bound APFS volumes on
  the verified Data container (Dev 16, QAS 16, build cache 6, module cache 2, root-owned
  management staging 8, root-owned release cache 8 GiB),
  reserve the aggregate 10 GiB snapshot budget, and provide a separate physical
  backup device of at least 20 GiB with an 8 GiB cap and at least 8 GiB measured
  free after APFS metadata. Keep retained work
  and shared caches. Production effect: storage/cost increase only; sandbox
  root expansion may require device rescan/filesystem growth; new mounts fail
  closed for new lanes. Rollback disables only new scopes, restores old cache
  mount views and retains all data; do not shrink or delete. Exact OCI volume
  IDs, sizes/VPU, quotas, backup targets and rollback receipts need an
  authorized provider operator before the approval is actionable. On
  `sandbox-host`, after the provider expands the existing boot BlockVolume to
  at least 80 GiB and the host operator verifies `/dev/sda`, the approved
  sequence is `sudo growpart /dev/sda 1` followed by
  `sudo resize2fs /dev/sda1`; then verify `lsblk`, `df -B1 /`, `df -i /` and
  the 30% floor. This is additive; rollback does not shrink the filesystem or
  volume. If a step fails, preserve the larger volume, stop and correct the
  specific mismatch. Use an approved window if the provider rescan or
  filesystem operation requires interruption. The current
  sandbox root still fails the 30% floor, so PREP remains BLOCKED.
- **A02 networking:** the exact remote-host service proposal applies on all
  three private VNIC addresses: `linux-host` `10.0.0.200`, `sandbox-host`
  `10.0.0.14`, and `factory-sandbox` `10.0.0.30`. Add only these inbound TCP
  ports from the verified, stable Mac/control-plane source CIDR: `9443`
  release-record mTLS transport; `8444` Dev direct HTTPS; `8445` QAS direct
  HTTPS; `2224` Dev queued SSH; `2225` QAS queued SSH. Keep existing `8443`
  PRD and SSH `22` routes, plus Linux `9093`; preserve Factory's existing
  source-restricted `8443` rules (`81.95.203.65/32`, `31.61.247.138/32`).
  Linux/sandbox UFW-inactive/INPUT-ACCEPT and Factory's INPUT-REJECT policy are
  observations, not requests to weaken or replace host firewall policy. Mac
  egress `81.95.203.65` is a point-in-time observation and is not yet an
  approved stable source. No `0.0.0.0/0` or broad port range. Before any route
  change, the OCI operator must return exact VCN/subnet/NSG/security-list/
  route/NAT IDs, owner, current rule export, stable intended source CIDRs,
  exact additive rule bodies, independent route/TLS checks, and rollback
  handles. The intended production effect is reachability only for those
  authenticated services from that source; no listener or rule change ran.
  Rollback removes only the newly added exact rule IDs and verifies existing
  PRD routes/health.
- **A03 bootstrap cohort / root work:** record the fixed four-host sequence and
  failure-disable-all contract. Ubuntu root access is observed. On Mac,
  `sudo -n` failure proves unattended elevation is unavailable; an authorized
  interactive administrator can satisfy the gate. No such named route/session
  is evidenced yet. The exact Mac operation package is below; no key, CA,
  helper, listener, or service was installed.
- **A04–A07:** retain the per-host one-use bootstrap PKI, exact authority-body,
  finite renewal-grant, and normal PKI-rotation review schemas. All dependent
  public pins, digests, receipts, and mandates must be generated from approved
  preceding evidence at their declared checkpoints; none exists yet.
- **A08 PRD/producer maintenance:** the concrete producer inventory below
  identifies documented candidate controls and what remains unknown. Obtain
  each actual owner/operator, enumerate automation and direct clients, validate
  a pause/fence procedure, capture paired root/ingress metadata and zero-work
  state, and receive the scoped maintenance approval before any affected
  baseline refresh or L6 fence. No producer has been paused; no work or ACK was
  changed.
- **A09/A10:** retain only for their named registry-loss or authority/anchor-loss
  incidents. No such incident is asserted or rehearsed against production.

The engineering proposals and commands are actionable for review, but are not
approvals or execution receipts. The one consolidated external handoff should
request:

1. **OCI read-only access or operator evidence.** Provide a scoped read-only
   principal or name the authorized OCI operator. For each of the three hosts,
   return region/compartment/instance and boot-volume OCIDs, boot-volume size,
   attached Block Volume OCIDs/sizes/VPU or performance settings, VNIC OCID and
   public/private IPs, VCN/subnet/NSG/security-list/route-table IDs, NAT/IGW
   paths, exact current rule IDs/ports/source CIDRs, and boot/block-volume
   backup policy/retention. Identify the operator who can create the approved
   new 96 GiB lane/management volume and 20 GiB backup volume per host, expand
   the sandbox boot volume to at least 80 GiB, add only approved network rules,
   and provide rollback handles. Do not place credentials in Git or evidence.
2. **Mac administrator route and backup device.** Name an authorized local
   administrator who can join a supervised interactive session for the A01
   storage operations. Passwordless sudo is not required; no password or
   credential needs to be shared. Identify/provide a distinct Mac backup device
   of at least 20 GiB (or approve an equivalent independent target), its stable
   device/container identity, and ownership/retention. The measured internal
   `disk3` container is not that independent target.
3. **Gate-specific approvals.** Once exact IDs and fresh measurements exist,
   approve the materialized A01 storage/fixture package and A02 additive rule
   bodies at their declared checkpoints. The `linux-host` unified-cgroup
   correction requires a staged Docker/containerd compatibility result, an
   explicit production maintenance window, and a named console/out-of-band
   operator for reboot/rollback. No reboot window is currently approved.
4. **Later producer/maintenance evidence.** Name the actual owner/operator and
   writer inventory for each external root, then provide a tested pause/fence,
   zero-work verification and scoped A08 window before a baseline refresh or
   L6 fence. No producer is to be paused by this discovery package.

Tomasz Walczuk remains the accountable owner/default coordinator; this asks for
missing technical capability and evidence, not a new owner. When the OCI and
Mac route inputs arrive, refresh the relevant observations and exact approval
packages. L0 still requires its full fresh baseline and actual sandbox/cgroup
gates to pass.

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

## Initial PREP gate verdict (historical) and corrected current status

The initial PREP closeout recorded BLOCKED because OCI provider/network access
was not configured, the sandbox root had only 3% free against the 30% floor,
and Linux host-management cgroup-v2 enforcement was not established. Those
three blockers remain material. The initial text also concluded Mac privileged
access was unavailable and A01/A02 packages were incomplete. Those conclusions
were premature and are corrected here: `sudo -n` proves only that unattended
elevation is unavailable; the engineering proposals and actionable A01/A02
rule/storage packages are now prepared; an authorized interactive Mac
administrator route, exact OCI resource/rule identities and approvals remain
external inputs. Ownership stays assigned to Tomasz Walczuk, independent of
who has the required technical access.

Current status remains **PREP BLOCKED; L0 NOT STARTED**. The sandbox's actual
capacity floor fails today; its proposed expanded root plus lane/management and
backup envelope is not provisioned and cannot be claimed as PASS. Linux's
hybrid hierarchy and factory controller propagation still need approved
correction and verification. OCI network/volume/backup owner evidence, the Mac
interactive admin route and distinct backup device, and tested producer pause
receipts remain open. The discovery above is not a complete or green L0
inventory; fresh L0 remains mandatory when its actual prerequisites pass.

At the original PREP closeout, no source tests, installed tests, defect probes,
mailbox requests, ACKs, or service changes were run. Documentation checks and
the original S handoff are recorded below. The failure does not authorize L0
or any dependent live unit; it does not block independent source-only work
authorized by the later owner decision.

## Original Source S delivery (historical)

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

## Incremental-readiness amendment Source S and evidence delivery

The owner-decision amendment and consolidated remediation package were
committed as Source S `36843bb5a9ae29907422f16cf5aef7994c5b347f`
(`phase(LANES-PREP-00): record incremental readiness and remediation package`).
It includes the preceding local documentation commit `b669213` and updates
the plan, handbook, handoff, ledger and PREP evidence. `git diff --check`
passed before commit. No executable source suite, installed test, defect probe,
mailbox request/ACK, or service/configuration change was performed as part of
this documentation amendment.

| Delivery check | Command or guard | Result |
| --- | --- | --- |
| Mac source | `git status --short --branch`; `git rev-parse HEAD origin/dev`; `git show --stat --oneline HEAD` | `dev`, clean; HEAD and fetched `origin/dev` both equal Source S |
| Primary Gitea | `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev`; direct `ls-remote origin refs/heads/dev` | Remote push advanced `4968027..36843bb`; the push then could not update the local tracking ref under the read-only `.git` sandbox. Direct `ls-remote` confirmed the primary ref at exact Source S; an explicit-identity `git fetch origin dev` updated the local tracking ref afterward. |
| GitHub mirror | `git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev` | Returned exact Source S before Ubuntu pulls |
| `linux-host` | Verified clean `dev`, expected GitHub origin, pre-pull HEAD `4968027f88b70a7588fbf3824c04f92441fcb0bb`; explicit-identity `pull --ff-only origin dev` | Exit 0; post-pull HEAD Source S; clean `dev` |
| `sandbox-host` | Same branch, worktree and origin guards; pre-pull HEAD `4968027f88b70a7588fbf3824c04f92441fcb0bb`; explicit-identity `pull --ff-only origin dev` | Exit 0; post-pull HEAD Source S; clean `dev` |
| `factory-sandbox` | Same branch, worktree and origin guards; pre-pull HEAD `4968027f88b70a7588fbf3824c04f92441fcb0bb`; explicit-identity `pull --ff-only origin dev` | Exit 0; post-pull HEAD Source S; clean `dev` |

The Mac-to-host connections used explicit configured host identities and
verified host keys: alias `remote-session-runner` with
`/Users/tomasz.walczuk/.ssh/remote-session-runner`, `sandbox.env` with
`/Users/tomasz.walczuk/.ssh/dev.slidestud.io`, and `factory-sandbox` with
`/Users/tomasz.walczuk/.ssh/factory-sandbox`; each used
`IdentitiesOnly=yes`, `BatchMode=yes`, `StrictHostKeyChecking=yes`, and
`UpdateHostKeys=no`. Ubuntu Git pulls used
`/home/ubuntu/.ssh/gavrilo123456-github` with identity-only, batch and strict
host-key checking. No tracked file was copied by `scp` or `rsync`.
