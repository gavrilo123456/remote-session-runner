package release

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
)

type l314Observer struct {
	live  lanecapacity.LiveObservation
	err   error
	calls int
}

func (o *l314Observer) ObserveCapacity(context.Context) (lanecapacity.LiveObservation, error) {
	o.calls++
	return o.live, o.err
}

func TestLanesL314AuthorityAndHostVerificationAcceptBothPolicyScopes(t *testing.T) {
	for _, scope := range []lanecapacity.Scope{lanecapacity.ScopeHostManagement, lanecapacity.ScopeLaneRuntime} {
		t.Run(string(scope), func(t *testing.T) {
			fixture := newL305Fixture(t)
			policyEnvelope, measurementEnvelope, live := l314SignedCapacityRecords(t, fixture, scope)
			verified, err := fixture.anchor.VerifyCapacityPolicy(context.Background(), policyEnvelope, measurementEnvelope, &l314Observer{live: live})
			if err != nil {
				t.Fatalf("valid %s policy rejected: %v", scope, err)
			}
			policy := verified.Policy()
			if verified.RecordID() == "" || verified.MeasurementRecordID() == "" || policy.Scope != scope || policy.HostID != "linux-host" {
				t.Fatalf("verification result lost policy identity: %+v", verified)
			}
		})
	}
}

func TestLanesL314RejectsPolicyBoundToDifferentMeasurementEnvelope(t *testing.T) {
	fixture := newL305Fixture(t)
	policyEnvelopeRaw, measurementEnvelopeRaw, live := l314SignedCapacityRecords(t, fixture, lanecapacity.ScopeLaneRuntime)
	var policyEnvelope Envelope
	if err := decodeStrict(policyEnvelopeRaw, &policyEnvelope); err != nil {
		t.Fatal(err)
	}
	var policy lanecapacity.Policy
	if err := decodeStrict(policyEnvelope.Payload, &policy); err != nil {
		t.Fatal(err)
	}
	policy.MeasurementID = strings.Repeat("f", 64)
	policyPayload := l302JSON(t, policy)
	policyEnvelopeRaw, err := SignEnvelope("capacity_policy", policyPayload,
		SigningIdentity{KeyID: fixture.authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: fixture.authority})
	if err != nil {
		t.Fatal(err)
	}
	observer := &l314Observer{live: live}
	if _, err := fixture.anchor.VerifyCapacityPolicy(context.Background(), policyEnvelopeRaw, measurementEnvelopeRaw, observer); !errors.Is(err, ErrCapacityPolicyRejected) || !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("policy naming a different signed measurement was not rejected: %v", err)
	}
	if observer.calls != 0 {
		t.Fatalf("invalid measurement binding triggered a live observation %d times", observer.calls)
	}
}

