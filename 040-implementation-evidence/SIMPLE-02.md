# SIMPLE-02 — lane-aware runtime configuration and authenticated routing

**State: IN PROGRESS — source implementation is ready for S delivery; full
source gates and synchronized-worker validation are pending.** No lane was
installed or started. This evidence does not claim live Dev/QAS acceptance or
change the installed unsuffixed PRD services.

## Predecessor and scope

Predecessor `SIMPLE-01` is PASS at verified evidence delivery commit
`965c3ba569e763979d4d6c5a99de88414bd9084f`; its inventory is
[`SIMPLE-01.md`](SIMPLE-01.md). Source changes remain in the authoritative Mac
`dev` checkout. The SIMPLE profile uses the existing Mac and three Ubuntu
workers, with current unsuffixed PRD compatibility and explicit `dev`, `qas`,
or `prd` selection for complete V3 runtime configs.

The source contract derives the existing roots, component/service identities,
SSH port 22, direct HTTPS 8443/8444/8445, lane-specific sockets, databases,
mailboxes, workspaces and logs. It does not introduce a dedicated SSH daemon,
cgroup slice, volume, host, account or release authority. V1/V2 configs remain
accepted as unsuffixed PRD compatibility formats.

## Implemented in the source candidate

- Added an active-profile namespace derivation that fixes the per-lane Mac and
  Linux paths, LaunchAgent/systemd identities and direct ports while retaining
  the existing SSH listener on port 22.
- Added a complete V3 runtime loader joining the fixed lane route profiles to
  the existing environment, controller, execution-context, limits, retention
  and mailbox registries. Mac Dev/QAS can be remote-only or explicitly include
  `mac-workstation`; each Mac config must bind all three remote workers.
- Required Mac inbox roots to remain inside the selected lane root. Linux
  configs bind exactly one host profile and its derived local paths. Dev/QAS
  default to two sessions and one running command and reject higher configured
  ceilings.
- Preserved legacy V1/V2 parsing and reject ignored V3 lane selectors on those
  formats. Added [`runtime.schema.json`](../src/internal/config/schemas/v3/runtime.schema.json)
  for the complete V3 runtime shape.
- Direct HTTPS now checks the selected server certificate fingerprint and URI
  SAN at the Mac client, and the server checks its certificate/SPKI/IP/URI,
  client-CA fingerprint, sole principal-map identity, and the authorized client
  certificate/SPKI/URI before an HTTP handler can run. Client certificate and
  key are still loaded only from owner-controlled files.
- The Mac-local CLI endpoint now resolves the API socket from its selected
  config. The Mac queued route uses port 22 and validates the lane's configured
  host-key fingerprint against its owner-only known-hosts file before creating
  the SSH caller. OpenSSH retains strict host-key checking, `IdentitiesOnly`,
  batch mode and the fixed bridge command.

## Source checks recorded before S delivery

The Mac used Go 1.27.1 with the shared caches
`/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`.

Focused command:

```sh
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache GOTOOLCHAIN=local \
"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go" \
test ./src/internal/config ./src/internal/runnerclient ./src/internal/runnercli \
./src/internal/runnerd ./src/internal/runnerlocal ./src/internal/sshclient -count=1
```

Result: PASS on the Mac. The focused packages passed after correcting one test
fixture's Unix-socket path length; the initial fixture bind error was not a
product failure. `python3 -m json.tool src/internal/config/schemas/v3/runtime.schema.json`
and `git diff --check` also passed. The first sandboxed `make check` run was
blocked by the sandbox denying the unrelated `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`
test's read-only `/bin/ps` inspection. The same repository gate then passed
outside the sandbox, and a subsequent `make check` returned exit code 0 on the
Mac. Source commit/push/mirror verification and Ubuntu validation remain
pending before S can be called delivered.

## Gates and limitations

`SIMPLE-02` does not create, install, or inspect actual Dev/QAS certificates,
private keys, SSH bridge keys or known-host files. The loader validates
path-only references and structural fingerprint bindings; client/server
runtime checks validate the material when that selected service starts. The
Mac SSH validator reads the selected known-host file but does not read private
key bytes. Candidate creation must still pair each per-host material set and
prove the configured fingerprints against its generated artifacts.

Dev/QAS installation, real port reachability, mailbox execution, direct and
queued cross-host acceptance, Mac-local command acceptance, lane restart
isolation, PRD health comparison and ACK/capacity release are **NOT RUN**.
Physical and historical advanced F gates are superseded/out of scope, not
passed. B021/B024 retained evidence and held ACKs were not accessed or changed;
BUG-022/023 remain deferred and open.
