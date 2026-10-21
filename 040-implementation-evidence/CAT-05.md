# CAT-05 — Frozen candidate and live QAS qualification

## Current status — 2026-10-21 18:56 UTC: CAT-05 PASS; QAS open for normal API use

The owner-authorized admission opening is complete. QAS operation
`1349821d4c363aff4b43ed92a61bcc21` is `released` on all four hosts, with 16
transitions and common receipt SHA-256
`9172b0f3219e65a2fa3048a4f4010f1cb1f6d896df5b741898bc188fbf68e46e`.
The Dev-accepted `cat-20261021-ebc4446` release and per-host bundle bytes are
unchanged. All seven ordinary API/queued routes passed without maintenance
tokens. At 18:56 UTC, Dev and QAS were ready on all four hosts, with empty work
inventories and zero error counters. API admissions are open for normal use;
PRD process identities, configuration and binary hashes are unchanged.

The opening sequence, released receipts and postflight evidence are in
[Dev/QAS admission opening](CAT-Dev-QAS-admission-open-2026-10-21.md).
No external publisher control was operated; the existing publisher pause
instruction remains in force. No mailbox request or ACK was published, and
filesystem-mailbox acceptance remains **NOT RUN**. The held snapshots and
handoff below are historical and precede this opening; CAT-06 blockers remain
unchanged.

## Historical acceptance and held closeout — before admission opening

**Status: PASS** for the sealed release `cat-20261021-ebc4446` on QAS,
operation `1349821d4c363aff4b43ed92a61bcc21`. Project publishers remain paused;
the QAS admission hold remains closed. Filesystem-mailbox acceptance is
**NOT RUN**. No mailbox request or ACK was published.

## Candidate identity and delivery

No build or dependency resolution occurred during QAS promotion. The exact
Dev-accepted source and sealed release were retained:

- Source revision: `ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`.
- Release manifest SHA-256: `ede9121d5a41a85944a98d1b585aa99aa36c343244c587dfb540aa57b852c62c`.
- Four-host receipt: stage `complete`, 15 transitions, common SHA-256
  `caaeb634c83d4bd60e2ecfab17bbf9c0ddeb285bfc51d20a9b79fff35de063c4`.
- Completion evidence SHA-256: `eaeaed7c3aaabb6cbbe832d90d21da7cbc0c0726449831e9e6ade27eb6fe0d44`.
- Final postflight record SHA-256: `42bc59dadce43a2ae16b49dcb1e4b904dfb86fbb217c789c005bf33bca2035dc`
  (private host evidence at `/private/tmp/cat-20261021-ebc4446-qas-attempt4/134-final-closeout.json`).

The four QAS selectors resolve to the same release identity and their native
per-host bundle. All databases report schema 40. Configuration hashes remain
lane-specific:

| Host | Bundle SHA-256 | QAS configuration SHA-256 | Schema |
| --- | --- | --- | --- |
| `mac-control-plane` | `942570512c160953b450839d88f7b8a79c6e5a4b59db31fb89e8f94c16fc3475` | `e00766f638ceb1f6102989ac1b2325128fda54daf6cabe63e87fd8beeb74c89a` | 40 |
| `linux-host` | `4e7795aa8d5484d8ed77efd3977b5d5693dba6b3d4ffd56d71cf439485b4766f` | `3084e76eb6068d2d45fe82d4f259c2eec765628fc8cc15af2670743312bbe91b` | 40 |
| `sandbox-host` | `ba07943174dd7d1a7eafa49a3d9a005884ee1e9beeb11f4e10b1b12b90bd9a07` | `f69e5df67057c81588c6e553bec9c9d701495b71e878ae52021f4ad0af39cb41` | 40 |
| `factory-sandbox` | `6f95571437db8d96bebf0d166416a4f129a55c05dce0eb706b9fa32d099f4762` | `9dfcadaebf7bfeb9cd1d7b56fc2517ef3e693970a5f5e006d4c228db0694729f` | 40 |

## QAS runtime acceptance

All seven bounded five-second route checks passed: Mac-local, plus direct-mTLS
and queued-SSH on each worker. Each accepted command produced complete,
non-truncated output, exit 0, terminal event sequence 4, and closed teardown.
Direct requests returned HTTP 202 with readiness HTTP 200; queued SSH returned
the terminal `result` response.

