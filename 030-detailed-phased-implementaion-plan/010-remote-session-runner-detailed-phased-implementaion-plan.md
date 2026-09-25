# Remote Session Runner — detailed phased implementation plan

**Status:** implementation plan. At plan creation, this repository contains designs but no Runner code or executable Runner tests.<br>
**Authority:** the [initial design](../020-initial-design/010-remote-session-runner-initial-design.md) defines PoC scope; the [detailed design](../030-detailed-design/010-remote-session-runner-detailed-design.md) defines contracts, failure behavior, and test IDs. The older remote-only v2 proposal is reference material only where compatible.<br>
**Target:** one restricted-account macOS workstation and one Linux sandbox host; a shared Go execution core with target-specific runtime adapters.<br>
**Size:** 142 serial phases, each budgeted for roughly **60–120 minutes of Luna-class agent hands-on implementation and verification** with its prerequisites already available (about 142–284 hands-on hours total). This is a planning estimate, not a deadline. External host provisioning, approvals, waiting, and physical power testing are excluded. A phase that proves too large must be split before it is treated as complete.

## 1. Mandatory per-phase working protocol

### 1.1 Reload context before **every** phase

Before editing code in `Pnnn`, freshly read from disk **through end-of-file**: (1) this entire current plan, (2) the entire current initial design, and (3) the entire current detailed design. Read in chunks if a tool truncates output. Neither a previous phase's read nor a conversation summary counts. Then read current `README.md`, applicable `AGENTS.md`/repo instructions if present, current Git status and HEAD, the prior phase's committed evidence and diff, and **all current code, tests, schemas, migrations, configuration, and deployment files relevant to this phase**. Search first, then open the chosen files completely; follow newly discovered dependencies. Revisit the sections and test IDs named in the row as a focus, not as a substitute for the full design reads. Use the older v2 source only for a specific compatible detail.

Before coding, create/update `040-implementation-evidence/Pnnn.md` with pre-phase HEAD, design/plan blob revisions, complete file-read inventory, target execution machine, planned test gate, prerequisites, and unresolved assumptions. In `P001`, creating that evidence directory is part of the first edit. If context is compacted, the agent restarts, or a design changes during the phase, repeat the full reads and update the record **before continuing**. Resolve conflicting contracts in the design/plan explicitly; do not invent PoC behavior in code.

### 1.2 Serial PASS, evidence, and commit gate

The only implementation order is `P001 → P002 → ... → P142`. `Pnnn+1` **must not start** until `Pnnn` has: its exact deliverable; its focused automated assertions; the full currently available hermetic suite; its named real-process/host suite on the specified machine; `go vet ./...` once Go exists; race-enabled tests for concurrency changes; `git diff --check`; inspected diff; an evidence record marked `PASS`; **one non-empty, phase-scoped commit**; and a clean post-commit worktree. A skipped or unavailable test is `NOT RUN`, not `PASS`. No parallel coding of future phases is allowed. Read-only review is fine, but it cannot advance a future phase's gate.

Tests should carry detailed-design IDs (`D-01`, `M-03`, etc.) in names or metadata. `make test` runs `go test ./...` without live-host dependencies; add `make test-mac`, `make test-linux`, `make test-twohost`, `make test-soak`, and `make test-powerloss` only when their real fixtures exist. Run focused tests first, then the full currently available hermetic suite. Add/update a prior-schema migration fixture with **every** migration, and rerun the `D-12` import-graph check when packages or imports change. Record exact commands, exit codes, execution machine/OS/runtime, fixture/profile, observed results, limitations, base commit, and next phase in the evidence file. The resulting commit hash is recorded in the **next** phase's preflight, since a commit cannot contain its own hash; `P142` reports its final hash in the post-commit handoff. Commit format: `phase(Pnnn): <one deliverable>`.

If a required test fails, a required host is unavailable, or a phase exceeds a safe 1–2-hour coding slice, stop. Do not mark it complete, skip it, commit failing work as a successful phase, or start the next phase. Split oversized scope into smaller serial phases through a reviewed plan amendment, then renumber downstream rows/ledger before resuming. Required accounts, certificates, keys, runtime profiles, and test hosts are prerequisites, never fake test results. Material design changes are reviewed and committed separately before affected phase work resumes.

### 1.3 What each test tier proves

