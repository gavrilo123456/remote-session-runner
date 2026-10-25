# PRD Linux acceptance probe diagnostic — 2026-10-25

## Status

The Linux direct-mTLS acceptance probe for PRD operation
`63471d41af07b7afaf577d6a0989cc03` remains **BLOCKED**. The exact accepted job
has not produced a terminal result. It was not resubmitted, replayed, or
reported as successful.

The selected release is `rr-20261024-43a4e675` with digest
`093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`.
The PRD cohort remains admission-held by this operation. QAS and Dev were not
changed.

## Exact helper observation

At `2026-10-25T18:19:28Z`, the corrected Mac observer performed a status-only
GET for the already accepted Linux job. It did not submit a request or use a
one-time token. The observer exited 1 with zero stdout bytes and this bounded,
sanitized stderr:

```text
accepted probe status read returned HTTP 503 (response_code maintenance_admission_closed)
exit status 1
```

The exact controller result-decoding classification is **`empty stdout`**.
The helper's 503 is the underlying status-read failure; no job response was
available to decode. The response confirms the maintenance admission fence is
closed and does not establish the job's terminal outcome.

The accepted scope was for `linux-host`, lane `prd`, transport `direct-mtls`,
environment `linux-dev`, configuration digest
`c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f`, request
digest `928274f138b9e1ae9a49c779871a9af191b04d4c489a767938e628bd9ac0dc79`,
job `job-7fae7172e801f2e22e7d6fce6014403d`. Its observation scope expired at
`2026-10-25T15:25:13.393746848Z`; only the read-only status path was attempted
after expiry.

## Read-only Linux job inspection

At `2026-10-25T17:54:30.045451588Z`, a temporary Linux/AMD64 diagnostic binary
(`SHA-256 68d21f86ddf4ab3803249ddf509bbe5ac9b88a7b8c8252332aeaf7075fced372`)
used `store.OpenExistingRestartPreflightReadOnly` and a read-only SQLite
transaction on `linux-host`. It did not migrate, checkpoint, or write the
database. The observation found:

- PRD selected `rr-20261024-43a4e675`; schema 40; PRD admission hold matched
  operation `63471d41af07b7afaf577d6a0989cc03`.
- PRD service was active. Work inventory was not empty: one active session,
  one active session reservation, one resumable job, and no active command
  slot, queued command, running command, or pending recovery finalization.
- Exact job `job-7fae7172e801f2e22e7d6fce6014403d` remained
  `creating_session`; session `sess-548ea3986a2b736f9643b2b402188830` remained
  `creating`; the associated command identity is
  `cmd-7dc98e864e7abd3a9c2596c3c4fe9399`, but there is no command row or
  command event. Teardown is `pending`; the session reservation has neither
  cleanup confirmation nor release time.

These records are incomplete execution evidence. They do not prove that the
probe succeeded or that execution never began. No capacity was released.

A fresh read-only observation at `2026-10-25T18:27:44.306061315Z` found the
same job/session states, no command row, event or command slot, and the same
unreleased session reservation. The PRD service remained active and the
operation's admission hold remained in place. The diagnostic binary was
streamed to `/tmp`, run once, and removed; no source files were copied to the
Ubuntu checkout.

## Source correction and validation

The existing `catalog-probe-once` observer correction now resolves one unique
legacy V1/V2 Mac environment policy by target, authenticated controller,
empty source, and the fixed five-second limit. V3 environment naming and
endpoint-controller checks remain strict. The observer reached the configured
Linux endpoint, so legacy policy resolution was not the cause of this 503.

The promotion controller now bounds helper stdout/stderr, reports a specific
decode category and the helper's allowlisted sanitized diagnostic, and retains
the process exit code. Its read-only failure recognizer accepts only known
decode categories, recognized sanitized helper diagnostics, and numeric exit
codes; it does not turn an observation failure into acceptance.

On the Mac with Go 1.27.1 darwin/arm64:

| Check | Result |
|---|---|
| `go test ./src/internal/releaseflow ./src/cmd/catalog-probe-once -count=1` | PASS |
| `go vet ./src/internal/releaseflow ./src/cmd/catalog-probe-once` | PASS |
| `go build -trimpath -buildvcs=false ./src/cmd/runner-release` | PASS |
| `go build -trimpath -buildvcs=false ./src/cmd/catalog-probe-once` | PASS |

These are controller/helper source checks, not live PRD functional acceptance.

## Git delivery and worker checks

