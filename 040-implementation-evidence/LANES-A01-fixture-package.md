# A01 decision package — disposable L2 controls fixtures

**Status:** Prepared for owner decision; no volumes, partitions, filesystems,
mounts, quotas, services, boot settings, firewall rules, or credentials have
been changed. This package provisions only disposable F-test fixtures. It does
not provision the real Dev/QAS/management storage described separately in
[`LANES-PREP-00.md`](LANES-PREP-00.md), and does not unblock `LANES-L3-19` by
itself.

## 1. Decision requested

Approve or decline the following bounded fixture preparation:

1. An OCI operator creates and attaches, on each Ubuntu host, a new 128 GB
   Balanced data volume and a separate 50 GB Balanced backup volume. Estimated
   list-price storage cost is **$22.70 USD/month** for the six Linux fixture
   volumes, before tax, discounts, and any non-default backup policy.
2. A Mac operator supplies two blank, separately identified external physical
   disks: at least 64 GB for four isolated APFS test volumes and at least 32 GB
   for the separate APFS backup test volume. The existing inventory found no
   suitable physical external disk; disk images are refused. No hardware price
   is included because no device or supplier has been selected.
3. After exact device identities are returned, the authorized host operators
   perform the guarded fixture-only format, mount, owner/mode and quota setup
   below. The F runner then validates the exact owner-approved manifest and
   SHA-256 before it writes any probe data.

The accountable coordinator remains Tomasz Walczuk. Before provisioning, name
an OCI operator with tenancy permission to inspect/create/attach Block Volumes
in the three instance compartments and a Mac administrator/operator authorized
to erase only the two owner-approved blank external disks, create APFS
containers/volumes, and set their fixture ownership. They use their own
interactive authenticated sessions; credentials are never passed to test
workers. The current Mac account is UID 502 and cannot elevate unattended; the
Ubuntu SSH profiles can administer their hosts but do not provide OCI API
access. No credential or key is requested for Git or this package. Operators
return resource identities and command results, not private keys or tokens.

## 2. Resource allocation

### Linux fixture volumes

Each host gets two new, non-shared Block Volumes in the **same region and
Availability Domain as that host's compute instance**. Allocation is in
decimal GB because OCI uses GB for volume sizes; filesystem budgets below use
GiB.

| Host | Region from current inventory | Data device | Separate backup device | Existing host gate |
| --- | --- | ---: | ---: | --- |
| `linux-host` | `eu-marseille-1` | 128 GB Balanced, 10 VPUs/GB | 50 GB Balanced, 10 VPUs/GB | Hybrid v1/v2 cgroups; A02 compatibility test and approved reboot required before F |
| `sandbox-host` | `eu-frankfurt-1` | 128 GB Balanced, 10 VPUs/GB | 50 GB Balanced, 10 VPUs/GB | Unified v2; preserve PRD `runnerd.service` and its existing shared Go caches |
| `factory-sandbox` | `uk-london-1` | 128 GB Balanced, 10 VPUs/GB | 50 GB Balanced, 10 VPUs/GB | Unified v2; `io` does not currently propagate through `system.slice`; scoped A02 review/change required before F |

The 128 GB decimal data allocation provides about 119.2 GiB before filesystem
metadata and exceeds the exact 96 GiB usable minimum enforced by the manifest.
Its six data project hard quotas total 56 GiB and 1,703,936 inodes. The
separate backup filesystem has an 8 GiB quota and 262,144 inodes. Together the
seven project quotas are 64 GiB and 1,966,080 inodes across two devices. At
full quota use, the data filesystem has about 63.2 GiB and the backup
filesystem about 38.6 GiB remaining before metadata, both above the 30% floor.
The F preflight rechecks measured available bytes before and after each probe.
The 50 GB decimal backup allocation provides
about 46.6 GiB before metadata, exceeds the 20 GiB usable minimum, and carries
one 8 GiB / 262,144-inode project quota on its own device.

| Linux fixture project | Project ID | Hard bytes | Hard inodes | Fixture-only directory |
| --- | ---: | ---: | ---: | --- |
| Dev control | 1001 | 16 GiB | 524,288 | `data/dev` |
| QAS control | 1002 | 16 GiB | 524,288 | `data/qas` |
| Management | 1003 | 8 GiB | 131,072 | `data/management` |
| Go build cache control | 1004 | 6 GiB | 196,608 | `data/go-build` |
| Go module cache control | 1005 | 2 GiB | 65,536 | `data/go-modules` |
| Release cache control | 1006 | 8 GiB | 262,144 | `data/release-cache` |
| Backup control | 1007 | 8 GiB | 262,144 | `backup` on the separate device |

