package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrReleaseTransportPKIRotation = errors.New("release-transport PKI rotation rejected")

type ReleaseTransportPKIRotationHost struct {
	HostID                      string
	OldCAFingerprint            string
	NewCAFingerprint            string
	OldCertificateFingerprint   string
	OldSPKIFingerprint          string
	NewCertificateFingerprint   string
	NewSPKIFingerprint          string
	PreviousActivePeerSetDigest string
	RequestID                   string
	IssuerReceiptID             string
	PackageDigest               string
	ActivePeerSetDigest         string
	RollbackArtifactDigest      string
}

// ReleaseTransportPKIRotationPlan contains only signed record IDs and public
// fingerprints. It cannot carry private keys, CSRs, installer paths, or
// commands. Hosts are fixed in Mac, linux, sandbox, factory order.
type ReleaseTransportPKIRotationPlan struct {
	PreviousManifestID       string
	OverlapManifestID        string
	RetirementManifestID     string
	RequiredLedgerGeneration uint64
	OverlapNotAfter          time.Time
	Hosts                    []ReleaseTransportPKIRotationHost
}

func (p ReleaseTransportPKIRotationPlan) Validate(now time.Time) error {
	if !isManagementRecordID(p.PreviousManifestID) || !isManagementRecordID(p.OverlapManifestID) ||
		!isManagementRecordID(p.RetirementManifestID) || p.PreviousManifestID == p.OverlapManifestID ||
		p.OverlapManifestID == p.RetirementManifestID || p.RequiredLedgerGeneration == 0 || now.IsZero() ||
		!p.OverlapNotAfter.After(now) || p.OverlapNotAfter.Sub(now) > maxPKIOverlapValidity || len(p.Hosts) != len(canonicalHosts) {
		return ErrReleaseTransportPKIRotation
	}
	unique := make(map[string]struct{}, len(p.Hosts)*6)
	for i, host := range p.Hosts {
		if host.HostID != releaseTransportHostOrder[i] || !isManagementDigest(host.OldCAFingerprint) || !isManagementDigest(host.NewCAFingerprint) ||
			host.OldCAFingerprint == host.NewCAFingerprint || !isManagementDigest(host.OldCertificateFingerprint) ||
			!isManagementDigest(host.NewCertificateFingerprint) || host.OldCertificateFingerprint == host.NewCertificateFingerprint ||
			!isManagementDigest(host.OldSPKIFingerprint) || !isManagementDigest(host.NewSPKIFingerprint) || host.OldSPKIFingerprint == host.NewSPKIFingerprint ||
			!isManagementRecordID(host.PreviousActivePeerSetDigest) ||
			!isManagementRecordID(host.RequestID) || !isManagementRecordID(host.IssuerReceiptID) || !isManagementRecordID(host.PackageDigest) ||
			!isManagementRecordID(host.ActivePeerSetDigest) || !isManagementDigest(host.RollbackArtifactDigest) {
			return ErrReleaseTransportPKIRotation
		}
		if host.PreviousActivePeerSetDigest == host.ActivePeerSetDigest {
			return ErrReleaseTransportPKIRotation
		}
		for _, value := range []string{host.RequestID, host.IssuerReceiptID, host.PackageDigest, host.ActivePeerSetDigest, host.PreviousActivePeerSetDigest,
			host.OldCertificateFingerprint, host.OldSPKIFingerprint, host.NewCertificateFingerprint, host.NewSPKIFingerprint} {
			if _, exists := unique[value]; exists {
				return ErrReleaseTransportPKIRotation
			}
			unique[value] = struct{}{}
		}
	}
	return nil
}

type PKIRotationRollbackReceipt string

type ReleaseTransportPKIRotationBackend interface {
	FreshPKIRotationGuard(context.Context, string) (ManagementUpdateGuard, error)
	CapturePKIRotationProtectedState(context.Context, string) (ManagementProtectedState, error)
	PreparePKIRotationRollback(context.Context, ReleaseTransportPKIRotationHost) (PKIRotationRollbackReceipt, error)
	InstallDualTrustAndOverlapPeers(context.Context, PKIRotationRollbackReceipt, ReleaseTransportPKIRotationHost, time.Time) error
	InstallNewTransportLeafAndReload(context.Context, PKIRotationRollbackReceipt, ReleaseTransportPKIRotationHost) error
	ProbeNewTransportChainAndTime(context.Context, ReleaseTransportPKIRotationHost) error
	VerifyOldTransportLeafAccepted(context.Context, ReleaseTransportPKIRotationHost) error
	RollbackToPriorTransport(context.Context, PKIRotationRollbackReceipt, ReleaseTransportPKIRotationHost) error
	VerifyPriorTransport(context.Context, PKIRotationRollbackReceipt, ReleaseTransportPKIRotationHost) error
	VerifyOldTransportLeafDenied(context.Context, ReleaseTransportPKIRotationHost) error
	CommitRetirementManifest(context.Context, string) error
	RecordTransportLeafRetirement(context.Context, ReleaseTransportPKIRotationHost) error
	RemoveRetiredTransportLeaf(context.Context, ReleaseTransportPKIRotationHost) error
}