The source correction was committed on `dev` as `d3aca0212d667adf6239ba2dad92ece8ea019526`.
The primary Gitea `origin/dev` and automatic GitHub `github/dev` mirror both
resolved to that exact SHA. The three clean Ubuntu `dev` checkouts fast-forwarded
from `12ce5f0bad02f94256fc072a6bf7387300766794` to the same commit before
validation.

| Host | Toolchain/platform | Test | Vet | Native controller/helper build |
|---|---|---|---|---|
| `linux-host` | Go 1.27.1, linux/amd64 | PASS, focused packages | PASS | PASS; SHA-256 `ad76a1c2a350485ba3acad805558ed12f17f6cfe243bd7c2b58abf16ab73f213` / `648359c393d5b01f008d4ea590c888f9b94344fb47da4861d64a4907af4a3667` |
| `sandbox-host` | Go 1.27.1, linux/arm64 | PASS, focused packages | PASS | PASS; SHA-256 `7713734f1bf229fd9eeb78d11f22e1f50b7a23631ea935620e4f0cc7aed2d6f1` / `60a52c5581825dd909fa14e68c614c853dde08f17c37af46bf60032b617de085` |
| `factory-sandbox` | Go 1.27.1, linux/amd64 | PASS, focused packages | PASS | PASS; SHA-256 `d984027d0eb27442f6e1dad8409fe651d554787970293e3b0319c2d99001a12d` / `cc02ebdfd6e3d9ead72625fcacdff2386ea31075866f70973fd617c5899350cf` |

Each test ran `go test ./src/internal/releaseflow ./src/cmd/catalog-probe-once -count=1`;
build outputs were task-owned `/tmp` files removed after their processes exited.
These checks validate the synchronized controller and observer source. No
Runner service bundle was rebuilt or installed by this correction.

## Remaining gate

The four-hour PRD continuation approval expired at `2026-10-25T16:17Z`. The
existing accepted job also leaves a live reservation and non-empty work
inventory. Therefore this evidence does not authorize restarting, recovering,
releasing capacity, opening admissions, or creating another probe. Continue
only with a current approved PRD window and a fresh, supported disposition of
the exact incomplete job. Preserve the operation, accepted scope, database,
history, and current admission hold in the meantime.

## Follow-on authorization and bounded Linux recovery correction

The owner subsequently authorized a new four-hour PRD window beginning with
fresh operational preflight, supported non-success disposition of only the
exact Linux direct-mTLS probe above, one fresh retry after supported recovery,
and completion of this same operation. This later authorization supersedes
the expired-window statement above; the new start/end are recorded only when
the fresh preflight begins. Dev, QAS, and project mailbox routing remain out
of scope.

The last captured execution stage remains `creating_session` / session state
`creating`. The direct ingress log says `outcome=allowed`; no command row, event,
slot, runtime generation, or terminal output exists. The recorded 503 is a
separate expired-capability status-read failure. The old installed code did
not persist a later runtime/OS error, so the underlying cause is **not yet
identified**. No success is inferred, and the old request is not replayed.

The controller-side correction is limited to the exact operation
`63471d41af07b7afaf577d6a0989cc03` and job
`job-7fae7172e801f2e22e7d6fce6014403d`:

- `runner-lane catalog-probe-disposition` requires the selected PRD release,
  exact operation hold, stopped Linux PRD service, matching sealed bundle,
  configuration/schema/work evidence, accepted direct-mTLS scope and absent
  runtime-owner marker. It records the job/session as lost with the explicit
  reason `catalog_probe_owner_abandoned_outcome_unknown`, leaves command
  outcome/output unknown, appends lifecycle history, and keeps the reservation
  held.
- The existing `runnerd recover-stalled --lost-session` path remains the only
  capacity-release step. It must independently prove the runtime boundary,
  no unexpected retained work and successful finalization. It does not read or
  replay the script.
- A fresh capability is allowed only after the exact pair is lost and the
  reservation has both supported cleanup and release timestamps, no command,
  event or slot exists, and a consistent read snapshot proves zero remaining
  work. The original accepted scope is preserved byte-for-byte in a private
  history file; the fresh scope stays in the existing v3 format and receives
  the single `-retry-1` identity.
- Promotion observes the existing capability first. It selects this one-time
  retry only for this exact Linux PRD operation/job after the original scope
  expired; an unexpired accepted scope remains observation-only. It verifies
  the returned helper idempotency key matches the newly prepared capability.