The registered F `go test` subprocess writes its build cache and temporary
files only to the fixture `go-build` project and uses `go-modules` as its
module cache. The outer repository command uses the normal Ubuntu Go caches at
`/home/ubuntu/.cache/go-build` and `/home/ubuntu/go/pkg/mod` as required by the
host toolchain; fixture preparation never copies, mounts, purges, or changes
those paths. F-generated Go cache entries stay on the disposable volumes until
separately approved cleanup. The sandbox host's
existing root is 3% free and fails the real-rollout 30% gate. That requires its
separate later boot-volume expansion; this F package does not expand or clean
the root. The empty `io.max` under the existing sandbox PRD service is not a
failure and is not changed.

### Mac fixture volumes

Use one blank external physical device of at least 64 GB for one APFS test
container and four quota-bound volumes, plus a **different** blank physical
device of at least 32 GB for the backup APFS container and volume. All F writes
go to those external devices. The test and backup disks must both differ from
the production Data disk and from each other. `diskutil info -plist` must report
`VirtualOrPhysical=Physical`, `Internal=false`, and `WholeDisk=true`; the
manifest must bind each APFS container to
its verified `ParentWholeDisk`; disk images and `Unknown` physicality are
rejected.

| Role | Isolated fixture path | APFS quota | External container requirement |
| --- | --- | ---: | --- |
| Dev | `/private/tmp/remote-session-runner-lanes-fixtures/<fixture-id>/dev` | 15 GiB | Same 64 GB test container as QAS and test-cache volumes |
| QAS | `/private/tmp/remote-session-runner-lanes-fixtures/<fixture-id>/qas` | 15 GiB | Same 64 GB test container as Dev and test-cache volumes |
| Go build cache control | `.../<fixture-id>/go-build` | 2 GiB | Same 64 GB test container |
| Go module cache control | `.../<fixture-id>/go-modules` | 2 GiB | Same 64 GB test container |
| Backup control | `.../<fixture-id>/backup` | 3 GiB | Separate 32 GB physical device and APFS container |

The 64 GB test device has a 34 GiB total APFS quota envelope; the 32 GB backup
device has a 3 GiB quota. F verifies that each external container retains at
least 30% free after its projected fixture quotas. F also reads the production
Data container and checks its separate planned 70 GiB envelope against the
`max(20 GiB, 15%)` floor. The current PREP observation leaves 28,685,623,500 B
above that floor after the 70 GiB projection. It does not write to the Data
volume or shared caches.

The real Mac Dev/QAS, host-management, release, cache and snapshot quotas remain
in the separate rollout stage. The existing PREP rollout package allocates
66 GiB of those future maxima; the L2-08 source guard evaluates a 70 GiB
envelope that also includes the 24 GiB PRD host reserve. Reconcile the different
accounting categories against the final signed policy before real provisioning;
the more conservative 70 GiB F read does not authorize early rollout volumes.

## 3. Provider pricing and valid shapes

