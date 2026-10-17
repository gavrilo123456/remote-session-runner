package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrAuthorityDenied  = errors.New("release authority operation denied")
	ErrAuthorityMandate = errors.New("release authority mandate is invalid or unavailable")
	ErrAuthorityReplay  = errors.New("release authority approval or record replay rejected")
	ErrAuthorityKey     = errors.New("release authority key unavailable or mismatched")
	ErrAuthorityPlan    = errors.New("release authority plan is invalid")
)

// authorityAgentEvidence is constructed by the Mac socket adapter after it
// validates the audit token, launchd label and code identity. It is never
// decoded from a request body.
type authorityAgentEvidence struct {
	UID                 uint32
	PID                 int
	AuditTokenValidated bool
	LaunchdLabel        string
	CodeRequirement     string
}

type authorityAgentIdentity struct {
	UID             uint32
	LaunchdLabel    string
	CodeRequirement string
}

func authorityAgentAllowed(evidence authorityAgentEvidence, expected authorityAgentIdentity, goos string) bool {
	if evidence.UID != expected.UID || evidence.PID <= 0 {
		return false
	}
	return goos == "darwin" && expected.UID == 0 && evidence.AuditTokenValidated &&
		evidence.LaunchdLabel == expected.LaunchdLabel && evidence.CodeRequirement == expected.CodeRequirement &&
		validMacPeerExpectation(peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
			launchdLabel: expected.LaunchdLabel, codeRequirement: expected.CodeRequirement})
}

type authorityKeyProvider interface {
	LoadAuthorityKey(string) (SigningIdentity, error)
}

type fixedAuthorityKeyProvider struct{}

func (fixedAuthorityKeyProvider) LoadAuthorityKey(keyID string) (SigningIdentity, error) {
	if runtime.GOOS != "darwin" || os.Geteuid() != 0 || !validKeyID(keyID) {
		return SigningIdentity{}, ErrAuthorityKey
	}
	path := "/Library/Application Support/RemoteSessionRunner-release-authority-signer/release-authority.ed25519"
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return SigningIdentity{}, ErrAuthorityKey
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0o777 != 0o600 {
		return SigningIdentity{}, ErrAuthorityKey
	}
	privateKey, err := io.ReadAll(io.LimitReader(file, ed25519.PrivateKeySize+1))
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return SigningIdentity{}, ErrAuthorityKey
	}
	return SigningIdentity{KeyID: keyID, Role: SignerRoleReleaseAuthority, PrivateKey: ed25519.PrivateKey(privateKey)}, nil
}

// InteractiveAuthorityAuthorizer is implemented by the fixed Mac approval
// frontend. The frontend has no signer-socket access; tests inject an isolated
// authorizer so no production credential or UI is needed for H-tier checks.
type InteractiveAuthorityAuthorizer interface {
	AuthorizeAuthorityMandate(context.Context, string) (string, error)
}

type AuthorityApprovalTool struct {
	journal    *AuthorityJournal
	authorizer InteractiveAuthorityAuthorizer
	now        func() time.Time
	newID      func() (string, error)
}

func NewAuthorityApprovalTool(journal *AuthorityJournal, authorizer InteractiveAuthorityAuthorizer) (*AuthorityApprovalTool, error) {
	if journal == nil || authorizer == nil {
		return nil, ErrAuthorityStateUnavailable
	}
	return &AuthorityApprovalTool{journal: journal, authorizer: authorizer, now: time.Now, newID: newAuthorityID}, nil
}

