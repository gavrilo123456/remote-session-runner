package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	maxAuthorityRenewalIssuances = 32
	ledgerRenewalCycle           = 5 * time.Minute
	localProbePeerURI            = "launchd:com.remote-session-runner.release-transport"
)

var (
	ErrAuthorityGrant                   = errors.New("release authority renewal grant is invalid or unavailable")
	ErrAuthorityGrantExpired            = errors.New("release authority renewal grant expired")
	ErrAuthorityGrantExhausted          = errors.New("release authority renewal grant exhausted")
	ErrAuthorityGrantPublicationPending = errors.New("previous renewal is not confirmed published")
	ErrAuthorityTimeProbe               = errors.New("release authority time probe is invalid, stale, or replayed")
)

// DirectiveRenewalGrant is immutable approved scope plus an append-only
// issuance chain. The current record ID and generation advance only after the
// signed envelope is committed to the authority journal.
type DirectiveRenewalGrant struct {
	ID                       string                     `json:"grant_id"`
	Template                 ingressDirectivePayload    `json:"directive_template"`
	InitialRecordID          string                     `json:"initial_record_id"`
	InitialContextDigest     string                     `json:"initial_context_digest"`
	StartLeaseGeneration     uint64                     `json:"start_lease_generation"`
	MaximumLeaseGeneration   uint64                     `json:"maximum_lease_generation"`
	CurrentLeaseGeneration   uint64                     `json:"current_lease_generation"`
	CurrentRecordID          string                     `json:"current_record_id"`
	LeaseDurationMillis      int64                      `json:"lease_duration_millis"`
	ExpiresAt                string                     `json:"expires_at"`
	RenewalWarningAt         string                     `json:"renewal_warning_at"`
	IssuanceCap              uint32                     `json:"issuance_cap"`
	IssuedCount              uint32                     `json:"issued_count"`
	PendingPublicationRecord string                     `json:"pending_publication_record_id,omitempty"`
	Issuances                []authorityRenewalIssuance `json:"issuances"`
}

// LedgerRenewalGrant is the single root-authority grant for the global ledger
// chain. A successor renewal cannot start until all canonical hosts confirm
// receipt of the prior signed generation.
type LedgerRenewalGrant struct {
	ID                       string                     `json:"grant_id"`
	InitialState             RevocationLedgerState      `json:"initial_ledger_state"`
	CurrentGeneration        uint64                     `json:"current_generation"`
	CurrentRecordID          string                     `json:"current_record_id"`
	MaximumGeneration        uint64                     `json:"maximum_generation"`
	ExpiresAt                string                     `json:"expires_at"`
	RenewalWarningAt         string                     `json:"renewal_warning_at"`
	IssuanceCap              uint32                     `json:"issuance_cap"`
	IssuedCount              uint32                     `json:"issued_count"`
	PendingPublicationRecord string                     `json:"pending_publication_record_id,omitempty"`
	PendingPublicationHosts  []string                   `json:"pending_publication_hosts,omitempty"`
	Issuances                []authorityRenewalIssuance `json:"issuances"`
}

type authorityRenewalIssuance struct {
	Generation       uint64          `json:"generation"`
	PreviousRecordID string          `json:"previous_record_id"`
	RecordID         string          `json:"record_id"`
	IssuedAt         string          `json:"issued_at"`
	Envelope         json.RawMessage `json:"envelope"`
	PublishedAt      string          `json:"published_at,omitempty"`
	PublishedHosts   []string        `json:"published_hosts,omitempty"`
}

// AuthorityRenewalAlert is durable operator-facing state, not a transient log
// line. Alert IDs are deterministic so repeated scans do not duplicate them.
type AuthorityRenewalAlert struct {
	ID          string `json:"alert_id"`
	GrantID     string `json:"grant_id"`
	GrantKind   string `json:"grant_kind"`
	Reason      string `json:"reason"`
	CreatedAt   string `json:"created_at"`
	Issued      uint32 `json:"issued_count"`
	IssuanceCap uint32 `json:"issuance_cap"`
}

type authorityTimeProbeRecord struct {
	Challenge              TimeProbeChallenge `json:"challenge"`
	HostID                 string             `json:"host_id"`
	Transport              string             `json:"transport"`
	PeerURI                string             `json:"peer_uri"`
	LedgerGeneration       uint64             `json:"ledger_generation"`
	ReceivedMonotonicNanos uint64             `json:"received_monotonic_nanos"`
	Used                   bool               `json:"used"`
	Expired                bool               `json:"expired,omitempty"`
	Response               json.RawMessage    `json:"response,omitempty"`
}

// authorityHostPeer is constructed only after the fixed transport verifies a
// local socket peer or the remote host's mTLS identity. It is not decoded from
// an endpoint request body.
type authorityHostPeer struct {
	hostID       string
	transport    string
	peerURI      string
	peerVerified bool
}

// authorityHostPeerAfterVerification is the narrow adapter seam used after
// transport-level peer verification has produced a canonical host identity.
func authorityHostPeerAfterVerification(hostID, transport, peerURI string) (authorityHostPeer, error) {
	peer := authorityHostPeer{hostID: hostID, transport: transport, peerURI: peerURI, peerVerified: true}
	if !validAuthorityHostPeer(peer) {
		return authorityHostPeer{}, ErrAuthorityDenied
	}
	return peer, nil
}

type DirectiveRenewalGrantSpec struct {
	MaximumLeaseGeneration uint64
	LeaseDuration          time.Duration
	ExpiresAt              time.Time
	RenewalWarningAt       time.Time
	IssuanceCap            uint32
}

type LedgerRenewalGrantSpec struct {
	MaximumGeneration uint64
	ExpiresAt         time.Time
	RenewalWarningAt  time.Time
	IssuanceCap       uint32
}

