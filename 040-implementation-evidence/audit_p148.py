#!/usr/bin/env python3
"""Read-only final evidence crosswalk checks for phase P148."""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / "040-implementation-evidence"
PLAN = ROOT / "030-detailed-phased-implementaion-plan/010-remote-session-runner-detailed-phased-implementaion-plan.md"
DESIGN = ROOT / "030-detailed-design/010-remote-session-runner-detailed-design.md"
P144 = EVIDENCE / "P144.md"

TEST_ID = re.compile(
    r"(?:D|I|M|O|R)-\d{2}|P-(?:MAC|LNX|NET|CLI|MBX|STORE|OPS)-\d{2}|F-\d{2}"
)


def fail(message: str) -> None:
    print(f"P148 audit FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        fail(f"cannot read {path.relative_to(ROOT)}: {exc}")


def table_cells(line: str) -> list[str]:
    return [cell.strip() for cell in line.strip().strip("|").split("|")]


def section(source: str, start: str, end: str) -> str:
    try:
        return source[source.index(start) : source.index(end, source.index(start))]
    except ValueError:
        fail(f"expected section not found: {start!r} / {end!r}")


plan_text = read(PLAN)
design_text = read(DESIGN)
p144_text = read(P144)

# Extract every executable evidence ID in detailed-design §14.
design_tests = section(
    design_text,
    "## 14. Automated-test architecture",
    "## 15. Acceptance traceability and evidence",
)
test_ids: list[str] = []
for line in design_tests.splitlines():
    cells = table_cells(line)
    if cells and cells[0].startswith("`") and cells[0].endswith("`"):
        candidate = cells[0][1:-1]
        if TEST_ID.fullmatch(candidate):
            test_ids.append(candidate)
if len(test_ids) != len(set(test_ids)):
    fail("detailed-design §14 contains duplicate test IDs")

# Extract the plan's designated full-owner phase for each §14 ID.
owner_section = section(plan_text, "## 3. Full test ownership", "## 4. Completion and handoff")
owners: dict[str, str] = {}
for line in owner_section.splitlines():
    if not line.startswith("|"):
        continue
    cells = table_cells(line)
    if len(cells) != 4:
        continue
    for id_cell, owner_cell in ((cells[0], cells[1]), (cells[2], cells[3])):
        owner_match = re.search(r"P\d{3}", owner_cell)
        if not owner_match:
            continue
        for test_id in TEST_ID.findall(id_cell):
            owners[test_id] = owner_match.group()
if set(owners) != set(test_ids):
    fail(f"plan owner ledger mismatch; missing={sorted(set(test_ids) - set(owners))}, extra={sorted(set(owners) - set(test_ids))}")

# Extract P144's evidence pointer and ensure it names the same full owner.
coverage_section = section(
    p144_text,
    "### P144/§14 coverage matrix",
    "### §15 and initial-design acceptance reconciliation",
)
coverage: dict[str, str] = {}
for line in coverage_section.splitlines():
    if not line.startswith("|"):
        continue
    cells = table_cells(line)
    if len(cells) != 3:
        continue
    candidate = cells[0].strip("`")
    if TEST_ID.fullmatch(candidate):
        if candidate in coverage:
            fail(f"P144 coverage duplicates {candidate}")
        owner_match = re.search(r"P\d{3}", cells[1])
        if not owner_match:
            fail(f"P144 coverage has no phase pointer for {candidate}")
        coverage[candidate] = owner_match.group()

if set(coverage) != set(test_ids):
    fail(f"P144 coverage mismatch; missing={sorted(set(test_ids) - set(coverage))}, extra={sorted(set(coverage) - set(test_ids))}")
for test_id in test_ids:
    if coverage[test_id] != owners[test_id]:
        fail(f"{test_id} points to {coverage[test_id]}, plan assigns {owners[test_id]}")
    owner_file = EVIDENCE / f"{coverage[test_id]}.md"
    if not owner_file.is_file():
        fail(f"missing full-owner evidence file for {test_id}: {owner_file.name}")

# Require the complete serial evidence-file set through P147 and the P148 record.
missing_phase_files = [
    f"P{phase:03}.md"
    for phase in range(1, 148)
    if not (EVIDENCE / f"P{phase:03}.md").is_file()
]
if missing_phase_files:
    fail(f"missing phase evidence: {', '.join(missing_phase_files)}")
if not (EVIDENCE / "P148.md").is_file():
    fail("P148 evidence record is missing")

# Verify first appearance of phase IDs in the committed history is serial.
subjects = subprocess.check_output(
    ["git", "-C", str(ROOT), "log", "--reverse", "--format=%s", "HEAD"],
    text=True,
).splitlines()
first_seen: list[int] = []
for subject in subjects:
    for number in sorted({int(value) for value in re.findall(r"P(\d{3})", subject)}):
        if 1 <= number <= 147 and number not in first_seen:
            first_seen.append(number)
if first_seen != list(range(1, 148)):
    fail("committed P001–P147 identifiers are missing or first appear out of order")

# The seven §15 acceptance areas must have a P144 reconciliation row each.
acceptance_section = p144_text[p144_text.index("### §15 and initial-design acceptance reconciliation") :]
acceptance_rows = [
    line
    for line in acceptance_section.splitlines()
    if line.startswith("|") and len(table_cells(line)) == 3 and table_cells(line)[0] not in {"Detailed §15 acceptance area", "---"}
]
if len(acceptance_rows) != 7:
    fail(f"expected seven P144 §15 acceptance rows, found {len(acceptance_rows)}")

# Final actual-host evidence must identify the correct machine/account.
host_evidence = {
    "P143.md": ("AMAK2KJ6X9JJJ", "tomasz.walczuk", "oracle-yuta-konopka-ubuntu-micro-02", "ubuntu"),
    "P145.md": ("AMAK2KJ6X9JJJ", "tomasz.walczuk"),
    "P146.md": ("oracle-yuta-konopka-ubuntu-micro-02", "ubuntu"),
    "P147.md": ("AMAK2KJ6X9JJJ", "tomasz.walczuk", "oracle-yuta-konopka-ubuntu-micro-02", "ubuntu"),
}
for filename, labels in host_evidence.items():
    text = read(EVIDENCE / filename)
    if not re.search(r"\bPASS\b", text):
        fail(f"{filename} has no PASS result")
    for label in labels:
        if label not in text:
            fail(f"{filename} lacks required machine/account label {label!r}")

# Preserve the approved limited P143 path without implying a physical PASS.
p143_text = read(EVIDENCE / "P143.md")
for required in ("NOT RUN", "UNVERIFIED", "approved software-crash-only"):
    if required not in p143_text:
        fail(f"P143 does not preserve the required limitation marker {required!r}")

print(
    "P148 audit PASS: "
    f"{len(test_ids)}/{len(test_ids)} detailed-design §14 IDs map to their plan owner; "
    f"{len(acceptance_rows)} §15 areas reconciled; "
    "P001–P147 evidence files and serial commit identifiers present; "
    "Mac/Ubuntu/two-host final evidence labeled; P143 physical limitation preserved."
)
