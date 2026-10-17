# LANES-L2-05 — bounded log capture and stream wiring

**State:** PASS — source acceptance only. No installed lane service,
LaunchAgent, systemd unit, production log, candidate installation, Runner
request, ACK, credential, firewall rule, or host configuration was changed.

## Predecessor and scope

- Ordered predecessor: L2-04 evidence closeout E
  `01f74177acc522853ece79caaf8e4cd0c700dd1e`, clean and synchronized on Mac
  `dev`, primary Gitea, exact GitHub mirror, and all three Ubuntu `dev`
  checkouts before this source unit.
- Initial source commit `e82e799ad43249e629bca1aaa22c3b03af24625f` passed the
  first all-host validation. A final rotation review then found that an
  inconsistent stale temporary file could cause deletion of the oldest
  archive before refusal. Corrected Source S2 is
  `d55ed372d7d6d138fcacdeaf03d085d48c6a6609`; it is the accepted revision and
  the predecessor for evidence closeout.
- `LANES-B021-02` and `LANES-B024-02` remain **BLOCKED**. Their evidence was
  preserved at the predecessor and S (blob IDs `3e9700485f48ad75ad42ba04334d5b3871a36c7a`
  and `15211861260284d0923c9d64259bd7746700399e`). The B021 maintenance
  closeout, publisher pause, final operational approval, and installed probe
  are not prerequisites for this independent source unit and were not
  attempted. BUG-022/023 remain deferred. The existing maintenance package
  remains a snapshot; it was not rebuilt.
- Tests use temporary directories and child scripts only. They read no
  production credential or mailbox state. The tests did not publish requests
  or ACKs and did not run installed services.

## Read inputs at predecessor E

| Input | Blob |
| --- | --- |
| `AGENTS.md` | `3402877131d9fcea513f5bae756582e4127ee892` |
| Lane detailed design | `f7404c2d5a0b35706f3a9e867991bffcb6171d93` |
| Lane phased plan | `082e73ac62a7815c3ed15778eb8162d81a43c347` |
| Lane execution handbook | `d04a659df5c5834725ea05b9b047f4ad296b21bc` |
| Execution ledger | `af356463d084a14d2253ce4526ebc2d1e94739f5` |
| L2-04 closeout | `f834b059847e080406af547c11217dfcbc7d132a` |
| Lane coverage manifest | `5be206d762b6efc3f5762dc78e732645a020078a` |
| Preserved B021-02 evidence | `3e9700485f48ad75ad42ba04334d5b3871a36c7a` |
| Preserved B024-02 evidence | `15211861260284d0923c9d64259bd7746700399e` |

## Implementation

Added `src/internal/logcapture/logcapture.go` and the
`src/cmd/runner-log-capture` entry point. Added the binary to the Makefile's
explicit command smoke set (`make build` covers all `src/cmd/...` packages),
and wired the fixed Dev/QAS LaunchAgent and systemd templates to capture both
child streams in one lane-local log file.
Both direct launchd streams and systemd stdout/stderr remain directed to
`/dev/null` / `null`. The fixed Linux entrypoint checks the wrapper path and
mode before execution. PRD templates and installed V2 PRD logging were not
changed.

- Production limits are 64 MiB active, seven 64 MiB rotations, and one 64 MiB
  rotation temporary, for a validated maximum budget of 576 MiB. Overflowing
  limit arithmetic fails closed.
- Rotation renames the active file to one `.rotate.tmp`, syncs the directory,
  then atomically publishes the numbered archive. Startup recovers one
  consistent temporary rotation; ambiguous active-plus-temporary state,
  symlinks, wrong file modes, over-limit files, invalid names, and corrupt
  counter state are rejected. An error after the active-to-temporary rename
  stops the wrapper so it cannot create a second active log beside uncertain
  rotation state. Before evicting the oldest archive, rotation now confirms
  that no temporary file already exists. The regression verifies that
  refusing such an inconsistent state preserves every archive.
- Active, rotated, temporary, and counter files require mode `0600`. The
  two-slot CRC counter is synced after every dropped-byte update and is exposed
  as JSON `{"dropped_log_bytes":N}` by `--status`. Ordinary storage/write
  failures count bytes that could not be retained and allow the child to
  continue; unsafe state or counter persistence failures stop the wrapper.
- The wrapper preserves the child exit code, fails wrapper startup with exit
  78, captures stdout and stderr through the same bounded writer, and limits
  waiting for inherited output descriptors after child exit.
- Log and status file opens reject a final-component symlink (`O_NOFOLLOW` on
  the supported Darwin and Linux hosts), and opened active logs are checked
  again for regular-file type, exact mode, and the expected size.
