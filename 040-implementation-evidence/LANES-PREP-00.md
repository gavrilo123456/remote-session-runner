# LANES-PREP-00 — readiness packet discovery

**State:** BLOCKED. Read-only discovery completed to the extent permitted by
current access; required provider/root access and material feasibility gates
remain unresolved. The owner separately authorized direct observations for
both PREP-00 and L0, but this failed PREP exit prevents the full L0 unit from
starting under the serial plan.

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

| Host | Current root C / U / free | Go build / modules / toolchain (B) | Go cache inodes build / modules | Other selected root use (B) |
| --- | --- | --- | --- | --- |
| `linux-host` | `48274628608 / 25560256512 / 22697594880` | `682254336 / 364761088 / 295497728` | `6552 / 7966` | journal `4303667200`; apt `161435648`; Docker `2692218880`; containerd `323584` |
| `sandbox-host` | `54958841856 / 53209952256 / 1732112384` | `639860736 / 364556288 / 290099200` | `6441 / 7863` | journal `839299072`; apt `155574272`; Docker `1448550400`; containerd `1241088` |
| `factory-sandbox` | `50839412736 / 4198699008 / 46623936512` | `326692864 / 364343296 / 295510016` | `4154 / 7863` | journal `97533952`; apt `110231552` |

The following is the concrete **engineering proposal**, not an A01 approval or
configured policy. Each Linux host receives a new 80 GiB lane/management
filesystem and a separate 16 GiB cache-backup block volume. Format the new
lane/management filesystem as ext4 with the `project` feature, mount with
`prjquota`, and verify byte and inode hard limits before any service uses it.
Existing roots lack that feature; do not reformat or retrofit the production
root. ext4 requires the `project` feature for `prjquota` ([ext4 manual](https://www.man7.org/linux/man-pages/man5/ext4.5.html)).

| Project | Canonical mount/bind path | Hard byte budget | Hard inode budget | Contents |
| --- | --- | ---: | ---: | --- |
| Dev (`project_id=1001`) | Volume child bound to `/home/ubuntu/.local/share/remote-session-runner-dev` | 16 GiB | 524,288 | Dev root, workspace, DB/WAL, inbox, outputs and logs; the 576 MiB log cap is included |
| QAS (`project_id=1002`) | Volume child bound to `/home/ubuntu/.local/share/remote-session-runner-qas` | 16 GiB | 524,288 | QAS root, workspace, DB/WAL, inbox, outputs and logs; the 576 MiB log cap is included |
| Management staging (`project_id=1003`) | `/var/lib/remote-session-runner/management` | 8 GiB | 131,072 | Builder, staging, release transport, anchor/helper and bounded management logs |
| Go build cache (`project_id=1004`) | Bind at existing `/home/ubuntu/.cache/go-build` | 6 GiB | 196,608 | Copy current cache with owner/mode verification; preserve the old directory under the mount for rollback |
| Go module cache (`project_id=1005`) | Bind at existing `/home/ubuntu/go/pkg/mod` | 2 GiB | 65,536 | Copy current cache with owner/mode verification; preserve the old directory under the mount for rollback |
| Cache backup (`project_id=1006`, separate device) | `/var/lib/remote-session-runner/cache-backup` | 8 GiB of 16 GiB | 262,144 | Latest and immediately previous verified release-cache backups; on limit, refuse the new backup and request owner disposition, never auto-delete |

The first five projects total 48 GiB on an 80 GiB volume, leaving 40% of its
bytes free at every hard cap. Set inode density to at least one inode per 16
KiB (about 5.24 million on 80 GiB); the proposed 1,441,792 project inode caps
leave over 70% unassigned. The 16 GiB backup filesystem is a second block
device, not a second partition on the same device, and has at most 50% of bytes
and 25% of inodes allocated. The stable Go cache paths remain the ones returned
by the explicit Go binary. Bind mounting is a proposed authorized operation;
it is not installed.

For sandbox-host, expand its backing boot BlockVolume so `/dev/sda1` grows to
at least 80 GiB. Current use is 49.55 GiB, so that root target leaves 38.06%
free; the minimum root size from the present use alone is 70.79 GiB. Then the
80 GiB lane/management volume and distinct 16 GiB backup device give this
full-envelope calculation (current root use already includes PRD, Docker,
journal, toolchain and old caches; the new 48 GiB budget includes a full 8 GiB
cache allowance and the separate backup budget adds 8 GiB):

```text
sandbox capacity = 80 + 80 + 16 = 176 GiB
sandbox peak use = 49.55 + 48 + 8 = 105.55 GiB
projected free = 70.45 GiB = 40.03% (>= 30%)
```

The other proposed aggregate envelopes also clear 30%: linux-host has about
140.96 GiB capacity / 79.80 GiB peak use / 43.38% free, and factory-sandbox
about 143.35 GiB / 59.91 GiB / 58.21% free. Each existing root itself remains
above 30%; each new filesystem meets its own floor. These calculations are
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
| `factory-sandbox` | Keep unified-v2. Root advertises `io`, but `system.slice` does not currently expose it. Validate controller propagation for new lane/management services with their I/O accounting/limits; if the parent needs I/O accounting enabled, change only the minimum required systemd parent configuration under a separate host approval, with no PRD `runnerd.service` change. | Confirm the new service sees `io`, an actual per-device `io.max` appears and is enforced in L2, descendants stay within the lane, and PRD health remains green. Account for any additional I/O accounting on other system.slice services in workload review. Roll back only the new scope/drop-in after units stop and verify the original PRD service remains active. |

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
| Host-management staging | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-host-management` | 8 GiB |
| Runner-managed snapshots | Aggregate across approved lane volumes | 10 GiB total |
| Release-cache backup | Separate physical device, not in `disk3` | 16 GiB device; 8 GiB budget and independent 8 GiB low-water |

The four Data-container quota groups plus the future snapshot ceiling consume
58 GiB at maximum. Using full maxima in addition to current use conservatively
preserves both the existing cache trees under the new mountpoints and every
other current Data/System/Preboot/Recovery/VM byte:

```text
Data container C = 494384795648 B; current unallocated F = 178005270528 B
floor = max(20 GiB, ceil(0.15 * C)) = 74157719348 B
future maximum = (16 + 16 + 6 + 2 + 8 + 10) GiB = 62,277,025,792 B
projected unallocated = F - future maximum = 115,728,244,736 B
margin over floor = 41,570,525,388 B
```

The APFS volumes should be mounted at the same cache paths the Makefile exports.
Seed each new cache volume with a verified copy; leave the original cache data
under the mountpoint for rollback. Do not run `go clean` or delete shared cache
contents. `diskutil list` showed only the internal physical store for `disk3`;
the mounted MyTE disk image is not a distinct backup device. The separate 16
GiB backup device is therefore still an input, and the Mac equation does not
count it against Data-container capacity.

After the exact A01 storage package and interactive administrator route are
approved, the named Mac administrator's operation list is: (1) prepare only
the six empty owner-only mountpoint directories, with Tomasz UID 502 ownership
and `0700` mode; (2) use Disk Utility/`diskutil apfs addVolume disk3 APFS` to
create the six quota-bound volumes at the approved paths and record generated
volume UUIDs and mount receipts; (3) seed the two new cache volumes without
removing source data; (4) install the owner-only volume/mount policy; (5) verify
`diskutil apfs list`, each UUID/mount/quota, `df`, owner/mode, cache read
integrity, Data unallocated floor, and snapshot total; and (6) stop on any
identity or headroom mismatch. Actual UUIDs are produced only at this approved
step. No such operation ran in PREP.

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
