# SIMPLE-03 — selected-lane installation and release rollback

**State: PASS — source, delivery, and worker validation complete.** This
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

## Git delivery and worker validation

Source revision S is
`9f5268b0216a587d7375d534f857c6d33d1e9557`
(`phase(SIMPLE-03): selected-lane installers and rollback`) on Mac branch
`dev`. The source worktree was clean after commit. Mac pushed `origin/dev` to
primary Gitea; `github/dev` resolved to the exact same SHA before any worker
pull. All three Ubuntu checkouts were clean on `dev` at predecessor
`cbeb9b1ea48cbe41563cbcea8372560c3a2a756f`, then fast-forwarded from their
GitHub `origin` with `--ff-only`. Each resulting `HEAD` matched S and each
worktree was clean.

This documentation-only commit is the evidence closeout E. It records the
verified source and worker revisions; its own SHA is intentionally not embedded
in this file.

Delivery commands on the Mac:

```sh
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/local-gitea-key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' push origin dev
git -c core.sshCommand='ssh -i /Users/tomasz.walczuk/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' ls-remote github refs/heads/dev
```

Worker pull command, run on each Ubuntu host after the clean-checkout check:

```sh
git -c core.sshCommand='ssh -i /home/ubuntu/.ssh/gavrilo123456-github -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes' pull --ff-only origin dev
```

The push advanced primary `origin/dev` from
`cbeb9b1ea48cbe41563cbcea8372560c3a2a756f` to S. The mirror query returned
`9f5268b0216a587d7375d534f857c6d33d1e9557 refs/heads/dev`.

The full source gate returned exit 0 on the Mac and all three workers:

```sh
make check
```

On Ubuntu the exact command was run from the synchronized source checkout
through the authorized SSH fallback:

```sh
GOMAXPROCS=1 nice -n 10 ionice -c 3 make check
```

The fallback avoided placing the full compile/test/vet/smoke workload on the
installed unsuffixed PRD Runner and validated the host-local synchronized
checkout directly. SSH used batch mode, explicit identity, strict known-host
checking and `UpdateHostKeys=no`: `remote-session-runner` with
`/Users/tomasz.walczuk/.ssh/remote-session-runner` for `linux-host`,
`sandbox.env` with `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` for
`sandbox-host`, and `factory-sandbox` with
`/Users/tomasz.walczuk/.ssh/factory-sandbox` for `factory-sandbox`. On every
worker the Makefile selected
`/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go`.

| Machine | Revision after validation | `make check` | Available disk before → after (KiB) | PRD postflight |
| --- | --- | --- | ---: | --- |
| Mac `mac-control-plane` | S | PASS | Not measured for this source gate | Existing installed PRD not changed |
| `linux-host` | S, clean | PASS, exit 0 | 20,897,720 → 20,877,716 | `runnerd.service` active; readiness `ready`; no active slots or queued work |
| `sandbox-host` | S, clean | PASS, exit 0 | 2,664,056 → 2,643,620 | `runnerd.service` active; readiness `ready`; the pre-existing one session slot remained; no active commands or queued work |
| `factory-sandbox` | S, clean | PASS, exit 0 | 44,583,152 → 44,563,096 | `runnerd.service` active; readiness `ready`; no active slots or queued work |

The host-local revision and clean-worktree checks passed; available-space and
memory samples were captured before and after; `systemctl is-active
runnerd.service` and GET `/health/ready` over each existing Unix socket
reported active/ready. No source files were copied to Ubuntu outside
Git. No Dev/QAS service, configuration, credential, mailbox, request or ACK
was created or changed. SIMPLE-03 is complete at source and delivery level;
installed-lane acceptance remains in SIMPLE-04.

## Runtime status

Selected-lane installation and live route acceptance remain **NOT RUN**; they
belong to SIMPLE-04. The source implementation does not establish available
disk headroom, live port reachability, required lane credentials, or deployed
health. The install operation checks actual selected-lane state and refuses
uncertain retained work before replacing binaries. PRD was not modified.
Retained requests, ACKs, held ACKs, and unrelated work were not accessed or
changed.
