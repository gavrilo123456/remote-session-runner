package runtime

import (
	"context"
	"errors"
	"reflect"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func bug031Owner() RuntimeOwnershipRecord {
	return RuntimeOwnershipRecord{PID: 12345, ProcessGroupID: 12345, UID: 502, ProcessStartIdentity: "1794378081786412"}
}

func bug031Member(pid int, status uint32) domain.RuntimeCleanupMember {
	return domain.RuntimeCleanupMember{PID: pid, ParentPID: 1, ProcessGroupID: 12345, UID: 502, Status: status, ProcessStartIdentity: "1794378081786413"}
}

func bug031Diagnostic(t *testing.T, err error) domain.RuntimeCleanupDiagnostic {
	t.Helper()
	var carrier domain.RuntimeCleanupDiagnosticCarrier
	if err == nil || !errors.As(err, &carrier) {
		t.Fatalf("cleanup refusal has no typed diagnostic: %v", err)
	}
	d := carrier.RuntimeCleanupDiagnostic()
	if !domain.ValidateRuntimeCleanupDiagnostic(d) {
		t.Fatalf("diagnostic failed safe-metadata validation: %+v", d)
	}
	if d.ObservedStartedAt == "" || d.ObservedEndedAt == "" || d.DeadlineAt == "" {
		t.Fatalf("group observation lacks its time boundary: %+v", d)
	}
	return d
}

func TestBUG031MacCleanupAcceptsOnlyPositiveQuiescentGroupProof(t *testing.T) {
	for name, members := range map[string][]domain.RuntimeCleanupMember{
		"empty native snapshot": nil,
		"verified zombie":       {bug031Member(12346, macProcessStatusZombie)},
		"verified zombie group": {bug031Member(12346, macProcessStatusZombie), bug031Member(12347, macProcessStatusZombie)},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) {
				calls++
				return members, nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("quiescent proof calls=%d error=%v", calls, err)
			}
		})
	}
	calls := 0
	if err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) {
		calls++
		if calls < 3 {
			return []domain.RuntimeCleanupMember{bug031Member(12346, 3)}, nil
		}
		return nil, nil
	}); err != nil || calls != 3 {
		t.Fatalf("transient live member proof calls=%d error=%v", calls, err)
	}
}

func TestBUG031MacCleanupRefusesLiveAndMixedGroupsWithBoundedEvidence(t *testing.T) {
	for name, members := range map[string][]domain.RuntimeCleanupMember{
		"live":  {bug031Member(12346, 3)},
		"mixed": {bug031Member(12346, 3), bug031Member(12347, macProcessStatusZombie)},
	} {
		t.Run(name, func(t *testing.T) {
			before := append([]domain.RuntimeCleanupMember(nil), members...)
			err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), 20*time.Millisecond, func(int) ([]domain.RuntimeCleanupMember, error) { return members, nil })
			d := bug031Diagnostic(t, err)
			if !errors.Is(err, ErrRuntimeOwnershipRecord) || d.Stage != "group_wait" || d.Code != "group_still_runnable" ||
				d.RunnableCount != 1 || d.ZombieCount != len(members)-1 || d.MemberCount != len(members) || !reflect.DeepEqual(before, members) {
				t.Fatalf("live group refusal=%+v error=%v", d, err)
			}
			copy := d.Members[0]
			d.Members[0].PID = 777
			if again := bug031Diagnostic(t, err); again.Members[0] != copy {
				t.Fatal("diagnostic exposed a mutable internal member slice")
			}
		})
	}
}