OCI supports 50 GB through 16 TB in 1 GB increments, requires the volume and
instance to share an Availability Domain, and accepts `--vpus-per-gb 10` for
Balanced. Balanced service limits scale at 60 IOPS/GB and 480 KB/s/GB (up to
25,000 IOPS and 480 MB/s per volume). The shape's instance-level I/O limit may
be lower, so actual attached-device throughput and IOPS are still measured
before exact lane limits are accepted. See [OCI volume creation](https://docs.oracle.com/en-us/iaas/tools/oci-cli/latest/oci_cli_docs/cmdref/bv/volume/create.html),
[Balanced performance](https://docs.oracle.com/en-us/iaas/Content/Block/Concepts/blockvolumebalancedperformance.htm),
and the [OCI volume attachment command](https://docs.oracle.com/en-us/iaas/tools/oci-cli/latest/oci_cli_docs/cmdref/compute/volume-attachment/attach.html).

Oracle's current published list price is $0.0255/GB-month for storage plus
$0.017/GB-month for Balanced performance. Six volumes total 534 GB:

```text
3 hosts × (128 GB data + 50 GB backup) = 534 GB
534 GB × ($0.0255 + $0.017) = $22.695/month ≈ $22.70/month
per host: 178 GB × $0.0425 = $7.565/month
```

This estimate uses the [current OCI price list](https://www.oracle.com/cloud/price-list/)
accessed 2026-10-17 and excludes backup policies, tax, regional/contract
discounts and unrelated instance costs. Mac external-device cost is a separate
one-time purchase or loan, not part of the OCI estimate. The real Linux
rollout's six 128/50 GB volumes are a second, separate allocation at a later
stage and would add another estimated $22.70/month if provisioned at the same
sizes. The sandbox root expansion and real Mac APFS setup are not included in
the immediate fixture estimate.

## 4. Exact provisioning and guarded fixture setup

### OCI operator: inspect first, then create only the named fixtures

The operator first reads each instance's OCID, region, Availability Domain,
compartment, shape, boot-volume identity/current size, existing attachments,
and supported attachment type. The PREP record does not contain instance
OCIDs, compartments, AD names, shape capabilities, or active OCI credentials.
Do not infer any of those from the known VNIC OCIDs. The operator must verify
that the host regions above match live instance metadata and that all six new
volume names/size/performance values match this package before the first
create.

For each host, after owner A01 approval and with values returned from the
read-only inventory, create the two new volumes:

```sh
set -euo pipefail
: "${HOST_ID:?set the exact canonical host ID}"
: "${INSTANCE_OCID:?set the verified instance OCID}"
: "${INSTANCE_AD:?set the instance Availability Domain}"
: "${COMPARTMENT_OCID:?set the verified compartment OCID}"
: "${FIXTURE_ID:?set the owner-approved fixture ID}"
DATA_VOLUME_OCID="$(oci bv volume create \
  --availability-domain "$INSTANCE_AD" \
  --compartment-id "$COMPARTMENT_OCID" \
  --display-name "rsr-l2-fixture-${HOST_ID}-${FIXTURE_ID}-data" \
  --size-in-gbs 128 --vpus-per-gb 10 \
  --wait-for-state AVAILABLE --query data.id --raw-output)"

BACKUP_VOLUME_OCID="$(oci bv volume create \
  --availability-domain "$INSTANCE_AD" \
  --compartment-id "$COMPARTMENT_OCID" \
  --display-name "rsr-l2-fixture-${HOST_ID}-${FIXTURE_ID}-backup" \
  --size-in-gbs 50 --vpus-per-gb 10 \
  --wait-for-state AVAILABLE --query data.id --raw-output)"
```

Attach each returned OCID to only its matching instance, choosing one exact
supported `--type` (`iscsi`, `paravirtualized`, `nvme`, or
`service_determined`) from the live instance/shape and attachment inventory.
Do not enable multi-instance sharing. Preserve each returned attachment OCID
and lifecycle result:

```sh
set -euo pipefail
DATA_ATTACHMENT_OCID="$(oci compute volume-attachment attach \
  --instance-id "$INSTANCE_OCID" --volume-id "$DATA_VOLUME_OCID" \
  --type "$ATTACHMENT_TYPE" --wait-for-state ATTACHED \
  --query data.id --raw-output)"
BACKUP_ATTACHMENT_OCID="$(oci compute volume-attachment attach \
  --instance-id "$INSTANCE_OCID" --volume-id "$BACKUP_VOLUME_OCID" \
  --type "$ATTACHMENT_TYPE" --wait-for-state ATTACHED \
  --query data.id --raw-output)"
oci compute volume-attachment get --volume-attachment-id "$DATA_ATTACHMENT_OCID" \
  --query 'data.{id:id,volume_id:"volume-id",instance_id:"instance-id",device:device,state:"lifecycle-state"}' --output json
oci compute volume-attachment get --volume-attachment-id "$BACKUP_ATTACHMENT_OCID" \
  --query 'data.{id:id,volume_id:"volume-id",instance_id:"instance-id",device:device,state:"lifecycle-state"}' --output json
```

Save both returned attachment OCIDs. The OCI operator confirms each query
returns the exact new volume OCID, instance
OCID, attachment OCID, provider device mapping, and `ATTACHED` state before
handing the device identity to the Ubuntu operator.

If OCI reports a shape or availability mismatch, stop before retrying with a
different AD, type or compartment. Return the provider error and corrected
read-only facts for owner review. The operator records each volume OCID and
attachment OCID alongside host, AD, region, size, VPU setting and effective
performance. If a create/attach transaction partially succeeds, preserve and
report the exact resources already created; do not delete them automatically.
No new volume ID is guessed or written to Git.

### Ubuntu operator: verify identities before formatting

Use only the configured SSH identity and `sudo` on its matching Ubuntu host.
Set `FIXTURE_ROOT=/mnt/remote-session-runner-lanes-fixtures/<fixture-id>` and
derive the device from the newly returned attachment. For each device, verify
the provider attachment's returned `volume-id`, `instance-id`, attachment
OCID, and `device` mapping. On the host, correlate that mapping with the new
device path, its resolved kernel name, the independent 32- or 33-hex `lsblk SERIAL`,
filesystem UUID (not assigned until format), major/minor and capacity. Do not
assume the serial equals the OCI volume OCID. The exact provider-to-host device
mapping must be unambiguous before continuing. Then confirm the device is new
and unused, not a root ancestor, mounted filesystem, or device already present
in the fixture manifest:

```sh
set -euo pipefail
: "${LANE_DEVICE:?set the exact data device from the OCI attachment mapping}"
: "${BACKUP_DEVICE:?set the exact backup device from the OCI attachment mapping}"
ROOT_SOURCE="$(findmnt --noheadings --raw --output SOURCE --target /)"
ROOT_TREE="$(lsblk --inverse --noheadings --raw --output MAJ:MIN "$ROOT_SOURCE" | sort -u)"
LANE_REAL="$(readlink -f -- "$LANE_DEVICE")"
BACKUP_REAL="$(readlink -f -- "$BACKUP_DEVICE")"
if [ "$LANE_REAL" = "$BACKUP_REAL" ]; then
  echo 'refusing aliased data and backup devices' >&2
  exit 1
fi
for DEVICE_REAL in "$LANE_REAL" "$BACKUP_REAL"; do
  test -b "$DEVICE_REAL"
  DEVICE_TREE="$(lsblk --inverse --noheadings --raw --output MAJ:MIN "$DEVICE_REAL" | sort -u)"
  for DEVICE_MM in $DEVICE_TREE; do
    if printf '%s\n' "$ROOT_TREE" | grep -Fxq "$DEVICE_MM"; then
      echo "refusing fixture device in root-device ancestry: $DEVICE_REAL" >&2
      exit 1
    fi
  done
  if sudo findmnt --noheadings --source "$DEVICE_REAL"; then
    echo "refusing mounted fixture device: $DEVICE_REAL" >&2
    exit 1
  fi
done
sudo lsblk --bytes --paths --output NAME,TYPE,SIZE,SERIAL,WWN,FSTYPE,UUID,MAJ:MIN,MOUNTPOINTS
sudo findmnt --target /
sudo udevadm info --query=property --name="$LANE_DEVICE"
sudo udevadm info --query=property --name="$BACKUP_DEVICE"
sudo wipefs --no-act "$LANE_DEVICE"
sudo wipefs --no-act "$BACKUP_DEVICE"
```

The last two commands must show no signature. The attachment IDs and volume
OCIDs are recorded separately from the host serials; save the exact observed
mapping in the manifest (`volume_id`, `attachment_id`, `device_path`, resolved
`device_id`, observed hex `device_serial`, UUID, major/minor and capacity). If the
attachment response does not identify the host path, stop without formatting.
After the A01 operator authorizes the exact empty devices, use:

```sh
set -euo pipefail
: "${FIXTURE_ROOT:?set the exact new fixture root}"
if [ -e "$FIXTURE_ROOT" ]; then
  echo "refusing existing fixture root with possible retained data: $FIXTURE_ROOT" >&2
  exit 1
fi
sudo install -d -o ubuntu -g ubuntu -m 0700 -- "$FIXTURE_ROOT"
sudo install -d -o ubuntu -g ubuntu -m 0700 -- "$FIXTURE_ROOT/data" "$FIXTURE_ROOT/backup"
sudo mkfs.ext4 -O project,quota -E quotatype=prjquota -i 16384 -L RSR-L2-FDATA "$LANE_DEVICE"
sudo mkfs.ext4 -O project,quota -E quotatype=prjquota -i 16384 -L RSR-L2-FBACKUP "$BACKUP_DEVICE"
sudo mount -t ext4 -o noatime,prjquota "$LANE_DEVICE" "$FIXTURE_ROOT/data"
sudo mount -t ext4 -o noatime,prjquota "$BACKUP_DEVICE" "$FIXTURE_ROOT/backup"
sudo chown ubuntu:ubuntu "$FIXTURE_ROOT/data" "$FIXTURE_ROOT/backup"
sudo chmod 0700 "$FIXTURE_ROOT/data" "$FIXTURE_ROOT/backup"
```

Create only the seven `data/{dev,qas,management,go-build,go-modules,release-cache}`
project directories. Set each to `ubuntu:ubuntu`, mode `0700`, assign its
numeric project ID, and set its hard byte and inode limits. `setquota` block
limits below are KiB (hard bytes divided by 1024):

```sh
set -euo pipefail
set_fixture_project() {
  project_id="$1"; project_path="$2"; hard_bytes="$3"; hard_inodes="$4"; quota_mount="$5"
  sudo install -d -o ubuntu -g ubuntu -m 0700 -- "$project_path"
  sudo chattr -p "$project_id" +P "$project_path"
  sudo setquota -P "$project_id" 0 "$((hard_bytes / 1024))" 0 "$hard_inodes" "$quota_mount"
}
set_fixture_project 1001 "$FIXTURE_ROOT/data/dev"           $((16*1024*1024*1024)) 524288 "$FIXTURE_ROOT/data"
set_fixture_project 1002 "$FIXTURE_ROOT/data/qas"           $((16*1024*1024*1024)) 524288 "$FIXTURE_ROOT/data"
set_fixture_project 1003 "$FIXTURE_ROOT/data/management"    $(( 8*1024*1024*1024)) 131072 "$FIXTURE_ROOT/data"
set_fixture_project 1004 "$FIXTURE_ROOT/data/go-build"      $(( 6*1024*1024*1024)) 196608 "$FIXTURE_ROOT/data"
set_fixture_project 1005 "$FIXTURE_ROOT/data/go-modules"    $(( 2*1024*1024*1024))  65536 "$FIXTURE_ROOT/data"
set_fixture_project 1006 "$FIXTURE_ROOT/data/release-cache" $(( 8*1024*1024*1024)) 262144 "$FIXTURE_ROOT/data"
set_fixture_project 1007 "$FIXTURE_ROOT/backup"             $(( 8*1024*1024*1024)) 262144 "$FIXTURE_ROOT/backup"
unset -f set_fixture_project
```

The data filesystem uses project IDs 1001–1006; the separate backup filesystem
uses ID 1007. Numeric project IDs avoid editing shared `/etc/projects` or
`/etc/projid`. Do not add `/etc/fstab` entries or enable project quotas on root.
Verify exact `findmnt`, `tune2fs`, `lsblk`, `blkid`, `df -B1`, `df -i`, project
quota reports, owner/mode and live controller inventory. Do not run a pressure
test unless the resulting owner-only manifest is complete and its SHA-256 has
an exact owner A01 approval.

### Mac operator: verify two external devices before APFS creation

After exact A01 approval, the Mac administrator/operator inventories both
connected external devices with `diskutil list -plist` and
`diskutil info -plist /dev/diskN`. Confirm serial/model and size with the owner,
`VirtualOrPhysical=Physical`, `Internal=false`, and `WholeDisk=true`. Require
two different whole-disk identities; neither may be production `disk0` or a
disk image, and neither may contain mounted or retained data. If
either device is not blank and specifically approved for erasure, stop.

Only after that exact blank-device check, create one empty APFS partition on
each approved external whole disk. Do not use the production Data disk or any
disk image:

```sh
set -euo pipefail
: "${FIXTURE_ROOT:?set the exact new fixture root}"
: "${TEST_WHOLE_DISK:?set the approved test whole-disk identifier}"
: "${BACKUP_WHOLE_DISK:?set the approved backup whole-disk identifier}"
if [ -e "$FIXTURE_ROOT" ]; then
  echo "refusing existing fixture root with possible retained data: $FIXTURE_ROOT" >&2
  exit 1
fi
sudo install -d -o 502 -g 20 -m 0700 -- "$FIXTURE_ROOT" \
  "$FIXTURE_ROOT/dev" "$FIXTURE_ROOT/qas" "$FIXTURE_ROOT/go-build" \
  "$FIXTURE_ROOT/go-modules" "$FIXTURE_ROOT/backup"
sudo diskutil partitionDisk "/dev/$TEST_WHOLE_DISK" GPT \
  '%Apple_APFS%' '%noformat%' R
sudo diskutil partitionDisk "/dev/$BACKUP_WHOLE_DISK" GPT \
  '%Apple_APFS%' '%noformat%' R
```

If one partition/container/volume operation succeeds and a later command
fails, stop and preserve the exact newly created fixture resources for owner
disposition. Do not erase or delete partially created resources as an automatic
rollback.

Read back `diskutil list -plist` for each approved whole disk. Set
`TEST_STORE` and `BACKUP_STORE` only from the newly created `Apple_APFS`
partitions that are children of those exact whole disks. Stop if the returned
partition map or parent identity differs. Then create APFS containers on those
new partitions:

```sh
sudo diskutil apfs createContainer "/dev/$TEST_STORE"
sudo diskutil apfs createContainer "/dev/$BACKUP_STORE"
```

Read back `diskutil apfs list -plist`; set `TEST_CONTAINER` and
`BACKUP_CONTAINER` only from those returned container references. Add the four
test volumes to the first and the backup volume to the other container:

```sh
set -euo pipefail
sudo diskutil apfs addVolume "$TEST_CONTAINER" APFS "RSR-L2-Dev" -quota 15g -mountpoint "$FIXTURE_ROOT/dev"
sudo diskutil apfs addVolume "$TEST_CONTAINER" APFS "RSR-L2-QAS" -quota 15g -mountpoint "$FIXTURE_ROOT/qas"
sudo diskutil apfs addVolume "$TEST_CONTAINER" APFS "RSR-L2-GoBuild" -quota 2g -mountpoint "$FIXTURE_ROOT/go-build"
sudo diskutil apfs addVolume "$TEST_CONTAINER" APFS "RSR-L2-GoModules" -quota 2g -mountpoint "$FIXTURE_ROOT/go-modules"
sudo diskutil apfs addVolume "$BACKUP_CONTAINER" APFS "RSR-L2-Backup" -quota 3g -mountpoint "$FIXTURE_ROOT/backup"
```

Enable ownership on only those returned APFS volume devices, then set each
mount root to UID 502:GID 20, mode `0700`. Verify actual volume UUIDs, device
identifiers, container capacities/free bytes, exact quotas, mount paths,
physical parents, and owners using `diskutil apfs list -plist`,
`diskutil info -plist`, `df -B1`, and `diskutil info`. Do not create any
fixture volume inside the production Data container and do not move or resize
the shared Go caches.

After reading each `APFSVolumeUUID`/`DeviceIdentifier` pair back from the new
container inventory, assign each `VOLUME_DEVICE` from that exact row and run:

```sh
sudo diskutil enableOwnership "$DEV_VOLUME_DEVICE"
sudo diskutil enableOwnership "$QAS_VOLUME_DEVICE"
sudo diskutil enableOwnership "$BUILD_VOLUME_DEVICE"
sudo diskutil enableOwnership "$MODULES_VOLUME_DEVICE"
sudo diskutil enableOwnership "$BACKUP_VOLUME_DEVICE"
sudo chown 502:20 "$FIXTURE_ROOT/dev" "$FIXTURE_ROOT/qas" \
  "$FIXTURE_ROOT/go-build" "$FIXTURE_ROOT/go-modules" "$FIXTURE_ROOT/backup"
sudo chmod 0700 "$FIXTURE_ROOT/dev" "$FIXTURE_ROOT/qas" \
  "$FIXTURE_ROOT/go-build" "$FIXTURE_ROOT/go-modules" "$FIXTURE_ROOT/backup"
```

Do not substitute or reuse a device identifier from an earlier inventory.

The authorized operator prepares the owner-only manifest outside Git only
after these observed identities are returned. The owner reviews its exact
bytes, approves its SHA-256 under A01, and the same digest is passed to
`make test-lanes-controls`. Generated UUIDs and device IDs must never be
fabricated or committed as source constants.

## 5. F procedure, production impact and cleanup

For Linux, `test-lanes-controls` proves exact mount, UUID, provider serial,
capacity and major/minor identities before `systemd-run` creates a uniquely
named transient scope. The Go test and test-only listener/session/wrapper/bridge
workers run as the invoking `ubuntu` user, have no inherited operator/provider
environment, and bind no non-loopback socket. The scope is limited to CPU 25%,
memory 2 GiB, 64 tasks, and 16 MiB/s/1,000 IOPS writes on only the verified
fixture devices. The F assertion fills every project to the byte and inode
hard-limit boundary, requires quota refusal, exercises the 576 MiB bounded log
capture, and checks at least 30% data-filesystem free space. It never installs
or restarts a Runner service or writes live caches.

Immediately before each Linux F command, the host operator runs `sudo -v`
interactively in that same operator shell. The launcher uses `sudo -n` only so
an expired timestamp fails closed without an unexpected prompt; a previously
authorized interactive sudo session satisfies it, so passwordless sudo is not
required. The Mac operator uses its authorized interactive administrator route
for the approved filesystem setup; F verification itself is read-only except
for task-owned files on fixture volumes.

For Mac, the verifier reads the production Data volume and checks the full
70 GiB projected envelope against the `max(20 GiB, 15%)` Data-container floor.
All quota-filling/log writes target the separate external APFS test volumes;
the backup test uses the second external disk. Each mount must exactly match
the manifest's UUID, quota, container capacity, device and physical parent; the
projected quotas must preserve at least 30% free on each fixture container.
The F assertion fills each approved quota to refusal, exercises bounded logs,
cleans only task-owned probe files, and rechecks the read-only Data floor.

Volume creation/attachment adds only the named disposable devices; it does not
replace boot or PRD storage. Running F still consumes shared host resources:
Linux is capped at 25% of one CPU, 2 GiB RAM and 64 tasks, with writes limited
to 16 MiB/s and 1,000 IOPS on each fixture volume. The provider instance's
aggregate I/O ceiling can be lower, so fixture traffic could contend with PRD
storage. Mac F is limited by disposable-volume quotas but has no CPU/RAM
reservation; its temporary work can use shared compute and external USB
bandwidth. Run the bounded probes only in an owner-approved time when the
service owner confirms the expected brief load is acceptable. Stop on PRD
health degradation, unexpected host load, or any write outside the fixture
identities; preserve all evidence and fixtures for diagnosis.

The only persistent resources A01 creates are specifically named disposable
fixture volumes; production services, PRD roots and shared caches remain in
place. Failed preflight means no test writes run. If a probe fails, it cleans
only its uniquely named task-owned probe files and reports failure; diagnose
before rerunning. Never remove old mailbox work, ACKs, logs, or caches.

After F evidence has been read and all dependent disposable fixture tests are
complete, cleanup is a separately approved operation using the exact recorded
identities: stop the test scope, confirm no fixture process/mount is open,
unmount only the five approved Linux fixture paths or five approved Mac APFS
volumes, then detach only the six recorded OCI fixture attachments. Retain the
volume contents and evidence until owner disposition. Delete only the exact
fixture volume OCIDs or erase only the two exact Mac external disks after a
separate explicit cleanup approval. Rollback on any issue is to stop fixture
tests, preserve volume contents, detach only the newly created fixture
attachments if safe, and leave all production resources untouched.

After separate cleanup approval and after archiving the accepted evidence, the
Ubuntu operator unmounts the exact recorded mounts; the OCI operator detaches
and deletes only the recorded fixture resources:

```sh
sudo umount -- "$FIXTURE_ROOT/data"
sudo umount -- "$FIXTURE_ROOT/backup"
oci compute volume-attachment detach --volume-attachment-id "$DATA_ATTACHMENT_OCID" --wait-for-state SUCCEEDED
oci compute volume-attachment detach --volume-attachment-id "$BACKUP_ATTACHMENT_OCID" --wait-for-state SUCCEEDED
oci compute volume-attachment list --compartment-id "$COMPARTMENT_OCID" --all --query "data[?id=='$DATA_ATTACHMENT_OCID']" --output json
oci compute volume-attachment list --compartment-id "$COMPARTMENT_OCID" --all --query "data[?id=='$BACKUP_ATTACHMENT_OCID']" --output json
oci bv volume delete --volume-id "$DATA_VOLUME_OCID" --wait-for-state TERMINATED
oci bv volume delete --volume-id "$BACKUP_VOLUME_OCID" --wait-for-state TERMINATED
```

Before each OCI deletion, re-read its volume and attachment records and match
the recorded OCIDs, fixture display names, instance and host. If an attachment
is not detached or any identity differs, stop and retain the volume. On Mac,
after the same evidence/approval checkpoint, delete only the five fixture
volume devices and their two fixture containers read from the current APFS
inventory:

```sh
sudo diskutil apfs deleteVolume "$DEV_VOLUME_DEVICE"
sudo diskutil apfs deleteVolume "$QAS_VOLUME_DEVICE"
sudo diskutil apfs deleteVolume "$BUILD_VOLUME_DEVICE"
sudo diskutil apfs deleteVolume "$MODULES_VOLUME_DEVICE"
sudo diskutil apfs deleteVolume "$BACKUP_VOLUME_DEVICE"
sudo diskutil apfs deleteContainer "$TEST_CONTAINER"
sudo diskutil apfs deleteContainer "$BACKUP_CONTAINER"
```

Do not run cleanup commands as rollback to a failed F assertion. Failed or
uncertain acceptance retains the fixtures and all evidence for diagnosis; any
failed delete or detach stops the remainder and preserves the already returned
provider identities.

## 6. Separate rollout resources and host-maintenance gates

The later real Linux rollout remains the PREP-00 design: one 128 GB data
volume and one 50 GB separate backup volume per Ubuntu host, formatted and
mounted at the actual management/cache/Dev/QAS locations only in its prescribed
provisioning stage. That is another estimated $22.70/month, not included in the
fixture total. `sandbox-host` separately needs its boot filesystem expanded to
at least 80 GiB usable (the current 55 GB-looking filesystem size is not an
OCI-verified boot-volume size); the proposal is a 90 GB boot volume. If live OCI
metadata confirms a 55 GB current volume, the 35 GB increase adds about
$1.49/month at the same Balanced list rate. The exact current boot OCID/size,
rescan and online/offline-grow procedure, and window must be verified before
that later operation. Preserve existing root contents and caches; do not use
cleanup to pass the gate. See [OCI online volume resizing](https://docs.oracle.com/en-us/iaas/Content/Block/Tasks/update-online-resize-block-boot-volume.htm)
and the [OCI Linux grow-filesystem utility guidance](https://docs.oracle.com/en-us/iaas/oracle-linux/oci/oci-utilities-reference.htm).

`linux-host` cgroup conversion remains its own A02 maintenance action: test
active Docker/containerd compatibility, preserve the original GRUB entry,
apply the documented v2 boot arguments, reboot in an explicit approved window
with console/out-of-band rollback, then verify PRD health and controller
placement. `factory-sandbox` controller propagation is separately scoped: try
only the new lane/management scopes first; if `io` still does not propagate,
review and approve the exact `system.slice.d/20-runner-lanes-io.conf` change,
`daemon-reload`, sibling impact, PRD health checks and rollback in its own
window. Neither is part of the volume-creation approval or fixture runner.
`sandbox-host` needs no PRD service change.

`LANES-L3-19` remains **BLOCKED** until its ordered, genuine physical gates
pass. Preparing or creating these disposable F fixtures cannot be recorded as
L3-19 acceptance.

## 7. Source implementation and delivery evidence

The fixture command, closed owner-approved manifest, guarded Linux/Mac F
procedures, and disposable cache isolation were delivered in Source S1
`5712da31545fd02b01b59824ba7633a2aabd86cc`. Its first `linux-host` full suite
found that one exact-digest regression used only a Mac manifest; the mismatch
was recorded and corrected at Source S2
`5979da100389125ce81ec6942b43c5ef3ec45f2b`. S2 passed `make check build` on
Mac and all three Ubuntu hosts. Focused race tests passed on Mac, linux-host and
sandbox-host; factory-sandbox race was NOT RUN because `CGO_ENABLED=0` and `cc`
is unavailable. Linux amd64/arm64 cross-compilation passed for both test
packages and the command. The entry point rejected a missing external approval
manifest before running F assertions. This is source validation only; no
physical F procedure ran.

At the S2 source-delivery checkpoint, Gitea `origin/dev` and automatic GitHub
`github/dev` both resolved to S2. Evidence-only commit
`2869a65edf3e86724dc8d40a6f344f47940a97f3` was then pushed to Gitea, verified
at the exact mirror SHA, and fast-forwarded to all three clean Ubuntu
checkouts; `git show --check` passed there and on Mac. The generated volume,
attachment and filesystem identities remain blank until an approved operator
creates and measures each device; none has been guessed or committed.