Hermetic tests prove rules, transactions, and fake-adapter behavior, not Mac account permissions, Linux limit enforcement, SSH/mTLS deployment, two-host reconnect, performance, or physical power-loss durability. A row naming a real host is incomplete until that host-class test passes and its evidence identifies the machine. A software kill or VM hard-off is not a physical host power cut. At `P137`, either pass `P-STORE-01` on disposable representative storage **or** obtain explicit user approval for a software-crash-only durability claim. In the latter case `P-STORE-01` remains `NOT RUN`/unverified; `P138`–`P142` may finish only with that limited claim prominently stated. Later code/config changes affecting acceptance, SQLite/WAL sync, event persistence, or recovery invalidate prior power-loss evidence; rerun `P-STORE-01` on both representative hosts before claiming physical survival, or revert to the explicitly limited claim. No phase may assert an untested physical-durability guarantee.

## 2. Serial implementation phases

Every row inherits §1's fresh-context, 60–120-minute sizing, automated-test, evidence, and commit gate. Focused IDs may be partial fixtures; §3 identifies the first phase where the **full** detailed-design case must pass. No future-phase test is silently waived.

### Stage 0 — contracts and test harness (detailed design §§1–4, 13–14)

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P001` | Add Go module, six `cmd/` stubs, package skeleton, pinned tool versions, `Makefile`, and hermetic fixture helper; no behavior yet. | `go test ./...`, `go vet ./...`, help/version smoke. |
| `P002` | Freeze v1 resource, state, error, and command-event schemas with golden fixtures. | Valid/invalid schema and error-envelope tests; §§3–4. |
| `P003` | Freeze v1 Unix-socket/HTTPS OpenAPI routes, `202` scopes, read/event/job responses, and target rules. | Route and local/direct contract fixture tests; §4. |
| `P004` | Freeze versioned SSH-bridge and mailbox request/response/ACK wire schemas. | Round-trip, required-field, and unsupported-major fixtures; §§4.1, 9. |
| `P005` | Add domain IDs, controller identity, immutable target types, and a static package-import boundary test that grows with the graph. | `D-12` initial graph subset; reject forbidden edges in every package added later. |
| `P006` | Add environment/target/source/limit validation and truthful capability types. | `D-02` pure validation subset; incompatible/unenforceable combinations reject. |
| `P007` | Add pure session/command transition tables and terminal rules. | `D-01` edge-table subset, including immutable target. |
| `P008` | Add versioned canonical JSON hashing and pure idempotency comparison, excluding mailbox `request_id`. | `D-05` semantic-equality/conflict golden hashes. |
| `P009` | Add strict owner-restricted config parsing, explicit environment registry, secret references, defaults, and unknown-security-field rejection. | `D-14` config subset and invalid-field/secret-value tests. |
| `P010` | Add SQLite open/migration scaffolding on both authorities: WAL, FK, timeout, sync, owner modes. | `D-14` migration subset with prior-schema reopen. |

### Stage 1 — shared authority and fake-runtime core (detailed design §§3, 5–6, 11–12, 14)

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P011` | Add `exec_sessions`, lifecycle tables, and target/controller-preserving store methods. | Atomic session-state/lifecycle fixtures. |
| `P012` | Add create acceptance with idempotency and transactional 20-session reservation. | `D-15` create/race subset; no runtime starts pre-commit. |
| `P013` | Add command/script/event tables and exact-byte transaction that creates sequence-1 `command_queued`. | `D-04` command acceptance/reopen subset; corrupt script rejects. |
| `P014` | Add persistent idempotency lookup/conflict on both resource types under concurrency. | `D-05` store subset; duplicate keys return same ID/hash. |
| `P015` | Add contiguous authoritative command ordinals and per-session eligibility. | `D-03` authority-order subset. |
| `P016` | Add atomic event append, replay cursor, state/event transaction, and terminal final-sequence bookkeeping. | Full `D-01`; event sequence/terminal transaction fixtures. |
| `P017` | Add bounded event subscribers with register-before-replay and sequence de-duplication. | `I-03` core replay/live and overflow subset; race suite. |
| `P018` | Add fair scheduler: one command/session, four durable live host slots, cancellation/loss reservations. | `D-17` slot subset; race suite. |
| `P019` | Add fake-runtime create/ready/failure orchestration through one shared `ExecutionService`. | Full `D-02`: invalid target/policy is rejected before authoritative DB acceptance; `D-11` create subset and `I-01` fake create/read. |
| `P020` | Add fake-runtime command start/output/terminal behavior and shell-exit vs transport distinction. | Full `D-04`; `I-02` core subset; fake `I-01` submit/read. |
| `P021` | Add queued/running cancel and close transitions with a single race-safe terminal outcome. | `R-03` fake subset; closed queued work never starts. |
| `P022` | Add fake-clock idle, command timeout, and maximum-lifetime policy. | Full `D-13`, including failed teardown as `lost`. |
| `P023` | Add durable one-off job row, stable IDs, canonical payload, and key conflict. | `I-04` job-acceptance subset; restart after row commit. |
| `P024` | Add one-off create/command phase checkpoints and resume using the shared service. | `I-04` one-off execution/restart subset; no second source. |
| `P025` | Add one-off teardown/result checkpoint, preserving command outcome when teardown fails. | `I-04` teardown subset; stable IDs after each restart barrier. |
| `P026` | Add generation-based startup reconciliation and no-shell-reattachment for fake runtimes. | Full `D-11`; restart/residual-slot subset of `D-17`. |
| `P027` | Add authority metadata/output/idempotency GC and live-slot pins at 30/90-day boundaries. | `D-09` authority subset; no expiry of live-parent records. |
| `P028` | Integrate fake create/cleanup/restart/GC races and residual command slots. | Full `D-15` and `D-17`; race suite. |