func (t *AuthorityApprovalTool) CreateDirectiveRenewalGrant(ctx context.Context, initialEnvelope []byte, spec DirectiveRenewalGrantSpec) (string, error) {
	if t == nil || t.journal == nil || t.authorizer == nil {
		return "", ErrAuthorityGrant
	}
	state, err := t.journal.snapshot()
	if err != nil {
		return "", err
	}
	publicKey, err := decodeAuthorityAnchorKey(state)
	if err != nil {
		return "", err
	}
	verified, err := VerifyEnvelope(initialEnvelope, map[string]TrustedKey{state.AnchorKeyID: {
		KeyID: state.AnchorKeyID, Role: SignerRoleReleaseAuthority, PublicKey: publicKey,
	}})
	if err != nil || verified.RecordType != "ingress_admission_directive" || ValidateIngressAdmissionDirective(verified.Payload) != nil {
		return "", ErrAuthorityGrant
	}
	contextDigest, _, err := authorityPlanContext(verified.RecordType, verified.Payload)
	if err != nil || state.AcceptedRecordIDs[verified.RecordID] != contextDigest {
		return "", ErrAuthorityGrant
	}
	var directive ingressDirectivePayload
	if decodeStrict(verified.Payload, &directive) != nil || directive.RequiredLedgerGeneration != state.LedgerGeneration {
		return "", ErrAuthorityGrant
	}
	now := t.now().Round(0).UTC()
	expires := spec.ExpiresAt.Round(0).UTC()
	warning := spec.RenewalWarningAt.Round(0).UTC()
	if err := validateGrantBounds(now, expires, warning, spec.LeaseDuration, directive.LeaseGeneration,
		spec.MaximumLeaseGeneration, spec.IssuanceCap); err != nil {
		return "", err
	}
	grantID, err := t.newID()
	if err != nil || !validAuthorityStateID(grantID) {
		return "", ErrAuthorityGrant
	}
	directive.PredecessorRecordID = ""
	grant := DirectiveRenewalGrant{ID: grantID, Template: directive, InitialRecordID: verified.RecordID, InitialContextDigest: contextDigest,
		StartLeaseGeneration: directive.LeaseGeneration, MaximumLeaseGeneration: spec.MaximumLeaseGeneration,
		CurrentLeaseGeneration: directive.LeaseGeneration, CurrentRecordID: verified.RecordID,
		LeaseDurationMillis: spec.LeaseDuration.Milliseconds(), ExpiresAt: expires.Format(time.RFC3339Nano),
		RenewalWarningAt: warning.Format(time.RFC3339Nano), IssuanceCap: spec.IssuanceCap, Issuances: []authorityRenewalIssuance{}}
	grantDigest := hexDigest(authorityMarshalJSON(grant))
	message := fmt.Sprintf("Approve finite directive renewals grant %s for directive record %s, context sha256:%s, maximum lease generation %d, cap %d, expiry %s, warning %s",
		grant.ID, verified.RecordID, grantDigest, grant.MaximumLeaseGeneration, grant.IssuanceCap, grant.ExpiresAt, grant.RenewalWarningAt)
	receipt, err := t.authorizer.AuthorizeAuthorityMandate(ctx, message)
	if err != nil || !validAuthorityStateID(receipt) {
		return "", ErrAuthorityDenied
	}
	if err := t.journal.installDirectiveGrant(grant, state.LedgerGeneration, now); err != nil {
		return "", err
	}
	return grantID, nil
}

func (t *AuthorityApprovalTool) CreateLedgerRenewalGrant(ctx context.Context, spec LedgerRenewalGrantSpec) (string, error) {
	if t == nil || t.journal == nil || t.authorizer == nil {
		return "", ErrAuthorityGrant
	}
	state, err := t.journal.snapshot()
	if err != nil {
		return "", err
	}
	current, err := authorityLedgerState(state)
	if err != nil {
		return "", err
	}
	now := t.now().Round(0).UTC()
	if ValidateRevocationLedgerFreshForMutation(current, now) != nil {
		return "", ErrAuthorityGrant
	}
	expires := spec.ExpiresAt.Round(0).UTC()
	warning := spec.RenewalWarningAt.Round(0).UTC()
	if err := validateGrantBounds(now, expires, warning, ledgerRenewalCycle, current.Generation,
		spec.MaximumGeneration, spec.IssuanceCap); err != nil {
		return "", err
	}
	grantID, err := t.newID()
	if err != nil || !validAuthorityStateID(grantID) {
		return "", ErrAuthorityGrant
	}
	grant := LedgerRenewalGrant{ID: grantID, InitialState: cloneRevocationLedgerState(current),
		CurrentGeneration: current.Generation, CurrentRecordID: current.RecordID,
		MaximumGeneration: spec.MaximumGeneration, ExpiresAt: expires.Format(time.RFC3339Nano),
		RenewalWarningAt: warning.Format(time.RFC3339Nano), IssuanceCap: spec.IssuanceCap,
		Issuances: []authorityRenewalIssuance{}}
	grantDigest := hexDigest(authorityMarshalJSON(grant))
	message := fmt.Sprintf("Approve finite global ledger renewals grant %s for predecessor %s generation %d, revoked sets sha256:%s, maximum generation %d, cap %d, expiry %s, warning %s",
		grant.ID, current.RecordID, current.Generation, hexDigest(authorityMarshalJSON(struct {
			Records []string `json:"records"`
			Keys    []string `json:"keys"`
		}{current.RevokedRecordIDs, current.RevokedKeyIDs})), grant.MaximumGeneration, grant.IssuanceCap, grant.ExpiresAt, grant.RenewalWarningAt)
	message += " grant sha256:" + grantDigest
	receipt, err := t.authorizer.AuthorizeAuthorityMandate(ctx, message)
	if err != nil || !validAuthorityStateID(receipt) {
		return "", ErrAuthorityDenied
	}
	if err := t.journal.installLedgerGrant(grant, state.LedgerGeneration, now); err != nil {
		return "", err
	}
	return grantID, nil
}

func validateGrantBounds(now, expires, warning time.Time, cycle time.Duration, start, maximum uint64, cap uint32) error {
	if now.IsZero() || !expires.After(now) || !warning.After(now) || !warning.Before(expires) || cycle <= 0 ||
		expires.Sub(warning) < 2*cycle || maximum <= start || maximum-start < 2 ||
		cap < 2 || cap > maxAuthorityRenewalIssuances || uint64(cap) > maximum-start {
		return ErrAuthorityGrant
	}
	return nil
}

func validAuthorityHostPeer(peer authorityHostPeer) bool {
	if !peer.peerVerified || !slices.Contains(canonicalHostIDs, peer.hostID) {
		return false
	}
	if peer.hostID == "mac-control-plane" {
		return peer.transport == "local" && peer.peerURI == localProbePeerURI
	}
	return peer.transport == "mtls" && peer.peerURI == "spiffe://remote-session-runner/host/"+peer.hostID+"/release-transport"
}

func authorityLedgerState(state authorityRootState) (RevocationLedgerState, error) {
	issued, errIssued := time.Parse(time.RFC3339Nano, state.LedgerIssuedAt)
	expires, errExpires := time.Parse(time.RFC3339Nano, state.LedgerExpiresAt)
	ledger := RevocationLedgerState{Generation: state.LedgerGeneration, RecordID: state.LedgerRecordID,
		RevokedRecordIDs: append([]string(nil), state.RevokedRecordIDs...), RevokedKeyIDs: append([]string(nil), state.RevokedKeyIDs...),
		IssuedAt: issued.UTC(), ExpiresAt: expires.UTC()}
	if errIssued != nil || errExpires != nil || !validRevocationLedgerState(ledger) {
		return RevocationLedgerState{}, ErrAuthorityGrant
	}
	return ledger, nil
}

func cloneRevocationLedgerState(value RevocationLedgerState) RevocationLedgerState {
	value.RevokedRecordIDs = append([]string(nil), value.RevokedRecordIDs...)
	value.RevokedKeyIDs = append([]string(nil), value.RevokedKeyIDs...)
	return value
}

