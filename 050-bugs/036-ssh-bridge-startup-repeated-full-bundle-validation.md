# BUG-036 — SSH bridge startup repeatedly validates the full bundle

## Summary

| Field | Value |
| --- | --- |
| Status | `TRIAGED` — confirmed startup work and background-call churn; no correction implemented |
| Severity | High |
| Reported | 2026-11-19 Warsaw; observations 2026-11-18 UTC |
| Affected components | Mac background remote observation/pings; worker `runner-ssh-bridge` catalog startup |
| Observed release | `rr-20261117-bug033-fd20b1e`, source `fd20b1ebb5b551a1e9d143082101ae5d8843dd7b` |
| Fixed revision | None |
| Verification | Source inspection and bounded live observations; permanent correction and installed performance acceptance NOT RUN |

## Observed behavior and confirmed cost

The owner's Mac recording showed repeated `runner-ssh-bridge` processes on
Linux `79.72.94.59`. One read-only 18.202-second sample at 23:22 UTC observed
six process incarnations across Dev, QAS and PRD. Four were already reparented
to PID 1; several later exited. A separate Mac ancestry trace ties repeated
SSH calls to each lane's `runner-local`. These are per-SSH invocations; a
bridge crash/restart loop has not been established. No protocol input was read,
so the exact operation of each sampled bridge is unknown.

The startup path validates the selected catalog before serving stdin:

- `src/internal/sshbridge/serve.go`: `Run` calls
  `ValidateSelectedCatalogExecutableForPlan`.
- `src/internal/laneinstall/catalog_selector.go`: selector resolution calls
  `InspectCatalogRelease`, then calls `readSimpleBundleAtWithModes` again.
- `src/internal/laneinstall/catalog.go` and `simple_bundle.go`: each full load
  reads every local component and validates manifests, component digests and
  the bundle digest. There are additional in-memory validation/hash passes.

A metadata-only read of Linux's selected manifest found four components
containing **46,817,135 bytes**. The two loads therefore cover **93,634,270
bytes per bridge invocation**. This is source-path data volume, not measured
physical disk I/O or a measured exact CPU cost. The metadata check read zero
binary bytes. Frequent invocations multiply this avoidable work, including
PRD bridge startup while the PRD `runnerd` service is stopped. Bridge-local
ping does not establish that the private Runner service is active.

This is separate from BUG-035's daemon policy sweep over terminal session
history. BUG-034/035 source `7b11dbc` does not remove this duplicate startup
load or the background call frequency.

## Comparison and attribution limits

Both Linux and Factory report two logical CPUs and approximately 951 MiB RAM.
Factory's comparable 18.150-second sample at 23:33 UTC found one short-lived
PRD bridge and **2.93%** reported CPU steal. Linux's earlier sample reported
**91.05%** steal. Factory had approximately 713 MiB available RAM and about
325 MiB swap used. Equal nominal size does not establish equal scheduling or
traffic. Sampling can miss processes that start and exit between scans, and
CPU tick deltas are lower bounds, not precise attribution of host CPU cost.

The owner separately approved stopping all three Mac authorities. Supported
Dev/QAS/PRD stops completed at **23:41:34 UTC**, leaving all six Mac agents
unloaded. A Linux sample starting **23:42:28 UTC** observed **zero bridges**
for 18.398 seconds, while reported steal remained **93.47%**. The exact native
qualification supervisor `22368` / start `136089` and Go process `22648` /
start `137479` remained unchanged. Pausing the callers removed the observed
bridge traffic; it did not resolve the host's CPU starvation. This does not
prove the cause of steal or guarantee faster completion of qualification.

## Preservation and current operational boundary

The stop was a separate owner-approved temporary maintenance action, not a
permanent fix. No selector/configuration change, release deployment, replay,
ACK, recovery or capacity release was authorized or performed by this
investigation. Original GPU exchanges and Linux's retained LOST capacity stay
preserved. No normal-load or client-resumption clearance follows.

After the Mac stop, supported `prd-status` reports inactive. The supported
aggregate PRD inventory failed with:

```text
runner-lane catalog-work-inventory failed: selected lane state cannot be proven safe for maintenance
```

That historical refusal was followed by a successful supported read at
2026-11-19 04:49:17 UTC, outside the restricted shell with umask `077`: Mac PRD
was inactive on the same release/configuration and schema 40, with all seven
selected-lane work counts zero. The read created a zero-byte WAL and 32 KiB
SHM, both mode `0600`; the authority DB retained its inode, size and mtime. The
exact cause of the earlier generic refusal remains unproven. This later Mac
inventory does not establish remote cleanup or authorize restart/recovery.
No service was restarted. Separate diagnostic snapshots and capacity
fingerprints remain preservation evidence only.

A later 15-second idle sample at 2026-11-19 04:43:51 UTC measured **0.03%**
steal and no bridges. During the new native test run, one 5-second sample
measured **24.91%** steal and no bridges. These observations show that the
93.47% reading was not a permanent host state; they do not identify the cause
of changing CPU availability. The full Linux source suite later passed in
510.250 seconds without a bridge correction or a runtime restart.

## Minimal corrective direction — not implemented

Carry the already validated bundle together with the catalog status within one
invocation, so selector resolution need not load it again. Preserve every
selected-catalog, executable-path, ownership, mode, symlink/hardlink, manifest,
digest, schema/platform and replacement/race check. Keep the public status API
unchanged. No global/external cache, TTL trust shortcut or component-only
validation bypass is proposed. Use deterministic mutation tests, including
non-bridge component corruption and changes between independent invocations,
and structural instrumentation proving one full load. Two independent reads
alone are not an atomic generation proof.

Treat repeated background call frequency as a separate part of this issue:
consider bounded per-route observation backoff/deduplication while preserving
uncertainty, projection freshness and request identities. A catalog refactor
alone cannot remove all bridge CPU or hypervisor steal. Any implementation must
be a new qualified source cut. BUG-034/035 source `7b11dbc` subsequently passed
all four native gates on all four hosts; those results do not verify a new
bridge correction. Corrective BUG-036 code and installed acceptance remain
NOT RUN.

## Private evidence

- Owner recording: `.tmp/Screen Recording 2026-11-19 at 00.17.08.mov`.
- Linux baseline: `/private/tmp/rsr-linux-bridge-churn-readonly-7yhzrhg_/sample.json`.
- Mac caller ancestry: `/private/tmp/rsr-mac-bridge-callers-readonly-3x49bjiy/mac-caller-trace.json`.
- Factory comparison: `/private/tmp/rsr-factory-bridge-comparison-readonly-4r9ei69z/sample.json`.
- Linux manifest metadata: `/private/tmp/rsr-linux-selected-bundle-metadata-readonly-tv8ktcl4/metadata.json`.
- Approved pause package: `/private/tmp/rsr-mac-bridge-caller-pause-b6jzyzzc/MAINTENANCE.md`, `PLAN.json`, `OWNER-APPROVAL.json`.
- Pause results: same directory, `stop-results.json`.
- Postflight/gap: same directory, `post/checks.json`, `post/prd-inventory.stderr`, and `post/process-identities.json`.
- Linux after pause: same directory, `linux-postpause-sample.json`.
- Later supported Mac PRD inventory: same directory, `post/escalated-readonly-refresh-20261119/report.json`.
- Later idle sample: `/private/tmp/rsr-linux-cpu-fresh-j0tc9pdm/sample.json`.
- Native-load sample: `/private/tmp/rsr-bug034035-linux-shortdisk-vet_bceo/linux-host-19b0975cd470494a9bba672af1a56d9a/source-raw-jq2swiu9/capture.json`.
- Final four-host qualification and durable archive: [BUG-034 evidence](../040-implementation-evidence/BUG-034.md#exact-combined-source-native-qualification-7b11dbc).

`PLAN.json` and the initial `DIAGNOSIS.md` are preserved pre-action artifacts;
their prepared/pending wording is superseded by the separate approval,
stop-results and postflight evidence, not silently rewritten.