### Stage 2 — agent and actual target runtimes (detailed design §§6–7, 10–11, 14)

The restricted Mac account and designated Linux host/profile must exist before their real-host gates. A fake adapter cannot certify either. Use the same persistent-shell contract cases on both targets.

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P029` | Add bounded agent control frames, generation handshake, reserved descriptors, and parser. | Frame fuzz/bounds; printed marker and wrong generation cannot spoof control. |
| `P030` | Add one persistent Bash with private, synced mode-0600 sourced scripts through the agent. | `R-01` real-process precursor: two commands preserve shell state. |
| `P031` | Add command-scoped stdout/stderr pipes, concurrent raw-byte drain, and at-most-16-KiB raw chunks with normal-load flush near 50 ms. | `R-02` pipe-order subset; `O-01` raw-byte precursor; chunk bound/flush tests. |
| `P032` | Add EOF/control completion barrier, uncertain-boundary loss, and no cross-command bytes. | `R-02` delayed reader, `exit`/`exec`/reserved-FD subset. |
| `P033` | Add 100 MiB combined output cap, exactly one truncation event, continued drain, and bounded buffers. | Full `O-02` slow-reader/large-output test. |
| `P034` | Add signal/timeout initiation and confirmed-stop vs `lost` decision. | `R-03` process-interrupt subset; no false cancelled terminal. |
| `P035` | Add descendant inspection/cleanup and retained capacity for uncertain stop/EOF. | `R-03` process/child subset; no replacement shell or extra slot. |
| `P036` | Add macOS process adapter under configured restricted account. | Real Mac UID/allowed-denied OS access subset of `P-MAC-01`. |
| `P037` | Add local `empty`, exact `git_revision`, and explicit `local_worktree` source/provenance. | Real Mac `P-MAC-02`; uncommitted files visible only for chosen worktree. |
| `P038` | Add Linux runtime-profile preflight/doctor on intended rootless Podman host. | Record non-root, mount, socket, CPU/memory/PID/disk/network feasibility. Keep persistent-volume profiles disabled until lifecycle tests exist. If the only remote profile cannot enforce required controls, stop: amend the design/plan for another tested runtime/profile; disabling it alone is not `PASS`. |
| `P039` | Add Linux sandbox prepare/start/inspect/cleanup with session/generation labels and non-root agent. | Real Linux launch/teardown subset of `P-LNX-01`. |
| `P040` | Enforce and measure CPU, memory, and PID limits in Linux sandbox. | Real-host limit tripwires; unsupported controls fail closed. |
| `P041` | Enforce disk/network policy in the Linux sandbox. | Real-host disk/network tripwires; unsupported controls fail closed. |
| `P042` | Enforce approved mounts/no runtime socket and verify labeled teardown. | Full real-host `P-LNX-01`; persistent volumes remain unadvertised. |
| `P043` | Run shared persistent-shell and unsafe-boundary suite on Mac process and Linux sandbox adapters. | Full real-host `R-01` and `R-02`; unsafe shell loses session without replacement. |
| `P044` | Add Linux exact-Git-revision source preparation with host-only read-only credential. | Real Linux `P-LNX-02`; moving branch pinned, no credential/hooks/host writes. |
| `P045` | Wire `runnerd` to shared service/store/runtime via owner-only private Unix API for create/read session. | Remote private session `I-01` subset; real socket modes. |
| `P046` | Add private `runnerd` submit/read/events operations and stable authority idempotency. | Remote private command/event `I-01` subset; exact script survives restart. |
| `P047` | Add private `runnerd` cancel/close/run/get-job operations. | Remote private `I-01`/`I-04` subset; no second execution core. |

### Stage 3 — complete bridge, Mac ingress, and Router (detailed design §§4–5, 8, 10–11, 14)

The real SSH key, pinned host key, forced-command account, and two-host connectivity are prerequisites to the real network gate. The bridge operations are completed before Router dispatch uses them.

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P048` | Add versioned NDJSON bridge framing/`hello`/`ping` and server-side key-to-controller mapping. | Major/additive protocol and forged-controller subset of `D-14`; frame fuzz. |
| `P049` | Forward bridge create/read session through private `runnerd` with no shell logic. | Bridge session request/reply/retry fixtures. |
| `P050` | Forward bridge submit/read command with stable IDs/keys. | Bridge command fixtures; no SSH-exit-as-command-exit. |
| `P051` | Forward bridge run/get-job with stable job IDs/keys. | Remote authoritative one-off/job-read fixtures; never-delivered Mac intent reads belong to Mac ingress. |
| `P052` | Forward bridge cancel/close with controller checks and error mapping. | Private cancel/close bridge round-trip/negative fixtures. |
| `P053` | Forward bridge event replay/follow with base64-encoded output chunks of at most 16 KiB raw bytes and cursor error mapping. | Bridge chunk-bound/stream/reconnect/overflow fixtures. |
| `P054` | Package Linux SSH forced-command restrictions and test server-side escapes. | `D-14` bridge/config/authority-migration subset; real-host forced-command/PTY/forward/controller negatives, `P-NET-01` server subset. |
| `P055` | Add Mac SSH client with pinned host key, bounded frames, stable mutation IDs, and distinct transport errors. | Full real two-host `P-NET-01`; `F-02` fake pre/post-send subset. |
| `P056` | Add Mac `local_intents` migration with full immutable payload/hash and requested lifecycle. | Restart/corrupt-payload subset of `D-16`; Mac receipt is not target acceptance. |
| `P057` | Add Mac local idempotency and intent retry/conflict transaction. | Same key/hash retains ID; changed payload conflicts, including after restart. |
| `P058` | Add unique Mac intent ordinals, claim/renew lease, ordered eligibility, and recovery queries. | `D-14` current-schema subset and `D-03`/`D-06` Mac-store subsets; lease-renewal race test. |
| `P059` | Wire `runner-locald` shared service/agent/store and owner-only `/internal/v1/accept-intent`: reload committed Mac intent by ID, verify immutable hash/ordinal, never accept Router-supplied script bytes. | Local `I-01` subset; private socket, forged-payload, ID/hash, and restart fixtures; no second engine. |
| `P060` | Add locald private read, cancel, close, and event-subscription calls by stable resource ID. | Local private-operation contract, controller/owner and no-arbitrary-payload negatives. |
| `P061` | Run daemon-level Mac account and source-provenance suites through locald. | Full real Mac `P-MAC-01` and `P-MAC-02`; UID/OS access/worktree evidence. |
| `P062` | Add owner-only Mac Unix API create/read session and local-intent acceptance; handlers get no SSH dependencies. | `I-05` early-create subset; `I-06` handler tripwire subset. |
| `P063` | Add Mac Unix API submit/read with immutable target/controller checks. | `I-01` Mac-command subset; direct-created resource denied; no SSH loader/dialer. |
| `P064` | Add Mac Unix API event replay/follow through local-authority store. | `I-03` Mac-local subset; no handler-side SSH connection. |
| `P065` | Add Mac Unix API cancel/close through durable intents/shared service. | `I-06` Unix-handler subset; no handler-side remote connection. |
| `P066` | Add Mac Unix API run/get-job with stable job correlation. | `I-04` Unix one-off subset; handler cannot dial SSH. |
| `P067` | Add Router lease claim and **local** driver dispatch to locald by stable ID/hash. | `D-06` local-race subset; only locald starts local shell. |
| `P068` | Add Router **remote** ordered driver over the complete bridge, distinct Mac intent and target ordinals; block later same-session dispatch until prior terminal output is reconciled. | `D-03` ordered-dispatch subset and full `D-10`: no fallback or invented target. |
| `P069` | Reconcile uncertain send by stable ID/key/hash after before/after-send fault. | Full `D-06`; `F-02` uncertainty subset; no duplicate target start. |
| `P070` | Add 24-hour fake-clock uncertainty deadline and stop retries before key guarantee expires. | `F-02` Router-state subset; deadline retains `indeterminate` for later file response. |
| `P071` | Add early command intents waiting on create readiness/failure, preserving order. | `I-05` early-create/race subset; no pre-ready target accept. |
| `P072` | Add close/cancel-before-delivery and uncertain create/submit race handling. | `D-03` cancelled-predecessor subset and `I-05` Mac-intent subset; proven non-delivery never invents target state. |
| `P073` | Add Mac remote event mirror `(command_id, sequence)` dedup and contiguous cursor transaction. | Full `D-07`, including crash at cursor commit. |
| `P074` | Add irrecoverable gap record, terminal confirmation, and later-command unblock. | Full `D-03` and `D-08`; cancelled predecessor and unknown gap never permit unsafe reorder. Remote-gap subset of `O-03`. |
| `P075` | Add Mac remote session/command projections with `observed_at` and stale status. | Projection read fixtures; projections never become authority. |
| `P076` | Add remote job projection and Mac API mirrored event follow. | Mac mirror `I-03`/`I-04` subsets with durable cursor. |
| `P077` | Route queued one-off jobs with stable job/session/command IDs and target create/command checkpoints. | `I-04` queued-create/command subset; no second script. |
| `P078` | Add queued-job teardown/result resume and proven-never-delivered job read. | `I-04` queued-job outcome subset, including teardown failure. |
| `P079` | Run cross-component restart/corrupt-payload suite across API, Router, job coordinator, and executor. | Full `D-16`; bytes survive every accepted barrier; no empty/different script executes. |

