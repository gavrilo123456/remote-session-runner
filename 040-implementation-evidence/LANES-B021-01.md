# LANES-B021-01 — timeout normalization and dispatch source regression

**State:** PASS — source-only regression and four-machine source validation. This does not close BUG-021's installed deadline gate or authorize a service refresh.

## Preflight and dependency

- Pre-unit Mac dev HEAD was f816fc9e09bf8f84fe0cd7116f0677b4a381ffe7, clean, on 2026-10-16.
- The previous ordered unit, LANES-B024-02, remains **BLOCKED**. Its blocker evidence is [LANES-B024-02.md](LANES-B024-02.md), blob 532d557496b427250093ba8642868c1607207be5 at pre-unit HEAD f816fc9e09bf8f84fe0cd7116f0677b4a381ffe7. Its missing A08 writer/owner and tested pause/fence package is unchanged. No predecessor PASS or closeout SHA is inferred.
- The owner's incremental-readiness decision allows this independent source-only unit to proceed. The timeout correction and hermetic tests use only source fixtures and temporary roots; they do not need provisioned lane volumes, OCI credentials, cgroup maintenance, or a production service update.
- LANES-PREP-00 remains BLOCKED and LANES-L0 remains BLOCKED / NOT STARTED. This unit does not change those states.
- Prior bug applicability: BUG-021 is the affected source path; BUG-022 and BUG-023 are distinct reports and remain in their prescribed later sequence. BUG-024's original held ACK and the four already-consumed fresh route ACKs remain unchanged.

## Read inventory at pre-unit HEAD

The lane idea, detailed design, phased plan, handbook, original PoC design/plan, AGENTS.md, ledger, blocked predecessor, bug reports, acceptance evidence, and relevant implementation/tests were reviewed. Blob IDs are at the stated pre-unit HEAD.

| Input | Blob |
| --- | --- |
| AGENTS.md | 73cd911515483e3b5c43cb0ce4e4ea0ed9bb8ff8 |
| Lane initial idea | a0f67a0747ee49d71bf318784162970fe68f35e3 |
| Lane detailed design | dcfd8acb56fc5154cc207dbf06e2432c87a81442 |
| Lane phased plan | ec333494acf3fc9aa6ebc79499a7630083ea6066 |
| Lane execution handbook | 13d9501a0d8ae14f3c9a6dbf4848b6527ab6c84a |
| Original PoC design document, 010-initial-idea/020-md/runner_remote_execution_detailed_design_v2.md | 5659973dddaf886c9e872b24c2702dc8ae64d34e |
| Original detailed design, 030-detailed-design/010-remote-session-runner-detailed-design.md | 587654e9b71948519cc6a9e8d0c4813405e42e29 |
| Original phased plan, 030-detailed-phased-implementaion-plan/010-remote-session-runner-detailed-phased-implementaion-plan.md | 260bc76b32da215bfc8ad0f1adf05889aa6c244f |
| Lane execution ledger | 260cf135eac044729f0766339352491ec169ac05 |
| Blocked predecessor LANES-B024-02.md | 532d557496b427250093ba8642868c1607207be5 |
| BUG-021 report | bc3dfd1afc38548bd18231774595198a5b568ac1 |
| BUG-022 report | d194da008eb8f843cf97d0e496a696a8257935b3 |
| BUG-023 report | a603119d409a6b30f217a930e88e9b7c7ac667a4 |
| BUG-024 report | 59d95f49ee38f9decc75a21b7f9c1d67ffd960c9 |
| BUG-024 evidence | a36d05d18e35a723ee9f5fa40e82dbb120a63445 |
| BUG-024 acceptance manifest | 20047ffecb1ea0801c0228a24ccfc22c32c8099a |
| src/internal/mailbox/receipt.go | 681f554b52635312628ab1b95652956633e923c1 |
| src/internal/localapi/server.go | 0cf9862cb35850f1bf8a882038ed4e9a8807bbbb |
| src/internal/dispatcher/remote.go | 6d27213d9a559be1176db64e6f4f52b996c924f0 |
| src/internal/sshbridge/forward.go | 01707ec29b8bc6a40695b418cff96cf40fcd8713 |
| src/internal/execution/service.go | fd3ccc4af9114d611209c4defe27c73fd7f54899 |
| src/internal/execution/p025_test.go | 2f45959ae17ff0a4546ccc53f005f796e4f4c8b5 |
| src/internal/runnerd/runtime_lock_test.go | b08a4da8d9087dcbab1ed1c523044990c8e9a48c |

## Source correction

The source change canonicalizes a valid top-level timeout_seconds alias into limits.command_timeout_seconds before durable mailbox receipt hashing, local intent persistence, and remote dispatch. Equivalent alias-only and explicit-limit requests now produce the same intent identity; conflicting positive values are rejected before intent creation. The SSH bridge converts canonical integral JSON numbers such as 5e0 into integer JSON for the runnerd limit contract and rejects fractional, negative, or string values before forwarding.

Regression coverage proves alias/explicit idempotency identity, conflict rejection, stable remote frame identity, integer bridge forwarding, and that the requested deadline reaches execution while the environment policy ceiling still rejects a longer deadline.

