# SIMPLE-03 — selected-lane installation and release rollback

**State: IN PROGRESS pending source delivery and worker validation.** This
source unit follows the delivered SIMPLE-02 closeout
`cbeb9b1ea48cbe41563cbcea8372560c3a2a756f`. The unit adds source-side
installation and rollback controls only. No Dev or QAS service, configuration,
binary, mailbox, database, or key has been installed or changed. Existing
unsuffixed PRD remains running on its installed version.

## Active-profile implementation

- `runner-lane` exposes fixed-host/fixed-lane plan, build, stage, install,
  status, start, stop, rollback, and existing-port-22 bridge-artifact commands.
  Its plan builder accepts only `mac-control-plane`, `linux-host`,
  `sandbox-host`, and `factory-sandbox` with `dev` or `qas`; it has no PRD or
  arbitrary service target.
- Per-host candidate bundles pin Go 1.27.1, source revision, platform,
  component names, component SHA-256 digests, and an immutable bundle digest.
  The Mac builds the Linux amd64/arm64 and macOS arm64 candidates out of tree;
  generated bundles are staged outside the source checkout and transferred as
  build artifacts, never as changed source files.
- Installation writes only the selected lane. Existing updates require a
  fresh stopped/empty authority proof and a verified configuration plus
  SQLite online backup. Rollback checks the backup manifest, host/lane,
  ownership, file modes, digests, and database schema, then switches only the
  previous immutable binary bundle; it never restores or rewrites later
  configuration or request state.
- The Mac LaunchAgents and Linux systemd unit reference the selected lane's
  immutable `current/bin` path and logs. Linux unit installation reloads and
  enables only `runnerd-dev.service` or `runnerd-qas.service`; it does not start
  the service. Dev/QAS task capture uses a 16 MiB active file, four 16 MiB
  rotations, and a 16 MiB rotation temporary file. Unsuffixed PRD keeps its
  existing 64 MiB/seven-rotation default.
- The queued bridge installer writes the lane wrapper/controller map and one
  restricted port-22 authorized-key entry, preserving and backing up existing
  entries. Mac known-hosts files are lane/profile scoped. It creates no
  alternate sshd, listener, root release authority, cgroup or service restart.

## Source checks

Toolchain: Go 1.27.1 at
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go`.
Mac caches reused per `AGENTS.md` at
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`.

| Command | Result |
| --- | --- |
| `go test ./src/internal/laneinstall ./src/internal/logcapture ./src/cmd/runner-lane -run '^(TestSimple|TestLanesL205|TestCaptureCLI|TestRun|TestBundle)' -count=1` | PASS |
| `make check` | PASS: `go test ./...`, `go vet ./...`, and build/help/version smoke for every command including `runner-lane` |
| `git diff --check` | PASS |

The full test run emitted only macOS linker warnings about duplicate `-lproc`
libraries for existing test binaries; all test packages, vet and smoke checks
completed successfully.

## Git delivery

Source commit S: pending. Primary Gitea push, exact GitHub mirror SHA, clean
fast-forward pulls and validation on `linux-host`, `sandbox-host`, and
`factory-sandbox`: pending. Evidence-only closeout E will record the final
revisions and worker results.

## Runtime status

Selected-lane installation and live route acceptance remain **NOT RUN**; they
belong to SIMPLE-04. The source implementation does not establish available
disk headroom, live port reachability, required lane credentials, or deployed
health. The install operation checks actual selected-lane state and refuses
uncertain retained work before replacing binaries. PRD was not modified.
Retained requests, ACKs, held ACKs, and unrelated work were not accessed or
changed.