### Stage 4 — file-only mailbox (detailed design §§3–5, 8–9, 11–12, 14)

The mailbox is an adapter over Mac intent/projections, never an execution worker. Use owner-restricted temporary directories in hermetic tests. The file-only client in `P102` uses a real Mac/Linux pair and **never** calls CLI or API.

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P080` | Add marker-last importer with safe filename/path/mode/size/schema and regular-file-only checks. | Full `M-01`; partial/symlink/unmarked input never executes. |
| `P081` | Add durable single-use `request_id` binding, new-ID retry by key/hash, and crash-safe import receipt. | `D-05` mailbox subset; `M-08` retry subset. |
| `P082` | Add SQLite response revisions and immutable terminal-revision snapshot. | `M-04` revision subset; no terminal byte mutation. |
| `P083` | Add synced atomic outbox replacement and import/publish crash recovery. | `M-02` publication subset; reader never sees partial JSON. |
| `P084` | Add per-command NDJSON projector with valid-UTF-8-or-base64 chunks. | Full `O-01`; exact binary byte/order round-trip. |
| `P085` | Add active `get_command` snapshot freeze (`observed_at`, available cursor, file reference). | `M-05` active-snapshot subset; later event never retroactively covered. |
| `P086` | Add terminal completeness/truncation/gap/capture-loss rendering and full-answer triple condition. | `M-09` status subset; no missing bytes invented. |
| `P087` | Add event-file sync, trailing-line repair, and response republish after projector crash. | Full `M-03`; advertised cursor backed by contiguous synced lines. |
| `P088` | Add exact revision+cursor ACK, duplicate ACK, incomplete-output warning, and durable receipt. | `M-06` receipt subset: exact incomplete ACK acknowledges available bytes, not missing bytes. |
| `P089` | Remove imported inbox/ACK pairs after durable recording and garbage-collect unmarked drafts after 24 hours. | Crash-safe pair removal and fake-clock safe-file-only draft cleanup. |
| `P090` | Add ACK/no-ACK response cleanup deadlines and shared event-file references. | Full `M-06`: late ACK does not resurrect; early ACK never deletes another response's data. |
| `P091` | Pin Mac event payloads for terminal-response rebuild, even after normal output expiry. | `M-07` retention/rebuild subset; new reads still report `retention_expired`. |
| `P092` | Add Router retry cutoff at idempotency expiry and test GC with pinned live work. | `D-09` authority/Router subset, full `M-07`, and retention-dependent `M-09` subset. |
| `P093` | Wire file-only `create_session`/`get_session` to Mac intent/projection, with request correlation. | `M-10` session subset; no CLI/API calls by file client. |
| `P094` | Wire file-only `submit_command`/`get_command`, including active frozen response. | Full `M-05` and `I-02`; `M-10` command subset. |
| `P095` | Close mailbox key/retry/expiry matrix on working create/command operations and expose the post-expiry deduplication warning in file responses. | Full `D-05` and `M-08`; reused ID never overwrites response; old key is not advertised as safe to retry. |
| `P096` | Run terminal-revision and complete-answer matrix through active/expired file responses. | Full `M-04` and `M-09`; frozen cursors/terminal bytes do not mutate. |
| `P097` | Wire file-only `cancel_command`/`close_session` with delivery/uncertainty boundary. | `M-10` cancel/close subset; no fabricated authority state. |
| `P098` | Crash/restart importer and outbox around committed create/submit/cancel/close. | Full `M-02`; mutation is not rerun after response rebuild. |
| `P099` | Wire file-only `run` with stable job/session/command IDs and teardown result. | `M-10` run subset; never-delivered job remains an intent outcome. |
| `P100` | Execute seven-operation local/queued mailbox matrix, including direct-created denial. | Full `M-10`; run never executes twice on retry. |
| `P101` | Run before-send/after-commit/stream-loss reconciliation through file responses. | Full `F-02`; `indeterminate` is published, not guessed rejection. |
| `P102` | Run file-only local and queued-remote client on real hosts. | Full two-host `P-MBX-01`: exact ACK, IDs, incomplete-output detection. |
| `P103` | Exercise every Mac Unix/mailbox handler and Router with credential-loader and dialer tripwires. | Full `I-06`; only Router can load credentials/dial remote. |

### Stage 5 — direct HTTPS and common CLI (detailed design §§3–4, 10, 14)

Provision private-network server/client certificates, CA trust, controller mapping, and intended direct client before `P104`. The direct remote API rejects `local`; the CLI selects ingress explicitly, never from an opaque ID.

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P104` | Add private-bound in-process HTTPS listener, mTLS verification, and certificate-principal map. | Real two-host TLS/identity subset of `P-NET-02`; invalid peer fails before mutation. |
| `P105` | Add direct HTTPS create/read session with remote-only target and environment authorization. | Direct `I-01` session subset; local create/controller denial. |
| `P106` | Add direct HTTPS submit/read command using the shared service. | Full `I-05` with direct pre-ready rejection; direct `I-01` command subset and wrong-controller denial. |
| `P107` | Add direct HTTPS cancel/close and error/exit mapping. | Direct `I-01` lifecycle subset; shell failure distinct from transport failure. |
| `P108` | Add direct HTTPS run/get-job using shared one-off coordinator. | Direct `I-04` subset; teardown failure remains visible. |
| `P109` | Run common Unix/HTTPS contract and one-off restart/never-delivered fixtures. | Full `I-01` and `I-04`; no second script or fabricated queued job state. |
| `P110` | Add direct HTTPS event replay, cursor/expiry errors, and bounded stream framing. | `I-03` direct replay subset; no successful partial expired range. |
| `P111` | Add direct HTTPS live follow/overflow and resumable cursor behavior. | Direct `I-03` live subset; no handoff gap. |
| `P112` | Run three-path replay/follow and direct/mailbox gap/capture/expiry matrix. | Full `I-03` and `O-03`; no successful partial output history. |
| `P113` | Add real two-host client server-name checks and controller denial across read/events/mutations. | Full `P-NET-02`, including wrong/expired certs and cross-controller denial. |
| `P114` | Add reusable Unix-socket and HTTPS client library with explicit endpoint, typed resource/error results, and cursor-aware event interface. | Unit contract tests for both transports; IDs never infer ingress. |
| `P115` | Add CLI endpoint profiles, explicit `--endpoint`, create/status, default readiness wait, and `--no-wait` over the shared client library. | CLI create subset; pending ID survives wait timeout. |
| `P116` | Add CLI `exec`/`events`, cursor follow, nonzero/transport distinction, and completeness display. | `P-CLI-01` exec/events subset. |
| `P117` | Add CLI `cancel`/`close`/`run`, retaining explicit endpoint/profile rules. | `P-CLI-01` lifecycle/job subset; teardown failure visible. |
| `P118` | Validate post-90-day key-expiry warnings in direct responses, CLI, and file responses, with Router retry stopped. | Full `D-09`; no client or Router claims deduplication after the guarantee window. |
| `P119` | Exercise direct disconnect/replay during Mac SSH outage and later mirror catch-up. | Full real two-host `P-NET-03`; no rerun or falsely fresh projection. |
| `P120` | Run common CLI smoke on local, queued-remote, and direct-remote routes. | Full real-host `P-CLI-01`; same verbs, explicit endpoint, truthful target/errors. |

