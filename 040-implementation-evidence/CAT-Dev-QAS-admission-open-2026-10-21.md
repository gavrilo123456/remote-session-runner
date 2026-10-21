# Dev/QAS normal admissions opened — 2026-10-21

## Result and scope

**PASS: Dev and QAS are open for normal API use on the existing Mac and all
three Ubuntu workers.** The owner requested this opening independently of the
PRD upgrade. Ordinary execution was verified without maintenance tokens.

Both lanes retain the accepted release `cat-20261021-ebc4446`, source
`ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`, manifest SHA-256
`ede9121d5a41a85944a98d1b585aa99aa36c343244c587dfb540aa57b852c62c`.
No bundle, selector, configuration or schema changed. The two lanes continue
using their separate writable roots and schema-40 databases.

PRD was not stopped, restarted, upgraded or recovered. Its binary and
configuration hashes and service process identities match the opening
baseline on every host. CAT-06 remains blocked for the reasons in
[CAT-06](CAT-06.md); this operation does not satisfy or waive those gates.

No external publishing workflow was operated or resumed. The existing
external-publisher pause remains in effect; the owner may now use Dev/QAS
through their lane-specific routes. Filesystem-mailbox acceptance is
**NOT RUN**: no mailbox request or ACK was published, and retained work and
held ACKs were not altered. Bounded execution checks establish route
functionality, not sustained multi-project load acceptance.

## Preconditions and supported opening

The source checkout started on `dev` at
`42e562e86fc466dede959dd1b1e071899b1112e0`, with no tracked modifications.
The unrelated untracked `src/cmd/catalog-holdcheck/` was preserved and not run.
Existing external, verified `runner-lane` tools were used; no source build,
migration or new candidate was needed.

Before each lane's change, supported `catalog-work-inventory` found active
services on the accepted native bundles, unchanged lane-specific
configuration hashes, and zero session slots, command slots, queued commands
and resumable jobs. All four copies of the lane's completed receipt matched.
The initial Mac Dev/QAS mailbox inbox and ACK directories were empty.

The operational order was Dev, its acceptance and drained postflight, then
QAS, its acceptance and drained postflight. For each lane:

1. On **Mac**, run supported `catalog-cohort --action open` once against the
   completed operation while its service is active, binding the fresh private
   preflight evidence hash.
2. On **Mac**, use `runner-lane stop --host mac-control-plane --lane LANE`
   to stop the selected ingress and executor; then use the same supported
   stop command on **Linux, Sandbox, Factory**, in that order.
3. Preserve the generated released receipt; copy those exact bytes to a
   task-owned temporary directory on each worker. On each **worker**, run
   `catalog-cohort --host HOST --lane LANE --action sync --receipt-file FILE`
   while stopped. No receipt was edited or deleted manually.
4. Verify identical released receipts and absence of any holding receipt.
   Start **Linux, Sandbox, Factory**, then **Mac**, through supported
   `runner-lane start`.
5. Execute the seven ordinary route checks and verify final service health,
   exact identities and drained capacity.

A restart is required because the admission hold is cached when each process
starts; a receipt update alone does not open a running service. No host reboot,
root filesystem change, cache purge or new backup was performed.

| Lane | Operation | Final stage / transitions | Identical receipt SHA-256 on all four hosts |
| --- | --- | --- | --- |
| Dev | `8fdb7e5eb058a795601a2cff9e7bde78` | released / 14 | `cd5e3f06290f806ea66205cc283b1f27704ce9401f73fe31a7cbe60440d1cf9d` |
| QAS | `1349821d4c363aff4b43ed92a61bcc21` | released / 16 | `9172b0f3219e65a2fa3048a4f4010f1cb1f6d896df5b741898bc188fbf68e46e` |

The opening evidence digests were Dev
`41e597eeda7356016a9953c4868674e9918c2b04edc9beec3685e20ccc81b57d`
and QAS
`b68fa1374b15bb7f17080a0fa827158ed12156b0b370b1cc8970aeba7b4adea2`.
Earlier completed/held and superseded histories remain preserved.

## Ordinary route acceptance

The installed lane-specific `current/bin/runner` CLI on **Mac** submitted
one harmless `printf` per route, with unique `lanes-open-SvymA9-…`
idempotency keys and a 20-second client wait bound. No maintenance token was
supplied. Mac-local used endpoint `local`, environment
`mac-workstation-LANE`, target `local`, profile `mac-workstation`.
Direct worker endpoints were `linux-poc-LANE`, `sandbox-poc-LANE` and
`factory-sandbox-poc-LANE`. Queued routes used endpoint `local`,
environment `HOST-LANE`, target `remote` and profile `HOST`.

Every row below passed: expected output, exit 0, complete/non-truncated
output, final event sequence 4 and closed teardown. The queued CLI initially
returned its projection at cursor 3; subsequent read-only `events COMMAND_ID
--after 3` returned `command_succeeded` at sequence 4. No command was
resubmitted. The partial-range event reads correctly do not claim full
history in that individual read.

