# SIMPLE-05 — QAS promotion and acceptance

**Status: PASS.** The QAS services already held the exact per-host candidate
bytes accepted in Dev. This unit corrected the selected QAS SSH host pins,
reused those installations, completed bounded QAS acceptance, and verified
zero-work postflight. No Runner candidate binary was rebuilt or replaced.

## Source and synchronized validation

The installer correction is source commit
`cfcd23756b579e72fffc0e54d75cc137038d3696` on `dev`. It changes
`src/internal/laneinstall/simple_queued_ssh.go` to write a bare host token for
the default SSH port 22, which OpenSSH uses for its default-port lookup, and
updates the matching unit assertion. The failure was specific to the Mac QAS
`known_hosts` entries: they contained `[IP]:22` but not the bare IP. Strict
OpenSSH lookup failed with “No ED25519 host key is known”; using the already
verified host key through the supported alias reached the forced bridge. Dev
pins already contained the default-port form.

The source change was pushed to primary Gitea `origin/dev`; direct
`ls-remote` reads verified that both Gitea `origin/dev` and automatic GitHub
`github/dev` resolve to the exact `cfcd23756b579e72fffc0e54d75cc137038d3696`
commit. Before validation, all three Ubuntu `dev` checkouts were clean at that
commit. `GOMAXPROCS=2 GOFLAGS=-p=1 make check build` passed on Mac,
`linux-host`, `sandbox-host`, and `factory-sandbox`. The final factory run
returned exit 0 using a task-owned short `TMPDIR` at
`/home/ubuntu/.cache/rsr5`, removed by its exit trap. The earlier factory
attempts exposed the `/tmp` quota and an overlong temporary Unix-socket path;
neither was treated as a source PASS. The final run passed the complete gate.
Mac used the configured Go 1.27.1 toolchain and shared Go caches; Ubuntu used
its explicit Go 1.27.1 toolchain and normal account caches.

## QAS SSH pins

