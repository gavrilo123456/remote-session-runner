package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	SecurityEndpointProtocolVersion = 1
	securityEndpointMaxMessageBytes = 1 << 20
)

type SecurityEndpointKind string

const (
	SecurityEndpointObserve       SecurityEndpointKind = "observe"
	SecurityEndpointAuthorityView SecurityEndpointKind = "authority-state"
	SecurityEndpointRelease       SecurityEndpointKind = "release-transport"
)

type SecurityEndpointOperation string

const (
	SecurityOperationObserveRecord       SecurityEndpointOperation = "observe-record"
	SecurityOperationAuthoritySnapshot   SecurityEndpointOperation = "authority-snapshot"
	SecurityOperationBeginTimeProbe      SecurityEndpointOperation = "time-probe-begin"
	SecurityOperationAcceptTimeProbe     SecurityEndpointOperation = "time-probe-accept"
	SecurityOperationVerifyReleaseRecord SecurityEndpointOperation = "record-verify-cache"
	SecurityOperationRetrieveOutbox      SecurityEndpointOperation = "outbox-retrieve"
	SecurityOperationAttestCapacity      SecurityEndpointOperation = "capacity-attest"
	SecurityOperationAttestPKIRequest    SecurityEndpointOperation = "pki-request-attest"
)

var (
	ErrSecurityEndpointDenied      = errors.New("security endpoint peer or operation denied")
	ErrSecurityEndpointUnavailable = errors.New("security endpoint operation unavailable")
	ErrSecurityEndpointProtocol    = errors.New("invalid security endpoint protocol message")
	linuxServiceUnitPattern        = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)
	linuxServiceUserPattern        = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
	macLaunchdLabelPattern         = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	macCodeRequirementPattern      = regexp.MustCompile(`^identifier "([A-Za-z0-9.-]{1,255})" and anchor apple generic and certificate leaf\[subject\.OU\] = "([A-Z0-9]{10})"$`)
)

// SecurityEndpointRequest is the complete wire envelope. Each operation has
// its own closed body type; callers cannot submit paths, measured values, key
// bytes, or a generic command through this protocol.
type SecurityEndpointRequest struct {
	Version   int                       `json:"version"`
	Operation SecurityEndpointOperation `json:"operation"`
	Body      json.RawMessage           `json:"body"`
}

