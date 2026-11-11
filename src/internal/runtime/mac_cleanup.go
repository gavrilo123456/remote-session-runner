package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"syscall"
	"time"

	"remote-session-runner/src/internal/domain"
)

// MacCleanupDiagnosticError retains the cause for errors.Is/As while exposing
// only a bounded metadata record to the worker's diagnostic logger.
type MacCleanupDiagnosticError struct {
	diagnostic domain.RuntimeCleanupDiagnostic
	cause      error
}

func (e *MacCleanupDiagnosticError) Error() string {
	return "Mac runtime cleanup unconfirmed: " + e.diagnostic.Code
}

func (e *MacCleanupDiagnosticError) Unwrap() error { return e.cause }

func (e *MacCleanupDiagnosticError) RuntimeCleanupDiagnostic() domain.RuntimeCleanupDiagnostic {
	d := e.diagnostic
	d.Members = append([]domain.RuntimeCleanupMember(nil), d.Members...)
	return d
}

type macCleanupInspectionError struct {
	code  string
	cause error
}

func (e *macCleanupInspectionError) Error() string {
	return "Mac cleanup group observation unavailable"
}
func (e *macCleanupInspectionError) Unwrap() error { return e.cause }

func macCleanupInspectionFailure(code string, cause error) error {
	return &macCleanupInspectionError{code: code, cause: cause}
}

func macCleanupFailure(d domain.RuntimeCleanupDiagnostic, stage, code string, started time.Time, cause error) error {
	d.Stage, d.Code = stage, code
	if stage == "group_inspection" && (errors.Is(cause, syscall.EPERM) || errors.Is(cause, syscall.EACCES)) {
		d.Code = "group_inspection_denied"
	}
	d.ElapsedMS = time.Since(started).Milliseconds()
	d.ObservedStartedAt = started.UTC().Format(time.RFC3339Nano)
	d.ObservedEndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if d.ElapsedMS > 86_400_000 {
		d.ElapsedMS = 86_400_000
	}
	if errors.Is(cause, context.Canceled) {
		d.Stage, d.Code = "context", "cancelled"
	} else if errors.Is(cause, context.DeadlineExceeded) {
		d.Stage, d.Code = "context", "deadline_exceeded"
	}
	return &MacCleanupDiagnosticError{diagnostic: d, cause: cause}
}

// waitMacCleanupGroupQuiescent observes only the recorded shell group after
// the current adapter has reaped that shell. It sends no signals. A positively
// inspected zombie cannot execute; an unknown observation remains a refusal.
func waitMacCleanupGroupQuiescent(ctx context.Context, owner RuntimeOwnershipRecord, grace time.Duration,
	inspect func(int) ([]domain.RuntimeCleanupMember, error)) error {
	started := time.Now()
	d := domain.RuntimeCleanupDiagnostic{RootPID: owner.PID, RootStartIdentity: owner.ProcessStartIdentity,
		ProcessGroupID: owner.ProcessGroupID, OwnerUID: owner.UID, GraceMS: grace.Milliseconds()}
	d.DeadlineAt = started.Add(grace).UTC().Format(time.RFC3339Nano)
	fail := func(stage, code string, cause error) error { return macCleanupFailure(d, stage, code, started, cause) }
	if ctx == nil || inspect == nil || grace <= 0 || grace > time.Minute || owner.PID <= 0 ||
		owner.ProcessGroupID != owner.PID || owner.UID < 0 || !validMacCleanupStartIdentity(owner.ProcessStartIdentity) {
		return fail("ownership", "ownership_invalid", ErrRuntimeOwnershipRecord)
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	seenStarts := make(map[int]string)
	for {
		if err := ctx.Err(); err != nil {
			return fail("context", "cancelled", err)
		}
		d.SnapshotCount++
		d.MemberCount, d.RunnableCount, d.ZombieCount, d.MembersTruncated, d.Members = 0, 0, 0, false, nil
		members, err := inspect(owner.ProcessGroupID)
		if err != nil {
			code := "group_inspection_failed"
			var tagged *macCleanupInspectionError
			if errors.As(err, &tagged) {
				code = tagged.code
			}
			return fail("group_inspection", code, err)
		}
		if err := ctx.Err(); err != nil {
			return fail("context", "cancelled", err)
		}
		d.MemberCount, d.RunnableCount, d.ZombieCount = len(members), 0, 0
		d.Members = nil
		if len(members) > 4096 {
			d.MemberCount, d.MembersTruncated = 4096, true
			return fail("group_inspection", "group_observation_incomplete", ErrRuntimeOwnershipRecord)
		}
		seen := make(map[int]bool, len(members))
		for _, member := range members {
			if member.PID <= 0 || member.PID > 2_147_483_647 || member.ParentPID < 0 || member.UID < 0 ||
				member.ParentPID > 2_147_483_647 || member.UID > 4_294_967_295 || member.ProcessGroupID <= 0 ||
				seen[member.PID] || member.Status < 1 || member.Status > macProcessStatusZombie || !validMacCleanupStartIdentity(member.ProcessStartIdentity) {
				d.MembersTruncated = len(d.Members) < d.MemberCount
				return fail("group_inspection", "group_member_unknown", ErrRuntimeOwnershipRecord)
			}
			seen[member.PID] = true
			if member.ProcessGroupID != owner.ProcessGroupID || member.PID == owner.PID {
				d.MembersTruncated = len(d.Members) < d.MemberCount
				return fail("group_inspection", "group_identity_changed", ErrRuntimeOwnershipRecord)
			}
			if previous, ok := seenStarts[member.PID]; ok && previous != member.ProcessStartIdentity {
				d.MembersTruncated = len(d.Members) < d.MemberCount
				return fail("group_inspection", "group_identity_changed", ErrRuntimeOwnershipRecord)
			}
			if _, exists := seenStarts[member.PID]; !exists && len(seenStarts) >= 4096 {
				d.MembersTruncated = len(d.Members) < d.MemberCount
				return fail("group_inspection", "group_observation_incomplete", ErrRuntimeOwnershipRecord)
			}
			seenStarts[member.PID] = member.ProcessStartIdentity
			if len(d.Members) < domain.MaxRuntimeCleanupDiagnosticMembers {
				d.Members = append(d.Members, member)
			}
			if member.UID != owner.UID {
				d.MembersTruncated = len(d.Members) < d.MemberCount
				return fail("group_inspection", "group_member_unknown", ErrRuntimeOwnershipRecord)
			}
			if member.Status == macProcessStatusZombie {
				d.ZombieCount++
			} else {
				d.RunnableCount++
			}
		}
		d.MembersTruncated = len(d.Members) < d.MemberCount
		if d.RunnableCount == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fail("context", "cancelled", ctx.Err())
		case <-deadline.C:
			return fail("group_wait", "group_still_runnable", fmt.Errorf("%w: recorded group still has runnable members", ErrRuntimeOwnershipRecord))
		case <-ticker.C:
		}
	}
}

