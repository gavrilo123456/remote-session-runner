# Lane execution ledger

**Planning status:** complete ordered assignment. **Execution status:**
`LANES-PREP-00` is BLOCKED; `LANES-L0` is BLOCKED and NOT STARTED. The owner
authorizes independent source-only implementation and testing to proceed when
its exact inputs are available. `LANES-B024-01` is PASS as an independent
source/evidence unit with the exact blocked-L0 record cited below. Baseline and
live acceptance gates remain ordered and blocked by their own unmet
prerequisites. The owner has since clarified that incremental readiness applies
to successive independent source units: a blocked B021/B024 maintenance
closeout is not a global prerequisite for ordered lane source work. The ordered
L1 sequence is complete through `LANES-L1-13`; L2-06 is complete. L2-07 source
implementation and Mac gates are complete, but its required F acceptance is
BLOCKED by the unprovisioned A01 devices and host-specific cgroup prerequisites.
It has no evidence closeout E and is not accepted. L2-08 Mac source
implementation and Mac gates are recorded, but its native F acceptance is
BLOCKED by the unprovisioned APFS fixture volumes. It has no evidence closeout
E and is not accepted. Further source-only work may proceed only when
independent of these exact blockers, with the relevant blocker evidence cited.
L2-09 source and H gates are complete at `3a31fed3415ab5a3d07728dddf7e621f6bddc16c`,
but F remains BLOCKED by the unprovisioned L2-07 Linux and L2-08 Mac fixtures;
it has no acceptance or evidence closeout E. Successive source-only units may
proceed only after confirming independence from those specific blockers and
satisfying their own predecessor and phase gates.
`LANES-L3-01` Source S `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed`, its seven
H assertions, and full source gates pass on Mac and all three synchronized
Ubuntu checkouts. Gitea and GitHub refs match S and the Ubuntu worktrees are
clean. The unit remains BLOCKED from acceptance because L2-09 native F and its
evidence closeout E are unmet. Under the owner's incremental-readiness
authorization, an independent L3-02 hermetic source unit may proceed using this
delivered S while retaining the blocker; no unit acceptance or live gate is
implied. Following the owner's successive-source-unit decision, L3-02 source
`d82d171cc545cfdbf4e6f611b3f3b66faec074d8` and L3-03 source
`120589cfa6277cc6937ca6910c5e55828d456bc9` also passed their registered H and
full source gates on Mac and all three synchronized Ubuntu checkouts. L3-04
source `90c17965eb647863d0744d1715e05c2c025c2289` and L3-05 source
`e526070b424e37cc8aa8e4a31c9bfca60e564424` also passed their registered H/N
and full source gates on Mac and all three synchronized Ubuntu checkouts. These
ordered source continuations remain BLOCKED by the L3-01/L2-09 native F
predecessor chain; no live acceptance or evidence closeout E is claimed. L3-05
uses L3-04's delivered source S; L3-04's missing acceptance E does not block
independent source-only continuation under the owner's authorization. L3-06
source `8d1b7c876b00274f0284f15f27ebee8939a8d5a3` also passed eight H and one
native N assertion, `make check build`, and focused race on Mac, linux-host, and
sandbox-host; all three Ubuntu checkouts are clean and synchronized at S.
Factory-sandbox passed H/N and full source gates, with focused race NOT RUN
(`CGO_ENABLED=0`, no `cc`). L3-06 remains BLOCKED by the same ordered
L3-05/L3-04/L3-03/L3-02/L3-01/L2-09 acceptance chain and has no live
acceptance or evidence closeout E. Its delivered S is available to the next
independent source unit under the owner's authorization. L3-07 source
`203447fd18cf1fbd9ec53d74c3ff7562925868cc` passed six H and one native N
assertion, `make check build`, and focused race on Mac, linux-host, and
sandbox-host; all three Ubuntu checkouts are clean and synchronized at S.
Factory-sandbox passed H/N and full source gates, with focused race NOT RUN
(`CGO_ENABLED=0`, no `cc`). An initial S1 H run on all Ubuntu hosts exposed a
read-only fixture-cleanup error; the corrected cleanup and one-use resolver
guard were delivered at S and all required gates reran successfully. L3-07
remains BLOCKED by the ordered L3-06/L2-09/L2-07/L2-08 acceptance chain, with
no live build, host installation, or evidence closeout E. Its delivered S is
available to the next independent source unit under the owner's authorization.
L3-08 source `1844fb5abfd4dd5c6c7be731e5ac07fa9148c8ce` passed seven H
assertions and full source gates on Mac and all three synchronized Ubuntu
checkouts; focused race passed on Mac, linux-host, and sandbox-host. The
required storage F remains blocked, so L3-08 has no unit PASS or evidence
closeout E. L3-09 was an independent H+N source unit using L3-08's delivered
source; it did not consume the missing physical cache or backup devices. This
did not advance L3-08 acceptance.
L3-09 source `359629128250ee01585259be2c816e86449e3a4c` passed seven H and one
native N assertion, `make check build`, and focused race on Mac, linux-host,
and sandbox-host; all three Ubuntu checkouts are clean and synchronized at S.
Factory-sandbox passed H/N and full source gates; focused race was NOT RUN
(`CGO_ENABLED=0`, no `cc`). L3-09 remains BLOCKED because L3-08 F and evidence
closeout E are still unmet; no unit PASS, production helper installation, live
capacity measurement, live ingress acceptance, or E is claimed.
L3-10 is now in progress on delivered L3-09 source S. Its exact authority
mandate/replay source slice and six registered H assertions pass on Mac; full
source gates and synchronized Ubuntu validation remain pending. Required F is
not run because its separately approved disposable privileged Mac fixture is
not available. It has no PASS or E; no production signer, approval prompt,
socket, key or service was installed or used.
B021/B024 remain blocked, and the unstarted B022/B023 baseline units remain
deferred.
This ledger does not pass readiness, L0, any deferred bug gate or any live lane.

