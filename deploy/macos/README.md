# macOS launchd deployment

The Mac runs `runner-local` (Unix API, file mailbox, and Router) and
`runner-locald` (local execution authority) as `tomasz.walczuk` in the GUI
launchd domain. Commands use that account's operating-system permissions; the
workspace is not confinement.

From the repository root, run:

```sh
deploy/macos/install-launchagents.sh
```

The first installer run creates the selected host config from
`mac.yaml.example` when it is absent and exits without starting services.
Review that config, then rerun the installer. It contains selected paths and
controller/environment choices; private key contents remain in the external
`secrets/` directory. The installer builds the CLI and both Mac services with
the selected Go 1.27.1 toolchain, places executables under the service root,
validates and installs the versioned LaunchAgent plists, then bootstraps them
in the GUI launchd domain. The plists use explicit executable/config paths, an
owner-only umask, a fixed `PATH`, and logs beneath the private service root.

`mac.yaml.example` intentionally remains the version-1 compatibility template
for the existing `default` mailbox. To add named inboxes, start from
`mac.v2.yaml.example`, review the whole policy, and save the owner-only
candidate as `config/mac.next.yaml`. Apply it with:

```sh
deploy/macos/install-launchagents.sh --config \
  "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/config/mac.next.yaml"
```

The installer leaves the active `config/mac.yaml` in place while it builds and
validates the candidate. It then stops the mailbox ingress, waits for its Unix
socket to disappear, performs the retained-mailbox check, and only then replaces
`mac.yaml` and starts the new services. If quiescence or the final check fails,
the previous configuration remains in place and the prior LaunchAgents are
restored. Once candidate activation begins, the installer stops automatic
rollback: after a completed handoff the candidate is active, and before a
completed handoff the owner-only staged candidate is retained and its exact
rerun command is printed. Reverting after that boundary could strand a
marker-last request a same-user file producer published to the candidate root.
Do not copy a changed configuration directly over active `mac.yaml`.

At the candidate activation boundary, the installer records the complete inbox
set before handing off `mac.yaml`; `runner-local` repeats that registration
idempotently after it starts. The registry is append-only in this PoC: keep
every registered inbox ID and root unchanged. Adding an inbox is supported;
removing, renaming, or moving one needs a later explicit migration after its
files and durable work have been retired. The v2 example contains only the
current `linux-host`; a new remote profile requires its own P157 host gate.

Stop or unload the jobs with:

```sh
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.local.plist"
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.remote-session-runner.locald.plist"
```

On SIGTERM, each service stops accepting new work, drains within an eight-second
budget, uses the normal runtime cancellation/close path if work remains, checks
the committed audit tail, then closes event streams and removes its Unix socket.
The shared cleanup stage is bounded to five seconds; the combined 13-second
limit fits inside each plist's 15-second `ExitTimeOut`. Event records committed
before shutdown remain available after restart, and clients resume after the
last sequence they received. Run `make test-p131-macos-shutdown` on the selected
Mac account to verify the real LaunchAgent stop and restart path.