func validMacCleanupStartIdentity(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed != 0
}

// sampleMacCleanupGroup binds per-PID metadata to bounded before/after group
// enumerations. These callbacks are private native readers, not operator
// options. A disappearing member is accepted only when it is positively gone
// and a fresh list excludes it; a joined, moved or replaced member refuses.
func sampleMacCleanupGroup(groupID int, list func(int) ([]int, error),
	read func(int) (domain.RuntimeCleanupMember, bool, error)) ([]domain.RuntimeCleanupMember, error) {
	changed := func() error { return macCleanupInspectionFailure("group_identity_changed", ErrRuntimeOwnershipRecord) }
	readList := func() ([]int, error) {
		pids, err := list(groupID)
		if err != nil {
			return nil, err
		}
		if len(pids) > 4096 {
			return nil, macCleanupInspectionFailure("group_observation_incomplete", ErrRuntimeOwnershipRecord)
		}
		pids = append([]int(nil), pids...)
		slices.Sort(pids)
		for i, pid := range pids {
			if pid <= 0 || pid > 2_147_483_647 || (i != 0 && pids[i-1] == pid) {
				return nil, macCleanupInspectionFailure("group_observation_incomplete", ErrRuntimeOwnershipRecord)
			}
		}
		return pids, nil
	}
	first, err := readList()
	if err != nil {
		return nil, err
	}
	records := make(map[int]domain.RuntimeCleanupMember, len(first))
	for _, pid := range first {
		member, found, err := read(pid)
		if err != nil {
			return nil, err
		}
		if found {
			if member.PID != pid || member.ProcessGroupID != groupID || !validMacCleanupStartIdentity(member.ProcessStartIdentity) {
				return nil, changed()
			}
			if member.UID < 0 || member.UID > 4_294_967_295 || member.ParentPID < 0 || member.ParentPID > 2_147_483_647 || member.Status < 1 || member.Status > macProcessStatusZombie {
				return nil, macCleanupInspectionFailure("group_member_unknown", ErrRuntimeOwnershipRecord)
			}
			records[pid] = member
		}
	}
	reconcile := func(prior, current []int) error {
		priorSet, currentSet := make(map[int]bool, len(prior)), make(map[int]bool, len(current))
		for _, pid := range prior {
			priorSet[pid] = true
		}
		for _, pid := range current {
			currentSet[pid] = true
		}
		for _, pid := range current {
			if !priorSet[pid] {
				return changed()
			}
			if _, ok := records[pid]; !ok {
				return changed()
			}
		}
		for _, pid := range prior {
			if currentSet[pid] {
				continue
			}
			_, found, err := read(pid)
			if err != nil {
				return err
			}
			if found {
				return changed()
			}
			delete(records, pid)
		}
		return nil
	}
	middle, err := readList()
	if err != nil {
		return nil, err
	}
	if err := reconcile(first, middle); err != nil {
		return nil, err
	}
	for _, pid := range middle {
		member, found, err := read(pid)
		if err != nil {
			return nil, err
		}
		if !found {
			delete(records, pid)
			continue
		}
		prior := records[pid]
		if member.PID != pid || member.ProcessGroupID != groupID || member.UID != prior.UID ||
			member.ProcessStartIdentity != prior.ProcessStartIdentity ||
			(prior.Status == macProcessStatusZombie && member.Status != macProcessStatusZombie) {
			return nil, changed()
		}
		if member.ParentPID < 0 || member.ParentPID > 2_147_483_647 || member.Status < 1 || member.Status > macProcessStatusZombie {
			return nil, macCleanupInspectionFailure("group_member_unknown", ErrRuntimeOwnershipRecord)
		}
		records[pid] = member
	}
	last, err := readList()
	if err != nil {
		return nil, err
	}
	if err := reconcile(middle, last); err != nil {
		return nil, err
	}
	members := make([]domain.RuntimeCleanupMember, 0, len(last))
	for _, pid := range last {
		members = append(members, records[pid])
	}
	return members, nil
}