// RunReleaseTransportPKIRotation serially enables bounded dual trust and the
// old/new active-peer set, installs and probes the new identity at every
// endpoint, then commits retirement. It proves old-leaf denial at all
// endpoints before recording revocation or deleting any old leaf material.
// A failure during overlap rolls back the current host to its working chain;
// previously updated hosts retain the bounded overlap, so the old chain stays
// accepted and no retirement starts.
func RunReleaseTransportPKIRotation(ctx context.Context, plan ReleaseTransportPKIRotationPlan, backend ReleaseTransportPKIRotationBackend, now func() time.Time) error {
	if ctx == nil || backend == nil || now == nil || plan.Validate(now().UTC()) != nil {
		return ErrReleaseTransportPKIRotation
	}
	protectedBefore := make(map[string]ManagementProtectedState, len(plan.Hosts))
	for _, host := range plan.Hosts {
		guard, err := backend.FreshPKIRotationGuard(ctx, host.HostID)
		if err != nil || guard.HostID != host.HostID || guard.RequiredLedgerGeneration < plan.RequiredLedgerGeneration ||
			guard.InstalledManifestID != plan.PreviousManifestID || guard.ActivePeerSetDigest != host.PreviousActivePeerSetDigest ||
			guard.RollbackArtifactDigest != host.RollbackArtifactDigest || ValidateManagementUpdateGuard(guard) != nil ||
			plan.Validate(guard.Now.UTC()) != nil {
			return errors.Join(ErrReleaseTransportPKIRotation, err)
		}
		before, err := backend.CapturePKIRotationProtectedState(ctx, host.HostID)
		if err != nil || before.HostID != host.HostID || before.Validate() != nil {
			return errors.Join(ErrReleaseTransportPKIRotation, err)
		}
		protectedBefore[host.HostID] = before
		deadline := time.Now().Add(ManagementUpdateDeadline)
		operationCtx, cancel := context.WithDeadline(ctx, deadline)
		receipt, prepErr := backend.PreparePKIRotationRollback(operationCtx, host)
		if prepErr != nil || receipt == "" {
			cancel()
			return errors.Join(ErrReleaseTransportPKIRotation, prepErr)
		}
		fail := func(cause error) error {
			rollbackCtx, rollbackCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
			defer rollbackCancel()
			rbErr := backend.RollbackToPriorTransport(rollbackCtx, receipt, host)
			verifyErr := backend.VerifyPriorTransport(rollbackCtx, receipt, host)
			restored, stateErr := backend.CapturePKIRotationProtectedState(rollbackCtx, host.HostID)
			if rbErr != nil || verifyErr != nil || stateErr != nil || restored != before {
				return errors.Join(ErrManagementRollback, ErrReleaseTransportPKIRotation, cause, rbErr, verifyErr, stateErr)
			}
			return errors.Join(ErrReleaseTransportPKIRotation, cause)
		}
		if err := backend.InstallDualTrustAndOverlapPeers(operationCtx, receipt, host, plan.OverlapNotAfter); err != nil {
			cancel()
			return fail(err)
		}
		if err := backend.InstallNewTransportLeafAndReload(operationCtx, receipt, host); err != nil {
			cancel()
			return fail(err)
		}
		if err := backend.ProbeNewTransportChainAndTime(operationCtx, host); err != nil {
			cancel()
			return fail(err)
		}
		if err := backend.VerifyOldTransportLeafAccepted(operationCtx, host); err != nil {
			cancel()
			return fail(err)
		}
		after, err := backend.CapturePKIRotationProtectedState(operationCtx, host.HostID)
		if err != nil || after != before {
			cancel()
			return fail(errors.Join(ErrReleaseTransportPKIRotation, err))
		}
		cancel()
	}
	// Re-read every host immediately before publishing the old-leaf retirement
	// manifest. The overlap state and all unrelated protected state must still
	// match the signed operation after the serial installs finish.
	for _, host := range plan.Hosts {
		guard, err := backend.FreshPKIRotationGuard(ctx, host.HostID)
		if err != nil || guard.HostID != host.HostID || guard.InstalledManifestID != plan.OverlapManifestID ||
			guard.ActivePeerSetDigest != host.ActivePeerSetDigest || guard.RollbackArtifactDigest != host.RollbackArtifactDigest ||
			guard.RequiredLedgerGeneration < plan.RequiredLedgerGeneration || ValidateManagementUpdateGuard(guard) != nil ||
			plan.Validate(guard.Now.UTC()) != nil {
			return errors.Join(ErrReleaseTransportPKIRotation, err)
		}
		after, err := backend.CapturePKIRotationProtectedState(ctx, host.HostID)
		if err != nil || after != protectedBefore[host.HostID] {
			return errors.Join(ErrReleaseTransportPKIRotation, err)
		}
	}
	if err := backend.CommitRetirementManifest(ctx, plan.RetirementManifestID); err != nil {
		return errors.Join(ErrReleaseTransportPKIRotation, err)
	}
	for _, host := range plan.Hosts {
		if err := backend.VerifyOldTransportLeafDenied(ctx, host); err != nil {
			return errors.Join(ErrReleaseTransportPKIRotation, fmt.Errorf("old leaf still accepted at %s: %w", host.HostID, err))
		}
	}
	for _, host := range plan.Hosts {
		if err := backend.RecordTransportLeafRetirement(ctx, host); err != nil {
			return errors.Join(ErrReleaseTransportPKIRotation, err)
		}
		if err := backend.RemoveRetiredTransportLeaf(ctx, host); err != nil {
			return errors.Join(ErrReleaseTransportPKIRotation, err)
		}
	}
	return nil
}

var releaseTransportHostOrder = []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"}