func TestLanesL314LiveRemeasurementRejectsChangedIdentityAndLowWater(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*lanecapacity.LiveObservation, time.Time)
		wantErr error
	}{
		{name: "resource capacity changed", mutate: func(live *lanecapacity.LiveObservation, _ time.Time) {
			live.ResourceCapacity.MemoryBytes++
		}, wantErr: lanecapacity.ErrIdentityChanged},
		{name: "quota device identity changed", mutate: func(live *lanecapacity.LiveObservation, _ time.Time) {
			live.LinuxDevices[1].MajorMinor = "259:99"
		}, wantErr: lanecapacity.ErrIdentityChanged},
		{name: "shared cache path changed", mutate: func(live *lanecapacity.LiveObservation, _ time.Time) {
			live.GoCachePaths[0] = "/tmp/replaced-cache"
		}, wantErr: lanecapacity.ErrIdentityChanged},
		{name: "backup device changed", mutate: func(live *lanecapacity.LiveObservation, _ time.Time) {
			live.Identity.BackupDeviceID = "other-backup"
		}, wantErr: lanecapacity.ErrIdentityChanged},
		{name: "backup low-water crossed", mutate: func(live *lanecapacity.LiveObservation, _ time.Time) {
			live.Usage.BackupBytes = 19 * lanecapacity.GiB
		}, wantErr: lanecapacity.ErrCapacityExceeded},
		{name: "observation is stale", mutate: func(live *lanecapacity.LiveObservation, now time.Time) {
			live.ObservedAt = now.Add(-2 * time.Minute)
		}, wantErr: lanecapacity.ErrStaleMeasurement},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newL305Fixture(t)
			policyEnvelope, measurementEnvelope, live := l314SignedCapacityRecords(t, fixture, lanecapacity.ScopeLaneRuntime)
			state, err := fixture.anchor.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&live, state.HighestAuthorityTime)
			_, err = fixture.anchor.VerifyCapacityPolicy(context.Background(), policyEnvelope, measurementEnvelope, &l314Observer{live: live})
			if !errors.Is(err, ErrCapacityPolicyRejected) || !errors.Is(err, tc.wantErr) {
				t.Fatalf("unsafe live observation error = %v, want capacity-policy rejection wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestLanesL314MacScopesBindAPFSQuotaCacheBackupAndFreeFloor(t *testing.T) {
	policyRaw, measurementRaw, measurementID := l303MacPolicy(t)
	var policy lanecapacity.Policy
	var attestation capacityMeasurementPayload
	if decodeStrict(policyRaw, &policy) != nil || decodeStrict(measurementRaw, &attestation) != nil {
		t.Fatal("decode isolated Mac capacity fixtures")
	}
	measurement := attestation.Measurement
	live := liveObservationFromMeasurement(measurement)
	now := measurement.ObservedAt.Add(time.Second)
	live.ObservedAt = now
	if err := ValidateCapacityPolicyAgainstLiveObservation(policyRaw, measurementID, measurementRaw, live, now); err != nil {
		t.Fatalf("valid Mac lane-runtime policy rejected: %v", err)
	}

	policy.Scope, policy.Lane, policy.LaneRuntime = lanecapacity.ScopeHostManagement, "", nil
	policy.HostManagement = &lanecapacity.HostManagementBudget{
		Components: []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper",
			"runner-release-transport-pki-installer", "runner-release-authority-signer", "runner-release-authority-agent",
			"runner-release-authority-approval", "runner-release-transport-pki-issuer"},
		PeakBytes: 2 * lanecapacity.GiB, StagingBudgetBytes: lanecapacity.GiB, LogBudgetBytes: lanecapacity.GiB,
		GoCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
		BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB,
		GoCachePaths: measurement.GoCachePaths,
		MacData: &lanecapacity.MacDataBudget{PRDHostBudgetBytes: 24 * lanecapacity.GiB,
			SharedCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
			SnapshotBudgetBytes: 10 * lanecapacity.GiB, BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB},
	}
	hostPolicyRaw := l302JSON(t, policy)
	if err := ValidateCapacityPolicyAgainstLiveObservation(hostPolicyRaw, measurementID, measurementRaw, live, now); err != nil {
		t.Fatalf("valid Mac host-management policy rejected: %v", err)
	}

	changed := live
	changed.StorageVolumes = append([]lanecapacity.StorageVolume(nil), live.StorageVolumes...)
	changed.StorageVolumes[1].QuotaBytes++
	if err := ValidateCapacityPolicyAgainstLiveObservation(hostPolicyRaw, measurementID, measurementRaw, changed, now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("changed APFS quota identity accepted: %v", err)
	}
	changed = live
	changed.GoCachePaths = []string{lanecapacity.MacGoBuildCachePath, "/private/tmp/replaced-module-cache"}
	if err := ValidateCapacityPolicyAgainstLiveObservation(hostPolicyRaw, measurementID, measurementRaw, changed, now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("changed shared cache paths accepted: %v", err)
	}
	changed = live
	changed.Identity.BackupDeviceID = "disk9"
	if err := ValidateCapacityPolicyAgainstLiveObservation(hostPolicyRaw, measurementID, measurementRaw, changed, now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("changed backup device identity accepted: %v", err)
	}
	changed = live
	changed.Usage.BackupBytes = 19 * lanecapacity.GiB
	if err := ValidateCapacityPolicyAgainstLiveObservation(hostPolicyRaw, measurementID, measurementRaw, changed, now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("backup low-water violation accepted: %v", err)
	}
	changed = live
	changed.Usage.SnapshotBytes = 11 * lanecapacity.GiB
	if err := ValidateCapacityPolicyAgainstLiveObservation(hostPolicyRaw, measurementID, measurementRaw, changed, now); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Mac snapshot/free-space floor violation accepted: %v", err)
	}
}