type SecurityEndpointResponse struct {
	Version int             `json:"version"`
	Body    json.RawMessage `json:"body,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type ObserveRecordRequest struct {
	Envelope       json.RawMessage `json:"envelope"`
	TimeProbe      json.RawMessage `json:"time_probe"`
	ExpectedHostID string          `json:"expected_host_id"`
	Lane           string          `json:"lane"`
	Configuration  string          `json:"resolved_configuration_digest"`
}

type TimeProbeAcceptRequest struct {
	ChallengeID string          `json:"challenge_id"`
	Response    json.RawMessage `json:"response"`
}

type AddressedRecordRequest struct {
	RecordID string          `json:"record_id"`
	Envelope json.RawMessage `json:"envelope"`
}

type OutboxRetrieveRequest struct {
	RecordID string `json:"record_id"`
}

type PKIRequestAttestationRequest struct {
	MandateID string `json:"pki_issuance_mandate_id"`
}

// AnchorAuthoritySnapshot is the minimum immutable public trust/high-water
// view the Mac authority agent needs. It intentionally contains no private
// material and no mutation handles.
type AnchorAuthoritySnapshot struct {
	SchemaVersion        int                                 `json:"schema_version"`
	Generation           uint64                              `json:"generation"`
	HostID               string                              `json:"host_id"`
	AuthorityKeys        []ReleaseKeyState                   `json:"authority_keys"`
	HostAttestationKeys  []ReleaseKeyState                   `json:"host_attestation_keys"`
	RevocationLedger     RevocationLedgerState               `json:"revocation_ledger"`
	HighestAuthorityTime time.Time                           `json:"highest_authority_time,omitempty"`
	DirectiveHighWater   map[string]AnchorDirectiveHighWater `json:"directive_high_water"`
	ActiveRuntime        map[string]AnchorRuntimeContext     `json:"active_runtime"`
	PendingCandidate     map[string]AnchorRuntimeContext     `json:"pending_candidate"`
}

type SecurityEndpointOperations interface {
	ObserveRecord(context.Context, ObserveRecordRequest) (any, error)
	AuthoritySnapshot(context.Context) (AnchorAuthoritySnapshot, error)
	BeginTimeProbe(context.Context) (TimeProbeChallenge, error)
	AcceptTimeProbe(context.Context, TimeProbeAcceptRequest) (any, error)
	VerifyAndCacheRecord(context.Context, AddressedRecordRequest) (any, error)
	RetrieveOutboxRecord(context.Context, OutboxRetrieveRequest) (any, error)
	AttestCapacity(context.Context) (any, error)
	AttestPKIRequest(context.Context, PKIRequestAttestationRequest) (any, error)
}

type peerExpectation struct {
	kind                  SecurityEndpointKind
	goos                  string
	hostID                string
	runnerUID             uint32
	transportUID          uint32
	transportUser         string
	transportUnit         string
	transportControlGroup string
	transportExecutable   string
	launchdLabel          string
	codeRequirement       string
}

type observedPeer struct {
	uid             uint32
	pid             int
	unit            string
	controlGroup    string
	executablePath  string
	executableUID   uint32
	executableMode  os.FileMode
	executableDev   uint64
	executableInode uint64
	executableHash  string
	serviceAccount  bool
	launchdLabel    string
	codeRequirement string
	codeValid       bool
	auditToken      [8]uint32
}

// newSecurityEndpoint returns a connection handler. The caller must supply an
// expectation obtained from its validated, root-owned installation contract;
// peer facts themselves are always collected from the accepted socket by the
// platform implementation.
func newSecurityEndpoint(kind SecurityEndpointKind, ops SecurityEndpointOperations, expectation peerExpectation) (*SecurityEndpoint, error) {
	if ops == nil || expectation.kind != kind || expectation.goos != runtime.GOOS {
		return nil, fmt.Errorf("%w: endpoint construction lacks operations or matching peer expectation", ErrSecurityEndpointUnavailable)
	}
	if !validEndpointKind(kind) {
		return nil, fmt.Errorf("%w: unknown endpoint kind %q", ErrSecurityEndpointUnavailable, kind)
	}
	if err := validatePeerExpectation(expectation); err != nil {
		return nil, err
	}
	return &SecurityEndpoint{kind: kind, ops: ops, expectation: expectation}, nil
}

type SecurityEndpoint struct {
	kind        SecurityEndpointKind
	ops         SecurityEndpointOperations
	expectation peerExpectation
}

// ServeListener serves a launchd/systemd supplied Unix listener. It never
// creates or unlinks the production socket itself; the service manager owns
// the fixed endpoint path and permissions.
func (e *SecurityEndpoint) ServeListener(ctx context.Context, listener *net.UnixListener) error {
	if e == nil || listener == nil || ctx == nil {
		return ErrSecurityEndpointUnavailable
	}
	wantPath, err := endpointSocketPath(e.kind, e.expectation.goos)
	if err != nil {
		return err
	}
	address, ok := listener.Addr().(*net.UnixAddr)
	if !ok || address.Name != wantPath {
		return fmt.Errorf("%w: listener does not match its fixed endpoint path", ErrSecurityEndpointDenied)
	}
	listener.SetUnlinkOnClose(false)
	var active sync.WaitGroup
	sem := make(chan struct{}, 16)
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer func() {
		stopClose()
		active.Wait()
	}()
	for {
		conn, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if temporary, ok := acceptErr.(interface{ Temporary() bool }); ok && temporary.Temporary() {
				continue
			}
			return fmt.Errorf("accept fixed security endpoint connection: %w", acceptErr)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			_ = conn.Close()
			return ctx.Err()
		}
		active.Add(1)
		go func(conn *net.UnixConn) {
			defer active.Done()
			defer func() { <-sem }()
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			_ = e.ServeConn(ctx, conn)
			_ = conn.Close()
		}(conn)
	}
}

func (e *SecurityEndpoint) ServeConn(ctx context.Context, conn *net.UnixConn) error {
	if e == nil || conn == nil || e.ops == nil {
		return ErrSecurityEndpointUnavailable
	}
	peer, err := inspectUnixPeer(conn, e.expectation)
	if err != nil || !peerAllowed(e.expectation, peer) {
		return fmt.Errorf("%w: %v", ErrSecurityEndpointDenied, err)
	}
	request, err := readSecurityEndpointRequest(conn)
	if err != nil {
		return err
	}
	responseBody, err := e.dispatch(ctx, request)
	response := SecurityEndpointResponse{Version: SecurityEndpointProtocolVersion}
	if err != nil {
		response.Error = safeEndpointError(err)
	} else if responseBody != nil {
		response.Body, err = json.Marshal(responseBody)
		if err != nil {
			return fmt.Errorf("encode security endpoint response: %w", err)
		}
	}
	if err := json.NewEncoder(conn).Encode(response); err != nil {
		return fmt.Errorf("write security endpoint response: %w", err)
	}
	return nil
}

func (e *SecurityEndpoint) dispatch(ctx context.Context, request SecurityEndpointRequest) (any, error) {
	if request.Version != SecurityEndpointProtocolVersion {
		return nil, fmt.Errorf("%w: unsupported version", ErrSecurityEndpointProtocol)
	}
	switch e.kind {
	case SecurityEndpointObserve:
		if request.Operation != SecurityOperationObserveRecord {
			return nil, ErrSecurityEndpointDenied
		}
		var body ObserveRecordRequest
		if err := decodeEndpointBody(request.Body, &body); err != nil || len(body.Envelope) == 0 || len(body.TimeProbe) == 0 ||
			body.ExpectedHostID != e.expectation.hostID || !validAnchorLane(body.Lane) || !validDigestID(body.Configuration) {
			return nil, fmt.Errorf("%w: invalid observe request", ErrSecurityEndpointProtocol)
		}
		return e.ops.ObserveRecord(ctx, body)
	case SecurityEndpointAuthorityView:
		if request.Operation != SecurityOperationAuthoritySnapshot || !emptyEndpointBody(request.Body) {
			return nil, ErrSecurityEndpointDenied
		}
		return e.ops.AuthoritySnapshot(ctx)
	case SecurityEndpointRelease:
		switch request.Operation {
		case SecurityOperationBeginTimeProbe:
			if !emptyEndpointBody(request.Body) {
				return nil, ErrSecurityEndpointProtocol
			}
			return e.ops.BeginTimeProbe(ctx)
		case SecurityOperationAcceptTimeProbe:
			var body TimeProbeAcceptRequest
			if err := decodeEndpointBody(request.Body, &body); err != nil || !validOpaqueID(body.ChallengeID) || len(body.Response) == 0 {
				return nil, fmt.Errorf("%w: invalid time-probe response", ErrSecurityEndpointProtocol)
			}
			return e.ops.AcceptTimeProbe(ctx, body)
		case SecurityOperationVerifyReleaseRecord:
			var body AddressedRecordRequest
			if err := decodeEndpointBody(request.Body, &body); err != nil || !validDigestID(body.RecordID) || len(body.Envelope) == 0 {
				return nil, fmt.Errorf("%w: invalid addressed release record", ErrSecurityEndpointProtocol)
			}
			return e.ops.VerifyAndCacheRecord(ctx, body)
		case SecurityOperationRetrieveOutbox:
			var body OutboxRetrieveRequest
			if err := decodeEndpointBody(request.Body, &body); err != nil || !validDigestID(body.RecordID) {
				return nil, fmt.Errorf("%w: invalid outbox lookup", ErrSecurityEndpointProtocol)
			}
			return e.ops.RetrieveOutboxRecord(ctx, body)
		case SecurityOperationAttestCapacity:
			if !emptyEndpointBody(request.Body) {
				return nil, fmt.Errorf("%w: capacity profile/value/path is caller-controlled", ErrSecurityEndpointDenied)
			}
			return e.ops.AttestCapacity(ctx)
		case SecurityOperationAttestPKIRequest:
			var body PKIRequestAttestationRequest
			if err := decodeEndpointBody(request.Body, &body); err != nil || !validOpaqueID(body.MandateID) {
				return nil, fmt.Errorf("%w: invalid PKI mandate reference", ErrSecurityEndpointProtocol)
			}
			return e.ops.AttestPKIRequest(ctx, body)
		default:
			return nil, ErrSecurityEndpointDenied
		}
	default:
		return nil, ErrSecurityEndpointDenied
	}
}

func (a *SecurityAnchor) AuthoritySnapshot() (AnchorAuthoritySnapshot, error) {
	state, err := a.Snapshot()
	if err != nil {
		return AnchorAuthoritySnapshot{}, err
	}
	return AnchorAuthoritySnapshot{
		SchemaVersion: state.SchemaVersion, Generation: state.Generation, HostID: state.HostID,
		AuthorityKeys: cloneReleaseKeys(state.AuthorityKeys), HostAttestationKeys: cloneReleaseKeys(state.HostAttestationKeys),
		RevocationLedger: cloneRevocationLedger(state.RevocationLedger), HighestAuthorityTime: state.HighestAuthorityTime,
		DirectiveHighWater: cloneDirectiveHighWater(state.DirectiveHighWater),
		ActiveRuntime:      cloneRuntimeContexts(state.ActiveRuntime), PendingCandidate: cloneRuntimeContexts(state.PendingCandidate),
	}, nil
}

func cloneReleaseKeys(keys []ReleaseKeyState) []ReleaseKeyState {
	copyOf := make([]ReleaseKeyState, len(keys))
	for i, key := range keys {
		copyOf[i] = key
		copyOf[i].PublicKey = append([]byte(nil), key.PublicKey...)
	}
	return copyOf
}

func cloneRevocationLedger(ledger RevocationLedgerState) RevocationLedgerState {
	if ledger.RevokedRecordIDs != nil {
		ledger.RevokedRecordIDs = append([]string{}, ledger.RevokedRecordIDs...)
	}
	if ledger.RevokedKeyIDs != nil {
		ledger.RevokedKeyIDs = append([]string{}, ledger.RevokedKeyIDs...)
	}
	return ledger
}

func cloneDirectiveHighWater(values map[string]AnchorDirectiveHighWater) map[string]AnchorDirectiveHighWater {
	copyOf := make(map[string]AnchorDirectiveHighWater, len(values))
	for key, value := range values {
		copyOf[key] = value
	}
	return copyOf
}

func cloneRuntimeContexts(values map[string]AnchorRuntimeContext) map[string]AnchorRuntimeContext {
	copyOf := make(map[string]AnchorRuntimeContext, len(values))
	for key, value := range values {
		copyOf[key] = value
	}
	return copyOf
}

func readSecurityEndpointRequest(reader io.Reader) (SecurityEndpointRequest, error) {
	limited := io.LimitReader(reader, securityEndpointMaxMessageBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return SecurityEndpointRequest{}, fmt.Errorf("read security endpoint request: %w", err)
	}
	if len(data) == 0 || len(data) > securityEndpointMaxMessageBytes {
		return SecurityEndpointRequest{}, ErrSecurityEndpointProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request SecurityEndpointRequest
	if err := decoder.Decode(&request); err != nil {
		return SecurityEndpointRequest{}, fmt.Errorf("%w: decode envelope: %v", ErrSecurityEndpointProtocol, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return SecurityEndpointRequest{}, ErrSecurityEndpointProtocol
	}
	if len(bytes.TrimSpace(request.Body)) == 0 {
		request.Body = json.RawMessage(`{}`)
	}
	return request, nil
}

func decodeEndpointBody(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrSecurityEndpointProtocol
	}
	return nil
}

func emptyEndpointBody(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte(`{}`)) || bytes.Equal(bytes.TrimSpace(raw), []byte(`null`))
}

func validEndpointKind(kind SecurityEndpointKind) bool {
	switch kind {
	case SecurityEndpointObserve, SecurityEndpointAuthorityView, SecurityEndpointRelease:
		return true
	default:
		return false
	}
}

func validatePeerExpectation(expectation peerExpectation) error {
	if expectation.goos != "darwin" && expectation.goos != "linux" {
		return fmt.Errorf("%w: unsupported peer platform %q", ErrSecurityEndpointUnavailable, expectation.goos)
	}
	if !isCanonicalHostID(expectation.hostID) {
		return fmt.Errorf("%w: peer policy lacks the fixed canonical host ID", ErrSecurityEndpointUnavailable)
	}
	switch expectation.kind {
	case SecurityEndpointObserve:
		if expectation.runnerUID == 0 {
			return fmt.Errorf("%w: observe endpoint cannot authorize root", ErrSecurityEndpointUnavailable)
		}
	case SecurityEndpointAuthorityView:
		if expectation.goos != "darwin" || !validMacPeerExpectation(expectation) {
			return fmt.Errorf("%w: authority-state endpoint requires a configured Mac agent identity", ErrSecurityEndpointUnavailable)
		}
	case SecurityEndpointRelease:
		if expectation.goos == "linux" {
			if expectation.transportUID == 0 || expectation.transportUser != "runner-release-transport" ||
				expectation.transportUnit != "runner-release-transport.service" ||
				expectation.transportControlGroup != "/system.slice/"+expectation.transportUnit ||
				!filepath.IsAbs(expectation.transportExecutable) || filepath.Clean(expectation.transportExecutable) != expectation.transportExecutable {
				return fmt.Errorf("%w: incomplete Linux transport identity", ErrSecurityEndpointUnavailable)
			}
		} else if !validMacPeerExpectation(expectation) {
			return fmt.Errorf("%w: incomplete Mac transport identity", ErrSecurityEndpointUnavailable)
		}
	default:
		return fmt.Errorf("%w: invalid endpoint kind", ErrSecurityEndpointUnavailable)
	}
	return nil
}

func validMacPeerExpectation(expectation peerExpectation) bool {
	if !macLaunchdLabelPattern.MatchString(expectation.launchdLabel) {
		return false
	}
	wantLabel := ""
	switch expectation.kind {
	case SecurityEndpointAuthorityView:
		wantLabel = "runner-release-authority-agent"
	case SecurityEndpointRelease:
		wantLabel = "runner-release-transport"
	default:
		return false
	}
	matches := macCodeRequirementPattern.FindStringSubmatch(expectation.codeRequirement)
	return expectation.launchdLabel == wantLabel && len(matches) == 3 && strings.HasSuffix(matches[1], "."+wantLabel)
}

func peerAllowed(expectation peerExpectation, peer observedPeer) bool {
	if peer.pid <= 0 {
		return false
	}
	switch expectation.kind {
	case SecurityEndpointObserve:
		return peer.uid != 0 && peer.uid == expectation.runnerUID
	case SecurityEndpointAuthorityView:
		return expectation.goos == "darwin" && peer.launchdLabel == expectation.launchdLabel && peer.codeValid && peer.codeRequirement == expectation.codeRequirement
	case SecurityEndpointRelease:
		if expectation.goos == "darwin" {
			return peer.launchdLabel == expectation.launchdLabel && peer.codeValid && peer.codeRequirement == expectation.codeRequirement
		}
		return peer.uid != 0 && peer.uid == expectation.transportUID && peer.serviceAccount &&
			peer.unit == expectation.transportUnit && peer.controlGroup == expectation.transportControlGroup &&
			peer.executablePath == expectation.transportExecutable && peer.executableUID == 0 &&
			peer.executableMode.IsRegular() && peer.executableMode.Perm()&0o022 == 0 &&
			peer.executableDev != 0 && peer.executableInode != 0 && peer.executableHash != ""
	default:
		return false
	}
}

func validDigestID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validOpaqueID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func safeEndpointError(err error) string {
	if errors.Is(err, ErrSecurityEndpointDenied) || errors.Is(err, ErrSecurityEndpointProtocol) {
		return "request denied"
	}
	if errors.Is(err, ErrSecurityEndpointUnavailable) {
		return "operation unavailable"
	}
	return "operation failed closed"
}

// protectedExecutableIdentity hashes the descriptor of the running process,
// not a caller-selected path. The platform inspector supplies the descriptor
// facts after confirming it is the fixed root-owned installation inode.
func protectedExecutableIdentity(hash string, mode os.FileMode, uid uint32, dev, inode uint64) bool {
	return validDigestID(hash) && mode.IsRegular() && mode.Perm()&0o022 == 0 && uid == 0 && dev != 0 && inode != 0
}

func sha256Hex(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:])
}

func endpointSocketPath(kind SecurityEndpointKind, goos string) (string, error) {
	if goos == "darwin" {
		switch kind {
		case SecurityEndpointObserve:
			return "/var/run/remote-session-runner/security-anchor.sock", nil
		case SecurityEndpointAuthorityView:
			return "/var/run/remote-session-runner/security-anchor-authority-state.sock", nil
		case SecurityEndpointRelease:
			return "/var/run/remote-session-runner/security-anchor-release-transport.sock", nil
		}
	}
	if goos == "linux" {
		switch kind {
		case SecurityEndpointObserve:
			return "/var/run/remote-session-runner/security-anchor.sock", nil
		case SecurityEndpointRelease:
			return "/var/run/remote-session-runner/security-anchor-release-transport.sock", nil
		}
	}
	return "", fmt.Errorf("%w: no socket path for %s on %s", ErrSecurityEndpointUnavailable, kind, goos)
}

func runEndpointContext(ctx context.Context, fn func() error) error {
	if ctx == nil {
		return ErrSecurityEndpointUnavailable
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fn()
	}
}

func validLinuxTransportExpectation(expectation peerExpectation) bool {
	return expectation.transportUser == "runner-release-transport" &&
		linuxServiceUserPattern.MatchString(expectation.transportUser) &&
		expectation.transportUnit == "runner-release-transport.service" &&
		expectation.transportControlGroup == "/system.slice/"+expectation.transportUnit &&
		filepath.IsAbs(expectation.transportExecutable) && filepath.Clean(expectation.transportExecutable) == expectation.transportExecutable
}