| Lane | Route | Job ID | Command ID |
| --- | --- | --- | --- |
| Dev | Mac-local | `job-848c8121c384d5734e07bf564dac3f09` | `cmd-8019da979c3c9556f1add8bfe71c1c64` |
| Dev | Linux direct | `job-4782aa848605fa96737c9b0ef1ed4cb3` | `cmd-c68c0a426db21b6f6d15ff669e5d0547` |
| Dev | Sandbox direct | `job-42a69efad3a999e7125385b747634617` | `cmd-d83573a5315bba31202def0a2f28e2b5` |
| Dev | Factory direct | `job-33050aeb4a1d297d3191ddc1eb990999` | `cmd-03c7c31f045e455fcc79d822fe677f54` |
| Dev | Linux queued | `job-e73ba62cf48a76087ed4154f8a2d4cc0` | `cmd-336727b1dc76884ea93712af9bfbee33` |
| Dev | Sandbox queued | `job-544bfb0301a4573d118c527052e59809` | `cmd-9951663f72297bd8302b1cefd8330e52` |
| Dev | Factory queued | `job-521350cfb6d582814f9786d03deeec7f` | `cmd-56f2d92ac3be92b56f6131c3d7c0ce62` |
| QAS | Mac-local | `job-f30bb5207864117ed7da719fbe8f5a5f` | `cmd-5a4376edf81da3cca54a4a3b9752dca3` |
| QAS | Linux direct | `job-08e64164994e3b8d7a8757b7ea575d25` | `cmd-b57b82aeb5733a376b78c0f68defa5d1` |
| QAS | Sandbox direct | `job-cd0a8c1b94048abdf960563ce914dce1` | `cmd-5d4c4b67c1ef39c5eec71f29b13e6276` |
| QAS | Factory direct | `job-d173e41adcedacbddd0b53359507e41b` | `cmd-6727c36e376f82fdcc1aef569388accd` |
| QAS | Linux queued | `job-d9150727b40c8fb066022f28d99bde4c` | `cmd-9b5d64572adb783604403106eed6eed1` |
| QAS | Sandbox queued | `job-0c80d8918cd41a59a6b607094e256b88` | `cmd-d2de8bfd3c5d603952bc1b30eafc59e1` |
| QAS | Factory queued | `job-d5e3c2e334f478796bde4f24bf3c4dff` | `cmd-c3749a1f7daa875bd2caae6d115eac39` |

Diagnostic invocation errors were not counted as acceptance: an initial Dev
direct endpoint used a profile name instead of its endpoint name and failed
locally before admission; a QAS sync invocation included the unsupported
extra `--operation-id` flag and failed argument validation before writing.
Correct supported invocations subsequently passed. An initial read queried
Linux user units; the actual services are system units, whose active process
identities were verified separately. No service-state conclusion relies on
the user-unit output.

## Final postflight and peer preservation

At **2026-10-21 18:55:58–18:56:03 UTC (20:55:58–20:56:03 CEST)**:

- All eight host/lane selections were active, ready, schema 40 and unchanged.
  No admission-holding receipt remained.
- All work inventories had zero active session slots, active command slots,
  queued commands and resumable jobs. Health also reported zero queued intents,
  mailbox backlog, storage errors, cleanup failures and event gaps.
- Both Mac ingress services reported all three remote routers ready.
- Dev process identities stayed unchanged through the QAS operation.
- PRD process identities, binaries and configuration hashes stayed unchanged
  through both lane operations. Existing PRD reservations were not touched.

| Host | Dev PID(s) | QAS PID(s) | Unchanged PRD PID(s) | Available bytes at runtime root |
| --- | --- | --- | --- | ---: |
| Mac | ingress 51010 / executor 51171 | ingress 58633 / executor 58693 | ingress 83103 / executor 83200 | 146,581,721,088 |
| Linux | 3462734 | 3468964 | 3858597 | 16,201,375,744 |
| Sandbox | 74811 | 83658 | 2353257 | 1,314,037,760 |
| Factory | 49649 | 53309 | 1970144 | 35,413,282,816 |

Capacity values are observations, not reserved allocations or a PRD upgrade
preflight. Existing per-host bundle/configuration hashes remain in
[CAT-04](CAT-04.md) and [CAT-05](CAT-05.md); the final audit compared all of
them against fresh pre-opening captures and found no changes.

Private raw evidence is retained at
`/private/tmp/runner-lanes-open.SvymA9/`: owner-only baseline snapshots,
opening evidence, exact released receipts, CLI outputs and final postflight.
`final-evidence.json` SHA-256:
`9a0dd70c499cc1cfce633e49d083d9fbdd738309c943bce6b54c00ca0d554ba9`.
No credentials or private configuration contents are copied into this report.

## Documentation validation and delivery

Only documentation is changed in the source checkout. No source tests were
rerun for these status records; live admission/route verification above is
the operational validation. `git diff --check` is required before delivery.
Delivery follows Mac `dev` → primary Gitea `origin/dev` → exact automatic
GitHub mirror SHA → clean `--ff-only` pulls on all three Ubuntu hosts.
The exact delivery commit and host results are recorded in the private
delivery receipt and the final operator handoff.
