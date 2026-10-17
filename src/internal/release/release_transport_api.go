package release

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	releaseTransportMaxRequestBytes = 16 << 20
	releaseTransportSocketPath      = "/var/run/remote-session-runner/release-transport.sock"
	releaseTransportAuthorityURI    = "urn:remote-session-runner:release-authority:mac-control-plane"
)

var (
	ErrReleaseTransportUnavailable = errors.New("release transport unavailable")
	ErrReleaseTransportDenied      = errors.New("release transport request denied")
	ErrReleaseTransportProtocol    = errors.New("invalid release transport request")
)

type releaseTransportHost struct {
	goos      string
	serverURI string
	publicIP  string
	privateIP string
}

var releaseTransportHosts = map[string]releaseTransportHost{
	"mac-control-plane": {goos: "darwin"},
	"linux-host": {
		goos: "linux", serverURI: "urn:remote-session-runner:release-transport:host:linux-host",
		publicIP: "129.151.232.40", privateIP: "10.0.0.200",
	},
	"sandbox-host": {
		goos: "linux", serverURI: "urn:remote-session-runner:release-transport:host:sandbox-host",
		publicIP: "132.226.205.205", privateIP: "10.0.0.14",
	},
	"factory-sandbox": {
		goos: "linux", serverURI: "urn:remote-session-runner:release-transport:host:factory-sandbox",
		publicIP: "145.241.255.66", privateIP: "10.0.0.30",
	},
}