- Seven H assertions cover flood/rotation, durable drop accounting, crash
  recovery, inconsistent-state refusal and archive retention, failed
  start/child exit and streams, the 576 MiB arithmetic, and fixed template
  wiring. One N assertion runs a real local shell child on each selected host,
  checks both streams and mode `0600`, and uses no network connection. The
  coverage manifest requires H+N for L2-05.

The Makefile now includes the wrapper in the source build/smoke command set;
this unit does not claim a signed release candidate or installed candidate
attestation. The canonical build/candidate matrix and sealed attestation work
remain in their later L3 units.

## Source gates on Mac

Commands ran on `mac-control-plane` (Darwin arm64), with Go 1.27.1 and the
Makefile's shared caches:
`GOCACHE=/private/tmp/remote-session-runner-gocache` and
`GOMODCACHE=/private/tmp/remote-session-runner-gomodcache`.

| Command | Result |
| --- | --- |
| `make test-lanes UNIT=LANES-L2-05 LANES_TIER=H` | PASS — all 7 registered assertions at S2 |
| `LANES_NATIVE_HOST_ID=mac-control-plane make test-lanes UNIT=LANES-L2-05 LANES_TIER=N` | PASS — 1 native assertion |
| `make check build` | PASS — full `go test ./...`, `go vet ./...`, command smoke, and command build |
| `gofmt`, `python3 -m json.tool 040-implementation-evidence/LANES-test-coverage.json`, `git diff --check` | PASS |

The first H run found a test-only mismatch: the LaunchAgent XML assertion
expected a trailing space after `--log-path`; it now checks the actual separate
`ProgramArguments` element. The first full-suite run then found a stale
dispatcher negative test that expected L2-05 to have no assertions; it now
uses still-unregistered L2-06. Both failures were corrected and the final H,
N, and complete source gates passed. After the initial all-host pass at
`e82e799`, final review found the archive-eviction edge case described above;
the new retention test and final seven-H/one-N gates passed at S2 on Mac and
all three Ubuntu hosts. The full Go suite emitted existing
duplicate `-lproc` linker warnings on Mac; they were warnings only.

## Source S delivery and host validation

Corrected Source S2 was committed on Mac `dev` as
`d55ed372d7d6d138fcacdeaf03d085d48c6a6609`. Primary Gitea `origin/dev` and the
automatic GitHub mirror `github/dev` each resolved by direct `ls-remote` to
that exact SHA. The Mac `dev` worktree is clean at S2. The three Ubuntu
checkouts were clean at `e82e799` before the correction pull; each
fast-forwarded from its GitHub mirror and was clean at exact S2 before the
final validation below.

The remote test commands on each host were:

```sh
make test-lanes UNIT=LANES-L2-05 LANES_TIER=H
LANES_NATIVE_HOST_ID=<canonical-host-id> make test-lanes UNIT=LANES-L2-05 LANES_TIER=N
make check build
```

The explicit Mac SSH identities used for execution were
`/Users/tomasz.walczuk/.ssh/remote-session-runner` (SSH alias
`remote-session-runner`), `/Users/tomasz.walczuk/.ssh/dev.slidestud.io` (alias
`sandbox.env`), and `/Users/tomasz.walczuk/.ssh/factory-sandbox` (alias
`factory-sandbox`), all with batch mode and strict host-key checking. Each
Ubuntu pull used its configured GitHub identity
`/home/ubuntu/.ssh/gavrilo123456-github` with `git pull --ff-only origin dev`.
Ubuntu reused its existing Go caches; no cache was cleaned or copied.

| Machine | Native identity / platform | H | N | `make check build` | Final checkout |
| --- | --- | --- | --- | --- | --- |
| Mac `mac-control-plane` | Darwin arm64; Go 1.27.1 | PASS (7) | PASS (1) | PASS | clean `dev`, S2 |
| `linux-host` (`oracle-yuta-konopka-ubuntu-micro-02`) | Linux amd64; Go 1.27.1 | PASS (7) | PASS (1) | PASS | clean `dev`, S2 |
| `sandbox-host` (`oracle-gustaw-janecki-ubuntu-flex-02`) | Linux arm64; Go 1.27.1 | PASS (7) | PASS (1) | PASS | clean `dev`, S2 |
| `factory-sandbox` (`oracle-waclaw-dziwulski-ubuntu-micro-01`) | Linux amd64; Go 1.27.1 | PASS (7) | PASS (1) | PASS | clean `dev`, S2 |

The ordinary Mac SSH execution route was used for checkout-local build and
test commands; Runner was not used because no mailbox operation was required.
No source was edited or copied on Ubuntu. No production Runner was stopped,
and no service, storage, firewall, credential, mailbox, or held ACK was
changed.

The source change is **PASS** for L2-05 source acceptance. Evidence closeout E
is a separate documentation-only commit after this source commit and must be
delivered through Gitea, exact mirror verification, and clean fast-forward
pulls on the three Ubuntu hosts before L2-06 proceeds.
