# LLM client guide (placeholder)

> **Status:** Placeholder only. It does not yet define an approved operating
> procedure. The existing guides linked below remain authoritative until the
> designated LLM operator records its verified workflow here.

## Purpose

This document will describe how an LLM or coding agent uses Remote Session
Runner effectively in practice. Its future author should capture observed,
repeatable behavior from a working integration rather than theoretical advice.

## Future author instructions

- Describe the actual integration boundary and tool capabilities available to
  the LLM.
- Distinguish verified steps from assumptions, examples, and host-specific
  details that can change.
- Include only safe request examples. Never include tokens, Authorization
  headers, certificates, private keys, or secret file contents.
- Link to the normative documentation instead of copying configuration or
  protocol rules that it already owns.

## Sections to complete

### 1. Preconditions and preflight

<!-- Explain what the LLM checks before it publishes work. -->

### 2. Selecting the inbox and execution target

<!-- Explain defaults, allowed overrides, and how the LLM avoids an unintended host. -->

### 3. Publishing a request

<!-- Explain request identity, idempotency, JSON creation, and marker-last publication. -->

### 4. Reading and acknowledging a result

<!-- Explain request_id → outbox → command_id → events → ACK. -->

### 5. Retrying and handling uncertainty

<!-- Explain when to reuse an idempotency key, create a new request ID, or stop. -->

### 6. Common failure triage

<!-- Add observed symptoms, the safe evidence to inspect, and the next action. -->

### 7. Efficient operating patterns

<!-- Add validated patterns for normal work, long-running work, concise handoff, and cleanup. -->

## References

- [CLI user guide](user-guide.md)
- [Mailbox guide](mailbox.md)
- [Configuration reference](configuration.md)
- [Operations runbook](operations.md)
- [Current-host evidence](current-host-evidence.md)