type ReleaseTransportPeerIdentity struct {
	URISAN                 string `json:"uri_san"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	SPKIFingerprint        string `json:"spki_fingerprint"`
}

// ReleaseTransportLocalPeerIdentity is installed with the Mac authority-agent
// policy. Its values must come from the reviewed host-management identity
// contract; it is not request-supplied identity data.
type ReleaseTransportLocalPeerIdentity struct {
	UID             uint32
	LaunchdLabel    string
	CodeRequirement string
}

// ReleaseTransportActivePeerSet is resolved from the current verified
// host-management manifest. It is never decoded from a request or Runner
// configuration. At most the bounded old/new rotation overlap is accepted.
type ReleaseTransportActivePeerSet struct {
	HostID         string                         `json:"host_id"`
	AuthorityPeers []ReleaseTransportPeerIdentity `json:"authority_peers"`
	TransportPeers []ReleaseTransportPeerIdentity `json:"transport_peers"`
}

type ReleaseTransportPeerSetSource interface {
	CurrentReleaseTransportPeerSet(context.Context, string) (ReleaseTransportActivePeerSet, error)
}

type ReleaseOutboxEntry struct {
	RecordID   string `json:"record_id"`
	RecordType string `json:"record_type"`
}

type ReleaseOutboxIndex struct {
	HostID  string               `json:"host_id"`
	Records []ReleaseOutboxEntry `json:"records"`
}

type ReleaseRecordCacheDisposition string

const (
	ReleaseRecordCached         ReleaseRecordCacheDisposition = "cached"
	ReleaseRecordAlreadyPresent ReleaseRecordCacheDisposition = "already_present"
)

type ReleaseRecordCacheResult struct {
	RecordID    string                        `json:"record_id"`
	Disposition ReleaseRecordCacheDisposition `json:"disposition"`
}

// ReleaseTransportOperations is implemented by the fixed security-anchor
// adapter. VerifyAndCacheRecord must validate the complete signed record and
// atomically retain its exact bytes: identical retries succeed, while a
// different body for an existing record ID is an integrity failure.
type ReleaseTransportOperations interface {
	ListOutbox(context.Context, string) ([]ReleaseOutboxEntry, error)
	ReadOutboxEnvelope(context.Context, string, string) ([]byte, error)
	VerifyAndCacheRecord(context.Context, string, AddressedRecordRequest) (ReleaseRecordCacheResult, error)
	BeginTimeProbe(context.Context, string) (TimeProbeChallenge, error)
	AcceptTimeProbe(context.Context, string, TimeProbeAcceptRequest) error
	AttestCapacity(context.Context, string) (string, error)
	AttestPKIRequest(context.Context, string, PKIRequestAttestationRequest) (string, error)
}

type ReleaseRecordTransportAPI struct {
	hostID     string
	operations ReleaseTransportOperations
	peers      ReleaseTransportPeerSetSource
	localAgent authorityAgentIdentity
	local      bool
}

type releaseLocalPeerContextKey struct{}

type releaseLocalPeerContextValue struct {
	evidence authorityAgentEvidence
	valid    bool
}

// NewReleaseRecordTransportAPI is the production constructor. The host ID is
// fixed by the lane matrix and must match the executing platform. The Mac
// instance has only its authenticated local-socket entry point; Ubuntu
// instances require active-peer state for mTLS requests.
func NewReleaseRecordTransportAPI(hostID string, operations ReleaseTransportOperations,
	peers ReleaseTransportPeerSetSource, localAgent ReleaseTransportLocalPeerIdentity) (*ReleaseRecordTransportAPI, error) {
	host, ok := releaseTransportHosts[hostID]
	if !ok || runtime.GOOS != host.goos || operations == nil {
		return nil, fmt.Errorf("%w: fixed host/platform operations are required", ErrReleaseTransportUnavailable)
	}
	return newReleaseRecordTransportAPI(hostID, operations, peers, authorityAgentIdentity{
		UID: localAgent.UID, LaunchdLabel: localAgent.LaunchdLabel, CodeRequirement: localAgent.CodeRequirement,
	})
}

// newReleaseRecordTransportAPIFixture bypasses only the runtime platform check
// so hermetic tests can exercise every fixed host profile on any build host.
func newReleaseRecordTransportAPIFixture(hostID string, operations ReleaseTransportOperations,
	peers ReleaseTransportPeerSetSource, localAgent authorityAgentIdentity) (*ReleaseRecordTransportAPI, error) {
	if _, ok := releaseTransportHosts[hostID]; !ok {
		return nil, ErrReleaseTransportUnavailable
	}
	return newReleaseRecordTransportAPI(hostID, operations, peers, localAgent)
}

func newReleaseRecordTransportAPI(hostID string, operations ReleaseTransportOperations,
	peers ReleaseTransportPeerSetSource, localAgent authorityAgentIdentity) (*ReleaseRecordTransportAPI, error) {
	host := releaseTransportHosts[hostID]
	if operations == nil {
		return nil, ErrReleaseTransportUnavailable
	}
	api := &ReleaseRecordTransportAPI{hostID: hostID, operations: operations, peers: peers}
	if host.goos == "darwin" {
		if peers != nil || !validReleaseAuthorityAgentIdentity(localAgent) {
			return nil, fmt.Errorf("%w: Mac transport requires its fixed authority-agent identity", ErrReleaseTransportUnavailable)
		}
		api.localAgent, api.local = localAgent, true
		return api, nil
	}
	if peers == nil || localAgent != (authorityAgentIdentity{}) {
		return nil, fmt.Errorf("%w: Ubuntu transport requires active-peer source and no local peer override", ErrReleaseTransportUnavailable)
	}
	return api, nil
}

func validReleaseAuthorityAgentIdentity(identity authorityAgentIdentity) bool {
	return identity.UID == 0 && identity.LaunchdLabel == "runner-release-authority-agent" &&
		validMacPeerExpectation(peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
			launchdLabel: identity.LaunchdLabel, codeRequirement: identity.CodeRequirement})
}

// TLSConfig creates the fixed Ubuntu TLS 1.3 server policy. The server leaf
// must match the host's literal public IP, URI SAN, and active fingerprint;
// clientCAs is the installed release-transport trust domain.
func (a *ReleaseRecordTransportAPI) TLSConfig(serverCertificate tls.Certificate, clientCAs *x509.CertPool) (*tls.Config, error) {
	if a == nil || a.local || a.operations == nil || a.peers == nil || clientCAs == nil {
		return nil, ErrReleaseTransportUnavailable
	}
	host, ok := releaseTransportHosts[a.hostID]
	if !ok || host.goos != "linux" {
		return nil, ErrReleaseTransportUnavailable
	}
	leaf, err := releaseCertificateLeaf(&serverCertificate)
	if err != nil {
		return nil, err
	}
	set, err := a.peers.CurrentReleaseTransportPeerSet(context.Background(), a.hostID)
	if err != nil || validateReleaseTransportPeerSet(set, a.hostID) != nil ||
		!releaseTransportCertificateMatches(leaf, host.serverURI, host.publicIP, set.TransportPeers, x509.ExtKeyUsageServerAuth) {
		return nil, fmt.Errorf("%w: server certificate is outside the active host identity", ErrReleaseTransportDenied)
	}
	serverCertificate.Leaf = leaf
	base := &tls.Config{
		MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: clientCAs, Certificates: []tls.Certificate{serverCertificate},
		NextProtos: []string{"http/1.1"},
	}
	base.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		current, err := a.peers.CurrentReleaseTransportPeerSet(context.Background(), a.hostID)
		if err != nil || validateReleaseTransportPeerSet(current, a.hostID) != nil ||
			!releaseTransportCertificateMatches(leaf, host.serverURI, host.publicIP, current.TransportPeers, x509.ExtKeyUsageServerAuth) {
			return nil, fmt.Errorf("%w: current server certificate is no longer active", ErrReleaseTransportDenied)
		}
		selected := base.Clone()
		selected.GetConfigForClient = nil
		return selected, nil
	}
	return base, nil
}

// Serve accepts only the fixed private Ubuntu bind or fixed Mac Unix socket.
// It does not create, unlink, or change the listener or its service manager.
func (a *ReleaseRecordTransportAPI) Serve(ctx context.Context, listener net.Listener,
	serverCertificate tls.Certificate, clientCAs *x509.CertPool) error {
	if a == nil || ctx == nil || listener == nil {
		return ErrReleaseTransportUnavailable
	}
	host := releaseTransportHosts[a.hostID]
	if host.goos == "darwin" {
		unixListener, ok := listener.(*net.UnixListener)
		address, addrOK := listener.Addr().(*net.UnixAddr)
		if !a.local || !ok || !addrOK || address.Name != releaseTransportSocketPath {
			return fmt.Errorf("%w: Mac listener is not the fixed local socket", ErrReleaseTransportDenied)
		}
		unixListener.SetUnlinkOnClose(false)
		return a.serve(ctx, listener, func(connCtx context.Context, conn net.Conn) context.Context {
			unixConn, ok := conn.(*net.UnixConn)
			if !ok {
				return context.WithValue(connCtx, releaseLocalPeerContextKey{}, releaseLocalPeerContextValue{})
			}
			evidence, err := inspectAuthorityAgentPeer(unixConn, a.localAgent)
			valid := err == nil && authorityAgentAllowed(evidence, a.localAgent, "darwin")
			value := releaseLocalPeerContextValue{evidence: evidence, valid: valid}
			return context.WithValue(connCtx, releaseLocalPeerContextKey{}, value)
		})
	}
	if a.local {
		return ErrReleaseTransportDenied
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	expectedIP, ipErr := netip.ParseAddr(host.privateIP)
	listenerIP, listenerIPOK := netip.AddrFromSlice(addressIP(address))
	if !ok || ipErr != nil || !listenerIPOK || listenerIP.Unmap() != expectedIP || address.Port != 9443 {
		return fmt.Errorf("%w: Ubuntu listener is not on its fixed private :9443 bind", ErrReleaseTransportDenied)
	}
	tlsConfig, err := a.TLSConfig(serverCertificate, clientCAs)
	if err != nil {
		return err
	}
	return a.serve(ctx, tls.NewListener(listener, tlsConfig), nil)
}

func addressIP(address *net.TCPAddr) []byte {
	if address == nil || address.IP == nil {
		return nil
	}
	return address.IP
}

func (a *ReleaseRecordTransportAPI) serve(ctx context.Context, listener net.Listener,
	connContext func(context.Context, net.Conn) context.Context) error {
	server := &http.Server{Handler: a, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10, ConnContext: connContext}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	err := server.Serve(listener)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *ReleaseRecordTransportAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a == nil || r == nil || a.operations == nil || a.hostID == "" {
		writeReleaseTransportError(w, http.StatusServiceUnavailable, "operation unavailable")
		return
	}
	if !a.authenticated(r) {
		writeReleaseTransportError(w, http.StatusForbidden, "request denied")
		return
	}
	if !a.local {
		host := releaseTransportHosts[a.hostID]
		if r.Host != host.publicIP+":9443" {
			writeReleaseTransportError(w, http.StatusMisdirectedRequest, "request denied")
			return
		}
	}
	if r.URL == nil || r.URL.RawQuery != "" || r.URL.Fragment != "" || r.URL.Path != r.URL.EscapedPath() ||
		r.URL.Opaque != "" || strings.Contains(r.URL.Path, "//") || strings.Contains(r.URL.Path, "..") {
		writeReleaseTransportError(w, http.StatusBadRequest, "invalid request")
		return
	}
	a.dispatchHTTP(w, r)
}

func (a *ReleaseRecordTransportAPI) authenticated(r *http.Request) bool {
	if a.local {
		value, ok := r.Context().Value(releaseLocalPeerContextKey{}).(releaseLocalPeerContextValue)
		return ok && value.valid && authorityAgentAllowed(value.evidence, a.localAgent, "darwin")
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 || a.peers == nil {
		return false
	}
	set, err := a.peers.CurrentReleaseTransportPeerSet(r.Context(), a.hostID)
	if err != nil || validateReleaseTransportPeerSet(set, a.hostID) != nil {
		return false
	}
	return releaseTransportCertificateMatches(r.TLS.PeerCertificates[0], releaseTransportAuthorityURI, "",
		set.AuthorityPeers, x509.ExtKeyUsageClientAuth)
}

func (a *ReleaseRecordTransportAPI) dispatchHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/v1/outbox-index" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "request body is not allowed")
			return
		}
		entries, err := a.operations.ListOutbox(r.Context(), a.hostID)
		if err != nil || !validOutboxIndex(a.hostID, entries) {
			writeReleaseTransportError(w, http.StatusServiceUnavailable, "operation unavailable")
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].RecordID < entries[j].RecordID })
		writeReleaseTransportJSON(w, http.StatusOK, ReleaseOutboxIndex{HostID: a.hostID, Records: entries})
	case strings.HasPrefix(path, "/v1/outbox/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/v1/outbox/")
		if !validDigestID(id) || strings.Contains(id, "/") || !requestBodyEmpty(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "invalid request")
			return
		}
		raw, err := a.operations.ReadOutboxEnvelope(r.Context(), a.hostID, id)
		if err != nil || !validOutboxEnvelope(a.hostID, id, raw) {
			writeReleaseTransportError(w, http.StatusNotFound, "record unavailable")
			return
		}
		writeReleaseTransportRawJSON(w, http.StatusOK, raw)
	case strings.HasPrefix(path, "/v1/records/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(path, "/v1/records/")
		if !validDigestID(id) || strings.Contains(id, "/") {
			writeReleaseTransportError(w, http.StatusBadRequest, "invalid record address")
			return
		}
		raw, err := readReleaseTransportBody(r)
		if err != nil || !jsonContentType(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "invalid record body")
			return
		}
		envelope, err := parseTransportEnvelope(id, raw)
		if err != nil || !allowedInboundRecordType(envelope.RecordType) || ValidatePayload(envelope.RecordType, envelope.Payload) != nil ||
			!recordAddressedToHost(envelope.RecordType, envelope.Payload, a.hostID) {
			writeReleaseTransportError(w, http.StatusBadRequest, "record rejected")
			return
		}
		result, err := a.operations.VerifyAndCacheRecord(r.Context(), a.hostID,
			AddressedRecordRequest{RecordID: id, Envelope: append(json.RawMessage(nil), raw...)})
		if err != nil || result.RecordID != id ||
			(result.Disposition != ReleaseRecordCached && result.Disposition != ReleaseRecordAlreadyPresent) {
			writeReleaseTransportError(w, http.StatusConflict, "record rejected")
			return
		}
		writeReleaseTransportJSON(w, http.StatusOK, result)
	case path == "/v1/time-probe/challenge" && r.Method == http.MethodPost:
		if !requestBodyEmpty(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "request body is not allowed")
			return
		}
		challenge, err := a.operations.BeginTimeProbe(r.Context(), a.hostID)
		if err != nil || !validOpaqueID(challenge.ID) {
			writeReleaseTransportError(w, http.StatusServiceUnavailable, "operation unavailable")
			return
		}
		writeReleaseTransportJSON(w, http.StatusCreated, challenge)
	case strings.HasPrefix(path, "/v1/time-probe/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(path, "/v1/time-probe/")
		if id == "challenge" || !validOpaqueID(id) || strings.Contains(id, "/") || !jsonContentType(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "invalid time-probe address")
			return
		}
		response, err := readReleaseTransportBody(r)
		if err != nil {
			writeReleaseTransportError(w, http.StatusBadRequest, "invalid time-probe response")
			return
		}
		if err = a.operations.AcceptTimeProbe(r.Context(), a.hostID,
			TimeProbeAcceptRequest{ChallengeID: id, Response: response}); err != nil {
			writeReleaseTransportError(w, http.StatusConflict, "time-probe rejected")
			return
		}
		writeReleaseTransportJSON(w, http.StatusOK, map[string]string{"challenge_id": id, "status": "accepted"})
	case path == "/v1/capacity-measurement" && r.Method == http.MethodPost:
		if !requestBodyEmpty(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "request body is not allowed")
			return
		}
		recordID, err := a.operations.AttestCapacity(r.Context(), a.hostID)
		if err != nil || !validDigestID(recordID) {
			writeReleaseTransportError(w, http.StatusServiceUnavailable, "operation unavailable")
			return
		}
		writeReleaseTransportJSON(w, http.StatusCreated, map[string]string{"record_id": recordID})
	case strings.HasPrefix(path, "/v1/pki-certificate-request/") && r.Method == http.MethodPost:
		id := strings.TrimPrefix(path, "/v1/pki-certificate-request/")
		if !validOpaqueID(id) || strings.Contains(id, "/") || !requestBodyEmpty(r) {
			writeReleaseTransportError(w, http.StatusBadRequest, "invalid PKI mandate reference")
			return
		}
		recordID, err := a.operations.AttestPKIRequest(r.Context(), a.hostID,
			PKIRequestAttestationRequest{MandateID: id})
		if err != nil || !validDigestID(recordID) {
			writeReleaseTransportError(w, http.StatusConflict, "PKI request rejected")
			return
		}
		writeReleaseTransportJSON(w, http.StatusCreated, map[string]string{"record_id": recordID})
	default:
		if knownReleaseTransportPath(path) {
			w.Header().Set("Allow", allowedReleaseTransportMethod(path))
			writeReleaseTransportError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeReleaseTransportError(w, http.StatusNotFound, "operation not found")
	}
}

func validateReleaseTransportPeerSet(set ReleaseTransportActivePeerSet, hostID string) error {
	host, ok := releaseTransportHosts[hostID]
	if !ok || set.HostID != hostID || len(set.AuthorityPeers) < 1 || len(set.AuthorityPeers) > 2 {
		return ErrReleaseTransportDenied
	}
	seen := make(map[string]struct{})
	for _, peer := range set.AuthorityPeers {
		if peer.URISAN != releaseTransportAuthorityURI || !validDigestID(peer.CertificateFingerprint) || !validDigestID(peer.SPKIFingerprint) {
			return ErrReleaseTransportDenied
		}
		key := peer.CertificateFingerprint + ":" + peer.SPKIFingerprint
		if _, duplicate := seen[key]; duplicate {
			return ErrReleaseTransportDenied
		}
		seen[key] = struct{}{}
	}
	if host.goos == "darwin" {
		if len(set.TransportPeers) != 0 {
			return ErrReleaseTransportDenied
		}
		return nil
	}
	if len(set.TransportPeers) < 1 || len(set.TransportPeers) > 2 {
		return ErrReleaseTransportDenied
	}
	seen = make(map[string]struct{})
	for _, peer := range set.TransportPeers {
		if peer.URISAN != host.serverURI || !validDigestID(peer.CertificateFingerprint) || !validDigestID(peer.SPKIFingerprint) {
			return ErrReleaseTransportDenied
		}
		key := peer.CertificateFingerprint + ":" + peer.SPKIFingerprint
		if _, duplicate := seen[key]; duplicate {
			return ErrReleaseTransportDenied
		}
		seen[key] = struct{}{}
	}
	return nil
}

func releaseTransportCertificateMatches(certificate *x509.Certificate, expectedURI, expectedIP string,
	peers []ReleaseTransportPeerIdentity, usage x509.ExtKeyUsage) bool {
	now := time.Now()
	if certificate == nil || certificate.IsCA || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) ||
		certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 || !certificateAllowsUsage(certificate, usage) ||
		!exactReleaseCertificateURI(certificate, expectedURI) || len(certificate.DNSNames) != 0 {
		return false
	}
	if expectedIP == "" {
		if len(certificate.IPAddresses) != 0 {
			return false
		}
	} else {
		ip, err := netip.ParseAddr(expectedIP)
		if err != nil || len(certificate.IPAddresses) != 1 || !certificate.IPAddresses[0].Equal(ip.AsSlice()) {
			return false
		}
	}
	certificateFP := releaseCertificateFingerprint(certificate)
	spkiFP := releaseSPKIFingerprint(certificate)
	for _, peer := range peers {
		if peer.URISAN == expectedURI && peer.CertificateFingerprint == certificateFP && peer.SPKIFingerprint == spkiFP {
			return true
		}
	}
	return false
}

func certificateAllowsUsage(certificate *x509.Certificate, wanted x509.ExtKeyUsage) bool {
	return certificate != nil && len(certificate.ExtKeyUsage) == 1 && certificate.ExtKeyUsage[0] == wanted
}

func exactReleaseCertificateURI(certificate *x509.Certificate, expected string) bool {
	return certificate != nil && len(certificate.URIs) == 1 && expected != "" && certificate.URIs[0].String() == expected
}

func releaseCertificateFingerprint(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	digest := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(digest[:])
}

func releaseSPKIFingerprint(certificate *x509.Certificate) string {
	if certificate == nil {
		return ""
	}
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(digest[:])
}

func releaseCertificateLeaf(certificate *tls.Certificate) (*x509.Certificate, error) {
	if certificate == nil || len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
		return nil, ErrReleaseTransportUnavailable
	}
	if certificate.Leaf != nil {
		return certificate.Leaf, nil
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid server certificate", ErrReleaseTransportUnavailable)
	}
	return leaf, nil
}

func validOutboxIndex(hostID string, entries []ReleaseOutboxEntry) bool {
	if !isCanonicalHostID(hostID) || len(entries) > 4096 {
		return false
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !validDigestID(entry.RecordID) || !allowedOutboxRecordType(entry.RecordType) {
			return false
		}
		if _, ok := seen[entry.RecordID]; ok {
			return false
		}
		seen[entry.RecordID] = struct{}{}
	}
	return true
}

func validOutboxEnvelope(hostID, recordID string, raw []byte) bool {
	envelope, err := parseTransportEnvelope(recordID, raw)
	if err != nil || len(raw) > releaseTransportMaxRequestBytes || !allowedOutboxRecordType(envelope.RecordType) ||
		ValidatePayload(envelope.RecordType, envelope.Payload) != nil {
		return false
	}
	var payload struct {
		HostID string `json:"host_id"`
	}
	return json.Unmarshal(envelope.Payload, &payload) == nil && payload.HostID == hostID
}

func parseTransportEnvelope(recordID string, raw []byte) (Envelope, error) {
	var envelope Envelope
	canonical, err := CanonicalizeJSON(raw)
	if err != nil || !byteSlicesEqual(canonical, raw) || !validDigestID(recordID) || !isJSONObject(canonical) ||
		!hasExactEnvelopeFields(canonical) || decodeStrict(canonical, &envelope) != nil ||
		envelope.ReleaseRecordVersion != ReleaseRecordVersion || envelope.RecordID != recordID ||
		envelope.SignatureAlgorithm != SignatureAlgorithmEd25519 || !validKeyID(envelope.SignerKeyID) || envelope.Signature == "" ||
		!isJSONObject(envelope.Payload) {
		return Envelope{}, ErrReleaseTransportProtocol
	}
	return envelope, nil
}

func allowedOutboxRecordType(recordType string) bool {
	switch recordType {
	case "host_build_attestation", "promotion_attestation", "ingress_admission_attestation",
		"capacity_measurement_attestation", "pki_certificate_request", "host_management_attestation":
		return true
	default:
		return false
	}
}

func allowedInboundRecordType(recordType string) bool {
	switch recordType {
	case "candidate_manifest", "promotion_authorization", "lane_acceptance", "ingress_admission_directive",
		"capacity_policy", "host_management_manifest", "host_management_authorization", "host_attestation_key_set",
		"release_revocation_ledger", "authority_key_set":
		return true
	default:
		return false
	}
}

func recordAddressedToHost(recordType string, payload json.RawMessage, hostID string) bool {
	switch recordType {
	case "release_revocation_ledger", "authority_key_set", "lane_acceptance":
		return true
	case "candidate_manifest":
		var value candidateManifestPayload
		if json.Unmarshal(payload, &value) != nil {
			return false
		}
		for _, artifact := range value.Artifacts {
			if artifact.HostID == hostID {
				return true
			}
		}
	case "host_management_manifest":
		var value hostManagementManifestPayload
		if json.Unmarshal(payload, &value) != nil {
			return false
		}
		for _, artifact := range value.Artifacts {
			if artifact.HostID == hostID {
				return true
			}
		}
	default:
		var value struct {
			HostID string `json:"host_id"`
		}
		return json.Unmarshal(payload, &value) == nil && value.HostID == hostID
	}
	return false
}

func knownReleaseTransportPath(path string) bool {
	return path == "/v1/outbox-index" || path == "/v1/time-probe/challenge" || path == "/v1/capacity-measurement" ||
		strings.HasPrefix(path, "/v1/outbox/") || strings.HasPrefix(path, "/v1/records/") ||
		strings.HasPrefix(path, "/v1/time-probe/") || strings.HasPrefix(path, "/v1/pki-certificate-request/")
}

func allowedReleaseTransportMethod(path string) string {
	switch {
	case path == "/v1/outbox-index", strings.HasPrefix(path, "/v1/outbox/"):
		return http.MethodGet
	case path == "/v1/time-probe/challenge", path == "/v1/capacity-measurement",
		strings.HasPrefix(path, "/v1/pki-certificate-request/"):
		return http.MethodPost
	case strings.HasPrefix(path, "/v1/records/"), strings.HasPrefix(path, "/v1/time-probe/"):
		return http.MethodPut
	default:
		return ""
	}
}

func requestBodyEmpty(r *http.Request) bool {
	if r == nil || r.Body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1))
	return err == nil && len(data) == 0
}

func jsonContentType(r *http.Request) bool {
	if r == nil {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func readReleaseTransportBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, ErrReleaseTransportProtocol
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, releaseTransportMaxRequestBytes+1))
	if err != nil || len(data) == 0 || len(data) > releaseTransportMaxRequestBytes || !isJSONObject(data) {
		return nil, ErrReleaseTransportProtocol
	}
	return data, nil
}

func byteSlicesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func writeReleaseTransportJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeReleaseTransportRawJSON(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeReleaseTransportError(w http.ResponseWriter, status int, message string) {
	writeReleaseTransportJSON(w, status, map[string]string{"error": message})
}

var _ http.Handler = (*ReleaseRecordTransportAPI)(nil)