// CreateMandate returns only an opaque receipt. The exact body is already
// staged in protected state and the signer never accepts a caller body.
func (t *AuthorityApprovalTool) CreateMandate(ctx context.Context, planID string, lifetime time.Duration) (string, error) {
	if t == nil || t.journal == nil || t.authorizer == nil || !validAuthorityStateID(planID) || lifetime <= 0 || lifetime > MaxAuthorizationLifetime {
		return "", ErrAuthorityMandate
	}
	state, err := t.journal.snapshot()
	if err != nil {
		return "", err
	}
	plan, ok := state.Plans[planID]
	if !ok || plan.LedgerGeneration != state.LedgerGeneration {
		return "", ErrAuthorityPlan
	}
	message := "Approve one release authority signature for " + plan.RecordType + " body sha256:" + plan.BodyDigest +
		" context sha256:" + plan.ContextDigest + " at ledger generation " + fmt.Sprint(plan.LedgerGeneration)
	receiptID, err := t.authorizer.AuthorizeAuthorityMandate(ctx, message)
	if err != nil || !validAuthorityStateID(receiptID) {
		return "", ErrAuthorityDenied
	}
	mandateID, err := t.newID()
	if err != nil {
		return "", err
	}
	expires := t.now().UTC().Add(lifetime).Format(time.RFC3339Nano)
	mandate := AuthorityMandate{ID: mandateID, PlanID: plan.ID, RecordType: plan.RecordType, BodyDigest: plan.BodyDigest,
		ContextDigest: plan.ContextDigest, LedgerGeneration: plan.LedgerGeneration, ExpiresAt: expires, ApprovalReceipt: receiptID}
	err = t.journal.issueMandate(mandate)
	if err != nil {
		return "", err
	}
	return mandateID, nil
}

type AuthorityAgent struct {
	journal  *AuthorityJournal
	expected authorityAgentIdentity
	goos     string
	now      func() time.Time
	newID    func() (string, error)
}

func NewAuthorityAgent(journal *AuthorityJournal, expected authorityAgentIdentity, goos string) (*AuthorityAgent, error) {
	if journal == nil || goos != "darwin" || expected.UID != 0 {
		return nil, ErrAuthorityStateUnavailable
	}
	if !validMacPeerExpectation(peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
		launchdLabel: expected.LaunchdLabel, codeRequirement: expected.CodeRequirement}) {
		return nil, ErrAuthorityStateUnavailable
	}
	return &AuthorityAgent{journal: journal, expected: expected, goos: goos, now: time.Now, newID: newAuthorityID}, nil
}

// StagePlan imports one canonical closed authority body into root state. The
// signer itself receives only a mandate ID and retrieves this staged body.
func (a *AuthorityAgent) StagePlan(ctx context.Context, evidence authorityAgentEvidence, recordType string, payload []byte, ledgerGeneration uint64) (string, error) {
	if a == nil || !authorityAgentAllowed(evidence, a.expected, a.goos) || !authorityRecordType(recordType) {
		return "", ErrAuthorityDenied
	}
	canonical, err := CanonicalizeJSON(payload)
	if err != nil || !bytesEqual(canonical, payload) || ValidatePayload(recordType, canonical) != nil {
		return "", ErrAuthorityPlan
	}
	contextDigest, bodyLedger, err := authorityPlanContext(recordType, canonical)
	if err != nil || bodyLedger != 0 && bodyLedger != ledgerGeneration {
		return "", ErrAuthorityPlan
	}
	state, err := a.journal.snapshot()
	if err != nil {
		return "", err
	}
	if ledgerGeneration != state.LedgerGeneration {
		return "", ErrAuthorityPlan
	}
	id, err := a.newID()
	if err != nil {
		return "", err
	}
	plan := authorityPlan{ID: id, RecordType: recordType, Payload: append(json.RawMessage(nil), canonical...),
		BodyDigest: hexDigest(canonical), ContextDigest: contextDigest, LedgerGeneration: ledgerGeneration,
		StagedAt: a.now().UTC().Format(time.RFC3339Nano)}
	err = a.journal.stagePlan(plan)
	if err != nil {
		return "", err
	}
	return id, nil
}

type AuthoritySigner struct {
	journal      *AuthorityJournal
	keys         authorityKeyProvider
	expected     authorityAgentIdentity
	goos         string
	clock        func() time.Time
	monotonicNow func() (uint64, error)
}

