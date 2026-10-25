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