func (j *AuthorityJournal) installDirectiveGrant(grant DirectiveRenewalGrant, expectedLedger uint64, now time.Time) error {
	_, err := j.mutate(func(next *authorityRootState) error {
		if next.LedgerGeneration != expectedLedger || grant.ID == "" || next.DirectiveGrants != nil && next.DirectiveGrants[grant.ID].ID != "" {
			return ErrAuthorityGrant
		}
		for _, current := range next.DirectiveGrants {
			currentExpiry := mustParseAuthorityTime(current.ExpiresAt)
			if currentExpiry.After(now) && current.IssuedCount < current.IssuanceCap && sameDirectiveContext(current.Template, grant.Template) {
				return ErrAuthorityGrant
			}
		}
		if next.DirectiveGrants == nil {
			next.DirectiveGrants = map[string]DirectiveRenewalGrant{}
		}
		next.DirectiveGrants[grant.ID] = grant
		return nil
	})
	return err
}

func (j *AuthorityJournal) installLedgerGrant(grant LedgerRenewalGrant, expectedLedger uint64, now time.Time) error {
	_, err := j.mutate(func(next *authorityRootState) error {
		if next.LedgerGeneration != expectedLedger || next.LedgerRecordID != grant.CurrentRecordID ||
			grant.CurrentGeneration != expectedLedger || grant.ID == "" ||
			next.ActiveLedgerGrant != "" && ledgerGrantStillActive(next.LedgerGrants[next.ActiveLedgerGrant], now) {
			return ErrAuthorityGrant
		}
		if next.LedgerGrants == nil {
			next.LedgerGrants = map[string]LedgerRenewalGrant{}
		}
		if _, exists := next.LedgerGrants[grant.ID]; exists {
			return ErrAuthorityGrant
		}
		next.LedgerGrants[grant.ID] = grant
		next.ActiveLedgerGrant = grant.ID
		return nil
	})
	return err
}

func ledgerGrantStillActive(grant LedgerRenewalGrant, now time.Time) bool {
	expires, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	return err == nil && now.Before(expires) && grant.IssuedCount < grant.IssuanceCap && grant.CurrentGeneration < grant.MaximumGeneration
}

func sortedCanonicalHostIDs() []string {
	ids := append([]string(nil), canonicalHostIDs...)
	slices.Sort(ids)
	return ids
}