func l314SignedCapacityRecords(t *testing.T, fixture *l305Fixture, scope lanecapacity.Scope) (policyEnvelope, measurementEnvelope []byte, live lanecapacity.LiveObservation) {
	t.Helper()
	acceptedAt := fixture.acceptProbe(t)
	ledger := fixture.advanceLedger(t, 1, fixture.bootstrap.BootstrapLedgerAnchorID,
		fixture.clock.Add(-time.Minute), fixture.clock.Add(14*time.Minute))
	if err := fixture.anchor.ApplyTrustRecord(ledger); err != nil {
		t.Fatalf("install isolated fresh test ledger: %v", err)
	}
	state, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	now := state.HighestAuthorityTime
	if !now.Equal(acceptedAt) {
		t.Fatalf("anchor accepted time changed: got %s want %s", now, acceptedAt)
	}
	measurement := l314LinuxMeasurement(t, now)
	measurementDigest, err := measurement.EvaluationDigest()
	if err != nil {
		t.Fatal(err)
	}
	measurementPayload := capacityMeasurementPayload{HostID: "linux-host", Measurement: measurement,
		EvidenceRefs: []string{l303Digest}, EvaluationDigest: measurementDigest}
	measurementRaw := l302JSON(t, measurementPayload)
	if err := ValidateCapacityMeasurementAttestation(measurementRaw); err != nil {
		t.Fatalf("isolated measurement invalid: %v", err)
	}
	hostKeyID, hostPrivate := l304PrivateKey(32)
	measurementEnvelope, err = SignEnvelope("capacity_measurement_attestation", measurementRaw,
		SigningIdentity{KeyID: hostKeyID, Role: SignerRoleHostAttestation, HostID: "linux-host", PrivateKey: hostPrivate})
	if err != nil {
		t.Fatal(err)
	}
	verifiedMeasurement, err := VerifyEnvelope(measurementEnvelope, anchorHostAttestationKeys(state, now))
	if err != nil {
		t.Fatal(err)
	}
	policy := l314LinuxPolicy(measurement, verifiedMeasurement.RecordID, scope)
	policyRaw := l302JSON(t, policy)
	policyEnvelope, err = SignEnvelope("capacity_policy", policyRaw,
		SigningIdentity{KeyID: fixture.authorityID, Role: SignerRoleReleaseAuthority, PrivateKey: fixture.authority})
	if err != nil {
		t.Fatal(err)
	}
	live = liveObservationFromMeasurement(measurement)
	live.ObservedAt = now
	return policyEnvelope, measurementEnvelope, live
}

func l314LinuxMeasurement(t *testing.T, now time.Time) lanecapacity.Measurement {
	t.Helper()
	resource := lanecapacity.ResourceVector{CPUMilli: 32_000, MemoryBytes: 128 * lanecapacity.GiB, Tasks: 100_000,
		IOReadBytesPerSecond: lanecapacity.GiB, IOWriteBytesPerSecond: lanecapacity.GiB, IOReadIOPSMax: 100_000, IOWriteIOPSMax: 100_000}
	paths := []string{"/home/ubuntu/.cache/go-build", "/home/ubuntu/go/pkg/mod"}
	measurement := lanecapacity.Measurement{ID: "l314-linux-measurement", Identity: lanecapacity.Identity{
		HostID: "linux-host", Platform: lanecapacity.PlatformLinux, DeviceID: "lane", Filesystem: "ext4", Mountpoint: "/home/ubuntu",
		CapacityBytes: 4 * 1024 * lanecapacity.GiB, CapacityInodes: 10_000_000,
		BackupDeviceID: "backup", BackupFilesystem: "ext4", BackupMountpoint: lanecapacity.LinuxBackupMountpoint, BackupCapacityBytes: 20 * lanecapacity.GiB},
		ObservedAt: now.Add(-5 * time.Second), ExpiresAt: now.Add(2 * time.Minute), ResourceCapacity: resource, GoCachePaths: paths,
		LinuxDevices: []lanecapacity.LinuxDevice{
			{ID: "backup", UUID: "22222222-2222-2222-2222-222222222222", MajorMinor: "259:2", Filesystem: "ext4",
				Mountpoint: lanecapacity.LinuxBackupMountpoint, CapacityBytes: 20 * lanecapacity.GiB, CapacityInodes: 1_000_000,
				ProjectQuotaFeature: true, ProjectQuotaActive: true},
			{ID: "lane", UUID: "11111111-1111-1111-1111-111111111111", MajorMinor: "259:1", Filesystem: "ext4",
				Mountpoint: lanecapacity.LinuxCapacityMountpoint, CapacityBytes: 96 * lanecapacity.GiB, CapacityInodes: 4_000_000,
				IOReadBytesPerSecondCapacity: lanecapacity.GiB, IOWriteBytesPerSecondCapacity: lanecapacity.GiB,
				IOReadIOPSCapacity: 100_000, IOWriteIOPSCapacity: 100_000, ProjectQuotaFeature: true, ProjectQuotaActive: true},
		},
		Usage: lanecapacity.Usage{UsedBytes: 100 * lanecapacity.GiB, UsedInodes: 2_000_000, GoCacheBytes: lanecapacity.GiB,
			ReleaseCacheBytes: lanecapacity.GiB, BackupBytes: 2 * lanecapacity.GiB}}
	if _, err := measurement.EvaluationDigest(); err != nil {
		t.Fatalf("invalid isolated Linux capacity measurement: %v", err)
	}
	return measurement
}

