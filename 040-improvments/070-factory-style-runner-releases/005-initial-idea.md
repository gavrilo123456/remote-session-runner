# Initial idea: Factory-style versioned Runner releases

**Status:** design direction selected 2026-10-19; design and plan only.
Implementation, publication and installed migration are **NOT STARTED**.

## Goal

Use the same deployment model as Agentic Software Factory: publish an immutable
version once, test it in Dev, select the same release in QAS, and promote it to
PRD through supported maintenance. Keep versioned executable releases under
`~/projects-runtime/runner-releases/` on each existing host.

Runner still has a Mac component set and native component sets for three Ubuntu
workers. One release identifies the source and the four per-host bundle hashes;
each machine stores only its own executable set. Each Dev/QAS/PRD instance has
an independent version selector. There is no global `current` selector.

Configuration, SQLite databases and sidecars, mailbox roots, sockets, credentials,
workspaces, logs and backups stay in their existing lane-specific locations.
This moves release storage and selection, not application data or accounts.

## Why

- Make release inventory, version identity and promotion consistent with Factory.
- Let Dev, QAS and PRD use different versions while sharing immutable bytes when
  they select the same host release.
- Keep previous releases available for compatible binary rollback.
- Preserve existing hosts, transports and ordinary selected-lane maintenance.

The [detailed design](010-factory-style-runner-releases-detailed-design.md)
defines paths, identity, selector validation, legacy migration and rollback.
The [phased implementation plan](015-factory-style-runner-releases-phased-implementation-plan.md)
defines the separate CAT sequence. Existing SIMPLE/LANES results remain history;
this registration does not claim those results prove catalog acceptance.

The owner selected **design and plan first**. No source code, installed paths,
service definitions, release selectors, configuration or database is changed by
this documentation assignment. Active BUG-025 corrections continue independently.