// RenewDirective constructs the only payload authorized by the stored grant.
// The caller cannot select a body, phase, host, lane, candidate, or lease.
func (s *AuthoritySigner) RenewDirective(ctx context.Context, evidence authorityAgentEvidence, grantID string) ([]byte, error) {
	if s == nil || !authorityAgentAllowed(evidence, s.expected, s.goos) || !validAuthorityStateID(grantID) {
		return nil, ErrAuthorityDenied
	}
	state, err := s.journal.snapshot()
	if err != nil {
		return nil, err
	}
	grant, ok := state.DirectiveGrants[grantID]
	if !ok {
		return nil, ErrAuthorityGrant
	}
	now := s.clock().Round(0).UTC()
	expires, errExpires := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	if errExpires != nil || !now.Before(expires) {
		return nil, ErrAuthorityGrantExpired
	}
	if grant.PendingPublicationRecord != "" {
		return nil, ErrAuthorityGrantPublicationPending
	}
	if grant.IssuedCount >= grant.IssuanceCap || grant.CurrentLeaseGeneration >= grant.MaximumLeaseGeneration {
		return nil, ErrAuthorityGrantExhausted
	}
	if grant.LeaseDurationMillis <= 0 || state.LedgerGeneration != grant.Template.RequiredLedgerGeneration {
		return nil, ErrAuthorityGrant
	}
	anchorKey, err := decodeAuthorityAnchorKey(state)
	if err != nil {
		return nil, err
	}
	identity, err := s.authoritySigningIdentity(state, anchorKey)
	if err != nil {
		return nil, err
	}
	defer clear(identity.PrivateKey)
	identity.Role = SignerRoleReleaseAuthority
	directive := grant.Template
	directive.LeaseGeneration = grant.CurrentLeaseGeneration + 1
	directive.PredecessorRecordID = grant.CurrentRecordID
	directive.NotBefore = now.Format(time.RFC3339Nano)
	leaseEnd := now.Add(time.Duration(grant.LeaseDurationMillis) * time.Millisecond)
	if expires.Before(leaseEnd) {
		leaseEnd = expires
	}
	directive.NotAfter = leaseEnd.Format(time.RFC3339Nano)
	payload, err := json.Marshal(directive)
	if err != nil || ValidateIngressAdmissionDirective(payload) != nil {
		return nil, ErrAuthorityGrant
	}
	payload, err = CanonicalizeJSON(payload)
	if err != nil {
		return nil, ErrAuthorityGrant
	}
	envelope, err := SignEnvelope("ingress_admission_directive", payload, identity)
	if err != nil {
		return nil, err
	}
	verified, err := VerifyEnvelope(envelope, map[string]TrustedKey{state.AnchorKeyID: {
		KeyID: state.AnchorKeyID, Role: SignerRoleReleaseAuthority, PublicKey: anchorKey,
	}})
	if err != nil || ValidateIngressAdmissionDirective(verified.Payload) != nil {
		return nil, ErrAuthorityGrant
	}
	issuedAt := now.Format(time.RFC3339Nano)
	_, err = s.journal.mutate(func(next *authorityRootState) error {
		current, exists := next.DirectiveGrants[grantID]
		if !exists || !sameDirectiveGrant(current, grant) || current.PendingPublicationRecord != "" ||
			next.LedgerGeneration != state.LedgerGeneration || next.AnchorKeyID != state.AnchorKeyID ||
			current.CurrentRecordID != grant.CurrentRecordID || current.CurrentLeaseGeneration != grant.CurrentLeaseGeneration ||
			current.IssuedCount != grant.IssuedCount || !now.Before(mustParseAuthorityTime(current.ExpiresAt)) {
			return ErrAuthorityReplay
		}
		if _, exists := next.AcceptedRecordIDs[verified.RecordID]; exists {
			return ErrAuthorityReplay
		}
		current.CurrentLeaseGeneration = directive.LeaseGeneration
		current.CurrentRecordID = verified.RecordID
		current.IssuedCount++
		current.PendingPublicationRecord = verified.RecordID
		current.Issuances = append(current.Issuances, authorityRenewalIssuance{Generation: directive.LeaseGeneration,
			PreviousRecordID: grant.CurrentRecordID, RecordID: verified.RecordID, IssuedAt: issuedAt,
			Envelope: append(json.RawMessage(nil), envelope...)})
		next.DirectiveGrants[grantID] = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

// RenewLedger issues one same-set global ledger successor. The signed envelope
// and generation advance become durable in one root-journal CAS before return.
func (s *AuthoritySigner) RenewLedger(ctx context.Context, evidence authorityAgentEvidence, grantID string) ([]byte, error) {
	if s == nil || !authorityAgentAllowed(evidence, s.expected, s.goos) || !validAuthorityStateID(grantID) {
		return nil, ErrAuthorityDenied
	}
	state, err := s.journal.snapshot()
	if err != nil {
		return nil, err
	}
	grant, ok := state.LedgerGrants[grantID]
	if !ok || state.ActiveLedgerGrant != grantID {
		return nil, ErrAuthorityGrant
	}
	now := s.clock().Round(0).UTC()
	expires, errExpires := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	if errExpires != nil || !now.Before(expires) {
		return nil, ErrAuthorityGrantExpired
	}
	if grant.PendingPublicationRecord != "" || len(grant.PendingPublicationHosts) != 0 {
		return nil, ErrAuthorityGrantPublicationPending
	}
	if grant.IssuedCount >= grant.IssuanceCap || grant.CurrentGeneration >= grant.MaximumGeneration {
		return nil, ErrAuthorityGrantExhausted
	}
	previous, err := authorityLedgerState(state)
	if err != nil || ValidateRevocationLedgerFreshForMutation(previous, now) != nil ||
		previous.Generation != grant.CurrentGeneration || previous.RecordID != grant.CurrentRecordID ||
		!slices.Equal(previous.RevokedRecordIDs, grant.InitialState.RevokedRecordIDs) ||
		!slices.Equal(previous.RevokedKeyIDs, grant.InitialState.RevokedKeyIDs) {
		return nil, ErrAuthorityGrant
	}
	anchorKey, err := decodeAuthorityAnchorKey(state)
	if err != nil {
		return nil, err
	}
	identity, err := s.authoritySigningIdentity(state, anchorKey)
	if err != nil {
		return nil, err
	}
	defer clear(identity.PrivateKey)
	identity.Role = SignerRoleReleaseAuthority
	ledgerExpiry := now.Add(MaxRevocationLedgerLifetime)
	payloadValue := revocationLedgerPayload{Generation: previous.Generation + 1,
		PredecessorRecordID: previous.RecordID, IssuedAt: now.Format(time.RFC3339Nano),
		ExpiresAt: ledgerExpiry.Format(time.RFC3339Nano), RevokedRecordIDs: append([]string(nil), previous.RevokedRecordIDs...),
		RevokedKeyIDs: append([]string(nil), previous.RevokedKeyIDs...)}
	payload, err := json.Marshal(payloadValue)
	if err != nil || ValidatePayload("release_revocation_ledger", payload) != nil {
		return nil, ErrAuthorityGrant
	}
	payload, err = CanonicalizeJSON(payload)
	if err != nil {
		return nil, ErrAuthorityGrant
	}
	envelope, err := SignEnvelope("release_revocation_ledger", payload, identity)
	if err != nil {
		return nil, err
	}
	nextLedger, err := ValidateRevocationLedger(envelope, previous, map[string]TrustedKey{state.AnchorKeyID: {
		KeyID: state.AnchorKeyID, Role: SignerRoleReleaseAuthority, PublicKey: anchorKey,
	}}, now)
	if err != nil {
		return nil, err
	}
	issuedAt := now.Format(time.RFC3339Nano)
	_, err = s.journal.mutate(func(next *authorityRootState) error {
		current, exists := next.LedgerGrants[grantID]
		if !exists || !sameLedgerGrant(current, grant) || next.ActiveLedgerGrant != grantID ||
			current.PendingPublicationRecord != "" || len(current.PendingPublicationHosts) != 0 ||
			next.LedgerGeneration != previous.Generation || next.LedgerRecordID != previous.RecordID ||
			current.CurrentGeneration != previous.Generation || current.CurrentRecordID != previous.RecordID ||
			current.IssuedCount != grant.IssuedCount || !now.Before(mustParseAuthorityTime(current.ExpiresAt)) {
			return ErrAuthorityReplay
		}
		current.CurrentGeneration = nextLedger.Generation
		current.CurrentRecordID = nextLedger.RecordID
		current.IssuedCount++
		current.PendingPublicationRecord = nextLedger.RecordID
		current.PendingPublicationHosts = sortedCanonicalHostIDs()
		current.Issuances = append(current.Issuances, authorityRenewalIssuance{Generation: nextLedger.Generation,
			PreviousRecordID: previous.RecordID, RecordID: nextLedger.RecordID, IssuedAt: issuedAt,
			Envelope: append(json.RawMessage(nil), envelope...)})
		next.LedgerGrants[grantID] = current
		next.LedgerGeneration = nextLedger.Generation
		next.LedgerRecordID = nextLedger.RecordID
		next.LedgerIssuedAt = nextLedger.IssuedAt.Format(time.RFC3339Nano)
		next.LedgerExpiresAt = nextLedger.ExpiresAt.Format(time.RFC3339Nano)
		next.RevokedRecordIDs = append([]string(nil), nextLedger.RevokedRecordIDs...)
		next.RevokedKeyIDs = append([]string(nil), nextLedger.RevokedKeyIDs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

func (s *AuthoritySigner) authoritySigningIdentity(state authorityRootState, anchorKey ed25519.PublicKey) (SigningIdentity, error) {
	identity, err := s.keys.LoadAuthorityKey(state.AnchorKeyID)
	if err != nil || identity.KeyID != state.AnchorKeyID || len(identity.PrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(identity.PrivateKey.Public().(ed25519.PublicKey), anchorKey) {
		return SigningIdentity{}, ErrAuthorityKey
	}
	identity.PrivateKey = append(ed25519.PrivateKey(nil), identity.PrivateKey...)
	return identity, nil
}

func (a *AuthorityAgent) RecordDirectivePublication(ctx context.Context, evidence authorityAgentEvidence, peer authorityHostPeer, grantID, recordID string) error {
	if a == nil || !authorityAgentAllowed(evidence, a.expected, a.goos) || !validAuthorityHostPeer(peer) ||
		!validAuthorityStateID(grantID) || !isLowerHexDigest(recordID) {
		return ErrAuthorityDenied
	}
	_, err := a.journal.mutate(func(next *authorityRootState) error {
		grant, ok := next.DirectiveGrants[grantID]
		if !ok || peer.hostID != grant.Template.HostID || grant.PendingPublicationRecord != recordID ||
			len(grant.Issuances) == 0 {
			return ErrAuthorityGrant
		}
		last := &grant.Issuances[len(grant.Issuances)-1]
		if last.RecordID != recordID || last.PublishedAt != "" || last.Generation != grant.CurrentLeaseGeneration {
			return ErrAuthorityReplay
		}
		last.PublishedAt = a.now().Round(0).UTC().Format(time.RFC3339Nano)
		last.PublishedHosts = []string{peer.hostID}
		grant.PendingPublicationRecord = ""
		next.DirectiveGrants[grantID] = grant
		return nil
	})
	return err
}

func (a *AuthorityAgent) RecordLedgerPublication(ctx context.Context, peer authorityHostPeer, grantID, recordID string) error {
	if a == nil || !validAuthorityHostPeer(peer) || !validAuthorityStateID(grantID) || !isLowerHexDigest(recordID) {
		return ErrAuthorityDenied
	}
	_, err := a.journal.mutate(func(next *authorityRootState) error {
		grant, ok := next.LedgerGrants[grantID]
		if !ok || next.ActiveLedgerGrant != grantID || grant.PendingPublicationRecord != recordID ||
			!slices.Contains(grant.PendingPublicationHosts, peer.hostID) || len(grant.Issuances) == 0 {
			return ErrAuthorityGrant
		}
		last := &grant.Issuances[len(grant.Issuances)-1]
		if last.RecordID != recordID || last.Generation != grant.CurrentGeneration || slices.Contains(last.PublishedHosts, peer.hostID) {
			return ErrAuthorityReplay
		}
		last.PublishedHosts = append(last.PublishedHosts, peer.hostID)
		slices.Sort(last.PublishedHosts)
		grant.PendingPublicationHosts = slices.DeleteFunc(grant.PendingPublicationHosts, func(value string) bool { return value == peer.hostID })
		if len(grant.PendingPublicationHosts) == 0 {
			last.PublishedAt = a.now().Round(0).UTC().Format(time.RFC3339Nano)
			grant.PendingPublicationRecord = ""
		}
		next.LedgerGrants[grantID] = grant
		return nil
	})
	return err
}

func (a *AuthorityAgent) RecordAuthorityTimeProbeChallenge(ctx context.Context, evidence authorityAgentEvidence, peer authorityHostPeer, challenge TimeProbeChallenge) error {
	if a == nil || !authorityAgentAllowed(evidence, a.expected, a.goos) || !validAuthorityHostPeer(peer) ||
		!validChallengeID(challenge.ID) || !validNonce(challenge.Nonce) {
		return ErrAuthorityDenied
	}
	monotonic, err := anchorMonotonicNanos()
	if err != nil || monotonic == 0 {
		return ErrAuthorityTimeProbe
	}
	now := a.now().Round(0).UTC()
	_, err = a.journal.mutate(func(next *authorityRootState) error {
		ledgerExpiry, parseErr := time.Parse(time.RFC3339Nano, next.LedgerExpiresAt)
		if parseErr != nil || !now.Before(ledgerExpiry) || next.LedgerGeneration == 0 {
			return ErrAuthorityTimeProbe
		}
		if next.TimeProbeRecords == nil {
			next.TimeProbeRecords = map[string]authorityTimeProbeRecord{}
		}
		if _, exists := next.TimeProbeRecords[challenge.ID]; exists {
			return ErrAuthorityReplay
		}
		for _, current := range next.TimeProbeRecords {
			if current.HostID == peer.hostID && !current.Used {
				if monotonic < current.ReceivedMonotonicNanos || monotonic-current.ReceivedMonotonicNanos <= uint64(MaxAuthorityTimeProbeRoundTrip) {
					return ErrAuthorityGrantPublicationPending
				}
				current.Used = true
				current.Expired = true
				next.TimeProbeRecords[current.Challenge.ID] = current
			}
		}
		next.TimeProbeRecords[challenge.ID] = authorityTimeProbeRecord{Challenge: challenge, HostID: peer.hostID,
			Transport: peer.transport, PeerURI: peer.peerURI, LedgerGeneration: next.LedgerGeneration,
			ReceivedMonotonicNanos: monotonic}
		return nil
	})
	return err
}

// SignAuthorityTimeProbe signs only the exact fresh challenge stored after
// transport authentication. It has no caller-supplied response body or record
// type and consumes the challenge durably before returning.
func (s *AuthoritySigner) SignAuthorityTimeProbe(ctx context.Context, evidence authorityAgentEvidence, challengeID string) ([]byte, error) {
	if s == nil || !authorityAgentAllowed(evidence, s.expected, s.goos) || !validChallengeID(challengeID) {
		return nil, ErrAuthorityDenied
	}
	state, err := s.journal.snapshot()
	if err != nil {
		return nil, err
	}
	record, ok := state.TimeProbeRecords[challengeID]
	if !ok || record.Used || !validAuthorityHostPeer(authorityHostPeer{hostID: record.HostID, transport: record.Transport, peerURI: record.PeerURI, peerVerified: true}) ||
		record.LedgerGeneration != state.LedgerGeneration {
		return nil, ErrAuthorityTimeProbe
	}
	monotonicClock := s.monotonicNow
	if monotonicClock == nil {
		monotonicClock = anchorMonotonicNanos
	}
	monotonic, err := monotonicClock()
	if err != nil || monotonic < record.ReceivedMonotonicNanos || monotonic-record.ReceivedMonotonicNanos > uint64(MaxAuthorityTimeProbeRoundTrip) {
		_, consumeErr := s.journal.mutate(func(next *authorityRootState) error {
			current, exists := next.TimeProbeRecords[challengeID]
			if !exists || !sameAuthorityProbeRecord(current, record) || current.Used {
				return ErrAuthorityReplay
			}
			current.Used = true
			current.Expired = true
			next.TimeProbeRecords[challengeID] = current
			return nil
		})
		return nil, errors.Join(ErrAuthorityTimeProbe, consumeErr)
	}
	ledgerExpiry, err := time.Parse(time.RFC3339Nano, state.LedgerExpiresAt)
	if err != nil || !s.clock().Before(ledgerExpiry) {
		return nil, ErrAuthorityTimeProbe
	}
	anchorKey, err := decodeAuthorityAnchorKey(state)
	if err != nil {
		return nil, err
	}
	identity, err := s.authoritySigningIdentity(state, anchorKey)
	if err != nil {
		return nil, err
	}
	defer clear(identity.PrivateKey)
	body := TimeProbeBody{Version: ReleaseTimePolicyVersion, Nonce: record.Challenge.Nonce,
		AuthorityUTCUnixMillis: s.clock().UTC().UnixMilli(), SignerKeyID: state.AnchorKeyID}
	response, err := SignTimeProbeResponse(body, identity.PrivateKey)
	if err != nil {
		return nil, err
	}
	_, err = s.journal.mutate(func(next *authorityRootState) error {
		current, exists := next.TimeProbeRecords[challengeID]
		if !exists || !sameAuthorityProbeRecord(current, record) || current.Used ||
			next.LedgerGeneration != record.LedgerGeneration || next.AnchorKeyID != state.AnchorKeyID {
			return ErrAuthorityReplay
		}
		current.Used = true
		current.Response = append(json.RawMessage(nil), response...)
		next.TimeProbeRecords[challengeID] = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (a *AuthorityAgent) CheckRenewalWarnings(ctx context.Context, evidence authorityAgentEvidence) ([]AuthorityRenewalAlert, error) {
	if a == nil || !authorityAgentAllowed(evidence, a.expected, a.goos) {
		return nil, ErrAuthorityDenied
	}
	now := a.now().Round(0).UTC()
	created := []AuthorityRenewalAlert{}
	_, err := a.journal.mutate(func(next *authorityRootState) error {
		if next.RenewalAlerts == nil {
			next.RenewalAlerts = map[string]AuthorityRenewalAlert{}
		}
		add := func(id, kind, grantKind, expiresText string, issued, cap uint32) {
			warning, parseErr := time.Parse(time.RFC3339Nano, expiresText)
			if parseErr == nil && now.Before(warning) {
				return
			}
			alertID := id + ":" + kind
			if _, exists := next.RenewalAlerts[alertID]; exists {
				return
			}
			alert := AuthorityRenewalAlert{ID: alertID, GrantID: id, GrantKind: grantKind, Reason: kind,
				CreatedAt: now.Format(time.RFC3339Nano), Issued: issued, IssuanceCap: cap}
			next.RenewalAlerts[alertID] = alert
			created = append(created, alert)
		}
		for id, grant := range next.DirectiveGrants {
			add(id, "renewal_warning", "directive", grant.RenewalWarningAt, grant.IssuedCount, grant.IssuanceCap)
			if grant.IssuanceCap-grant.IssuedCount <= 1 {
				add(id, "one_issuance_remaining", "directive", now.Format(time.RFC3339Nano), grant.IssuedCount, grant.IssuanceCap)
			}
		}
		for id, grant := range next.LedgerGrants {
			add(id, "renewal_warning", "ledger", grant.RenewalWarningAt, grant.IssuedCount, grant.IssuanceCap)
			if grant.IssuanceCap-grant.IssuedCount <= 1 {
				add(id, "one_issuance_remaining", "ledger", now.Format(time.RFC3339Nano), grant.IssuedCount, grant.IssuanceCap)
			}
		}
		return nil
	})
	return created, err
}

func sameAuthorityProbeRecord(left, right authorityTimeProbeRecord) bool {
	return left.Challenge == right.Challenge && left.HostID == right.HostID && left.Transport == right.Transport &&
		left.PeerURI == right.PeerURI && left.LedgerGeneration == right.LedgerGeneration &&
		left.ReceivedMonotonicNanos == right.ReceivedMonotonicNanos && left.Used == right.Used && left.Expired == right.Expired &&
		bytes.Equal(left.Response, right.Response)
}

func sameDirectiveGrant(left, right DirectiveRenewalGrant) bool {
	leftMutable := left
	rightMutable := right
	leftMutable.CurrentLeaseGeneration, rightMutable.CurrentLeaseGeneration = 0, 0
	leftMutable.CurrentRecordID, rightMutable.CurrentRecordID = "", ""
	leftMutable.IssuedCount, rightMutable.IssuedCount = 0, 0
	leftMutable.PendingPublicationRecord, rightMutable.PendingPublicationRecord = "", ""
	leftMutable.Issuances, rightMutable.Issuances = nil, nil
	return bytes.Equal(authorityMarshalJSON(leftMutable), authorityMarshalJSON(rightMutable))
}

func sameLedgerGrant(left, right LedgerRenewalGrant) bool {
	leftMutable := left
	rightMutable := right
	leftMutable.CurrentGeneration, rightMutable.CurrentGeneration = 0, 0
	leftMutable.CurrentRecordID, rightMutable.CurrentRecordID = "", ""
	leftMutable.IssuedCount, rightMutable.IssuedCount = 0, 0
	leftMutable.PendingPublicationRecord, rightMutable.PendingPublicationRecord = "", ""
	leftMutable.PendingPublicationHosts, rightMutable.PendingPublicationHosts = nil, nil
	leftMutable.Issuances, rightMutable.Issuances = nil, nil
	return bytes.Equal(authorityMarshalJSON(leftMutable), authorityMarshalJSON(rightMutable))
}

func mustParseAuthorityTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func validateAuthorityRenewalState(state authorityRootState, anchorKey ed25519.PublicKey) error {
	trusted := map[string]TrustedKey{state.AnchorKeyID: {
		KeyID: state.AnchorKeyID, Role: SignerRoleReleaseAuthority, PublicKey: anchorKey,
	}}
	pendingHostsByID := make(map[string]bool)
	for id, grant := range state.DirectiveGrants {
		if id != grant.ID || !validAuthorityStateID(id) || grant.Template.CandidateID == "" ||
			grant.Template.TransitionID == "" || !slices.Contains(canonicalHostIDs, grant.Template.HostID) ||
			grant.Template.LeaseGeneration != grant.StartLeaseGeneration || grant.Template.PredecessorRecordID != "" ||
			!isLowerHexDigest(grant.InitialRecordID) || !isLowerHexDigest(grant.InitialContextDigest) || !isLowerHexDigest(grant.CurrentRecordID) ||
			grant.MaximumLeaseGeneration <= grant.StartLeaseGeneration || grant.CurrentLeaseGeneration != grant.StartLeaseGeneration+uint64(grant.IssuedCount) ||
			grant.CurrentLeaseGeneration > grant.MaximumLeaseGeneration || grant.IssuanceCap < 2 || grant.IssuanceCap > maxAuthorityRenewalIssuances ||
			grant.IssuedCount > grant.IssuanceCap || uint64(grant.IssuanceCap) > grant.MaximumLeaseGeneration-grant.StartLeaseGeneration ||
			grant.LeaseDurationMillis <= 0 || len(grant.Issuances) != int(grant.IssuedCount) ||
			ValidateIngressAdmissionDirective(authorityMarshalJSON(grant.Template)) != nil {
			return ErrAuthorityStateRollback
		}
		initialContext, _, err := authorityPlanContext("ingress_admission_directive", authorityMarshalJSON(grant.Template))
		if err != nil || initialContext != grant.InitialContextDigest || state.AcceptedRecordIDs[grant.InitialRecordID] != grant.InitialContextDigest {
			return ErrAuthorityStateRollback
		}
		expires, errExpires := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		warning, errWarning := time.Parse(time.RFC3339Nano, grant.RenewalWarningAt)
		if errExpires != nil || errWarning != nil || !warning.Before(expires) ||
			expires.Sub(warning) < 2*time.Duration(grant.LeaseDurationMillis)*time.Millisecond {
			return ErrAuthorityStateRollback
		}
		previousID := grant.InitialRecordID
		for index, issue := range grant.Issuances {
			if issue.Generation != grant.StartLeaseGeneration+uint64(index)+1 || issue.PreviousRecordID != previousID ||
				!isLowerHexDigest(issue.RecordID) || issue.PublishedHosts == nil && issue.PublishedAt != "" {
				return ErrAuthorityStateRollback
			}
			verified, err := VerifyEnvelope(issue.Envelope, trusted)
			issued, errIssued := time.Parse(time.RFC3339Nano, issue.IssuedAt)
			if err != nil || errIssued != nil || verified.RecordType != "ingress_admission_directive" || verified.RecordID != issue.RecordID ||
				ValidateIngressAdmissionDirective(verified.Payload) != nil {
				return ErrAuthorityStateRollback
			}
			var directive ingressDirectivePayload
			if decodeStrict(verified.Payload, &directive) != nil || !sameDirectiveContext(directive, grant.Template) ||
				directive.LeaseGeneration != issue.Generation || directive.PredecessorRecordID != previousID ||
				!issued.Before(expires) || issue.PublishedHosts != nil && !slices.Equal(issue.PublishedHosts, []string{grant.Template.HostID}) {
				return ErrAuthorityStateRollback
			}
			if issue.PublishedAt != "" {
				published, err := time.Parse(time.RFC3339Nano, issue.PublishedAt)
				if err != nil || published.Before(issued) {
					return ErrAuthorityStateRollback
				}
			}
			previousID = issue.RecordID
		}
		if grant.IssuedCount == 0 && grant.CurrentRecordID != grant.InitialRecordID ||
			grant.IssuedCount > 0 && grant.CurrentRecordID != previousID {
			return ErrAuthorityStateRollback
		}
		if grant.PendingPublicationRecord != "" && (grant.IssuedCount == 0 || grant.PendingPublicationRecord != grant.CurrentRecordID ||
			grant.Issuances[len(grant.Issuances)-1].PublishedAt != "") {
			return ErrAuthorityStateRollback
		}
		if grant.PendingPublicationRecord == "" && grant.IssuedCount > 0 && grant.Issuances[len(grant.Issuances)-1].PublishedAt == "" {
			return ErrAuthorityStateRollback
		}
		if grant.PendingPublicationRecord != "" {
			if pendingHostsByID[grant.PendingPublicationRecord] {
				return ErrAuthorityStateRollback
			}
			pendingHostsByID[grant.PendingPublicationRecord] = true
		}
	}
	if state.ActiveLedgerGrant != "" {
		if _, ok := state.LedgerGrants[state.ActiveLedgerGrant]; !ok {
			return ErrAuthorityStateRollback
		}
	}
	for id, grant := range state.LedgerGrants {
		if id != grant.ID || !validAuthorityStateID(id) || !validRevocationLedgerState(grant.InitialState) ||
			grant.MaximumGeneration <= grant.InitialState.Generation || grant.CurrentGeneration != grant.InitialState.Generation+uint64(grant.IssuedCount) ||
			grant.CurrentGeneration > grant.MaximumGeneration || grant.IssuanceCap < 2 || grant.IssuanceCap > maxAuthorityRenewalIssuances ||
			grant.IssuedCount > grant.IssuanceCap || grant.MaximumGeneration-grant.InitialState.Generation < uint64(grant.IssuanceCap) ||
			len(grant.Issuances) != int(grant.IssuedCount) || !sortedUnique(grant.PendingPublicationHosts) {
			return ErrAuthorityStateRollback
		}
		expires, errExpires := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		warning, errWarning := time.Parse(time.RFC3339Nano, grant.RenewalWarningAt)
		if errExpires != nil || errWarning != nil || !warning.Before(expires) || expires.Sub(warning) < 2*ledgerRenewalCycle {
			return ErrAuthorityStateRollback
		}
		previous := cloneRevocationLedgerState(grant.InitialState)
		for index, issue := range grant.Issuances {
			issued, errIssued := time.Parse(time.RFC3339Nano, issue.IssuedAt)
			if errIssued != nil || issue.Generation != previous.Generation+1 || issue.PreviousRecordID != previous.RecordID ||
				!isLowerHexDigest(issue.RecordID) || issued.IsZero() {
				return ErrAuthorityStateRollback
			}
			ledger, err := ValidateRevocationLedger(issue.Envelope, previous, trusted, issued)
			if err != nil || ledger.Generation != issue.Generation || ledger.RecordID != issue.RecordID {
				return ErrAuthorityStateRollback
			}
			if issue.PublishedHosts != nil && (!sortedUnique(issue.PublishedHosts) || len(issue.PublishedHosts) > len(canonicalHostIDs)) {
				return ErrAuthorityStateRollback
			}
			if issue.PublishedAt != "" {
				published, err := time.Parse(time.RFC3339Nano, issue.PublishedAt)
				if err != nil || published.Before(issued) || len(issue.PublishedHosts) != len(canonicalHostIDs) ||
					!slices.Equal(issue.PublishedHosts, sortedCanonicalHostIDs()) {
					return ErrAuthorityStateRollback
				}
			}
			if index < len(grant.Issuances)-1 && issue.PublishedAt == "" {
				return ErrAuthorityStateRollback
			}
			previous = ledger
		}
		if grant.IssuedCount == 0 && (grant.CurrentGeneration != grant.InitialState.Generation || grant.CurrentRecordID != grant.InitialState.RecordID) ||
			grant.IssuedCount > 0 && (grant.CurrentGeneration != previous.Generation || grant.CurrentRecordID != previous.RecordID) {
			return ErrAuthorityStateRollback
		}
		if grant.PendingPublicationRecord != "" {
			if grant.IssuedCount == 0 || grant.PendingPublicationRecord != grant.CurrentRecordID ||
				len(grant.PendingPublicationHosts) == 0 || grant.Issuances[len(grant.Issuances)-1].PublishedAt != "" {
				return ErrAuthorityStateRollback
			}
		} else if len(grant.PendingPublicationHosts) != 0 || grant.IssuedCount > 0 && grant.Issuances[len(grant.Issuances)-1].PublishedAt == "" {
			return ErrAuthorityStateRollback
		}
	}
	if state.ActiveLedgerGrant != "" {
		grant := state.LedgerGrants[state.ActiveLedgerGrant]
		if grant.CurrentGeneration != state.LedgerGeneration || grant.CurrentRecordID != state.LedgerRecordID ||
			!slices.Equal(grant.InitialState.RevokedRecordIDs, state.RevokedRecordIDs) ||
			!slices.Equal(grant.InitialState.RevokedKeyIDs, state.RevokedKeyIDs) {
			return ErrAuthorityStateRollback
		}
	}
	for id, record := range state.TimeProbeRecords {
		if id != record.Challenge.ID || !validChallengeID(id) || !validNonce(record.Challenge.Nonce) ||
			!slices.Contains(canonicalHostIDs, record.HostID) || record.ReceivedMonotonicNanos == 0 || record.LedgerGeneration == 0 ||
			!validAuthorityHostPeer(authorityHostPeer{hostID: record.HostID, transport: record.Transport, peerURI: record.PeerURI, peerVerified: true}) {
			return ErrAuthorityStateRollback
		}
		if record.Used {
			if record.Expired {
				if len(record.Response) != 0 {
					return ErrAuthorityStateRollback
				}
				continue
			}
			var response signedTimeProbeResponse
			if len(record.Response) == 0 || decodeStrict(record.Response, &response) != nil || response.Nonce != record.Challenge.Nonce ||
				response.SignerKeyID != state.AnchorKeyID || verifyStoredAuthorityTimeProbe(record.Response, trusted) != nil {
				return ErrAuthorityStateRollback
			}
		} else if record.Expired || len(record.Response) != 0 {
			return ErrAuthorityStateRollback
		}
	}
	for id, alert := range state.RenewalAlerts {
		if id != alert.ID || alert.GrantID == "" || (alert.GrantKind != "directive" && alert.GrantKind != "ledger") ||
			(alert.Reason != "renewal_warning" && alert.Reason != "one_issuance_remaining") ||
			(alert.GrantKind == "directive" && state.DirectiveGrants[alert.GrantID].ID == "") ||
			(alert.GrantKind == "ledger" && state.LedgerGrants[alert.GrantID].ID == "") {
			return ErrAuthorityStateRollback
		}
		if _, err := time.Parse(time.RFC3339Nano, alert.CreatedAt); err != nil || alert.IssuanceCap < alert.Issued {
			return ErrAuthorityStateRollback
		}
	}
	return nil
}

func sameDirectiveContext(left, right ingressDirectivePayload) bool {
	return left.CandidateID == right.CandidateID && left.TransitionID == right.TransitionID && left.Lane == right.Lane &&
		left.HostID == right.HostID && left.ConfigurationDigest == right.ConfigurationDigest && left.Role == right.Role &&
		left.TransitionEpoch == right.TransitionEpoch && left.AdmissionGeneration == right.AdmissionGeneration &&
		left.RequiredLedgerGeneration == right.RequiredLedgerGeneration
}

func verifyStoredAuthorityTimeProbe(raw []byte, trusted map[string]TrustedKey) error {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ErrAuthorityStateRollback
	}
	var response signedTimeProbeResponse
	if decodeStrict(raw, &response) != nil || response.Version != ReleaseTimePolicyVersion || !validNonce(response.Nonce) || response.AuthorityUTCUnixMillis <= 0 {
		return ErrAuthorityStateRollback
	}
	key, ok := trusted[response.SignerKeyID]
	if !ok || key.Role != SignerRoleReleaseAuthority {
		return ErrAuthorityStateRollback
	}
	signature, err := decodeBase64Signature(response.Signature)
	body, bodyErr := canonicalProbeBody(response.TimeProbeBody)
	if err != nil || bodyErr != nil || !ed25519.Verify(key.PublicKey, append([]byte(AuthorityTimeProbeDomain), body...), signature) {
		return ErrAuthorityStateRollback
	}
	return nil
}

func authorityRenewalStatePreserved(before, after authorityRootState) bool {
	for id, old := range before.DirectiveGrants {
		current, ok := after.DirectiveGrants[id]
		if !ok || !sameDirectiveGrant(old, current) || current.IssuedCount < old.IssuedCount ||
			current.CurrentLeaseGeneration < old.CurrentLeaseGeneration || len(current.Issuances) < len(old.Issuances) {
			return false
		}
		for i := range old.Issuances {
			if !directiveRenewalIssuancePreserved(old.Issuances[i], current.Issuances[i], old.Template.HostID) {
				return false
			}
		}
		if old.PendingPublicationRecord != "" && current.PendingPublicationRecord == "" &&
			(old.IssuedCount == 0 || current.Issuances[len(current.Issuances)-1].PublishedAt == "") {
			return false
		}
	}
	for id, old := range before.LedgerGrants {
		current, ok := after.LedgerGrants[id]
		if !ok || !sameLedgerGrant(old, current) || current.IssuedCount < old.IssuedCount ||
			current.CurrentGeneration < old.CurrentGeneration || len(current.Issuances) < len(old.Issuances) {
			return false
		}
		for i := range old.Issuances {
			if !ledgerRenewalIssuancePreserved(old.Issuances[i], current.Issuances[i]) {
				return false
			}
		}
	}
	for id, old := range before.TimeProbeRecords {
		current, ok := after.TimeProbeRecords[id]
		if !ok || old.Challenge != current.Challenge || old.HostID != current.HostID || old.Transport != current.Transport ||
			old.PeerURI != current.PeerURI || old.LedgerGeneration != current.LedgerGeneration ||
			old.ReceivedMonotonicNanos != current.ReceivedMonotonicNanos || old.Used && !sameAuthorityProbeRecord(old, current) ||
			!old.Used && current.Used && (current.Expired == (len(current.Response) != 0)) {
			return false
		}
	}
	for id, old := range before.RenewalAlerts {
		current, ok := after.RenewalAlerts[id]
		if !ok || current != old {
			return false
		}
	}
	if before.LedgerGeneration == after.LedgerGeneration && (before.LedgerRecordID != after.LedgerRecordID ||
		before.LedgerIssuedAt != after.LedgerIssuedAt || before.LedgerExpiresAt != after.LedgerExpiresAt ||
		!slices.Equal(before.RevokedRecordIDs, after.RevokedRecordIDs) || !slices.Equal(before.RevokedKeyIDs, after.RevokedKeyIDs)) {
		return false
	}
	if after.LedgerGeneration > before.LedgerGeneration {
		active, ok := after.LedgerGrants[after.ActiveLedgerGrant]
		if !ok || after.LedgerGeneration != before.LedgerGeneration+1 || active.CurrentGeneration != after.LedgerGeneration ||
			active.CurrentRecordID != after.LedgerRecordID || !slices.Equal(before.RevokedRecordIDs, after.RevokedRecordIDs) ||
			!slices.Equal(before.RevokedKeyIDs, after.RevokedKeyIDs) {
			return false
		}
	}
	return true
}

func sameRenewalIssuance(left, right authorityRenewalIssuance) bool {
	return left.Generation == right.Generation && left.PreviousRecordID == right.PreviousRecordID &&
		left.RecordID == right.RecordID && left.IssuedAt == right.IssuedAt && bytes.Equal(left.Envelope, right.Envelope)
}

func directiveRenewalIssuancePreserved(before, after authorityRenewalIssuance, hostID string) bool {
	if !sameRenewalIssuance(before, after) {
		return false
	}
	if before.PublishedAt != "" {
		return before.PublishedAt == after.PublishedAt && slices.Equal(before.PublishedHosts, after.PublishedHosts)
	}
	if after.PublishedAt == "" {
		return len(after.PublishedHosts) == 0
	}
	return after.PublishedHosts != nil && slices.Equal(after.PublishedHosts, []string{hostID})
}

func ledgerRenewalIssuancePreserved(before, after authorityRenewalIssuance) bool {
	if !sameRenewalIssuance(before, after) || len(after.PublishedHosts) < len(before.PublishedHosts) {
		return false
	}
	for _, hostID := range before.PublishedHosts {
		if !slices.Contains(after.PublishedHosts, hostID) {
			return false
		}
	}
	if before.PublishedAt != "" {
		return before.PublishedAt == after.PublishedAt && slices.Equal(before.PublishedHosts, after.PublishedHosts)
	}
	return after.PublishedAt == "" || slices.Equal(after.PublishedHosts, sortedCanonicalHostIDs())
}

func decodeBase64Signature(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, ErrAuthorityStateRollback
	}
	return decoded, nil
}
