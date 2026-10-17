package release

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

var ErrIngressDirectiveRejected = errors.New("signed ingress directive rejected")

// ApplyIngressAdmissionDirective verifies a release-authority envelope
// against the current anchor, fresh time probe, and fresh revocation ledger,
// then atomically advances the lane's directive high-water and pending
// candidate context. It accepts an exact signed retry idempotently.
func (a *SecurityAnchor) ApplyIngressAdmissionDirective(envelopeRaw []byte) (AnchorDirectiveHighWater, error) {
	if a == nil || len(envelopeRaw) == 0 {
		return AnchorDirectiveHighWater{}, ErrIngressDirectiveRejected
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unhealthy || a.state.HighestAuthorityTime.IsZero() {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, ErrSecurityAnchorUnavailable)
	}
	acceptedAt := a.state.HighestAuthorityTime.UTC()
	if err := ValidateRevocationLedgerFreshForMutation(a.state.RevocationLedger, acceptedAt); err != nil {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, err)
	}
	verified, err := VerifyEnvelope(envelopeRaw, anchorAuthorityKeys(a.state, acceptedAt))
	if err != nil || verified.RecordType != "ingress_admission_directive" ||
		slices.Contains(a.state.RevocationLedger.RevokedRecordIDs, verified.RecordID) ||
		slices.Contains(a.state.RevocationLedger.RevokedKeyIDs, verified.SignerKeyID) {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, err)
	}
	if err := ValidateIngressAdmissionDirective(verified.Payload); err != nil {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, err)
	}
	var directive ingressDirectivePayload
	if err := decodeStrict(verified.Payload, &directive); err != nil {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, err)
	}
	notBefore, errBefore := time.Parse(time.RFC3339Nano, directive.NotBefore)
	notAfter, errAfter := time.Parse(time.RFC3339Nano, directive.NotAfter)
	now := a.now().UTC()
	if errBefore != nil || errAfter != nil || directive.HostID != a.state.HostID ||
		directive.RequiredLedgerGeneration != a.state.RevocationLedger.Generation ||
		now.Before(notBefore) || !now.Before(notAfter) || acceptedAt.Before(notBefore) || !acceptedAt.Before(notAfter) {
		return AnchorDirectiveHighWater{}, fmt.Errorf("%w: host, ledger, or signed validity interval mismatch", ErrIngressDirectiveRejected)
	}
	value := AnchorDirectiveHighWater{
		Lane: directive.Lane, HostID: directive.HostID, SignerKeyID: verified.SignerKeyID, TransitionID: directive.TransitionID,
		TransitionEpoch: directive.TransitionEpoch, AdmissionGeneration: directive.AdmissionGeneration,
		LeaseGeneration: directive.LeaseGeneration, PredecessorRecordID: directive.PredecessorRecordID,
		Role: directive.Role, CandidateManifestID: directive.CandidateID,
		ConfigurationDigest: directive.ConfigurationDigest, NotBefore: directive.NotBefore,
		NotAfter: directive.NotAfter, RequiredLedgerGeneration: directive.RequiredLedgerGeneration,
		RecordID: verified.RecordID,
	}
	if !validAnchorDirective(value) {
		return AnchorDirectiveHighWater{}, ErrIngressDirectiveRejected
	}
	if prior, ok := a.state.DirectiveHighWater[value.Lane]; ok && compareAnchorDirective(value, prior) == 0 {
		return prior, nil
	}
	if a.state.ProbeGeneration == a.state.ConsumedProbeGeneration {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected,
			invalidL3Trust("directive application requires a fresh unused helper time probe"))
	}
	if err := validateAnchorDirectiveSuccessor(a.state, value); err != nil {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, err)
	}
	if err := a.recordVerifiedDirectiveLocked(value); err != nil {
		return AnchorDirectiveHighWater{}, errors.Join(ErrIngressDirectiveRejected, err)
	}
	return value, nil
}

func validateAnchorDirectiveSuccessor(state SecurityAnchorState, value AnchorDirectiveHighWater) error {
	prior, exists := state.DirectiveHighWater[value.Lane]
	if !exists {
		if value.Role != "candidate_admission" || value.AdmissionGeneration != 0 || value.LeaseGeneration != 0 || value.PredecessorRecordID != "" {
			return invalidL3Trust("first lane directive must be a closed candidate with lease generation zero")
		}
		if _, active := state.ActiveRuntime[value.Lane]; active {
			return invalidL3Trust("first directive cannot replace an already anchored active runtime")
		}
		return nil
	}
	if compareAnchorDirective(value, prior) <= 0 {
		return invalidL3Trust("directive tuple is replayed, forked, or below its high-water")
	}
	if value.TransitionEpoch == prior.TransitionEpoch {
		if value.TransitionID != prior.TransitionID || value.Role != prior.Role ||
			value.CandidateManifestID != prior.CandidateManifestID || value.ConfigurationDigest != prior.ConfigurationDigest {
			return invalidL3Trust("directive identity changed within one transition epoch")
		}
		if value.PredecessorRecordID != prior.RecordID {
			return invalidL3Trust("same-epoch directive does not name its exact predecessor")
		}
		if value.AdmissionGeneration == prior.AdmissionGeneration {
			if value.LeaseGeneration != prior.LeaseGeneration+1 {
				return invalidL3Trust("directive lease renewal is not contiguous")
			}
			return nil
		}
		if value.Role == "candidate_admission" && value.AdmissionGeneration == prior.AdmissionGeneration+1 &&
			value.LeaseGeneration == 0 {
			return nil
		}
		return invalidL3Trust("candidate admission phase skipped or lease generation did not reset")
	}
	if value.AdmissionGeneration != 0 || value.LeaseGeneration != 0 || value.PredecessorRecordID != prior.RecordID {
		return invalidL3Trust("new directive epoch must start closed at lease zero and link the prior high-water")
	}
	if value.Role == "active_runtime_closure" {
		var candidate AnchorRuntimeContext
		if prior.Role == "candidate_admission" {
			candidate = AnchorRuntimeContext{Lane: prior.Lane, HostID: prior.HostID, TransitionID: prior.TransitionID,
				TransitionEpoch: prior.TransitionEpoch, CandidateManifestID: prior.CandidateManifestID,
				ConfigurationDigest: prior.ConfigurationDigest}
		} else {
			var ok bool
			candidate, ok = state.ActiveRuntime[value.Lane]
			if !ok {
				return invalidL3Trust("closure directive has no open or accepted incumbent runtime")
			}
		}
		if value.CandidateManifestID != candidate.CandidateManifestID || value.ConfigurationDigest != candidate.ConfigurationDigest ||
			value.TransitionEpoch <= candidate.TransitionEpoch {
			return invalidL3Trust("closure directive does not close the exact incumbent at a higher epoch")
		}
		return nil
	}
	if prior.Role != "active_runtime_closure" || value.TransitionID != prior.TransitionID || value.TransitionEpoch <= prior.TransitionEpoch {
		return invalidL3Trust("candidate admission must follow the same transition's higher-epoch closure")
	}
	return nil
}