func TestBUG031MacCleanupRefusesIncompleteAndChangingEvidence(t *testing.T) {
	base := bug031Member(12346, macProcessStatusZombie)
	for name, mutate := range map[string]func(*domain.RuntimeCleanupMember){
		"invalid pid":           func(m *domain.RuntimeCleanupMember) { m.PID = 0 },
		"unknown status":        func(m *domain.RuntimeCleanupMember) { m.Status = 0 },
		"foreign uid":           func(m *domain.RuntimeCleanupMember) { m.UID = 503 },
		"changed group":         func(m *domain.RuntimeCleanupMember) { m.ProcessGroupID = 999 },
		"root reappeared":       func(m *domain.RuntimeCleanupMember) { m.PID = 12345 },
		"missing incarnation":   func(m *domain.RuntimeCleanupMember) { m.ProcessStartIdentity = "" },
		"malformed incarnation": func(m *domain.RuntimeCleanupMember) { m.ProcessStartIdentity = "private-path-should-not-log" },
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			mutate(&m)
			err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) { return []domain.RuntimeCleanupMember{m}, nil })
			d := bug031Diagnostic(t, err)
			if d.Code != "group_identity_changed" && d.Code != "group_member_unknown" {
				t.Fatalf("unsafe group proof diagnostic=%+v", d)
			}
		})
	}
	for name, members := range map[string][]domain.RuntimeCleanupMember{
		"duplicate member":  {base, base},
		"snapshot overflow": make([]domain.RuntimeCleanupMember, 4097),
	} {
		t.Run(name, func(t *testing.T) {
			err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) { return members, nil })
			bug031Diagnostic(t, err)
		})
	}
	calls := 0
	err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) {
		calls++
		m := bug031Member(12346, 3)
		if calls > 1 {
			m.Status, m.ProcessStartIdentity = macProcessStatusZombie, "1794378081786999"
		}
		return []domain.RuntimeCleanupMember{m}, nil
	})
	if d := bug031Diagnostic(t, err); d.Code != "group_identity_changed" || calls != 2 {
		t.Fatalf("incarnation replacement was accepted: %+v calls=%d", d, calls)
	}
}

func TestBUG031MacCleanupInspectionErrorAndCancellationNeverProveAbsence(t *testing.T) {
	for name, cause := range map[string]error{"permission denied": syscall.EPERM, "incomplete native read": syscall.EIO,
		"full native snapshot":   macCleanupInspectionFailure("group_observation_incomplete", syscall.EOVERFLOW),
		"native membership race": macCleanupInspectionFailure("group_identity_changed", syscall.ESRCH)} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := waitMacCleanupGroupQuiescent(context.Background(), bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) {
				calls++
				if calls == 1 {
					return []domain.RuntimeCleanupMember{bug031Member(12346, 3)}, nil
				}
				return nil, cause
			})
			d := bug031Diagnostic(t, err)
			if !errors.Is(err, cause) || calls != 2 || d.MemberCount != 0 || len(d.Members) != 0 {
				t.Fatalf("inspection failure contains stale member evidence: %+v error=%v", d, err)
			}
			if name == "permission denied" && d.Code != "group_inspection_denied" {
				t.Fatalf("permission denial category=%s", d.Code)
			}
		})
	}
	for _, during := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		if !during {
			cancel()
		}
		err := waitMacCleanupGroupQuiescent(ctx, bug031Owner(), time.Second, func(int) ([]domain.RuntimeCleanupMember, error) {
			calls++
			cancel()
			return nil, nil
		})
		cancel()
		if d := bug031Diagnostic(t, err); !errors.Is(err, context.Canceled) || d.Code != "cancelled" || (!during && calls != 0) {
			t.Fatalf("cancelled absence proof=%+v calls=%d error=%v", d, calls, err)
		}
	}
}