func l314LinuxPolicy(measurement lanecapacity.Measurement, recordID string, scope lanecapacity.Scope) lanecapacity.Policy {
	resource := func(cpu, memory, tasks, ioBytes, iops uint64) lanecapacity.ResourceVector {
		return lanecapacity.ResourceVector{CPUMilli: cpu, MemoryBytes: memory, Tasks: tasks,
			IOReadBytesPerSecond: ioBytes, IOWriteBytesPerSecond: ioBytes, IOReadIOPSMax: iops, IOWriteIOPSMax: iops}
	}
	digest, _ := measurement.EvaluationDigest()
	policy := lanecapacity.Policy{Version: 1, HostID: "linux-host", Scope: scope, MeasurementID: recordID,
		EvaluationDigest: digest, NotBefore: measurement.ObservedAt, NotAfter: measurement.ExpiresAt, Compute: lanecapacity.ComputeLinuxCgroupV2}
	if scope == lanecapacity.ScopeHostManagement {
		policy.HostManagement = &lanecapacity.HostManagementBudget{
			Components: []string{"runner-release-builder", "runner-release-transport", "runner-security-anchor-helper", "runner-release-transport-pki-installer"},
			PeakBytes:  2 * lanecapacity.GiB, PeakInodes: 100_000, StagingBudgetBytes: lanecapacity.GiB, LogBudgetBytes: lanecapacity.GiB,
			GoCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
			BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB,
			GoCachePaths: measurement.GoCachePaths, IODeviceID: "lane",
			Resources: lanecapacity.ResourceEnvelope{PRDFloor: resource(2_000, 8*lanecapacity.GiB, 10_000, 100*1024*1024, 10_000),
				ManagementHardMax: resource(1_000, 4*lanecapacity.GiB, 2_000, 100*1024*1024, 10_000)},
		}
		return policy
	}
	policy.Scope, policy.Lane = lanecapacity.ScopeLaneRuntime, "dev"
	policy.LaneRuntime = &lanecapacity.LaneRuntimeBudget{
		PRDGrowthBytes: 6 * lanecapacity.GiB, DevHardBytes: 4 * lanecapacity.GiB, QASHardBytes: 4 * lanecapacity.GiB,
		ManagementGrowthBytes: 2 * lanecapacity.GiB, PRDGrowthInodes: 50_000, DevHardInodes: 50_000,
		QASHardInodes: 50_000, ManagementGrowthInodes: 50_000, GoCachePaths: measurement.GoCachePaths,
		GoCacheBudgetBytes: 4 * lanecapacity.GiB, ReleaseCacheBudgetBytes: 2 * lanecapacity.GiB,
		BackupBudgetBytes: 3 * lanecapacity.GiB, BackupLowWaterBytes: 2 * lanecapacity.GiB, IODeviceID: "lane",
		Resources: lanecapacity.ResourceEnvelope{PRDFloor: resource(2_000, 8*lanecapacity.GiB, 10_000, 100*1024*1024, 10_000),
			DevHardMax:        resource(4_000, 8*lanecapacity.GiB, 10_000, 100*1024*1024, 10_000),
			QASHardMax:        resource(4_000, 8*lanecapacity.GiB, 10_000, 100*1024*1024, 10_000),
			ManagementHardMax: resource(1_000, 4*lanecapacity.GiB, 2_000, 100*1024*1024, 10_000)},
	}
	return policy
}

var _ CapacityPolicyObserver = (*l314Observer)(nil)
