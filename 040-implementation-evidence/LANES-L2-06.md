# LANES-L2-06 — capacity scopes and durable byte admission

**State:** PASS — source acceptance at Source S. This unit adds policy and
transaction primitives; it does not issue a lane policy or claim live
enforcement, installation, or production capacity acceptance.

## Predecessor and scope

- Ordered predecessor: LANES-L2-05 evidence closeout E
  c86573e117c41e834874dee1e6d29b5024c1e084. The Mac checkout, Gitea primary,
  GitHub mirror, and all three Ubuntu dev checkouts were at this predecessor
  before Source S was delivered.
- Source S: 5e0f4b912b46708b7e8dbeba9cb483bb2df44850. The implementation
  introduces explicit Linux, Mac, and management capacity scopes; a
  measurement-bound envelope validator; schema 39 durable lane byte
  reservations; and transactional admission/release callbacks.
- LANES-B021-02 and LANES-B024-02 remain BLOCKED with their prior evidence
  preserved. Their maintenance closeout is not a prerequisite for this
  independent source unit. BUG-022/023 remain DEFERRED by owner scope; this
  unit does not start or expand their repair campaigns.
- Tests use temporary directories and SQLite fixtures. They do not read
  production credentials or mailbox state and do not publish Runner requests
  or ACKs. No installed service, host storage, APFS volume, cgroup, firewall,
  credential, or production configuration was changed.

## Read inputs at predecessor E

| Input | Blob at Source S |
| --- | --- |
| AGENTS.md | 3402877131d9fcea513f5bae756582e4127ee892 |
| Lane detailed design | f7404c2d5a0b35706f3a9e867991bffcb6171d93 |
| Lane phased plan | 082e73ac62a7815c3ed15778eb8162d81a43c347 |
| Lane execution handbook | d04a659df5c5834725ea05b9b047f4ad296b21bc |
| Execution ledger | d75c2bed87940df009dd7fd848d7c8fb5b24b25a |
| L2-05 evidence closeout | 6713b361edfb8756e4651bd5918d0d0cabf89915 |
| Lane coverage manifest | 4672d5d2141a8d81e066b03d49a3dd971cfb07ea |
| Preserved B021-02 evidence | unchanged at predecessor |
| Preserved B024-02 evidence | unchanged at predecessor |

## Implementation

The new capacity policy package rejects stale or malformed measurements,
changed host/cache/volume identities, inconsistent cache sets, incomplete
resource dimensions, false Mac compute-reservation claims, and any capacity
envelope that would consume protected PRD or host headroom. Its policy scopes
cover:

- Linux lane byte and inode budgets while preserving the declared PRD floor
  and 30% free-space/free-inode floors.
- Mac Dev/QAS storage based on measured APFS identities and usage, retained
  snapshots, shared caches, and the greater of 20 GiB or 15% free headroom.
  Mac compute is not represented as reserved by this storage policy.
- Management capacity with explicit shared cache, backup, host, and PRD
  accounting. Preparation and policy validation do not require future lane
  volumes or generated UUIDs.
- Mac PRD capacity represented by either measured host-free space or an
  explicitly measured APFS quota, without treating a missing compute
  reservation as a lane failure.

The evaluation digest binds the measurement, the selected usage, and storage
identity. A changed identity or stale measurement fails closed.

Schema migration 39 adds a durable per-lane byte-reservation ledger with
strictly enumerated accounting categories. The store API supports setting a
budget, taking a category-complete snapshot, admitting bytes, and releasing
bytes after cleanup. Reservation and the caller's acceptance mutation occur
inside one SQLite immediate transaction; a failed acceptance callback rolls
both back. Release similarly couples successful cleanup with reservation
deletion. Retries must present the complete category set and the same identity
and limit. Concurrent admissions cannot overcommit the budget.

Sixteen H assertions are registered in
040-implementation-evidence/LANES-test-coverage.json. They cover the Linux
and Mac floors, PRD preservation, shared caches and backups, no Mac compute
claim, measured volume/cache identities, stale and malformed measurements,
evaluation-digest binding, migration 38-to-39, persistent category
reservations, atomic acceptance rollback, cleanup-gated release, complete
retry categories, and concurrent capacity admission.

This unit supplies reusable accounting and validator mechanics. It does not
yet connect every live producer, writer, log, workspace, or backup operation
to these reservations; it does not provision disposable devices or volumes;
and it does not enforce Linux cgroups or Mac APFS quotas. Those controls remain
for L2-07/L2-08 and later integration units. No normal signed lane policy or
production management baseline is issued by L2-06.

## Source gates

The required gates ran on Mac Source S and on all three clean, synchronized
Ubuntu checkouts after their fast-forward pulls to S. The Ubuntu commands used
the Makefile's pinned Go 1.27.1 toolchain at
/home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go
and the accounts' existing Go caches.

| Machine | Identity / platform | L2-06 H | make check build | Checkout after validation |
| --- | --- | --- | --- | --- |
| Mac mac-control-plane | Darwin arm64; Go 1.27.1 | PASS (16 assertions) | PASS | clean worktree, dev at S |
| linux-host (oracle-yuta-konopka-ubuntu-micro-02) | Linux amd64; Go 1.27.1 | PASS (16 assertions) | PASS, exit 0 | clean dev at S |
| sandbox-host (oracle-gustaw-janecki-ubuntu-flex-02) | Linux arm64; Go 1.27.1 | PASS (16 assertions) | PASS, exit 0 | clean dev at S |
| factory-sandbox (oracle-waclaw-dziwulski-ubuntu-micro-01) | Linux amd64; Go 1.27.1 | PASS (16 assertions) | PASS, exit 0 | clean dev at S |

Commands on each Ubuntu host were:

    make test-lanes UNIT=LANES-L2-06 LANES_TIER=H
    make check build

The Mac also passed the H tier, full make check build, formatting, JSON
validation, and git diff --check. The first sandboxed Mac full-check attempt
was denied permission for a test's /bin/ps subprocess; the authorized
rerun passed. This was a test-execution sandbox restriction, not a source or
host failure.

## Source S delivery

Source S was committed on Mac dev as
5e0f4b912b46708b7e8dbeba9cb483bb2df44850. Direct ls-remote checks resolved
both primary Gitea origin/dev and automatic mirror github/dev to that exact
SHA. The Mac worktree is clean at S. The local origin tracking ref did not
advance after push because Git could not create its remote-tracking lock;
direct remote verification confirms the primary push succeeded, so no reset or
force operation was used.

The Ubuntu checkouts were verified clean on dev at predecessor E before pull,
then fast-forwarded from GitHub using
/home/ubuntu/.ssh/gavrilo123456-github and git pull --ff-only origin dev.
Each host's final HEAD was exactly S before the H and full source gates. Mac
SSH used explicit identities with batch mode and strict host-key checking:

| Host | SSH alias | Mac identity |
| --- | --- | --- |
| linux-host | remote-session-runner | /Users/tomasz.walczuk/.ssh/remote-session-runner |
| sandbox-host | sandbox.env | /Users/tomasz.walczuk/.ssh/dev.slidestud.io |
| factory-sandbox | factory-sandbox | /Users/tomasz.walczuk/.ssh/factory-sandbox |

No changed source was edited or copied on Ubuntu. No Runner service, request,
ACK, mailbox file, production work, storage, firewall rule, credential, or
held evidence was changed. This Source S is PASS for the L2-06 source gates.
This documentation-only evidence closeout E must be delivered through the
same Gitea-primary, exact-mirror, and clean Ubuntu fast-forward workflow before
LANES-L2-07 proceeds.