### Stage 6 — services, recovery, and qualification (detailed design §§10–17)

Service files are versioned with code. Host, soak, backup, and power tests need actual machines/profiles. These are separate bounded phases, not one unreviewed qualification batch.

| Phase | Bounded deliverable | Focused exit gate |
| --- | --- | --- |
| `P121` | Add macOS `launchd` definitions for Mac ingress/Router/locald and owner-only paths. | Real Mac start/stop/restart and socket/mailbox mode smoke. |
| `P122` | Add Linux `systemd` definitions for runnerd and forced-command deployment notes. | Real Linux start/stop/restart; invalid sandbox profile not ready. |
| `P123` | Add structured authorization/action audit, excluding raw scripts/output/credential values. | Full `I-07` across local, mailbox, direct and denial paths. |
| `P124` | Add component liveness/readiness and `doctor` with degraded Router status. | Full `P-OPS-01`; Mac ingress accepts durable intent during SSH/Linux outage. |
| `P125` | Add bounded operational counters and threshold logging. | Counters for slots, queue, dispatch, gaps, truncation, storage, cleanup, mailbox backlog. |
| `P126` | Implement graceful shutdown on Mac and Linux: stop acceptance/dispatch, bounded drain, cancel remainder through normal transitions, flush events/audit, and close streams with resumable cursors. | Real-process signal/shutdown tests on both hosts; no new work after drain begins and no false terminal result. |
| `P127` | Add named phase-barrier kill/restart harness, captured DB snapshots, and result assertions. | Harness self-tests for deterministic Mac API/Router/executor/agent/Bash barriers. |
| `P128` | Exercise Mac API and Router kill/restart with queued, accepted, and uncertain intents. | `F-01` ingress/Router subset; no duplicate execution or false rejection. |
| `P129` | Exercise locald/runnerd/agent/Bash death with known surviving child. | `F-01` executor/shell subset; no reattachment or replacement shell. |
| `P130` | Exercise four occupied slots and delayed stop/EOF on both hosts. | `R-03`/`F-01` capacity subset; fifth start waits, residual capacity retained. |
| `P131` | Exercise unattributed orphan/profile block and rerun pending/uncertain Mac races with residual slots. | Full real-host `R-03` and `F-01`; no unsafe new work or replacement shell. |
| `P132` | Inject full/locked SQLite and failed runtime cleanup on each authority. | Full `F-03`; no pre-commit execution, conservative post-start state. |
| `P133` | Add Mac WAL-aware online backup/restore rehearsal with service stop and generation bump. | `F-04` Mac subset; main-DB-only copy forbidden. |
| `P134` | Add Linux backup/restore rehearsal and Mac/Linux ID reconciliation before dispatch. | Full `F-04` on both authorities; old shells never reattach. |
| `P135` | Run reference-host 20-session/four-slot and bounded slow-subscriber soak. | `F-05` quota/memory subset with measured environment. |
| `P136` | Measure persisted-output visibility and long-load stability on reference hosts. | Full `F-05`; predeclare normal-load workload/statistic, meet the 500 ms visibility target, and report measurements/misses. An unmet target blocks `PASS`. |
| `P137` | On each authority's representative disposable storage run abrupt physical power cuts, or document explicit user-approved software-crash-only limitation. | `P-STORE-01` **only** if physical test passes; otherwise physical claim unverified/`NOT RUN`. |
| `P138` | Rerun full hermetic, prior-schema migration, race, parser-fuzz/property, and static-boundary suites; reconcile every §14 ID to evidence. | Full `D-12` on the complete package graph and full `D-14` on the final schema/protocol/config; no mandatory hermetic failure/skip; publish machine-labeled coverage matrix. |
| `P139` | Rerun actual Mac account/local runtime and mailbox service suites after final code changes. | `P-MAC-*`, Mac `R-*`, local API/mailbox operational gates current. |
| `P140` | Rerun actual Linux sandbox, exact-Git, service, and limit suites after final changes. | `P-LNX-*`, Linux `R-*`, rootless/teardown operational gates current. |
| `P141` | Rerun two-host SSH, mTLS, reconnect, file-only mailbox, and CLI suites. | `P-NET-*`, `P-MBX-01`, `P-CLI-01` current on named hosts. |
| `P142` | Audit all phase evidence and detailed-design §15; produce controlled-PoC handoff. | Every mandatory ID has correct-machine evidence; clean commit/worktree; approved power limitation stated. |