func TestBUG031MacNativeSamplerBindsMembershipAndIncarnationAcrossReadCut(t *testing.T) {
	base := bug031Member(12346, macProcessStatusZombie)
	for name, tc := range map[string]struct {
		lists [][]int
		reads func(int, int) (domain.RuntimeCleanupMember, bool, error)
		want  bool
	}{
		"stable zombie":                 {lists: [][]int{{12346}, {12346}, {12346}}, want: true},
		"stable empty":                  {lists: [][]int{nil, nil, nil}, want: true},
		"new member joined":             {lists: [][]int{{12346}, {12346, 12347}, {12346, 12347}}},
		"new member joined final cut":   {lists: [][]int{{12346}, {12346}, {12346, 12347}}},
		"member moved to another group": {lists: [][]int{{12346}, nil, nil}},
		"same PID replaced": {lists: [][]int{{12346}, {12346}, {12346}}, reads: func(pid, call int) (domain.RuntimeCleanupMember, bool, error) {
			m := base
			if call > 1 {
				m.ProcessStartIdentity = "1794378081786999"
			}
			return m, true, nil
		}},
		"member genuinely gone": {lists: [][]int{{12346}, nil, nil}, reads: func(pid, call int) (domain.RuntimeCleanupMember, bool, error) {
			if call > 1 {
				return domain.RuntimeCleanupMember{}, false, nil
			}
			return base, true, nil
		}, want: true},
		"member gone before first read": {lists: [][]int{{12346}, nil, nil}, reads: func(int, int) (domain.RuntimeCleanupMember, bool, error) {
			return domain.RuntimeCleanupMember{}, false, nil
		}, want: true},
		"member gone during second read": {lists: [][]int{{12346}, {12346}, nil}, reads: func(pid, call int) (domain.RuntimeCleanupMember, bool, error) {
			if call > 1 {
				return domain.RuntimeCleanupMember{}, false, nil
			}
			return base, true, nil
		}, want: true},
		"UID changed": {lists: [][]int{{12346}, {12346}, {12346}}, reads: func(pid, call int) (domain.RuntimeCleanupMember, bool, error) {
			m := base
			if call > 1 {
				m.UID = 503
			}
			return m, true, nil
		}},
		"zombie became live": {lists: [][]int{{12346}, {12346}, {12346}}, reads: func(pid, call int) (domain.RuntimeCleanupMember, bool, error) {
			m := base
			if call > 1 {
				m.Status = 3
			}
			return m, true, nil
		}},
		"initial unknown state": {lists: [][]int{{12346}, {12346}, {12346}}, reads: func(pid, call int) (domain.RuntimeCleanupMember, bool, error) {
			m := base
			if call == 1 {
				m.Status = 0
			}
			return m, true, nil
		}},
	} {
		t.Run(name, func(t *testing.T) {
			listCalls, readCalls := 0, 0
			members, err := sampleMacCleanupGroup(12345, func(int) ([]int, error) {
				value := tc.lists[listCalls]
				listCalls++
				return value, nil
			}, func(pid int) (domain.RuntimeCleanupMember, bool, error) {
				readCalls++
				if tc.reads != nil {
					return tc.reads(pid, readCalls)
				}
				return base, true, nil
			})
			if tc.want && err != nil {
				t.Fatalf("complete stable/gone proof refused: %v", err)
			} else if !tc.want && err == nil {
				t.Fatalf("changed native proof was accepted: %+v", members)
			}
		})
	}
	for name, list := range map[string]func(int) ([]int, error){
		"permission denied": func(int) ([]int, error) { return nil, syscall.EPERM },
		"overflow":          func(int) ([]int, error) { return make([]int, 4097), nil },
		"invalid PID":       func(int) ([]int, error) { return []int{0}, nil },
		"duplicate PID":     func(int) ([]int, error) { return []int{12346, 12346}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			if _, err := sampleMacCleanupGroup(12345, list, func(int) (domain.RuntimeCleanupMember, bool, error) {
				called = true
				return base, true, nil
			}); err == nil || called {
				t.Fatalf("incomplete native list reached a positive proof: called=%t error=%v", called, err)
			}
		})
	}
	for _, failAt := range []int{2, 3} {
		calls := 0
		_, err := sampleMacCleanupGroup(12345, func(int) ([]int, error) {
			calls++
			if calls == failAt {
				return nil, syscall.EPERM
			}
			return []int{12346}, nil
		}, func(int) (domain.RuntimeCleanupMember, bool, error) { return base, true, nil })
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("denied final native list became absence proof: calls=%d error=%v", calls, err)
		}
	}
}