The initial source commit S1 was 9e23da3397e88f9db7d4af80ee9e031d0088d72e. A full-suite run then exposed an unrelated test-fixture assumption: under the Mac Runner LaunchAgent's umask 077, a WriteFile(..., 0644) fixture was actually mode 0600, so TestRunnerdLifecycleLockRefusesUnsafePath incorrectly accepted it. This was reproduced directly with umask 077 and the isolated test. S2 c3ebe96367437ce17dc5e6edd166912aa0d0ac2f explicitly chmods that deliberately unsafe fixture to 0644; no production code changed in S2.

## H-tier source validation

All checks below used Go 1.27.1 against S2. Ubuntu tests ran only after the clean dev checkouts had been fast-forwarded to the exact verified mirror SHA.

| Machine | Focused B021 tests | Full make test | make vet | Focused race | Other |
| --- | --- | --- | --- | --- | --- |
| Mac mac-control-plane | Included in final full suite; pass | PASS under umask 077 | PASS under umask 077 | PASS under umask 077 | make build, make smoke, gofmt and git diff --check PASS |
| linux-host | PASS | PASS | PASS | PASS; CGO_ENABLED=1, CC=gcc | Clean dev at S2 before/after |
| sandbox-host | PASS | PASS | PASS | PASS; CGO_ENABLED=1, CC=gcc | Clean dev at S2 before/after |
| factory-sandbox | PASS | PASS | PASS | NOT RUN: CGO_ENABLED=0 | Clean dev at S2 before/after |

Mac commands after S2, using the Makefile's shared caches:

~~~sh
umask 077; make test
umask 077; make vet
umask 077; make build
umask 077; make smoke
umask 077; GOCACHE=/private/tmp/remote-session-runner-gocache GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" test -race ./src/internal/mailbox ./src/internal/localapi ./src/internal/dispatcher ./src/internal/sshbridge ./src/internal/execution -run '^TestBUG021' -count=1
~~~

Each Ubuntu host, through its configured explicit-key SSH connection to /home/ubuntu/projects/remote-session-runner, ran the following against the same S2 SHA after checking dev, clean status, and expected GitHub origin:

~~~sh
/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go version
GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test ./src/internal/mailbox ./src/internal/localapi ./src/internal/dispatcher ./src/internal/sshbridge ./src/internal/execution -run TestBUG021 -count=1
make test
make vet
GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test -race ./src/internal/mailbox ./src/internal/localapi ./src/internal/dispatcher ./src/internal/sshbridge ./src/internal/execution -run TestBUG021 -count=1
~~~

The final command passed on linux-host and sandbox-host. Factory-sandbox reported CGO_ENABLED=0 and did not run the race command. All other commands exited 0 on all three hosts. Host labels and SSH identities were: linux-host via remote-session-runner / ~/.ssh/remote-session-runner; sandbox-host via sandbox.env / ~/.ssh/dev.slidestud.io; factory-sandbox via factory-sandbox / ~/.ssh/factory-sandbox. Connections used IdentitiesOnly=yes, BatchMode=yes, StrictHostKeyChecking=yes, and UpdateHostKeys=no.

## Runner test-route deviation and retained work

A separate test-only mailbox run was attempted on the configured Mac mac-local route at S1 before the umask fixture correction. It was not a BUG-021 deadline probe. The command's focused B021 and focused race tests passed; full make test then failed at the unsafe-mode fixture described above. Its terminal result was complete and untruncated, but the job ended lost and teardown_outcome=lost. The exact request, response/event hashes, unACKed state, and preservation boundary are recorded in [BUG-024 evidence](BUG-024.md#additional-unacked-test-run). No ACK, recovery, service restart, capacity release, or other retained-work mutation was performed. This is why no further test suites were dispatched through the unsuffixed Runner; the explicitly authorized Mac shell and Ubuntu SSH routes completed the required H gates.

This separate lost-teardown item does not change the source-suite results or close any live gate. The historical BUG-024 response and deliberately held ACK are unchanged. The four fresh BUG-024 route requests remain ACKed and consumed as already recorded in BUG-024.md and BUG-024/acceptance.json.

## Source delivery

- S1 9e23da3397e88f9db7d4af80ee9e031d0088d72e was committed on Mac dev, pushed to primary Gitea, and verified at the exact automatic GitHub mirror SHA. The three clean Ubuntu dev checkouts were fast-forwarded to S1.
- Corrective S2 c3ebe96367437ce17dc5e6edd166912aa0d0ac2f was committed on Mac dev, pushed to primary Gitea, and independently verified at the exact github/dev SHA before host validation. linux-host, sandbox-host, and factory-sandbox were each clean on dev at S1 before pull --ff-only, then clean at S2 before and after validation.
- After the evidence-only closeout E, its exact mirror and host synchronization results are recorded in the final handoff. No service or host installation occurred in this source unit.

## Outcome and next gate

**LANES-B021-01 PASS** for timeout normalization/dispatch source regression only. BUG-021 remains NEW until the separately gated installed five-second acceptance passes. [LANES-B021-02.md](LANES-B021-02.md) records the exact blocker and action needed. BUG-022 and BUG-023 remain unstarted in the prescribed sequence; this unit did not probe or resolve them.