func NewAuthoritySigner(journal *AuthorityJournal, keys authorityKeyProvider, expected authorityAgentIdentity, goos string) (*AuthoritySigner, error) {
	if journal == nil || keys == nil || goos != "darwin" || expected.UID != 0 ||
		!validMacPeerExpectation(peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
			launchdLabel: expected.LaunchdLabel, codeRequirement: expected.CodeRequirement}) {
		return nil, ErrAuthorityStateUnavailable
	}
	return &AuthoritySigner{journal: journal, keys: keys, expected: expected, goos: goos, clock: time.Now,
		monotonicNow: anchorMonotonicNanos}, nil
}

// Sign consumes one exact approved plan. A retry after durable consumption
// returns the same saved envelope; it never signs a second body or advances a
// second authority record.
func (s *AuthoritySigner) Sign(ctx context.Context, evidence authorityAgentEvidence, mandateID string) ([]byte, error) {
	if s == nil || !authorityAgentAllowed(evidence, s.expected, s.goos) || !validAuthorityStateID(mandateID) {
		return nil, ErrAuthorityDenied
	}
	state, err := s.journal.snapshot()
	if err != nil {
		return nil, err
	}
	if saved, ok := state.ConsumedMandates[mandateID]; ok {
		return append([]byte(nil), saved.Envelope...), nil
	}
	mandate, ok := state.Mandates[mandateID]
	if !ok {
		return nil, ErrAuthorityMandate
	}
	plan, ok := state.Plans[mandate.PlanID]
	if !ok || plan.ID != mandate.PlanID || plan.RecordType != mandate.RecordType || plan.BodyDigest != mandate.BodyDigest ||
		plan.ContextDigest != mandate.ContextDigest || plan.LedgerGeneration != mandate.LedgerGeneration ||
		plan.LedgerGeneration != state.LedgerGeneration || !authorityRecordType(plan.RecordType) ||
		hexDigest(plan.Payload) != mandate.BodyDigest {
		return nil, ErrAuthorityMandate
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, mandate.ExpiresAt)
	if err != nil || !expiresAt.After(s.clock().UTC()) {
		return nil, ErrAuthorityMandate
	}
	canonical, err := CanonicalizeJSON(plan.Payload)
	if err != nil || !bytesEqual(canonical, plan.Payload) || ValidatePayload(plan.RecordType, canonical) != nil {
		return nil, ErrAuthorityPlan
	}
	contextDigest, bodyLedger, err := authorityPlanContext(plan.RecordType, canonical)
	if err != nil || contextDigest != mandate.ContextDigest || bodyLedger != 0 && bodyLedger != mandate.LedgerGeneration {
		return nil, ErrAuthorityMandate
	}
	anchorKey, err := decodeAuthorityAnchorKey(state)
	if err != nil {
		return nil, err
	}
	identity, err := s.keys.LoadAuthorityKey(state.AnchorKeyID)
	if err != nil || identity.KeyID != state.AnchorKeyID || len(identity.PrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(identity.PrivateKey.Public().(ed25519.PublicKey), anchorKey) {
		return nil, ErrAuthorityKey
	}
	identity.PrivateKey = append(ed25519.PrivateKey(nil), identity.PrivateKey...)
	defer clear(identity.PrivateKey)
	identity.Role = recordSignerRoles[plan.RecordType]
	if identity.Role != SignerRoleReleaseAuthority && identity.Role != SignerRolePreviousAuthorityKey {
		return nil, ErrAuthorityDenied
	}
	if identity.Role == SignerRolePreviousAuthorityKey {
		var payloadMap map[string]json.RawMessage
		if json.Unmarshal(canonical, &payloadMap) != nil || string(bytesTrimQuotes(payloadMap["previous_key_id"])) != identity.KeyID {
			return nil, ErrAuthorityPlan
		}
	}
	envelope, err := SignEnvelope(plan.RecordType, canonical, identity)
	if err != nil {
		return nil, err
	}
	verified, err := VerifyEnvelope(envelope, map[string]TrustedKey{state.AnchorKeyID: {
		KeyID: state.AnchorKeyID, Role: SignerRoleReleaseAuthority, PublicKey: anchorKey,
	}})
	if err != nil {
		return nil, err
	}
	_, err = s.journal.mutate(func(next *authorityRootState) error {
		currentMandate, exists := next.Mandates[mandateID]
		currentPlan, planExists := next.Plans[mandate.PlanID]
		if !exists || !planExists || currentMandate != mandate || currentPlan.BodyDigest != plan.BodyDigest ||
			currentPlan.ContextDigest != plan.ContextDigest || next.LedgerGeneration != mandate.LedgerGeneration {
			return ErrAuthorityReplay
		}
		if _, exists := next.AcceptedRecordIDs[verified.RecordID]; exists {
			return ErrAuthorityReplay
		}
		delete(next.Mandates, mandateID)
		delete(next.Plans, plan.ID)
		next.ConsumedMandates[mandateID] = authoritySignedResult{MandateID: mandateID, RecordID: verified.RecordID,
			Mandate: mandate, Envelope: append(json.RawMessage(nil), envelope...)}
		next.AcceptedRecordIDs[verified.RecordID] = plan.ContextDigest
		next.TransitionHeads[plan.ContextDigest] = verified.RecordID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

func authorityRecordType(recordType string) bool {
	role, ok := recordSignerRoles[recordType]
	return ok && (role == SignerRoleReleaseAuthority || role == SignerRolePreviousAuthorityKey)
}

func authorityPlanContext(recordType string, payload []byte) (string, uint64, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return "", 0, ErrAuthorityPlan
	}
	keys := []string{"candidate_manifest_id", "lane_transition_id", "lane", "release_lane", "host_id", "capacity_policy_id",
		"predecessor_record_id", "predecessor_acceptance_id", "predecessor_manifest_id", "predecessor_kind",
		"predecessor_installation_generation", "rollback_artifact_digest", "bootstrap_contract_digest", "bootstrap_installation_generation", "completed_cohort_evidence_digest",
		"host_management_manifest_id", "pki_certificate_request_id", "issuer_receipt", "certificate_trust_package_digest", "active_peer_set_digest", "issuer_artifact_tuple", "installer_artifact_tuple",
		"installation_generation", "ledger_generation", "required_ledger_generation", "required_revocation_ledger_generation",
		"required_host_management_manifest_id", "key_set_generation", "authority_key_set_generation", "previous_key_id", "successor_key_id", "policy_scope", "directive_role",
		"transition_epoch", "admission_generation", "lease_generation", "resolved_configuration_digest", "capacity_evaluation_digest"}
	context := map[string]json.RawMessage{"record_type": json.RawMessage(fmt.Sprintf("%q", recordType))}
	for _, key := range keys {
		if value, ok := fields[key]; ok {
			context[key] = append(json.RawMessage(nil), value...)
		}
	}
	canonical, err := CanonicalizeJSON(authorityMarshalJSON(context))
	if err != nil {
		return "", 0, ErrAuthorityPlan
	}
	var ledger uint64
	for _, key := range []string{"required_ledger_generation", "required_revocation_ledger_generation", "ledger_generation"} {
		if value, ok := fields[key]; ok {
			if json.Unmarshal(value, &ledger) != nil {
				return "", 0, ErrAuthorityPlan
			}
			break
		}
	}
	return hexDigest(canonical), ledger, nil
}

func decodeAuthorityAnchorKey(state authorityRootState) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(state.AnchorPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || !validKeyID(state.AnchorKeyID) {
		return nil, ErrAuthorityKey
	}
	return ed25519.PublicKey(key), nil
}

func newAuthorityID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func authorityMarshalJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func bytesEqual(left, right []byte) bool {
	return bytes.Equal(left, right)
}

func bytesTrimQuotes(value []byte) []byte {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	return value
}
