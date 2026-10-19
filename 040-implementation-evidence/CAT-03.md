# CAT-03 — Legacy adoption, launch paths and cohort maintenance

**Status:** source implementation complete on Mac; Ubuntu source validation and
the required Git delivery are pending. No installed lane, selector, service,
mailbox or host configuration has changed.

## Scope and discovered path assumptions

CAT-03 implements explicit copy-and-verify adoption for qualified legacy
Dev/QAS bundles, catalog-aware launch-path validation, refusal by legacy
installers to overwrite catalog-selected binaries, and a four-host maintenance
receipt/probe model. The source audit found:

- Mac Dev/QAS LaunchAgents and Linux systemd units already execute through
  `current/bin`; Linux's generated entrypoint still accepts only relative
  `releases/<sha256>` selectors.
- The Dev/QAS forced SSH wrapper already resolves `current/bin`; its artifact
  remains in the stable lane `bin` directory. The unsuffixed production bridge
  wrapper and Linux/Mac legacy installers have separate old-layout assumptions
  that must either honor a catalog selector or fail closed before writes.
- The CAT-02 selector primitives preserve validated legacy targets, but there
  is not yet an explicit legacy-bundle copy/adoption command or a whole-lane
  cohort receipt and scoped probe admission gate.

## Source implementation and local validation

CAT-03 adds explicit legacy Dev/QAS bundle adoption, catalog-aware executable
resolution in service entrypoints and fixed SSH bridges, fail-closed legacy
PRD installers/backup tools, four-host cohort receipts and switch ordering,
startup admission holds, and one-time bounded direct-mTLS maintenance probe
admission. PRD service artifacts have a separate guarded selector path; generic
Dev/QAS mutation remains unable to select PRD. The PRD switch path now acquires
the catalog lock and PRD lifecycle lock before stopping a service. Ubuntu-owned
queued-wrapper replacement is restricted to the current account and exact
expected prior hash.

Mac source checks at the CAT-03 source revision:

- `make test` — **PASS**. The restricted-shell attempt could not inspect a
  test-owned process with `/bin/ps`; the same suite was rerun from the approved
  Mac shell execution context and passed.
- `make vet` — **PASS**.
- `make build` — **PASS**.
- `make smoke` — **PASS**.
- `git diff --check` — **PASS**.

These are source/fixture checks, not installed lane acceptance. Ubuntu native
source validation and Git delivery will be recorded after the synchronized
source revision is available on each clean host checkout.

## Live acceptance and limitations

No release was published or selected and no Runner service was restarted.
Dev/QAS and PRD live migration remain **NOT RUN**. No mailbox requests or ACKs
may be published under the current owner instruction; mailbox-specific
acceptance remains **NOT RUN**. External project publishers remain paused and
must not be resumed by this unit.

CAT-04 Dev and CAT-05 QAS installed acceptance, normal publisher resumption,
and CAT-06 PRD migration remain separate gates. CAT-06 requires its own exact
operational package and explicit owner approval.