| Host / route | Job ID | Command ID | Result SHA-256 |
| --- | --- | --- | --- |
| Mac-local | `job-3ddf0e89cc977690c05acaaf4708c2e0` | `cmd-9d3923296798bef5dfdf7fdcbf2a5224` | `2dcdcbd3f2bc6a35a133f2f000fe9005f43eaccfc3a51e43e10418fe42d65a7f` |
| Linux direct-mTLS | `job-93e23e332a6e2808a41f9919769ec36f` | `cmd-ee26ee080dbc1c3082a0048d6485b7a5` | `28d7417dc764856c952d52dff845088169a383ea044830e2405eae598f6a4a73` |
| Linux queued-SSH | `job-f68d742c94c06b0f56ec8d84a6356633` | `cmd-7359e380837279514ff3c759a6d928d9` | `4a563ec55fbaece2cc79ef327b2266d315efdc0915a74225676532393a130c79` |
| Sandbox direct-mTLS | `job-21e115073400c727bdbe2241594ac9df` | `cmd-b3144ebd76855ca3efd486655a668f44` | `2ad03482b73d7a2953cc3f0b9e14ffbd4518343554e144b4f85c06d8fb0d0ae7` |
| Sandbox queued-SSH | `job-e7bb2d4f5974fbbeac9d55f249646475` | `cmd-c9b01cea9c9f0667c210a22050035536` | `74af1bb699f2cce82288358c15c1e4581c3901a06f5dd295d48ea14c75537fe3` |
| Factory direct-mTLS | `job-d65487bbca5d17c1c6a430ea4c00ede5` | `cmd-06d90d9f7cd873980b392d153c970cec` | `2e2556c3af47492fb0b228d1baae40ea480641531bb5bab0620e83e8b935f233` |
| Factory queued-SSH | `job-a7a4f9216f1d32b8e93135a25f7466a8` | `cmd-46bd6ca4ccb9436bb630524981505bf5` | `e9ea02d80bd83c5ee20689181ec40e70cb7ce466fc17e7bb0de0a24e7f0a6254` |

After acceptance, all four work inventories had zero active session slots,
command slots, queued commands, and resumable jobs. QAS services were active
after the cohort switch and supported receipt synchronization/restart steps.
Dev stayed active on the same ebc4446 per-host bundle with its separate
configuration and state; the unsuffixed PRD services remained active and were
not switched or restarted.

The QAS admission fence returned readiness HTTP 200 and rejected unscoped work
with HTTP 503 / `maintenance_admission_closed` (recorded as phase `blocked` in
the acceptance captures). Final supported hold checks, after the receipt was
completed and synchronized, returned exactly the expected hold on every host:

```text
host_id=mac-control-plane lane=qas held=true operation_id=1349821d4c363aff4b43ed92a61bcc21 error=false
host_id=linux-host lane=qas held=true operation_id=1349821d4c363aff4b43ed92a61bcc21 error=false
host_id=sandbox-host lane=qas held=true operation_id=1349821d4c363aff4b43ed92a61bcc21 error=false
host_id=factory-sandbox lane=qas held=true operation_id=1349821d4c363aff4b43ed92a61bcc21 error=false
```

The previous QAS selector/configuration and current schema were captured by the
supported preflight before the switch. On every host the prior bundle declared
`read_schema_max=40`, the current data schema was 40, and the configuration
digest remained unchanged. The supported selector compatibility tests at the
delivered ebc4446 revision cover schema, journal, and configuration checks.
No rollback was invoked: the accepted QAS selector and retained history remain
in place. The successful cohort switch exercises the installed upgrade and
restart path; interrupted-switch reconciliation and rollback guards were
covered by the delivered source/fixture gates. A live prior-binary startup was
not run.

## Capacity, attempts, and limits

The latest Sandbox measurement in this closeout was taken at
`2026-10-21T17:12:38Z`: `/dev/sda1` had 1,454,804,992 bytes available (98% used);
`/tmp` shares that filesystem. The owner removed journal files before this
measurement. Runner did not remove or modify journal contents. QAS preflight
had already created and verified its selected-lane backup; Sandbox reported
918,208,512 bytes available after that backup against the QAS operation's
536,870,912-byte required headroom. This does not establish capacity for a
future PRD backup; CAT-06 must measure it again against the actual PRD state
and required reserve.

Two diagnostic requests were not counted as acceptance: a Mac request aimed
at the unsuffixed PRD `local-api.sock` returned HTTP 400 `invalid_request` with
no job identity, and a Mac QAS `locald.sock` request returned HTTP 503 with no
job identity. The correct QAS Mac-local route passed as listed above. No
mailbox requests or ACKs were published. External project publishers remain
paused. No retained work, ACK, database, or shared cache was deleted or
replayed.

## CAT-06 handoff

CAT-05 is complete. CAT-06 may now start under the existing owner
authorization, after fresh PRD work/ownership inventories, publisher/admission
fence confirmation, compatible backups, and per-host capacity checks. Sandbox
space is a tight preflight constraint. Keep QAS and Dev unchanged and held;
PRD remains on its prior unsuffixed installation until all CAT-06 guards pass.
Mailbox-specific acceptance remains **NOT RUN** under the owner's restriction.
