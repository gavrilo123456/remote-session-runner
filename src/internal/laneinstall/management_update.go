package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ManagementDirectiveLeaseMargin = 900 * time.Second
	ManagementUpdateDeadline       = 600 * time.Second
	managementLedgerMaxAge         = 15 * time.Minute
)

var (
	ErrInvalidManagementUpdate = errors.New("host-management update guard or plan rejected")
	ErrManagementUpdate        = errors.New("host-management update failed")
	ErrManagementRollback      = errors.New("host-management rollback is not verified")
)

type ManagementLaneLease struct {
	Lane     string
	NotAfter time.Time
}

// ManagementUpdateGuard is returned by the protected host helper immediately
// before a host update. It contains only signed-ledger/directive facts and
// measured time bounds; callers cannot choose a weaker guard in the backend.
type ManagementUpdateGuard struct {
	InstalledManifestID      string
	ActivePeerSetDigest      string
	RollbackArtifactDigest   string
	HostID                   string
	LedgerGeneration         uint64
	RequiredLedgerGeneration uint64
	LedgerObservedAt         time.Time
	LedgerExpiresAt          time.Time
	Now                      time.Time
	AffectedLaneLeases       []ManagementLaneLease
	MaximumStageDuration     time.Duration
	MaximumRollbackDuration  time.Duration
}

func ValidateManagementUpdateGuard(guard ManagementUpdateGuard) error {
	if _, ok := canonicalHosts[guard.HostID]; !ok || !isManagementRecordID(guard.InstalledManifestID) ||
		!isManagementRecordID(guard.ActivePeerSetDigest) || !isManagementDigest(guard.RollbackArtifactDigest) ||
		guard.Now.IsZero() || guard.LedgerGeneration == 0 ||
		guard.LedgerGeneration < guard.RequiredLedgerGeneration || guard.LedgerObservedAt.IsZero() || guard.LedgerExpiresAt.IsZero() ||
		guard.LedgerObservedAt.After(guard.Now) || guard.Now.Sub(guard.LedgerObservedAt) > managementLedgerMaxAge || !guard.LedgerExpiresAt.After(guard.Now) ||
		guard.MaximumStageDuration <= 0 || guard.MaximumRollbackDuration <= 0 ||
		guard.MaximumRollbackDuration > ManagementUpdateDeadline ||
		guard.MaximumStageDuration > ManagementUpdateDeadline-guard.MaximumRollbackDuration {
		return ErrInvalidManagementUpdate
	}
	if len(guard.AffectedLaneLeases) != 3 {
		return ErrInvalidManagementUpdate
	}
	seen := make(map[string]struct{}, len(guard.AffectedLaneLeases))
	for _, lease := range guard.AffectedLaneLeases {
		if lease.Lane != "dev" && lease.Lane != "qas" && lease.Lane != "prd" || lease.NotAfter.IsZero() {
			return ErrInvalidManagementUpdate
		}
		if _, exists := seen[lease.Lane]; exists {
			return ErrInvalidManagementUpdate
		}
		seen[lease.Lane] = struct{}{}
		if lease.NotAfter.Before(guard.Now.Add(ManagementDirectiveLeaseMargin)) {
			return fmt.Errorf("%w: lane %s requires a planned signed closure before update", ErrInvalidManagementUpdate, lease.Lane)
		}
	}
	if len(seen) != 3 {
		return ErrInvalidManagementUpdate
	}
	return nil
}

type ManagementArtifactReference struct {
	Component      string
	ArtifactDigest string
}

// HostManagementUpdatePlan contains only immutable record and artifact IDs.
// The installer resolves those IDs through its verified cache; the plan has
// no caller-selected path, source, credential, or artifact bytes.
type HostManagementUpdatePlan struct {
	HostID                     string
	CurrentManifestID          string
	CurrentActivePeerSetDigest string
	TargetManifestID           string
	CurrentGeneration          uint64
	TargetGeneration           uint64
	RequiredLedgerGeneration   uint64
	RollbackArtifactDigest     string
	Artifacts                  []ManagementArtifactReference
}

func (p HostManagementUpdatePlan) Validate() error {
	if _, ok := canonicalHosts[p.HostID]; !ok || !isManagementRecordID(p.CurrentManifestID) || !isManagementRecordID(p.TargetManifestID) ||
		!isManagementRecordID(p.CurrentActivePeerSetDigest) || p.CurrentManifestID == p.TargetManifestID || p.CurrentGeneration == 0 || p.CurrentGeneration == ^uint64(0) ||
		p.TargetGeneration != p.CurrentGeneration+1 || p.RequiredLedgerGeneration == 0 || !isManagementDigest(p.RollbackArtifactDigest) {
		return ErrInvalidManagementUpdate
	}
	role, err := ManagementPackage(p.HostID)
	if err != nil || len(p.Artifacts) != len(role.Components) {
		return ErrInvalidManagementUpdate
	}
	for i, component := range role.Components {
		if p.Artifacts[i].Component != component.Name || !isManagementDigest(p.Artifacts[i].ArtifactDigest) {
			return ErrInvalidManagementUpdate
		}
	}
	return nil
}

type ManagementProtectedState struct {
	HostID                      string
	AuthorityStateDigest        string
	AuthorityPublicAnchorDigest string
	SecurityAnchorStateDigest   string
	LaneRootsDigest             string
	LaneProcessesDigest         string
	LaneConfigurationDigest     string
	LaneMailboxesDigest         string
	LaneIngressDigest           string
}

