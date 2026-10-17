package release

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
)

var ErrCapacityPolicyRejected = errors.New("host capacity policy rejected")

// CapacityPolicyObserver is implemented by the fixed protected host helper.
// It has no caller-selected paths or controller overrides.
type CapacityPolicyObserver interface {
	ObserveCapacity(context.Context) (lanecapacity.LiveObservation, error)
}

// VerifiedCapacityPolicy is a short-lived verification result. It is not a
// durable cache or authorization receipt; the caller uses it only after the
// helper has repeated the measurements and checked the policy envelope.
type VerifiedCapacityPolicy struct {
	recordID            string
	measurementRecordID string
	policy              lanecapacity.Policy
}

func (v VerifiedCapacityPolicy) RecordID() string            { return v.recordID }
func (v VerifiedCapacityPolicy) MeasurementRecordID() string { return v.measurementRecordID }
func (v VerifiedCapacityPolicy) Policy() lanecapacity.Policy {
	return cloneCapacityPolicy(v.policy)
}

// VerifyCapacityPolicy checks the exact authority-signed policy and host-
// signed measurement against this host's current anchored key/ledger state,
// obtains a fresh local observation, and reruns the capacity equations at
// the helper-accepted authority time. It does not mutate the anchor or install
// anything. Callers must use the returned policy immediately and still follow
// the separately approved installation protocol.
func (a *SecurityAnchor) VerifyCapacityPolicy(ctx context.Context, policyEnvelopeRaw, measurementEnvelopeRaw []byte,
	observer CapacityPolicyObserver) (VerifiedCapacityPolicy, error) {
	if a == nil || ctx == nil || observer == nil {
		return VerifiedCapacityPolicy{}, ErrCapacityPolicyRejected
	}
	state, err := a.Snapshot()
	if err != nil {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, err)
	}
	if state.HighestAuthorityTime.IsZero() {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, ErrInvalidTrustState)
	}
	if err := ValidateRevocationLedgerFreshForMutation(state.RevocationLedger, state.HighestAuthorityTime); err != nil {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, err)
	}
	now := state.HighestAuthorityTime.UTC()
	authorityKeys := anchorAuthorityKeys(state, now)
	hostKeys := anchorHostAttestationKeys(state, now)
	policyEnvelope, err := VerifyEnvelope(policyEnvelopeRaw, authorityKeys)
	if err != nil || policyEnvelope.RecordType != "capacity_policy" || isRevokedCapacityRecord(state, policyEnvelope.RecordID, policyEnvelope.SignerKeyID) {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, err)
	}
	measurementEnvelope, err := VerifyEnvelope(measurementEnvelopeRaw, hostKeys)
	if err != nil || measurementEnvelope.RecordType != "capacity_measurement_attestation" ||
		isRevokedCapacityRecord(state, measurementEnvelope.RecordID, measurementEnvelope.SignerKeyID) {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, err)
	}
	var policy lanecapacity.Policy
	var measurement capacityMeasurementPayload
	if decodeStrict(policyEnvelope.Payload, &policy) != nil || decodeStrict(measurementEnvelope.Payload, &measurement) != nil ||
		policy.HostID != state.HostID || measurement.HostID != state.HostID {
		return VerifiedCapacityPolicy{}, ErrCapacityPolicyRejected
	}
	if err := ValidateCapacityPolicyAgainstLiveObservation(policyEnvelope.Payload, measurementEnvelope.RecordID,
		measurementEnvelope.Payload, liveObservationFromMeasurement(measurement.Measurement), now); err != nil {
		return VerifiedCapacityPolicy{}, fmt.Errorf("%w: authority measurement check: %w", ErrCapacityPolicyRejected, err)
	}
	if err := ctx.Err(); err != nil {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, err)
	}
	live, err := observer.ObserveCapacity(ctx)
	if err != nil {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, err)
	}
	current, err := a.Snapshot()
	if err != nil || current.Generation != state.Generation {
		return VerifiedCapacityPolicy{}, errors.Join(ErrCapacityPolicyRejected, ErrSecurityAnchorRollback, err)
	}
	if err := ValidateCapacityPolicyAgainstLiveObservation(policyEnvelope.Payload, measurementEnvelope.RecordID,
		measurementEnvelope.Payload, live, now); err != nil {
		return VerifiedCapacityPolicy{}, fmt.Errorf("%w: %w", ErrCapacityPolicyRejected, err)
	}
	return VerifiedCapacityPolicy{recordID: policyEnvelope.RecordID, measurementRecordID: measurementEnvelope.RecordID, policy: policy}, nil
}

func anchorHostAttestationKeys(state SecurityAnchorState, now time.Time) map[string]TrustedKey {
	trusted := make(map[string]TrustedKey, len(state.HostAttestationKeys))
	for _, key := range state.HostAttestationKeys {
		if !validKeyState(key, now) || slices.Contains(state.RevocationLedger.RevokedKeyIDs, key.KeyID) {
			continue
		}
		trusted[key.KeyID] = TrustedKey{KeyID: key.KeyID, Role: SignerRoleHostAttestation, HostID: state.HostID,
			PublicKey: append(ed25519.PublicKey(nil), key.PublicKey...)}
	}
	return trusted
}

func isRevokedCapacityRecord(state SecurityAnchorState, recordID, keyID string) bool {
	return slices.Contains(state.RevocationLedger.RevokedRecordIDs, recordID) || slices.Contains(state.RevocationLedger.RevokedKeyIDs, keyID)
}

func cloneCapacityPolicy(value lanecapacity.Policy) lanecapacity.Policy {
	clone := value
	clone.LaneRuntime = cloneLaneRuntimeBudget(value.LaneRuntime)
	clone.HostManagement = cloneHostManagementBudget(value.HostManagement)
	return clone
}

func cloneLaneRuntimeBudget(value *lanecapacity.LaneRuntimeBudget) *lanecapacity.LaneRuntimeBudget {
	if value == nil {
		return nil
	}
	clone := *value
	clone.GoCachePaths = append([]string(nil), value.GoCachePaths...)
	if value.MacData != nil {
		mac := *value.MacData
		clone.MacData = &mac
	}
	return &clone
}

func cloneHostManagementBudget(value *lanecapacity.HostManagementBudget) *lanecapacity.HostManagementBudget {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Components = append([]string(nil), value.Components...)
	clone.GoCachePaths = append([]string(nil), value.GoCachePaths...)
	if value.MacData != nil {
		mac := *value.MacData
		clone.MacData = &mac
	}
	return &clone
}
