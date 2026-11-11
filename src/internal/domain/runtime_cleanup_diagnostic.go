package domain

import "time"

// RuntimeCleanupDiagnostic is bounded metadata about a cleanup refusal. It is
// diagnostic evidence, never authority to signal a process or release capacity.
// Session, job and generation bindings come from the execution authority.
type RuntimeCleanupDiagnostic struct {
	Stage             string                 `json:"stage"`
	Code              string                 `json:"code"`
	RootPID           int                    `json:"root_pid"`
	RootStartIdentity string                 `json:"root_start_identity,omitempty"`
	ProcessGroupID    int                    `json:"process_group_id"`
	OwnerUID          int                    `json:"owner_uid"`
	ObservedStartedAt string                 `json:"observed_started_at,omitempty"`
	ObservedEndedAt   string                 `json:"observed_ended_at,omitempty"`
	DeadlineAt        string                 `json:"deadline_at,omitempty"`
	ElapsedMS         int64                  `json:"elapsed_ms"`
	GraceMS           int64                  `json:"grace_ms"`
	SnapshotCount     int                    `json:"snapshot_count"`
	MemberCount       int                    `json:"member_count"`
	RunnableCount     int                    `json:"runnable_count"`
	ZombieCount       int                    `json:"zombie_count"`
	MembersTruncated  bool                   `json:"members_truncated"`
	Members           []RuntimeCleanupMember `json:"members,omitempty"`
}

type RuntimeCleanupMember struct {
	PID                  int    `json:"pid"`
	ParentPID            int    `json:"parent_pid"`
	ProcessGroupID       int    `json:"process_group_id"`
	UID                  int    `json:"uid"`
	Status               uint32 `json:"status"`
	ProcessStartIdentity string `json:"process_start_identity,omitempty"`
}

// RuntimeCleanupDiagnosticCarrier allows adapters to expose a safe diagnostic
// without making the execution/worker layers depend on a runtime implementation.
type RuntimeCleanupDiagnosticCarrier interface {
	RuntimeCleanupDiagnostic() RuntimeCleanupDiagnostic
}

const MaxRuntimeCleanupDiagnosticMembers = 16

// ValidateRuntimeCleanupDiagnostic rejects unexpected strings, unbounded
// collections and inconsistent counts before any diagnostic reaches a log.
// Empty identities represent unavailable observations, not absence proof.
func ValidateRuntimeCleanupDiagnostic(d RuntimeCleanupDiagnostic) bool {
	switch d.Stage {
	case "context", "session_state", "ownership", "root_wait", "group_inspection", "group_wait", "workspace_cleanup", "ownership_finalization":
	default:
		return false
	}
	switch d.Code {
	case "cancelled", "deadline_exceeded", "session_missing", "ownership_invalid", "root_identity_mismatch", "root_wait_failed",
		"group_inspection_failed", "group_inspection_denied", "group_observation_incomplete", "group_identity_changed", "group_member_unknown", "group_still_runnable",
		"workspace_cleanup_failed", "owner_marker_cleanup_failed", "cleanup_failed":
	default:
		return false
	}
	if !validCleanupProcessID(d.RootPID, true) || !validCleanupProcessID(d.ProcessGroupID, true) || d.OwnerUID < 0 ||
		uint64(d.OwnerUID) > 4_294_967_295 || !validCleanupStartIdentity(d.RootStartIdentity) ||
		!validCleanupDiagnosticTimes(d) || d.ElapsedMS < 0 || d.ElapsedMS > 86_400_000 || d.GraceMS < 0 || d.GraceMS > 60_000 ||
		d.SnapshotCount < 0 || d.SnapshotCount > 10_000 || d.MemberCount < 0 || d.MemberCount > 4096 ||
		d.RunnableCount < 0 || d.ZombieCount < 0 || d.RunnableCount+d.ZombieCount > d.MemberCount ||
		len(d.Members) > MaxRuntimeCleanupDiagnosticMembers || len(d.Members) > d.MemberCount ||
		(d.MembersTruncated != (len(d.Members) < d.MemberCount)) {
		return false
	}
	seen := make(map[int]bool, len(d.Members))
	for _, member := range d.Members {
		if !validCleanupProcessID(member.PID, false) || !validCleanupProcessID(member.ParentPID, true) ||
			!validCleanupProcessID(member.ProcessGroupID, false) || member.UID < 0 || uint64(member.UID) > 4_294_967_295 || member.Status > 255 ||
			!validCleanupStartIdentity(member.ProcessStartIdentity) || seen[member.PID] {
			return false
		}
		seen[member.PID] = true
	}
	return true
}

func validCleanupDiagnosticTimes(d RuntimeCleanupDiagnostic) bool {
	parse := func(raw string) (time.Time, bool) {
		if len(raw) > 35 || raw == "" || raw[len(raw)-1] != 'Z' {
			return time.Time{}, false
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		return at, err == nil
	}
	if d.ObservedStartedAt == "" && d.ObservedEndedAt == "" && d.DeadlineAt == "" {
		return true
	}
	start, startOK := parse(d.ObservedStartedAt)
	end, endOK := parse(d.ObservedEndedAt)
	if !startOK || !endOK || end.Before(start) || end.Sub(start) > 24*time.Hour {
		return false
	}
	if d.DeadlineAt != "" {
		deadline, ok := parse(d.DeadlineAt)
		if !ok || deadline.Before(start) || deadline.Sub(start) > 24*time.Hour {
			return false
		}
	}
	return true
}

func validCleanupProcessID(pid int, allowZero bool) bool {
	return pid >= 0 && pid <= 2_147_483_647 && (allowZero || pid != 0)
}

func validCleanupStartIdentity(identity string) bool {
	if len(identity) > 32 {
		return false
	}
	for _, ch := range identity {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
