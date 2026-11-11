package domain

import (
	"strings"
	"testing"
)

func TestRuntimeCleanupDiagnosticAcceptsBoundedObservedAndUnavailableEvidence(t *testing.T) {
	for _, d := range []RuntimeCleanupDiagnostic{
		{Stage: "context", Code: "cancelled"},
		{Stage: "group_wait", Code: "group_still_runnable", RootPID: 123, ProcessGroupID: 123,
			RootStartIdentity: "1794378081786412", OwnerUID: 502, ElapsedMS: 500, GraceMS: 500,
			SnapshotCount: 40, MemberCount: 1, RunnableCount: 1,
			Members: []RuntimeCleanupMember{{PID: 456, ParentPID: 1, ProcessGroupID: 123, UID: 502, Status: 2}}},
		{Stage: "group_inspection", Code: "group_observation_incomplete", MemberCount: 17, MembersTruncated: true},
		{Stage: "group_wait", Code: "group_still_runnable", ObservedStartedAt: "2026-11-11T06:20:42Z",
			ObservedEndedAt: "2026-11-11T06:20:42.500001Z", DeadlineAt: "2026-11-11T06:20:42.5Z"},
	} {
		if !ValidateRuntimeCleanupDiagnostic(d) {
			t.Fatalf("bounded observation refused: %+v", d)
		}
	}
}

func TestRuntimeCleanupDiagnosticRejectsUntrustedStringsAndUnboundedMetadata(t *testing.T) {
	valid := RuntimeCleanupDiagnostic{Stage: "group_wait", Code: "group_still_runnable", RootPID: 123,
		ProcessGroupID: 123, OwnerUID: 502, MemberCount: 1, RunnableCount: 1,
		Members: []RuntimeCleanupMember{{PID: 456, ParentPID: 1, ProcessGroupID: 123, UID: 502, Status: 2}}}
	for name, mutate := range map[string]func(*RuntimeCleanupDiagnostic){
		"raw cause stage":      func(d *RuntimeCleanupDiagnostic) { d.Stage = "/private/token-secret" },
		"raw cause code":       func(d *RuntimeCleanupDiagnostic) { d.Code = "script=private-payload" },
		"root start payload":   func(d *RuntimeCleanupDiagnostic) { d.RootStartIdentity = "private-password" },
		"unbounded root start": func(d *RuntimeCleanupDiagnostic) { d.RootStartIdentity = strings.Repeat("1", 33) },
		"negative pid":         func(d *RuntimeCleanupDiagnostic) { d.RootPID = -123 },
		"negative duration":    func(d *RuntimeCleanupDiagnostic) { d.ElapsedMS = -1 },
		"unbounded duration":   func(d *RuntimeCleanupDiagnostic) { d.ElapsedMS = 86_400_001 },
		"unbounded grace":      func(d *RuntimeCleanupDiagnostic) { d.GraceMS = 60_001 },
		"unbounded snapshots":  func(d *RuntimeCleanupDiagnostic) { d.SnapshotCount = 10_001 },
		"unbounded group":      func(d *RuntimeCleanupDiagnostic) { d.MemberCount = 4097; d.MembersTruncated = true },
		"bad total":            func(d *RuntimeCleanupDiagnostic) { d.ZombieCount = 1 },
		"missing truncation":   func(d *RuntimeCleanupDiagnostic) { d.MemberCount = 2 },
		"false truncation":     func(d *RuntimeCleanupDiagnostic) { d.MembersTruncated = true },
		"timestamp payload":    func(d *RuntimeCleanupDiagnostic) { d.ObservedStartedAt = "private-password" },
		"unpaired timestamp":   func(d *RuntimeCleanupDiagnostic) { d.ObservedStartedAt = "2026-11-11T06:20:42Z" },
		"deadline without interval": func(d *RuntimeCleanupDiagnostic) {
			d.DeadlineAt = "2026-11-11T06:20:42Z"
		},
		"non UTC timestamp": func(d *RuntimeCleanupDiagnostic) {
			d.ObservedStartedAt = "2026-11-11T06:20:42+01:00"
			d.ObservedEndedAt = "2026-11-11T06:20:43+01:00"
		},
		"reversed timestamps": func(d *RuntimeCleanupDiagnostic) {
			d.ObservedStartedAt = "2026-11-11T06:20:43Z"
			d.ObservedEndedAt = "2026-11-11T06:20:42Z"
		},
		"unbounded interval": func(d *RuntimeCleanupDiagnostic) {
			d.ObservedStartedAt = "2026-11-11T06:20:42Z"
			d.ObservedEndedAt = "2026-11-13T06:20:42Z"
		},
		"deadline before observation": func(d *RuntimeCleanupDiagnostic) {
			d.ObservedStartedAt = "2026-11-11T06:20:42Z"
			d.ObservedEndedAt = "2026-11-11T06:20:43Z"
			d.DeadlineAt = "2026-11-11T06:20:41Z"
		},
		"member start payload": func(d *RuntimeCleanupDiagnostic) { d.Members[0].ProcessStartIdentity = "argv-secret" },
		"duplicate member": func(d *RuntimeCleanupDiagnostic) {
			d.Members = append(d.Members, d.Members[0])
			d.MemberCount = 2
			d.RunnableCount = 2
		},
		"unbounded members": func(d *RuntimeCleanupDiagnostic) {
			d.Members = nil
			for i := 0; i <= MaxRuntimeCleanupDiagnosticMembers; i++ {
				d.Members = append(d.Members, RuntimeCleanupMember{PID: i + 1, ProcessGroupID: 123})
			}
			d.MemberCount = len(d.Members)
			d.RunnableCount = d.MemberCount
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := valid
			d.Members = append([]RuntimeCleanupMember(nil), valid.Members...)
			mutate(&d)
			if ValidateRuntimeCleanupDiagnostic(d) {
				t.Fatal("unsafe diagnostic accepted")
			}
		})
	}
}