## Earlier owner priority — 2026-10-16 (historical)

The owner initially assigned only `LANES-B021-02` and the `LANES-B024-02`
teardown work directly needed by that already-open maintenance. The current
retained pair is
`sess-d76592166342428ba1b7b703fc87bb35:cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`.
Keep its failed result, original held ACKs and all historical PASS evidence.
Do not expand this closeout into another standalone baseline bug campaign.

The four unstarted B022/B023 rows below remain **DEFERRED by owner scope**, not
PASS and not prerequisites of `LANES-READY` or L1–L3. BUG-022/023 remain open.
The subsequent owner instruction permits ordered independent lane source work
to proceed while that closeout remains blocked; retain the blocker evidence and
do not introduce another backlog repair as an intermediate unit.

Lane candidate timeout, explicit-limit, managed-session and PRD teardown
acceptance remain mandatory. Correct failures required by the active lane
unit within that unit and its immutable candidate chain. Additional standalone
baseline bug work requires a new owner instruction. Actual host protection,
provisioning, operational approval and dependent live gates are unchanged.

## Authoritative inputs and launch rule

- [Detailed design](../040-improvments/040-dev-qas-prd-three-machine-lanes/010-dev-qas-prd-three-machine-lanes-detailed-design.md)
- [Milestone plan and S/E delivery rules](../040-improvments/040-dev-qas-prd-three-machine-lanes/015-dev-qas-prd-three-machine-lanes-phased-implementation-plan.md)
- [Execution handbook: fixed choices, packet, checkpoints, units and failure paths](../040-improvments/040-dev-qas-prd-three-machine-lanes/020-dev-qas-prd-three-machine-lanes-execution-handbook.md)
- [Implementer-comment resolution evidence](LANES-plan-review.md)

Planning baseline: clean Mac `dev` at
`741ec963d8d8070608de4b96f21c2c6806ba0889`, 2026-10-16. The implementation
preflight records the then-current delivered HEAD and document blob revisions;
this historical planning baseline is not an installed-build assertion.

Continue the assigned in-scope units and declared dependencies below, excluding
the deferred baseline rows from predecessor requirements. PREP has recorded
discoverable facts and concrete correction packages, but remains BLOCKED; the
full L0 baseline cannot start until PREP prerequisites pass. Under the owner's
incremental-readiness decision, an independent hermetic/source-only unit may
be implemented and tested while an unrelated infrastructure prerequisite is
open, if its exact inputs are available. Mark that row IN PROGRESS and record
the source-only scope; do not mark an unmet predecessor, dependent unit, or
later live gate PASS. The handbook already supplies deliverable, proof tier,
host coverage, exit criteria and failure branch; another group-breakdown
document is not a prerequisite.
Per-unit evidence is `040-implementation-evidence/<unit-id>.md`. For ordinary
ordered units, record the predecessor's verified evidence-closeout SHA before
starting. For an explicitly authorized independent source-only unit whose
predecessor is blocked, record the predecessor's exact blocker-evidence path,
blob/commit revision and state, and why the source work is independent; do not
invent a predecessor PASS or closeout SHA. Aggregate L1–L7 summary files are
created when their last unit closes. A unit becomes PASS only after its actual
tests and S/E delivery, never from this assignment table.

