# R168 exact Mac PRD teardown recovery — complete

**Runner clearance: CLEARED for this exact teardown/capacity hold.** The owner
approved the prepared exact-scoped recovery and same-binary restart on
2026-11-14. Recovery, offline postflight and restarted-authority checks passed.
This is not a global-clean authority claim or a Factory qualification decision.

## Exact exchange and disposition

- Authority: Mac `mac-control-plane`, `tomasz.walczuk` / UID 502, unsuffixed PRD.
  The policy is `mac-dev / local/mac-workstation`; that name is not the Dev lane.
- Request: `req-asf-p09-r168-nacc-record-supervised-20261114-agent-001`.
- Session: `sess-e5d66620e61ddaa66f92968828390803`.
- Command: `cmd-877df533a8e0a90b0b8c38fa4bc740a8`.
- Job: `job-bc3fe465a2317ee53671f2a186beb57e`.

The qualified public scoped operator proved native root/group exit without
signals, archived the original target artifacts, revalidated workspace bytes,
then confirmed cleanup and released **only this session reservation** at
`2026-11-14T20:19:57.225396Z` / **21:19:57 Warsaw**. Its command slot had already
been released. The target workspace/ownership marker are absent and its
completed-teardown finalization is absent. Eight older session reservations and
one older command slot remain held, with unchanged pair/authority rows, markers
and workspaces. No peer recovery or capacity release occurred.

The original command remains **FAILED, exit 1**, complete/untruncated, response
revision 4/event cursor 8, and terminal-unacknowledged. Historical session/job
and teardown remain LOST as preserved audit history; the fresh cleanup and
capacity disposition is established by the reservation timestamps and qualified
operator/postflight evidence. No qualification was replayed and no request or
ACK was published. Factory reservations, project services and eligibility were
not changed.

Original response SHA-256:
`24c42df8bd239925aec55d9ef95e0673610291a1b7b746a9fafb5b8fe0a1f6ed`.
Original events SHA-256:
`79f42256649d48d70044311304403d81445990cd538dd758d5b364a3fa8dfee3`.
Both were independently unchanged before and after recovery/restart; neither
ACK JSON nor marker exists for the request.

## Strict preflight refresh during automatic retention

The first approved preflight safely refused an obsolete full-database metadata
fingerprint. It did not stop services or mutate recovery state. Consistent,
immutable backup-copy comparisons proved that only automatic response-retention
timestamps for old **already terminal and acknowledged** exchanges had advanced.
No metadata rows were added/removed, no new ACK/result was inferred, and the
original target and eight peer/ownership bindings were unchanged.

The exact approved fingerprint
`3dae8a582561062a813c54316ab69bb85b103168282f156f082e28622ef67dd7`
was cryptographically reconstructed from the original 19:58 backup plus only
the two proven retention updates before the 20:04 approved read cut. Subsequent
routine GC updates explained the later digests. The original package, hashes,
backups and explicit delta remain preserved; no projection field or recovery
predicate was excluded or relaxed to force acceptance.

Within the approved exact-pair maintenance scope, a fresh supported backup and
native read-only applicability check preceded supported `prd-stop`. A further
consistent backup after stop froze the current housekeeping state. Its full
metadata projection was again verified against the approved cut, allowing only
those old terminal/ACKed retention timestamps. The strict apply command used
the real frozen full metadata SHA:
`26284777ce531cc2587aa8b5a07a26b26fe960c2f71d5d2e73aca0033213935b`.
It verified that same complete digest after the exact transaction/finalization.
No live DB was inspected with ad hoc SQL or edited; forensic SQL reads were
restricted to immutable supported backup copies.

## Qualified operator and unchanged installed release

- Scoped operator source: `1c9252308ebaa81e4fafc88ae608cce4258d3fe3`.
- Operator SHA-256:
  `15ffeb6da011052b2b6fc909241fdf43422f52c2ea996348397de53dd931a872`.
- Selected PRD release stayed `rr-20261111-bug031-0b7c031`.
- Selected release manifest:
  `627870fe5245493742dbfb676ae9a9d41aaf95589fdc734c7a6390e34dfcddb8`.
- Selected Mac bundle:
  `97e93588e2ea7f82c1145d2c9f66fbfb0ccf3e6d9f777799a696347c6529b10e`.
- Configuration:
  `22f16457147c3d8f0c3e4b2781edc04f4caeca7374d5fcde6d5cb59151d5cd1a`.
- Installed service source stayed
  `0b7c031d7025b79e712bc52e73367a41bdefcf4e`; schema stayed 40.

Four-host source/native qualification and prior Git delivery are recorded in
[the preparation evidence](R168-scoped-teardown-recovery-preparation.md).
This execution changed no source, selector, service bytes, configuration,
mailbox assignment, peer lane or Ubuntu service. No source tests were repeated
for this operational closeout; `git diff --check` validates the documentation.

## Postflight and resumption evidence

The supported aggregate inventory and independent immutable post-apply backup
proved the one-reservation delta: nine to eight, old command slot count one,
zero active sessions/queued commands/running commands/resumable jobs, and no
pending recovery finalization. The complete normalized preservation digest and
every peer marker/workspace matched before supported `prd-start`.

At 20:22–20:23 UTC / **21:22–21:23 Warsaw**, both Mac PRD ingress and executor
returned HTTP 200/ready on the unchanged installed source. Fresh command/job
reads were authoritative and preserved the original failed result. Executor
session status returned LOST with the original runtime generation; mailbox
lifecycle confirmed terminal-unacknowledged. The complete supported detailed
inventory returned schema 40 with eight retained pairs, eight reservations and
one command slot; exact row-by-row comparison proved the eight peers unchanged
and the recovered target absent from retained capacity. No new command or
filesystem-mailbox execution probe was submitted.

Health reported no queued commands or mailbox backlog and zero current-process
storage/cleanup error counters. Thirteen pre-existing uncertain remote intents
remain preserved; they were not replayed. This authority is not globally empty.
The independent inventory diagnostic still says `safe_to_resume=not_established`
because an inventory alone never grants clearance. The combined qualified
recovery, offline proof, unchanged-byte restart and fresh postflight establish
**Runner clearance for this exact R168 hold**. External clients were not resumed
by the maintainer; separate Factory eligibility remains Factory-owned.

The older retained Bash PID 24637, previously observed live, was not listed by
authorized read-only `ps` after the shared stop/restart. That does not date or
attribute its exit. Its durable hold/marker/workspace remain unchanged. The
approved package disclosed shared-stop descriptor effects; no recovery signal
or new disposition was applied to that older peer.

Private evidence directory:
`/private/tmp/rsr-mac-exact-teardown-20261114-url98z29/`.
Key files: `R168-ROUTE-CLEARANCE.json`, `approved-scoped-apply.json`,
`APPROVED-FROZEN-BINDINGS.json`, `APPROVED-OFFLINE-POSTFLIGHT.json`,
`APPROVED-RESTART-READINESS.json`, `APPROVED-POSTRESTART-PRESERVATION.json`,
`approved-postrestart-detailed-inventory.json` and the approved GC-delta proofs.
The original archive manifest SHA is
`8b42ca126585e161eec70b996b7247fa1e3235d70317037c096ce9ece0ad98cd`;
all backups and the target archive remain preserved.