func (s ManagementProtectedState) Validate() error {
	if _, ok := canonicalHosts[s.HostID]; !ok || !isManagementDigest(s.AuthorityPublicAnchorDigest) ||
		!isManagementDigest(s.SecurityAnchorStateDigest) || !isManagementDigest(s.LaneRootsDigest) ||
		!isManagementDigest(s.LaneProcessesDigest) || !isManagementDigest(s.LaneConfigurationDigest) ||
		!isManagementDigest(s.LaneMailboxesDigest) || !isManagementDigest(s.LaneIngressDigest) {
		return ErrInvalidManagementUpdate
	}
	if s.HostID == "mac-control-plane" {
		if !isManagementDigest(s.AuthorityStateDigest) {
			return ErrInvalidManagementUpdate
		}
	} else if s.AuthorityStateDigest != "" {
		return ErrInvalidManagementUpdate
	}
	return nil
}

type ManagementRollbackReceipt string

// HostManagementUpdateBackend is the narrow privileged boundary used by the
// fixed installer. Implementations must use the root-verified candidate and
// exact rollback artifact from the plan; tests provide isolated fixtures.
type HostManagementUpdateBackend interface {
	FreshManagementUpdateGuard(context.Context, string) (ManagementUpdateGuard, error)
	CaptureManagementProtectedState(context.Context, string) (ManagementProtectedState, error)
	PrepareManagementRollback(context.Context, HostManagementUpdatePlan) (ManagementRollbackReceipt, error)
	StageManagementArtifacts(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error
	ActivateManagementUpdate(context.Context, ManagementRollbackReceipt) error
	VerifyManagementHealthAndTime(context.Context, ManagementRollbackReceipt) error
	CommitManagementManifest(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error
	RollbackManagementUpdate(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error
	VerifyManagementRollback(context.Context, ManagementRollbackReceipt, HostManagementUpdatePlan) error
}

// RunHostManagementUpdate stages and verifies one host-management revision.
// Any failure after rollback preparation restores and verifies the exact prior
// artifact. The same absolute ten-minute deadline covers the update and its
// rollback. The protected snapshot includes root trust and all active lane
// state, which must remain unchanged across both success and rollback.
func RunHostManagementUpdate(ctx context.Context, plan HostManagementUpdatePlan, backend HostManagementUpdateBackend) error {
	if ctx == nil || backend == nil || plan.Validate() != nil {
		return ErrInvalidManagementUpdate
	}
	guard, err := backend.FreshManagementUpdateGuard(ctx, plan.HostID)
	if err != nil || guard.HostID != plan.HostID || guard.InstalledManifestID != plan.CurrentManifestID ||
		guard.ActivePeerSetDigest != plan.CurrentActivePeerSetDigest || guard.RollbackArtifactDigest != plan.RollbackArtifactDigest ||
		guard.RequiredLedgerGeneration < plan.RequiredLedgerGeneration || ValidateManagementUpdateGuard(guard) != nil {
		return errors.Join(ErrInvalidManagementUpdate, err)
	}
	before, err := backend.CaptureManagementProtectedState(ctx, plan.HostID)
	if err != nil || before.HostID != plan.HostID || before.Validate() != nil {
		return errors.Join(ErrInvalidManagementUpdate, err)
	}
	deadline := time.Now().Add(ManagementUpdateDeadline)
	operationCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	receipt, err := backend.PrepareManagementRollback(operationCtx, plan)
	if err != nil {
		return errors.Join(ErrManagementUpdate, err)
	}
	if receipt == "" {
		return errors.Join(ErrManagementUpdate, ErrInvalidManagementUpdate)
	}
	rollback := func(cause error) error {
		rollbackCtx, rollbackCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		defer rollbackCancel()
		rbErr := backend.RollbackManagementUpdate(rollbackCtx, receipt, plan)
		verifyErr := backend.VerifyManagementRollback(rollbackCtx, receipt, plan)
		restored, stateErr := backend.CaptureManagementProtectedState(rollbackCtx, plan.HostID)
		if rbErr != nil || verifyErr != nil || stateErr != nil || restored != before {
			return errors.Join(ErrManagementRollback, ErrManagementUpdate, cause, rbErr, verifyErr, stateErr)
		}
		return errors.Join(ErrManagementUpdate, cause)
	}
	if err := backend.StageManagementArtifacts(operationCtx, receipt, plan); err != nil {
		return rollback(err)
	}
	if err := backend.ActivateManagementUpdate(operationCtx, receipt); err != nil {
		return rollback(err)
	}
	if err := backend.VerifyManagementHealthAndTime(operationCtx, receipt); err != nil {
		return rollback(err)
	}
	afterHealth, err := backend.CaptureManagementProtectedState(operationCtx, plan.HostID)
	if err != nil || afterHealth != before {
		return rollback(errors.Join(ErrInvalidManagementUpdate, err))
	}
	if err := backend.CommitManagementManifest(operationCtx, receipt, plan); err != nil {
		return rollback(err)
	}
	afterCommit, err := backend.CaptureManagementProtectedState(operationCtx, plan.HostID)
	if err != nil || afterCommit != before {
		return rollback(errors.Join(ErrInvalidManagementUpdate, err))
	}
	return nil
}

func isManagementRecordID(value string) bool {
	if len(value) != 64 || strings.Trim(value, "0123456789abcdef") != "" {
		return false
	}
	return true
}

func isManagementDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	return isManagementRecordID(strings.TrimPrefix(value, "sha256:"))
}