These changes are controller/operator code only; no Runner service bundle,
database schema, or sealed release bytes changed.

Mac Go 1.27.1 darwin/arm64 checks for this correction:

| Check | Result |
|---|---|
| `go test ./src/internal/store ./src/internal/laneinstall ./src/cmd/runner-lane ./src/cmd/catalog-probe-once ./src/internal/releaseflow -count=1` | PASS |
| `go vet ./src/internal/store ./src/internal/laneinstall ./src/cmd/runner-lane ./src/cmd/catalog-probe-once ./src/internal/releaseflow` | PASS |
| Native `runner-lane` build (`-trimpath -buildvcs=false`) | PASS; SHA-256 `0ad30ae58130461f5d915dd3cc314530e021ebaceb6a0909cd142a9d0fbf87f0` |
| Native `catalog-probe-once` build (`-trimpath -buildvcs=false`) | PASS; SHA-256 `65182bc34f8f7fcb17e4b4ec4dc8993db1fc641abbfb51e84809a49556ce45f8` |
| Native `runner-release` build (`-trimpath -buildvcs=false`) | PASS; SHA-256 `364fbc594695933fb94ec95535ef204146ee40842d73c7521d248d2c82ecb185` |

Git delivery and required worker checks for this controller correction are
pending. Live recovery and PRD functional acceptance have not run under the
new authorization.

## Bounded failed-unit recovery correction — source validation

The operator correction now limits the failed-unit/empty-cgroup fallback to
the one explicitly selected Linux PRD `--lost-session` input for this probe.
It rejects any job, pair, teardown, additional session, different config,
account, service root, service unit, or unit owner. Before recovery it binds
the stored job, planned command, session, reservation, request idempotency key,
direct-mTLS controller/ingress, route, fixed harmless script, commandless lost
state, and original lifecycle sequence to the recorded probe identities. It
also rejects unrelated live work and unrelated pending finalizations.

The existing lifecycle lock remains held continuously from stopped-state proof
through recovery, finalization, postflight and database close. The fallback
requires a valid failed-unit snapshot (`runnerd.service`, `ubuntu`, failed
substate, zero MainPID/ControlPID, empty ControlGroup, parsed exit timestamp,
and no pending unit jobs). It checks systemd state and jobs both before and
after each process scan, and repeats the complete guard immediately before the
recovery call. The loaded Linux PRD configuration is fingerprinted and must
remain unchanged.

The bounded `/proc` scan binds each inspected process to PID and kernel start
time, checks cgroup membership including nested descendants, and examines
same-account command lines, resolved cwd/executable paths, descriptors, and
the exact configured service/workspace/database/config paths. SQLite WAL,
SHM, and journal sidecars are included. A missing proc entry is accepted only
after confirming that the original PID incarnation disappeared; a missing
cmdline/cwd/exe/fd while the PID incarnation remains live, permission failure,
PID reuse during inspection, or unresolved process churn fails closed. Only
the exact verified recovery process is exempted; its binary must be outside
the installed PRD service tree. Dev/QAS roots do not match PRD path checks.

The existing empty-generation runtime-marker proof, atomic commandless
recovery transaction, crash finalization, and idempotent retry remain in use.
The correction does not migrate schema 40, alter or rebuild sealed service
release `rr-20261024-43a4e675`, or change installed service bytes.

Mac Go 1.27.1 darwin/arm64 validation for the operator correction:

| Check | Result |
|---|---|
| `go test ./src/internal/runnerd ./src/internal/execution ./src/internal/store -count=1` | PASS |
| `go vet ./src/internal/runnerd ./src/internal/execution ./src/internal/store` | PASS |
| `go build -trimpath -buildvcs=false -o <private-temp> ./src/cmd/runnerd` | PASS; SHA-256 `0d25f0b8b51f523758aa1ec205cfbdf7bed28c7e728d582470afd9ab8eba59f8`; temporary binary removed after exit |
| Focused recovery/process regressions | PASS, including PID reuse/disappearance, missing-but-live process evidence, denied proc reads/links, nested cgroups, peer-lane paths, config drift, pending/concurrent startup, exact probe tuple, and held/finalized retry states |

Earlier fixture defects were corrected: process fixtures now use distinct PIDs,
cwd/fd fixtures do not recreate existing entries, and a missing file on a still
live PID is explicitly rejected. Linux-native tests/build and Git delivery for
this correction are pending. No live recovery has run with this code yet.