## Readiness packet ownership

Tomasz Walczuk is accountable approver and default host/storage/network/provider/
producer coordinator; the implementer performs scoped authorized collection and
execution. Any delegation names the actual authorized operator. No owner is
left for a future milestone to choose. PREP has collected current host, storage,
cgroup, runtime and mailbox observations and prepared A01/A02/A03 proposals;
OCI owner access, Mac administrator route, sandbox capacity correction, Linux
cgroup maintenance and producer pause evidence remain open. L0 must gather its
full current baseline when its gates pass; only the already-open B021/B024 case
remains assigned for baseline verification. `LANES-READY` records the deferred
B022/B023 baseline rows, open dependencies and the verdict for each next
operation rather than requiring future provisioning results before source
work.

The packet must link exact approval checkpoints A01–A10 and real receipts when
issued. Future digests, key pins, UUIDs and short-lived mandates are generated
from verified preceding steps; they are not blank requirements to ask the
planner about or preapproved placeholder values. Missing access/capacity or a
failed gate stops the relevant unit with evidence and its prepared correction.

## Ordered units

| Unit | State | Evidence / delivered source SHA |
| --- | --- | --- |
| `LANES-PREP-00` | BLOCKED | [Readiness discovery and incremental-readiness delivery evidence](LANES-PREP-00.md); latest owner-instruction source S `d797d8519fcee0fbf40f0588f2e7393f6b96f74b`, evidence closeout `437554ccc3d372240b751c323f17bca9611cc0e8`; PREP still failed its sandbox-capacity and infrastructure/access gates |
| `LANES-L0` | BLOCKED — not started | [Gate record](LANES-L0.md); PREP-00 did not pass |
| `LANES-B024-01` | PASS | [Independent source/evidence revalidation](LANES-B024-01.md); Source S `5e5a07f1bc87a4cd52062d6f8eb729dce199e824`; tested code cohort `d90155bd8a9391a14759b420c7a49b48f5a0d0a8`; independent of blocked L0 |
| `LANES-B024-02` | BLOCKED | [A08 producer/control revalidation](LANES-B024-02.md); configured roots and direct writers are mapped, but SlideStud has active submissions and its owner-controlled pause has not been tested or approved |
| `LANES-B021-01` | PASS | [Timeout normalization and dispatch source regression](LANES-B021-01.md); Source S2 `c3ebe96367437ce17dc5e6edd166912aa0d0ac2f`; source-only H PASS, no installed acceptance inferred |
| `LANES-B021-02` | BLOCKED | [Consolidated maintenance package](LANES-B021-02.md); bounded exit-2 teardown recovery is implemented, tested and delivered at source `a2c2352750e7ba65cfdfb6d6e75063e29ea5940d`; the prior 30-minute window expired at 18:39:40 CEST, no new window or publisher-pause proof is established, and the e1912ac candidate package is historical. Exact-pair recovery, refreshed candidates, zero-work checks, bridge refresh, and installed acceptance remain open |
| `LANES-B022-01` | DEFERRED — not started | Owner scope decision above; no baseline repair assigned, no PASS claimed. Positive explicit-limit candidate gates remain mandatory |
| `LANES-B022-02` | DEFERRED — not started | Owner scope decision above; no baseline probe assigned or required before lane source work. No installed acceptance claimed |
| `LANES-B023-01` | DEFERRED — not started | Owner scope decision above; no baseline repair assigned, no PASS claimed. Managed-session candidate contracts remain mandatory |
| `LANES-B023-02` | DEFERRED — not started | Owner scope decision above; no baseline probe assigned or required before lane source work. No installed acceptance claimed |
| `LANES-READY` | NOT RUN | Not produced |
| `LANES-L1-01` | PASS | [Unit evidence](LANES-L1-01.md); H source gates and S delivery verified on Mac/Gitea/GitHub plus all three clean Ubuntu `dev` checkouts at `79221a9714561ebb97cc219de66150fc51191e32`. This source-only unit does not close blocked B021-02 or imply installation/live acceptance |
| `LANES-L1-02` | PASS | [Unit evidence](LANES-L1-02.md); six H assertions and full source gates passed at Source S `fbb866789a01f7dcc442164c51ccf255b95405db`; exact Gitea/GitHub SHA and all three clean Ubuntu `dev` checkouts verified. No installed/runtime acceptance implied |
| `LANES-L1-03` | PASS | [Unit evidence](LANES-L1-03.md); six H and six N assertions plus full source gates passed at Source S `1ae1253e2737d11beda1246a96c8c8f5263d3c53`; exact Gitea/GitHub SHA and all three clean Ubuntu `dev` checkouts verified. Native fixtures are temporary only; no live root or runtime acceptance is claimed |
| `LANES-L1-04` | PASS | [Unit evidence](LANES-L1-04.md); seven H assertions and full source gates passed on Mac and all three clean Ubuntu `dev` checkouts at Source S `1c6d72e7b8f05a8879d1a716e79d00d3bfa946a7`. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race was unavailable (`CGO_ENABLED=0`, no `cc`). No runtime acceptance is claimed |
| `LANES-L1-05` | PASS | [Unit evidence](LANES-L1-05.md); seven H assertions and full source gates passed at Source S `e56520701bb6346261b1c07640ad2b6c9c40c4cc` on Mac and all three clean Ubuntu `dev` checkouts. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race was unavailable (`CGO_ENABLED=0`, no `cc`). No runtime acceptance is claimed |
| `LANES-L1-06` | PASS | [Unit evidence](LANES-L1-06.md); six H assertions and full source gates passed on Mac and all three clean Ubuntu `dev` checkouts at Source S `ec2db7dbe8b9a45d5af486f2587a2954cc343f4b`. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race was unavailable (`CGO_ENABLED=0`, no `cc`). Evidence closeout E is delivered separately. B021/B024 remain blocked; no runtime acceptance is claimed |
| `LANES-L1-07` | PASS | [Unit evidence](LANES-L1-07.md); eight H assertions and full source gates passed on Mac and all three clean Ubuntu `dev` checkouts at Source S `c254d49e07a4972b9ddb5b96e3380bbf191e7e2b`. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race unavailable (`CGO_ENABLED=0`, no `cc`). No registry write or live acceptance is claimed |
| `LANES-L1-08` | PASS | [Unit evidence](LANES-L1-08.md); eight H assertions and full source gates passed on Mac and all three clean Ubuntu `dev` checkouts at Source S `2a991507ad04668cf4180cd54f879ed694a0a236`. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race unavailable (`CGO_ENABLED=0`, no `cc`). No publisher pause or live lane acceptance is claimed |
| `LANES-L1-09` | PASS | [Reversible PRD fence and deferred-pair ledger](LANES-L1-09.md); ordered predecessor L1-08 evidence closeout E `36477d3cd598c2f98543a02fc1d7fd4c1b6cbf44`; corrected Source S2 `342ce30e2d23249404cb1c5c874d0436dc5ceb06` passed Mac and all three Ubuntu H/full/vet/build/smoke gates. Focused race passed on linux-host and sandbox-host; unavailable on factory-sandbox (`CGO_ENABLED=0`, no `cc`). Evidence closeout delivered and synchronized; no live lane acceptance is claimed |
| `LANES-L1-10` | PASS | [Provisional ingress admission contexts](LANES-L1-10.md); Source S `10864883d428c7229740fe467b0b36c8301d2f4e` passed Mac and all three clean synchronized Ubuntu H/full/vet/build/smoke gates. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race unavailable (`CGO_ENABLED=0`, no `cc`). No installed or live lane acceptance is claimed |
| `LANES-L1-11` | PASS | [Mac/local-intent/importer/ACK admission and safe status](LANES-L1-11.md); Source S `4cb60b2545622c0415d424ef2d3bd46cd1280209` passed Mac and all three clean synchronized Ubuntu H/full/vet/build/smoke gates. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race unavailable (`CGO_ENABLED=0`, no `cc`). This docs-only E records verified S results; final E delivery gates the next unit. No service installation or live acceptance is claimed |
| `LANES-L1-12` | PASS | [Unit evidence](LANES-L1-12.md); ordered predecessor L1-11 evidence closeout E `258c2a795e1a3be228c61864893eb0b4dbd6e967`; source S `6eb6b3a195045023449cc32df135ad960b162145` passed H/full/vet/build/smoke on Mac and all three clean synchronized Ubuntu `dev` checkouts. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race unavailable (`CGO_ENABLED=0`, no `cc`). Evidence closeout E is this docs-only commit and is delivered by the required synchronized Git workflow. No PRD maintenance or live acceptance claimed; B021/B024 remain blocked, BUG-022/023 deferred |
| `LANES-L1-13` | PASS | [Unit evidence](LANES-L1-13.md); ordered predecessor L1-12 evidence closeout E `9b27d084afea60ac5bc2311b820f65ed133d7efd`; Source S `9958cb16dd8aca918d106f37dd10445b7f7a62d8` passed full L1 H and L1-03 N coverage plus full/vet/build/smoke on Mac and all three clean synchronized Ubuntu checkouts. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain deferred |
| `LANES-L2-01` | PASS | [Fixed lane plans and management role packages](LANES-L2-01.md); ordered predecessor L1 aggregate evidence closeout E `e52a9501b295777b4a474f494031d06d1097ccc7`; Source S `6bd44e183b2946f0634614eb4433cec9f85efee4` passed six H and native N plus `make check build` on Mac and all three clean synchronized Ubuntu `dev` checkouts. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). Source-only; no installation or live acceptance claimed. B021/B024 remain BLOCKED; BUG-022/023 remain deferred |
| `LANES-L2-02` | PASS | [Dedicated queued SSH listeners and forced wrappers](LANES-L2-02.md); ordered predecessor L2-01 evidence closeout E `88e44f4666bbc1a706ea5f66710e989d3b3d5803`; corrected Source S2 `6d6171d773ef450447198ec3dad7a48fe2c7d7d7` passed eight H and native N plus `make check build` on Mac and all three clean synchronized Ubuntu `dev` checkouts. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). No listener was installed or live cgroup/bridge acceptance claimed; B021/B024 remain BLOCKED and BUG-022/023 remain deferred |
| `LANES-L2-03` | PASS | [Direct and management endpoint/TLS material validation](LANES-L2-03.md); ordered predecessor L2-02 evidence closeout E `47cd2613add7c6a5ea327c40e86ac862cff42854`; Source S `94cca54f375260a646d8fe27d6f1311ec3cccbd6` passed four H and one loopback N assertion plus `make check build` on Mac and all three clean synchronized Ubuntu `dev` checkouts. Focused race passed on Mac, linux-host and sandbox-host; factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). No certificate issuance, listener, firewall/NAT, public reachability, PRD material, or service changes; B021/B024 remain BLOCKED and BUG-022/023 remain deferred |
| `LANES-L2-04` | PASS | [PKI local-key/CSR/package mechanics and issuance interfaces](LANES-L2-04.md); ordered predecessor L2-03 evidence closeout E `ebaa5e747c14e838026b1931544cf3b288877594`; Source S `dcbe658572763c7b870a725193a2e078ae1ca28a` passed five H and one loopback N assertion plus `make check build` on Mac and all three clean synchronized Ubuntu `dev` checkouts. Focused race passed on Mac. No production key/credential issuance, outbox publication, service/listener, firewall/NAT or PRD change; signed durable L3 enforcement remains future work. B021/B024 remain BLOCKED; BUG-022/023 remain deferred |
| `LANES-L2-05` | PASS | [Bounded log capture and stream wiring](LANES-L2-05.md); ordered predecessor L2-04 evidence closeout E `01f74177acc522853ece79caaf8e4cd0c700dd1e`; corrected Source S2 `d55ed372d7d6d138fcacdeaf03d085d48c6a6609` passed seven H and one native N assertion plus `make check build` on Mac and all three clean synchronized Ubuntu `dev` checkouts. B021/B024 remain BLOCKED; no log wrapper was installed and no live acceptance or candidate attestation is claimed. Evidence closeout E gates L2-06 |
| `LANES-L2-06` | PASS | [Capacity scopes and durable byte admission](LANES-L2-06.md); ordered predecessor L2-05 evidence closeout E `c86573e117c41e834874dee1e6d29b5024c1e084`; Source S `5e0f4b912b46708b7e8dbeba9cb483bb2df44850` passed all 16 H assertions and `make check build` on Mac and all three clean synchronized Ubuntu `dev` checkouts. Source-only policy and durable accounting primitives; no issued policy, live producer enforcement, service, or capacity change is claimed. B021/B024 remain BLOCKED; BUG-022/023 remain deferred. Evidence closeout E gates L2-07 |
| `LANES-L2-07` | BLOCKED — Source S `6f0db6d0aa831fc09162bf32ddd3cd45e6627e3e` and `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; required F not run | [Unit evidence](LANES-L2-07.md); no A01 disposable volumes or measured device performance; `linux-host` hybrid-cgroup compatibility/approved reboot and `factory-sandbox` I/O-controller propagation remain unresolved. Delivery and source checks are recorded; no PASS, F acceptance, or evidence closeout E claimed |
| `LANES-L2-08` | BLOCKED — Source S `ae386b1a774d9bceb3aa3d137b611a8abec16bb2` and `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; required native F not run | [Unit evidence](LANES-L2-08.md); Gitea primary push and exact GitHub mirror SHA verified; all three Ubuntu checkouts fast-forwarded from clean predecessor `c7f4e6db3a68ed965fac063958e7e8bffcc33278`, validated at Source S, and clean. No approved disposable APFS lane, shared-cache, and backup fixtures. No PASS, F acceptance, or evidence closeout E claimed |
| `LANES-L2-09` | BLOCKED — Source S `3a31fed3415ab5a3d07728dddf7e621f6bddc16c` and H plus `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; required F not run | [Unit evidence](LANES-L2-09.md); Gitea primary push and exact GitHub mirror SHA verified; each Ubuntu checkout fast-forwarded from clean `ae7da3b44847df10d1dc4ff483b6d741988b0ba4`, validated at S, and clean. F depends on the missing L2-07 Linux devices/controller prerequisites and L2-08 disposable Mac APFS fixtures. No PASS, F acceptance, or evidence closeout E claimed |
| `LANES-L3-01` | BLOCKED — Source S `f87dfa1a5f8f10fe9664f1c6e87c7a7517496bed`; seven H assertions and full source gates PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; Gitea/GitHub exact SHA verified | [Unit evidence](LANES-L3-01.md); L2-09 native F and E remain BLOCKED, so no PASS or unit acceptance is claimed. The owner's incremental-readiness authorization permits independent L3-02 hermetic source work using delivered S while retaining this blocker |
| `LANES-L3-02` | BLOCKED — Source S `d82d171cc545cfdbf4e6f611b3f3b66faec074d8`; eight H assertions, `make check build`, and focused race PASS on Mac; eight H plus `make check build` PASS on all three clean synchronized Ubuntu checkouts | [Unit evidence](LANES-L3-02.md); exact Gitea/GitHub S verified and all Ubuntu checkouts clean at S. L3-01 remains blocked by L2-09 native F, so no ordered L3-02 PASS or evidence closeout E is claimed |
| `LANES-L3-03` | BLOCKED — Source S `120589cfa6277cc6937ca6910c5e55828d456bc9`; nine H assertions, `make check build`, and focused race PASS on Mac; nine H plus `make check build` PASS on all three clean synchronized Ubuntu checkouts | [Unit evidence](LANES-L3-03.md); exact Gitea/GitHub S verified and all Ubuntu checkouts clean at S. L3-02 remains blocked by L3-01/L2-09 native F, so no ordered L3-03 PASS, live acceptance, or evidence closeout E is claimed |
| `LANES-L3-04` | BLOCKED — source S `90c17965eb647863d0744d1715e05c2c025c2289`; H, full source gates, and applicable focused race gates PASS at S on Mac and all three clean synchronized Ubuntu checkouts | [Unit evidence](LANES-L3-04.md); exact Gitea/GitHub SHA and host pulls verified. L3-03/L3-02/L3-01/L2-09 ordered acceptance remains BLOCKED by L2-07/L2-08 native infrastructure fixtures, so no unit PASS, live acceptance, or E is claimed. B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-05` | BLOCKED — source S `e526070b424e37cc8aa8e4a31c9bfca60e564424`; eight H and one native N assertion plus `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-05.md); exact Gitea/GitHub S verified and all host pulls validated. L3-04 remains BLOCKED by the ordered L3-03/L3-02/L3-01/L2-09 acceptance chain, so no PASS, live acceptance, or evidence closeout E is claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-06` | BLOCKED — source S `8d1b7c876b00274f0284f15f27ebee8939a8d5a3`; eight H, one native N, and `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-06.md); exact Gitea/GitHub S verified and all Ubuntu pulls/HEADs verified. L3-05/L3-04/L3-03/L3-02/L3-01/L2-09 acceptance remains BLOCKED; no PASS, live acceptance, endpoint installation, or E claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain deferred |
| `LANES-L3-07` | BLOCKED — source S `203447fd18cf1fbd9ec53d74c3ff7562925868cc`; six H, one native N, and `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-07.md); exact Gitea/GitHub S verified and all Ubuntu pulls/HEADs verified. The first S1 H runs exposed a fixture cleanup error, fixed and rerun at final S. L3-06/L2-09/L2-07/L2-08 acceptance remains BLOCKED; no live builder installation, privilege-transition proof, PASS, or E claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain deferred |
| `LANES-L3-08` | BLOCKED — Source S `1844fb5abfd4dd5c6c7be731e5ac07fa9148c8ce`; seven H assertions and full source gates PASS on Mac and all three clean synchronized Ubuntu checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-08.md); exact Gitea/GitHub S verified and all Ubuntu pulls/HEADs verified. Required F acceptance remains blocked by missing Linux A01/cache-backup devices and Mac APFS/cache-backup fixtures; no unit PASS, live acceptance, or evidence closeout E is claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain deferred |
| `LANES-L3-09` | BLOCKED — Source S `359629128250ee01585259be2c816e86449e3a4c`; seven H, one native N, and full source gates PASS on Mac and all three clean synchronized Ubuntu checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-09.md); exact Gitea/GitHub S verified and all Ubuntu pulls/HEADs verified. Ordered L3-08 F and evidence closeout E remain BLOCKED; no unit PASS, helper installation, live capacity/ingress acceptance, or evidence closeout E. Factory-sandbox focused race NOT RUN (`CGO_ENABLED=0`, no `cc`) |
| `LANES-L3-10` | BLOCKED — Source S `41c582f2463b7f49651f4855e29c0a1638393037`; six H assertions and full source gates PASS on Mac and all three clean synchronized Ubuntu checkouts; focused race PASS on Mac, linux-host and sandbox-host | [Unit evidence](LANES-L3-10.md); exact Gitea/GitHub S verified and all Ubuntu pulls/HEADs verified. Required F acceptance remains NOT RUN, awaiting a separately approved disposable privileged Mac fixture; L3-09/L3-08 ordered acceptance/E remain blocked. Factory-sandbox focused race NOT RUN (`CGO_ENABLED=0`, no `cc`). No unit PASS, production installation or evidence closeout E is claimed |
| `LANES-L3-11` | BLOCKED — Source S `83c74829cdf95ffeca448b77b1e82ff6fc4d8d0f`; five H and one N assertions plus `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host and sandbox-host | [Unit evidence](LANES-L3-11.md); exact Gitea/GitHub S verified and all Ubuntu checkouts clean at S. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`). Ordered L3-10 F remains NOT RUN, so no L3-11 unit PASS or E is claimed. B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-12` | BLOCKED — Source S `1880112ba6ce2a007f4b79ada4b330738400d404`; five H assertions, `make check build`, and focused race passed on Mac, linux-host, and sandbox-host; H/full/vet/build passed on factory-sandbox | [Unit evidence](LANES-L3-12.md); exact Gitea primary/GitHub mirror SHA verified and all three clean Ubuntu checkouts fast-forwarded to S and validated. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). Required F is NOT RUN because protected issuer/installer services are not installed; ordered L3-11/L3-10 F remains blocked. No PASS, live acceptance, or E claimed. B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-13` | BLOCKED — Source S2 `551e9b550ae630fdaed7b6d987df7cf4d76d596c`; six H, one native N, full source gates and smoke PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-13.md); S1 Linux compile failure was corrected by the fail-closed non-Darwin stub and all gates reran at S2. L3-12 F remains NOT RUN, with L3-11/L3-10 ordered acceptance blocked; no L3-13 PASS, live listener, or evidence-closeout E is claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`). B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-14` | IN PROGRESS — Source S `8369163e346bb2c90c0c6fcca16b521b621afcaa`; H, full source gates, smoke, and applicable focused race PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; required F NOT RUN | [Unit evidence](LANES-L3-14.md); exact Gitea primary/GitHub mirror SHA verified and all host HEADs equal S. Ordered L3-13/L3-12 acceptance remains BLOCKED; F depends on blocked L2-07 Linux fixtures/controller prerequisites and L2-08 Mac APFS fixtures. No PASS or evidence-closeout E claimed |
| `LANES-L3-15` | IN PROGRESS — Source S `c9bd10dad99ad9f233a17ecbb1f2db0d5ef391a3`; seven H and one N assertion plus full source/build/smoke gates PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, sandbox-host | [Unit evidence](LANES-L3-15.md); exact Gitea primary/GitHub mirror SHA and all Ubuntu heads verified at S. L3-14 physical F remains NOT RUN on blocked L2-07/L2-08 fixtures, so no ordered PASS, live acceptance, or unit PASS is claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=0`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-16` | IN PROGRESS — Source S `ef3e96a10eab52c0eb4bda691b8c19990d502b20`; eight H assertions and full source/build/smoke gates PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, sandbox-host | [Unit evidence](LANES-L3-16.md); exact Gitea primary/GitHub mirror SHA verified and all Ubuntu heads equal S. Ordered L3-15 F remains NOT RUN behind blocked L3-14/L2-07/L2-08 physical fixtures, so no L3-16 PASS, live update, or PKI acceptance is claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=1`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-17` | IN PROGRESS — Source S `e70f612fc764d2a409b9d6c0d09ea7c16f376583`; four H assertions and full source gates PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-17.md); exact Gitea primary/GitHub mirror SHA verified and all Ubuntu heads equal S. Ordered L3-15/L3-14 physical F remains NOT RUN behind blocked L2-07 Linux fixtures and L2-08 Mac APFS fixtures, so no unit PASS or live bootstrap acceptance is claimed. Factory-sandbox race NOT RUN (`CGO_ENABLED=1`, no `cc`). B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-18` | IN PROGRESS — Source S `29cbdf7c981d02a5a5fbc231027f9ceb6c583b98`; two registered H assertions, every L3-01–18 H gate, and `make check build` PASS on Mac and all three clean synchronized Ubuntu `dev` checkouts; focused race PASS on Mac, linux-host, and sandbox-host | [Unit evidence](LANES-L3-18.md); exact Gitea primary/GitHub mirror SHA verified and all Ubuntu heads equal S. Physical F remains NOT RUN: ordered L3-17/L3-15/L3-14 acceptance is blocked by L2-07 Linux fixtures and L2-08 Mac APFS fixtures, so no fresh L0, live package rehearsal, PASS, or bootstrap acceptance is claimed. Factory-sandbox race NOT RUN (`cc` unavailable). B021/B024 remain BLOCKED; BUG-022/023 remain DEFERRED |
| `LANES-L3-19` | BLOCKED — NOT RUN | Ordered genuine physical acceptance gates and evidence closeouts through L3-18 are not complete; L2-07 Linux F and L2-08 Mac F remain blocked by unprovisioned disposable fixtures and scoped host gates. This source preparation does not start or pass L3-19 |
| `LANES-L3-20` | NOT RUN | Not produced |
| `LANES-L3-21` | NOT RUN | Not produced |
| `LANES-L4-01` | NOT RUN | Not produced |
| `LANES-L4-02` | NOT RUN | Not produced |
| `LANES-L5-01` | NOT RUN | Not produced |
| `LANES-L5-02` | NOT RUN | Not produced |
| `LANES-L6-01` | NOT RUN | Not produced |
| `LANES-L6-02` | NOT RUN | Not produced |
| `LANES-L7-01` | NOT RUN | Not produced |
| `LANES-L7-02` | NOT RUN | Not produced |
| `LANES-L7-03` | NOT RUN | Not produced |
| `LANES-L7-04` | NOT RUN | Not produced |
| `LANES-L7-05` | NOT RUN | Not produced |
| `LANES-L7-06` | NOT RUN | Not produced |
| `LANES-L7-07` | NOT RUN | Not produced |
| `LANES-L7-08` | NOT RUN | Not produced |

Continuous bootstrap and live lane operations retain their internal per-host
checkpoints and grant renewals; do not add S/E pauses inside them. Read the
handbook's exact unit table rather than inferring a task from its ID alone.
No phase is accepted until its aggregate design/plan gate also passes.
Deferred baseline rows are retained for traceability and excluded from rollout
completion requirements; they never count as a successful capability test.