## 3. Full test ownership and coverage ledger

The full-owner phase must pass the **entire** detailed-design §14 case, including all named adapters, crashes, and host conditions; earlier rows may prove only a subset. The owner records exact command, execution machine, result, and evidence path. Full hermetic and actual-host suites are rerun across `P138`–`P141`. A newly added package also reruns `D-12` before its phase commits.

| Detailed-design ID | First full owner | Detailed-design ID | First full owner |
| --- | --- | --- | --- |
| `D-01` | `P016` | `D-02` | `P019` |
| `D-03` | `P074` | `D-04` | `P020` |
| `D-05` | `P095` | `D-06` | `P069` |
| `D-07` | `P073` | `D-08` | `P074` |
| `D-09` | `P118` | `D-10` | `P068` |
| `D-11` | `P026` | `D-12` | `P138`, rerun each package phase |
| `D-13` | `P022` | `D-14` | `P138`, extend each migration phase |
| `D-15` | `P028` | `D-16` | `P079` |
| `D-17` | `P028` | `I-01` | `P109` |
| `I-02` | `P094` | `I-03` | `P112` |
| `I-04` | `P109` | `I-05` | `P106` |
| `I-06` | `P103` | `I-07` | `P123` |
| `M-01` | `P080` | `M-02` | `P098` |
| `M-03` | `P087` | `M-04` | `P096` |
| `M-05` | `P094` | `M-06` | `P090` |
| `M-07` | `P092` | `M-08` | `P095` |
| `M-09` | `P096` | `M-10` | `P100` |
| `O-01` | `P084` | `O-02` | `P033` |
| `O-03` | `P112` | `R-01` | `P043` |
| `R-02` | `P043` | `R-03` | `P131` |
| `P-MAC-01` | `P061` | `P-MAC-02` | `P061` |
| `P-LNX-01` | `P042` | `P-LNX-02` | `P044` |
| `P-NET-01` | `P055` | `P-NET-02` | `P113` |
| `P-NET-03` | `P119` | `P-CLI-01` | `P120` |
| `P-MBX-01` | `P102` | `P-STORE-01` | `P137` only for physical claim |
| `F-01` | `P131` | `F-02` | `P101` |
| `F-03` | `P132` | `F-04` | `P134` |
| `F-05` | `P136` | `P-OPS-01` | `P124` |

This ledger is a checklist, not a claim that tests exist or passed now. At `P138`–`P142`, reconcile it with detailed-design §15 and initial-design acceptance criteria. Parser fuzz/property tests cover JSON/NDJSON, IDs, paths, base64, bounds, cursors, and hashes in their corresponding phases. Race tests cover subscribers, scheduler, leases, mailbox replacement, and reconciliation. No host-dependent result is green from a fake or a skipped test. Under an approved limited-durability path, `P-STORE-01` stays `NOT RUN` and outside the claim; it is never shown as a passing test.

## 4. Completion and handoff

The implementation is ready for a controlled PoC demonstration only after `P001`–`P142` individually pass and are committed in order, the worktree is clean, detailed-design §15 has current machine-labeled evidence, and every mandatory actual-host gate passes. If `P137` took the approved limited path, the handoff must say **physical power-loss survival is unverified**. Elapsed time, a failed or skipped gate, a missing host, or an incomplete commit never starts the next phase.