The supported `runner-lane bridge-install --host <host> --lane qas
--local-host-id mac-control-plane` path regenerated only the selected QAS
profile pins after verifying the existing host keys. Backups remain under
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas/backups/simple05-ssh-known-hosts-before-20261018/`.
The old files were preserved before replacement; the new bare-host tokens
were verified with `ssh-keygen -F` and each queued QAS route subsequently
passed a real Runner request.

| QAS profile | Prior file SHA-256 | Corrected file SHA-256 |
| --- | --- | --- |
| `linux-host` | `1488c78f74a34097fbbe2340f34e407869ba5072210be9cf38097912fa0929b5` | `58facc310f7e24943e99966a90966b837e6b7915f7a6ce9079d9e439e60c86d9` |
| `sandbox-host` | `9c5f6e6dcbc96da120843201a362e2021184ac19f351da32d5e63c207b08dfe8` | `9bbe485ecbc7a8ded37af511a82f3ef02bb352935692ffa9090d51f8618c2442` |
| `factory-sandbox` | `f42ec884da1a919751ea185518fa9ed1510f646e9380d2b8f96a99ce3d589684` | `42765b49c0368ac4f817b2b8ebb0420be04a668eb6aa3bc2659aab483f65a6ac` |

No PRD or Dev pin, direct route, firewall rule, Runner configuration or
service was changed. The QAS runner services were not restarted for this
correction.

## Exact QAS candidates

Each current QAS manifest records source revision
`2204e18ecd6eddc916ffa39e75ac905d3dbe2851` and Go 1.27.1. These bundle hashes
match the exact candidates already accepted in Dev; no rebuilt bytes were
promoted.

| Host | Bundle SHA-256 | Manifest SHA-256 | QAS component state |
| --- | --- | --- | --- |
| `mac-control-plane` | `0cbaff313de36ebe030144dd4192a1138aebb4b0ad54ca6fe93d6d0ff196f757` | `88e82159e2719bb510fe306b540464c94270866cb3fc120e696f0360845b617a` | `runner-local` PID `11311` and `runner-locald` PID `11372`, active |
| `linux-host` | `3826c1406efcb9cd240e9dab55df5b09f03035d8537e85036033ad2476d38b4c` | `cbc69ad44795d688326299f00c395c1cbeb1470f83f7e9bc21f28f30dcf33df1` | `runnerd-qas` PID `2892643`, active |
| `sandbox-host` | `6ab39974eb78cf78333a048b683a365b954d95894d785d00ef96681cb8f74917` | `bc550e86fa605da09d0ad3841b7ea9192907748c927693bbad6e09b75b3905d8` | `runnerd-qas` PID `1246561`, active |
| `factory-sandbox` | `d1b2399ed1af742afe6b4ffe01fd5d0e103d1efb3db7e12b41837a4e84cab37a` | `06c691b604742b07fe9c8d22c3eb28f8c81967701c067334f53162002b872c9a` | `runnerd-qas` PID `1352979`, active |

## Bounded live acceptance

Before QAS work, the Mac QAS API/locald and all three direct mTLS worker health
reports were `ready` at `2204e18`, with zero active sessions/commands, queued
commands/intents and mailbox backlog. The direct health reads used the
configured per-profile CA, client identity, server certificate fingerprint
and URI SAN. All probes used new QAS IDs except the read-only inspection of the
already-completed timeout result; the old limit-key conflict was not replayed.

| Path / behavior | Stable evidence | Result |
| --- | --- | --- |
| Direct Linux QAS | `simple05-20261018-direct-linux-01`; job `job-2244316db7c977e396f7c3b0c11fb9d8`; session `sess-0a387b0b36c5d2f5c1cd4250df8aac21`; command `cmd-7a95b1537ee36fc8d1307d0ad64bc576` | Succeeded, exit 0, complete output/events through sequence 4, closed teardown. |
| Direct sandbox QAS | `simple05-20261018-direct-sandbox-01`; job `job-416ef1ccba80af2a674584bf656ec563`; session `sess-082084ee406f619729434efd0ff98ec2`; command `cmd-a89009ff8975ddddb99cc47a3cc5176d` | Succeeded, exit 0, complete output/events through sequence 4, closed teardown. |
| Direct factory QAS | `simple05-20261018-direct-factory-01`; job `job-d03283f8a921fa03b7b3ceac05027e4e`; session `sess-c7b623be0b491c747e80771e0aa3398d`; command `cmd-66d0c8426aafafb6682421b4e746c1dd` | Succeeded, exit 0, complete output/events through sequence 4, closed teardown. |
| Mac-local QAS | `simple05-20261018-mac-local-01`; job `job-a2d6f929f0c0455a8ca9aca7706c5290`; session `sess-cf097ad9923b43b7b5e7734ee230b047`; command `cmd-8e40022959273c7cbad8344f643a809b` | Succeeded, exit 0, complete output/events through sequence 4, closed runtime teardown. |
| Queued Linux QAS through Mac ingress | `simple05-20261018-queued-linux-01`; job `job-7a84fe81c3a2fa075cd40e5b2e7a0bb5`; session `sess-dfd9151e56301f3d10e9cb7e2dbc9ef1`; command `cmd-8a29b7218db39413cbbaa244adcff6a8` | Succeeded, exit 0, complete event history through sequence 4, closed teardown. |
| Queued sandbox QAS through Mac ingress | `simple05-20261018-queued-sandbox-01`; job `job-9d2e7dafe661d92050abbc472521a592`; session `sess-5a555b9f74ab26284ab97623ad89be2a`; command `cmd-4fd197e398c0fa5b8fd8cc63e3efc668` | Succeeded, exit 0, complete event history through sequence 4, closed teardown. |
| Queued factory QAS through Mac ingress | `simple05-20261018-queued-factory-01`; job `job-3383ec76db5ce1ef4b7ad6b38fe3ecfb`; session `sess-4f82389644519efb9b7bd17834d6b503`; command `cmd-5fe55633036dbcaa98117df2c7b31815` | Succeeded, exit 0, complete event history through sequence 4, closed teardown. |
| Five-second target timeout | `simple05-20261018-timeout-linux-01`; job `job-4d96683f6be1501e2d9b2ccfb7e7ecf0`; session `sess-724253fd8307ffb7aedd4a08009e11eb`; command `cmd-271d4e09fafc05047eea48274eb5f25a` | `timed_out`, terminal event `command_timed_out`, sequence 3, complete nontruncated output, closed teardown. A follow-up read with the same key returned the same IDs and one queued/start/timeout history; no replay. |
| Permitted explicit output limit | `simple05-20261018-limit-linux-02`; job `job-31a39c7879f3623a6cf7afd4b9926a3d`; session `sess-c2c34b4315f00891966715c80b4ea5c3`; command `cmd-3eb221694481bfae3501c6d74ae9e4fd` | `limits.output_bytes_per_command=1024` accepted; succeeded, exit 0, complete output, sequence 4, closed teardown. |
| Managed session create/cancel/close | Create key `simple05-20261018-managed-linux-create-01`; session `sess-1d318eaec3b10d4d6613e6bf6920a262`; command key `simple05-20261018-managed-linux-cancel-command-01`; command `cmd-3a17a4ffb70c42fa9c18692e881344dd` | Session ready; bounded `sleep 30` reached running, was explicitly cancelled, terminal `command_cancelled` sequence 3 with complete nontruncated output; graceful close and status read both reported closed. |
| QAS mailbox request/read/ACK | Request/idempotency key `simple05-20261018-mailbox-linux-01`; job `job-7cef0a9eacdea1ca8d492016f4744b5a`; session `sess-faf1add7070dceb333fc2ccedc373498`; command `cmd-1e48fbfae959e15085a3b4bc23d34a1e` | Terminal response revision 2; succeeded exit 0; four events through cursor 4; complete nontruncated output; closed teardown; exact ACK written. The owner-only lifecycle endpoint reports `terminal_acknowledged`, `acknowledged=true`, terminal, and zero actionable pairs. |

The initial `limit-linux-01` request attempt returned HTTP 409 before accepting
work. Read-only inspection showed that key already belonged to a completed
QAS run with no explicit limit (`limits` absent), succeeded with complete
output and closed teardown. That retained result was left untouched; the
explicit-limit acceptance used the fresh `-02` key. The first mailbox publisher
attempt failed before publication because the restricted shell could not open
the QAS mailbox lock. The exact request, response and ACK paths were verified
absent before using the authorized Mac shell route. The one new QAS request and
its exact ACK then passed; unrelated retained files and held ACKs were not
changed. A temporary combined acceptance assertion initially equated
`command_timed_out` with command state `timed_out`; read-only event inspection
confirmed the distinct, correct labels. The corrected check passed.

## Postflight and production isolation

At `2026-10-18T09:07:50Z`–`09:07:51Z`, fresh direct mTLS health reports for
Linux, sandbox and factory QAS were ready at `2204e18`; each had zero active
sessions/commands, queued commands/intents, mailbox backlog, storage errors
and cleanup failures. At `2026-10-18T09:17:37.969261Z` and
`09:17:38.013797Z`, QAS Mac API and locald were ready with zero active work,
queues, mailbox backlog and storage errors. The mailbox lifecycle read
confirmed the new request acknowledged. PRD remains on its original installed
build with the retained session slot and was not changed; Dev was not changed.

The separate PRD investigation is recorded in the S11 addendum to
[SIMPLE-04](SIMPLE-04.md). Its fresh write/readiness checks passed, but the
installed logger still hides the raw SQLite/OS cause of recurring generic
queue-claim warnings. No root cause or corrective PRD mutation is claimed.

## Delivery and cleanup

The source predecessor `cfcd23756b579e72fffc0e54d75cc137038d3696` is already
on primary Gitea, its exact GitHub mirror and all three clean worker checkouts.
This evidence-only closeout follows the same Mac → Gitea → exact GitHub mirror
→ clean fast-forward worker delivery. No temporary acceptance test source was
retained in Git. No QAS service candidate was rebuilt or restarted, and no
production work or held ACK was removed.
