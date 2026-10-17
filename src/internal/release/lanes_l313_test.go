package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLanesL313APIUsesOnlyFixedHostAndPeerModes(t *testing.T) {
	backend := &l313Backend{outbox: make(map[string][]byte)}
	peers := &l313PeerSource{}
	if runtime.GOOS == "darwin" {
		api, err := NewReleaseRecordTransportAPI("mac-control-plane", backend, nil, ReleaseTransportLocalPeerIdentity{
			UID: 0, LaunchdLabel: "runner-release-authority-agent",
			CodeRequirement: l313AgentIdentity().CodeRequirement,
		})
		if err != nil || api == nil || !api.local {
			t.Fatalf("public Mac constructor failed on Darwin: api=%v err=%v", api, err)
		}
		if _, err := NewReleaseRecordTransportAPI("linux-host", backend, peers, ReleaseTransportLocalPeerIdentity{}); !errors.Is(err, ErrReleaseTransportUnavailable) {
			t.Fatalf("public constructor accepted a foreign platform host: %v", err)
		}
	} else if runtime.GOOS == "linux" {
		api, err := NewReleaseRecordTransportAPI("linux-host", backend, peers, ReleaseTransportLocalPeerIdentity{})
		if err != nil || api == nil || api.local {
			t.Fatalf("public Ubuntu constructor failed on Linux: api=%v err=%v", api, err)
		}
		if _, err := inspectAuthorityAgentPeer(nil, l313AgentIdentity()); !errors.Is(err, ErrSecurityEndpointUnavailable) {
			t.Fatalf("Linux must fail closed for Mac authority-agent peer inspection: %v", err)
		}
		if _, err := NewReleaseRecordTransportAPI("mac-control-plane", backend, nil, ReleaseTransportLocalPeerIdentity{
			UID: 0, LaunchdLabel: "runner-release-authority-agent",
			CodeRequirement: l313AgentIdentity().CodeRequirement,
		}); !errors.Is(err, ErrReleaseTransportUnavailable) {
			t.Fatalf("public constructor accepted a foreign platform host: %v", err)
		}
	} else {
		t.Fatalf("unexpected source-gate platform %q", runtime.GOOS)
	}
	if _, err := newReleaseRecordTransportAPIFixture("../linux-host", backend, peers, authorityAgentIdentity{}); !errors.Is(err, ErrReleaseTransportUnavailable) {
		t.Fatalf("noncanonical host identity was accepted: %v", err)
	}
	if _, err := newReleaseRecordTransportAPIFixture("linux-host", backend, nil, authorityAgentIdentity{}); !errors.Is(err, ErrReleaseTransportUnavailable) {
		t.Fatal("Ubuntu API accepted a missing active-peer source")
	}
	if _, err := newReleaseRecordTransportAPIFixture("linux-host", backend, peers, l313AgentIdentity()); !errors.Is(err, ErrReleaseTransportUnavailable) {
		t.Fatal("Ubuntu API accepted a Mac local-peer override")
	}
	if _, err := newReleaseRecordTransportAPIFixture("mac-control-plane", backend, peers, l313AgentIdentity()); !errors.Is(err, ErrReleaseTransportUnavailable) {
		t.Fatal("Mac API accepted an mTLS peer source")
	}
	mac, err := newReleaseRecordTransportAPIFixture("mac-control-plane", backend, nil, l313AgentIdentity())
	if err != nil {
		t.Fatalf("fixed Mac local API rejected: %v", err)
	}
	if _, err := mac.TLSConfig(tls.Certificate{}, x509.NewCertPool()); !errors.Is(err, ErrReleaseTransportUnavailable) {
		t.Fatalf("Mac local API exposed TLS listener: %v", err)
	}
	wrongSocket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := mac.Serve(context.Background(), wrongSocket, tls.Certificate{}, nil); !errors.Is(err, ErrReleaseTransportDenied) {
		t.Fatalf("Mac API accepted a non-Unix listener: %v", err)
	}
	_ = wrongSocket.Close()
}

func TestLanesL313PeerSetRequiresExactURIIPAndActiveFingerprints(t *testing.T) {
	for _, tc := range []struct {
		hostID, publicIP, privateIP, serverURI string
	}{
		{"linux-host", "129.151.232.40", "10.0.0.200", "urn:remote-session-runner:release-transport:host:linux-host"},
		{"sandbox-host", "132.226.205.205", "10.0.0.14", "urn:remote-session-runner:release-transport:host:sandbox-host"},
		{"factory-sandbox", "145.241.255.66", "10.0.0.30", "urn:remote-session-runner:release-transport:host:factory-sandbox"},
	} {
		t.Run(tc.hostID, func(t *testing.T) {
			host := releaseTransportHosts[tc.hostID]
			if host.publicIP != tc.publicIP || host.privateIP != tc.privateIP || host.serverURI != tc.serverURI {
				t.Fatalf("fixed endpoint matrix = %+v; want public=%s private=%s URI=%s", host, tc.publicIP, tc.privateIP, tc.serverURI)
			}
			fixture := l313TLSFixture(t, tc.hostID, releaseTransportAuthorityURI)
			serverPeer := l313IdentityForCertificate(fixture.server.Leaf, host.serverURI)
			clientPeer := l313IdentityForCertificate(fixture.client.Leaf, releaseTransportAuthorityURI)
			set := ReleaseTransportActivePeerSet{HostID: tc.hostID, AuthorityPeers: []ReleaseTransportPeerIdentity{clientPeer}, TransportPeers: []ReleaseTransportPeerIdentity{serverPeer}}
			if err := validateReleaseTransportPeerSet(set, tc.hostID); err != nil {
				t.Fatalf("valid pinned peer set rejected: %v", err)
			}
			if !releaseTransportCertificateMatches(fixture.server.Leaf, host.serverURI, host.publicIP, set.TransportPeers, x509.ExtKeyUsageServerAuth) {
				t.Fatal("exact server URI/IP/certificate/SPKI tuple was rejected")
			}
			if !releaseTransportCertificateMatches(fixture.client.Leaf, releaseTransportAuthorityURI, "", set.AuthorityPeers, x509.ExtKeyUsageClientAuth) {
				t.Fatal("exact authority URI/certificate/SPKI tuple was rejected")
			}
			if releaseTransportCertificateMatches(fixture.server.Leaf, host.serverURI, "203.0.113.9", set.TransportPeers, x509.ExtKeyUsageServerAuth) {
				t.Fatal("wrong literal server IP was accepted")
			}
			if releaseTransportCertificateMatches(fixture.client.Leaf, "urn:remote-session-runner:release-authority:other", "", set.AuthorityPeers, x509.ExtKeyUsageClientAuth) {
				t.Fatal("foreign authority URI was accepted")
			}
			noRole := *fixture.client.Leaf
			noRole.ExtKeyUsage = nil
			if releaseTransportCertificateMatches(&noRole, releaseTransportAuthorityURI, "", set.AuthorityPeers, x509.ExtKeyUsageClientAuth) {
				t.Fatal("authority certificate without the exact client-auth role was accepted")
			}
			dualRole := *fixture.client.Leaf
			dualRole.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
			if releaseTransportCertificateMatches(&dualRole, releaseTransportAuthorityURI, "", set.AuthorityPeers, x509.ExtKeyUsageClientAuth) {
				t.Fatal("certificate with an expanded TLS role was accepted")
			}
			set.TransportPeers[0].SPKIFingerprint = strings.Repeat("f", 64)
			if err := validateReleaseTransportPeerSet(set, tc.hostID); err != nil {
				t.Fatalf("well-formed but changed pin should be a valid state shape: %v", err)
			}
			api, err := newReleaseRecordTransportAPIFixture(tc.hostID, &l313Backend{}, &l313PeerSource{set: set}, authorityAgentIdentity{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := api.TLSConfig(fixture.server, fixture.roots); !errors.Is(err, ErrReleaseTransportDenied) {
				t.Fatalf("server leaf absent from active peer set was accepted: %v", err)
			}
			wrongBind, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer wrongBind.Close()
			if err := api.Serve(context.Background(), wrongBind, fixture.server, fixture.roots); !errors.Is(err, ErrReleaseTransportDenied) {
				t.Fatalf("Ubuntu API accepted a nonfixed private bind: %v", err)
			}
		})
	}
}

func TestLanesL313OutboxIndexAndReadAreHostBoundAndClosed(t *testing.T) {
	fixture := l313TLSFixture(t, "linux-host", releaseTransportAuthorityURI)
	backend := &l313Backend{outbox: make(map[string][]byte)}
	api, requestPeer := l313API(t, "linux-host", backend, fixture)
	attestation := l303Attestation(l303Authorization("linux-host", "dev", nil), "accepted")
	raw, err := SignEnvelope("promotion_attestation", l302JSON(t, attestation), SigningIdentity{
		KeyID: "host-linux-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PrivateKey: testSigningKey(32),
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	backend.entries = []ReleaseOutboxEntry{{RecordID: envelope.RecordID, RecordType: envelope.RecordType}}
	backend.outbox[envelope.RecordID] = raw
	index := l313Request(t, api, requestPeer, http.MethodGet, "/v1/outbox-index", "", "")
	if index.Code != http.StatusOK {
		t.Fatalf("outbox index status=%d body=%s", index.Code, index.Body.String())
	}
	var decoded ReleaseOutboxIndex
	if err := json.NewDecoder(index.Body).Decode(&decoded); err != nil || decoded.HostID != "linux-host" || !reflect.DeepEqual(decoded.Records, backend.entries) {
		t.Fatalf("outbox index=%+v err=%v", decoded, err)
	}
	read := l313Request(t, api, requestPeer, http.MethodGet, "/v1/outbox/"+envelope.RecordID, "", "")
	if read.Code != http.StatusOK || read.Body.String() != string(raw) {
		t.Fatalf("outbox record status=%d body=%s", read.Code, read.Body.String())
	}
	foreign := l303Attestation(l303Authorization("factory-sandbox", "dev", nil), "accepted")
	foreignRaw, err := SignEnvelope("promotion_attestation", l302JSON(t, foreign), SigningIdentity{
		KeyID: "host-factory-test", Role: SignerRoleHostAttestation, HostID: "factory-sandbox", PrivateKey: testSigningKey(32),
	})
	if err != nil {
		t.Fatal(err)
	}
	var foreignEnvelope Envelope
	if err := json.Unmarshal(foreignRaw, &foreignEnvelope); err != nil {
		t.Fatal(err)
	}
	backend.outbox[foreignEnvelope.RecordID] = foreignRaw
	badRead := l313Request(t, api, requestPeer, http.MethodGet, "/v1/outbox/"+foreignEnvelope.RecordID, "", "")
	if badRead.Code != http.StatusNotFound {
		t.Fatalf("foreign host outbox record status=%d body=%s", badRead.Code, badRead.Body.String())
	}
	backend.entries = append(backend.entries, ReleaseOutboxEntry{RecordID: envelope.RecordID, RecordType: envelope.RecordType})
	badIndex := l313Request(t, api, requestPeer, http.MethodGet, "/v1/outbox-index", "", "")
	if badIndex.Code != http.StatusServiceUnavailable {
		t.Fatalf("duplicate outbox index entry status=%d", badIndex.Code)
	}
}

func TestLanesL313AddressedRecordCacheIsIdempotentAndHostBound(t *testing.T) {
	fixture := l313TLSFixture(t, "linux-host", releaseTransportAuthorityURI)
	backend := &l313Backend{}
	api, requestPeer := l313API(t, "linux-host", backend, fixture)
	payload := map[string]any{
		"candidate_manifest_id": strings.Repeat("a", 64), "release_lane": "dev",
		"lane_transition_id":                 "018f3e56-789a-7abc-8def-0123456789ab",
		"accepted_promotion_attestation_ids": []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)},
		"acceptance_evidence_digest":         "sha256:" + strings.Repeat("b", 64), "accepted_at": "2026-10-17T12:00:00Z",
	}
	raw, err := SignEnvelope("lane_acceptance", l302JSON(t, payload), SigningIdentity{
		KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PrivateKey: testSigningKey(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	first := l313Request(t, api, requestPeer, http.MethodPut, "/v1/records/"+envelope.RecordID, string(raw), "application/json")
	if first.Code != http.StatusOK || backend.cacheCalls != 1 {
		t.Fatalf("first cache status=%d calls=%d body=%s", first.Code, backend.cacheCalls, first.Body.String())
	}
	second := l313Request(t, api, requestPeer, http.MethodPut, "/v1/records/"+envelope.RecordID, string(raw), "application/json")
	if second.Code != http.StatusOK || backend.cacheCalls != 2 {
		t.Fatalf("identical retry status=%d calls=%d body=%s", second.Code, backend.cacheCalls, second.Body.String())
	}
	var result ReleaseRecordCacheResult
	if err := json.NewDecoder(second.Body).Decode(&result); err != nil || result.Disposition != ReleaseRecordAlreadyPresent {
		t.Fatalf("identical retry result=%+v err=%v", result, err)
	}
	if !byteSlicesEqual(backend.cached[envelope.RecordID], raw) {
		t.Fatal("identical retry changed the retained record bytes")
	}

	wrongAddress := strings.Repeat("0", 64)
	if wrongAddress == envelope.RecordID {
		wrongAddress = strings.Repeat("1", 64)
	}
	misaddressed := l313Request(t, api, requestPeer, http.MethodPut, "/v1/records/"+wrongAddress, string(raw), "application/json")
	if misaddressed.Code != http.StatusBadRequest || backend.cacheCalls != 2 {
		t.Fatalf("record body whose ID differs from URL reached cache: status=%d calls=%d", misaddressed.Code, backend.cacheCalls)
	}
	if !recordAddressedToHost("candidate_manifest", json.RawMessage(`{"artifacts":[{"host_id":"linux-host"}]}`), "linux-host") {
		t.Fatal("candidate record containing this host was not addressed locally")
	}
	if recordAddressedToHost("candidate_manifest", json.RawMessage(`{"artifacts":[{"host_id":"factory-sandbox"}]}`), "linux-host") {
		t.Fatal("candidate record omitting this host was accepted")
	}
	changed := strings.Replace(string(raw), "018f3e56-789a-7abc-8def-0123456789ab", "018f3e56-789a-7abc-8def-0123456789ac", 1)
	collision := l313Request(t, api, requestPeer, http.MethodPut, "/v1/records/"+envelope.RecordID, changed, "application/json")
	if collision.Code != http.StatusConflict {
		t.Fatalf("same ID with changed bytes was not rejected: status=%d body=%s", collision.Code, collision.Body.String())
	}
	if backend.cacheCalls != 3 {
		t.Fatalf("unexpected cache request count after collision: %d", backend.cacheCalls)
	}
	if !byteSlicesEqual(backend.cached[envelope.RecordID], raw) {
		t.Fatal("same-ID collision changed the original retained record")
	}
}

func TestLanesL313APIRejectsOpenMethodsPathsBodiesAndUntrustedMacPeer(t *testing.T) {
	fixture := l313TLSFixture(t, "linux-host", releaseTransportAuthorityURI)
	backend := &l313Backend{}
	api, requestPeer := l313API(t, "linux-host", backend, fixture)
	for _, tc := range []struct {
		method, path, body, contentType string
		code                            int
	}{
		{http.MethodGet, "/v1/outbox-index?path=/etc/passwd", "", "", http.StatusBadRequest},
		{http.MethodGet, "/v1/files/etc/passwd", "", "", http.StatusNotFound},
		{http.MethodPost, "/v1/run", `{"script":"id"}`, "application/json", http.StatusNotFound},
		{http.MethodGet, "/v1/source/main.go", "", "", http.StatusNotFound},
		{http.MethodGet, "/v1/artifacts/build.tar", "", "", http.StatusNotFound},
		{http.MethodPost, "/v1/shell", `{"command":"id"}`, "application/json", http.StatusNotFound},
		{http.MethodPost, "/v1/sqlite", `{"query":"select 1"}`, "application/json", http.StatusNotFound},
		{http.MethodPost, "/v1/outbox-index", "", "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/outbox-index", "x", "", http.StatusBadRequest},
	} {
		response := l313Request(t, api, requestPeer, tc.method, tc.path, tc.body, tc.contentType)
		if response.Code != tc.code {
			t.Errorf("%s %s status=%d want %d body=%s", tc.method, tc.path, response.Code, tc.code, response.Body.String())
		}
	}
	wrongHost := httptest.NewRequest(http.MethodGet, "https://129.151.232.40:9443/v1/outbox-index", nil)
	wrongHost.Host = "132.226.205.205:9443"
	wrongHost.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{requestPeer}, VerifiedChains: [][]*x509.Certificate{{requestPeer}}}
	wrongHostResponse := httptest.NewRecorder()
	api.ServeHTTP(wrongHostResponse, wrongHost)
	if wrongHostResponse.Code != http.StatusMisdirectedRequest {
		t.Fatalf("cross-host authority request status=%d", wrongHostResponse.Code)
	}
	noPeer := httptest.NewRequest(http.MethodGet, "https://129.151.232.40:9443/v1/outbox-index", nil)
	noPeer.Host = "129.151.232.40:9443"
	noPeerResponse := httptest.NewRecorder()
	api.ServeHTTP(noPeerResponse, noPeer)
	if noPeerResponse.Code != http.StatusForbidden {
		t.Fatalf("request without verified mTLS identity status=%d", noPeerResponse.Code)
	}
	macBackend := &l313Backend{}
	mac, err := newReleaseRecordTransportAPIFixture("mac-control-plane", macBackend, nil, l313AgentIdentity())
	if err != nil {
		t.Fatal(err)
	}
	macRequest := httptest.NewRequest(http.MethodGet, "http://release-transport.local/v1/outbox-index", nil)
	macRequest = macRequest.WithContext(context.WithValue(macRequest.Context(), releaseLocalPeerContextKey{}, releaseLocalPeerContextValue{}))
	macResponse := httptest.NewRecorder()
	mac.ServeHTTP(macResponse, macRequest)
	if macResponse.Code != http.StatusForbidden || macBackend.indexCalls != 0 {
		t.Fatalf("unverified Mac peer reached API: status=%d calls=%d", macResponse.Code, macBackend.indexCalls)
	}
	validEvidence := releaseLocalPeerContextValue{valid: true, evidence: authorityAgentEvidence{
		UID: 0, PID: 123, AuditTokenValidated: true, LaunchdLabel: l313AgentIdentity().LaunchdLabel,
		CodeRequirement: l313AgentIdentity().CodeRequirement,
	}}
	macRequest = httptest.NewRequest(http.MethodGet, "http://release-transport.local/v1/outbox-index", nil)
	macRequest = macRequest.WithContext(context.WithValue(macRequest.Context(), releaseLocalPeerContextKey{}, validEvidence))
	macResponse = httptest.NewRecorder()
	mac.ServeHTTP(macResponse, macRequest)
	if macResponse.Code != http.StatusOK || macBackend.indexCalls != 1 {
		t.Fatalf("validated authority-agent local peer rejected: status=%d calls=%d", macResponse.Code, macBackend.indexCalls)
	}
}

func TestLanesL313FixedNoBodyOperationsAcceptOnlyOpaquePKIMandates(t *testing.T) {
	fixture := l313TLSFixture(t, "linux-host", releaseTransportAuthorityURI)
	backend := &l313Backend{capacityID: strings.Repeat("c", 64), pkiID: strings.Repeat("d", 64)}
	api, requestPeer := l313API(t, "linux-host", backend, fixture)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/time-probe/challenge", http.StatusCreated},
		{"/v1/capacity-measurement", http.StatusCreated},
		{"/v1/pki-certificate-request/mandate-1234567890", http.StatusCreated},
	} {
		response := l313Request(t, api, requestPeer, http.MethodPost, tc.path, "", "")
		if response.Code != tc.want {
			t.Errorf("POST %s status=%d want %d body=%s", tc.path, response.Code, tc.want, response.Body.String())
		}
	}
	if backend.beginCalls != 1 || backend.capacityCalls != 1 || backend.pkiCalls != 1 || backend.lastMandate != "mandate-1234567890" {
		t.Fatalf("fixed operations/caller scope mismatch: %+v", backend)
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/capacity-measurement", `{"host_id":"factory-sandbox","path":"/etc/passwd"}`},
		{"/v1/pki-certificate-request/mandate-1234567890", `{"csr":"caller bytes","path":"/tmp/key"}`},
		{"/v1/pki-certificate-request/../../etc/passwd", ""},
	} {
		response := l313Request(t, api, requestPeer, http.MethodPost, tc.path, tc.body, "application/json")
		if response.Code < 400 {
			t.Errorf("unsafe fixed operation accepted: path=%s status=%d", tc.path, response.Code)
		}
	}
	if backend.capacityCalls != 1 || backend.pkiCalls != 1 {
		t.Fatalf("invalid bodies reached helper operations: %+v", backend)
	}
}

func TestLanesL313NativeMTLSListenerBindsExactHostAndActiveClient(t *testing.T) {
	fixture := l313TLSFixture(t, "linux-host", releaseTransportAuthorityURI)
	backend := &l313Backend{}
	api, _ := l313API(t, "linux-host", backend, fixture)
	serverConfig, err := api.TLSConfig(fixture.server, fixture.roots)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(api)
	server.TLS = serverConfig
	server.StartTLS()
	defer server.Close()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	clientFor := func(certificate tls.Certificate) *http.Client {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: fixture.roots,
				ServerName: releaseTransportHosts["linux-host"].publicIP, Certificates: []tls.Certificate{certificate}},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, server.Listener.Addr().String())
			},
		}
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	baseURL := "https://129.151.232.40:9443/v1/outbox-index"
	validClient := clientFor(fixture.client)
	response, err := validClient.Get(baseURL)
	if err != nil {
		t.Fatalf("valid pinned mTLS request failed: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || backend.indexCalls != 1 {
		t.Fatalf("valid mTLS status=%d index calls=%d", response.StatusCode, backend.indexCalls)
	}
	peers := api.peers.(*l313PeerSource)
	serverSPKI := peers.set.TransportPeers[0].SPKIFingerprint
	peers.set.TransportPeers[0].SPKIFingerprint = strings.Repeat("f", 64)
	if response, err := clientFor(fixture.client).Get(baseURL); err == nil {
		_ = response.Body.Close()
		t.Fatal("TLS handshake accepted a server certificate removed from the active pin set")
	}
	peers.set.TransportPeers[0].SPKIFingerprint = serverSPKI
	authorityFingerprint := peers.set.AuthorityPeers[0].CertificateFingerprint
	peers.set.AuthorityPeers[0].CertificateFingerprint = strings.Repeat("f", 64)
	response, err = validClient.Get(baseURL)
	if err != nil {
		t.Fatalf("removed active authority pin should be rejected by HTTP policy: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || backend.indexCalls != 1 {
		t.Fatalf("removed active authority certificate was accepted: status=%d index calls=%d", response.StatusCode, backend.indexCalls)
	}
	peers.set.AuthorityPeers[0].CertificateFingerprint = authorityFingerprint
	wrongURI := l313IssueLeaf(t, fixture.caTemplate, fixture.caDER, fixture.caPrivate, "urn:remote-session-runner:release-authority:other", nil,
		x509.ExtKeyUsageClientAuth)
	response, err = clientFor(wrongURI).Get(baseURL)
	if err != nil {
		t.Fatalf("wrong URI should complete CA-authenticated TLS before application pin denial: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || backend.indexCalls != 1 {
		t.Fatalf("wrong authority URI status=%d index calls=%d", response.StatusCode, backend.indexCalls)
	}
}

func l313Request(t *testing.T, api *ReleaseRecordTransportAPI, peer *x509.Certificate,
	method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://129.151.232.40:9443"+path, strings.NewReader(body))
	request.Host = "129.151.232.40:9443"
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if peer != nil {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peer}, VerifiedChains: [][]*x509.Certificate{{peer}}}
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func l313API(t *testing.T, hostID string, backend *l313Backend, fixture l313CertFixture) (*ReleaseRecordTransportAPI, *x509.Certificate) {
	t.Helper()
	serverHost := releaseTransportHosts[hostID]
	set := ReleaseTransportActivePeerSet{HostID: hostID,
		AuthorityPeers: []ReleaseTransportPeerIdentity{l313IdentityForCertificate(fixture.client.Leaf, releaseTransportAuthorityURI)},
		TransportPeers: []ReleaseTransportPeerIdentity{l313IdentityForCertificate(fixture.server.Leaf, serverHost.serverURI)},
	}
	peers := &l313PeerSource{set: set}
	api, err := newReleaseRecordTransportAPIFixture(hostID, backend, peers, authorityAgentIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	return api, fixture.client.Leaf
}

type l313PeerSource struct {
	set ReleaseTransportActivePeerSet
	err error
}

func (s *l313PeerSource) CurrentReleaseTransportPeerSet(_ context.Context, hostID string) (ReleaseTransportActivePeerSet, error) {
	if s.err != nil {
		return ReleaseTransportActivePeerSet{}, s.err
	}
	set := s.set
	set.AuthorityPeers = append([]ReleaseTransportPeerIdentity(nil), set.AuthorityPeers...)
	set.TransportPeers = append([]ReleaseTransportPeerIdentity(nil), set.TransportPeers...)
	if hostID != set.HostID {
		return ReleaseTransportActivePeerSet{}, ErrReleaseTransportDenied
	}
	return set, nil
}

type l313Backend struct {
	entries       []ReleaseOutboxEntry
	outbox        map[string][]byte
	cached        map[string][]byte
	indexCalls    int
	readCalls     int
	cacheCalls    int
	beginCalls    int
	acceptCalls   int
	capacityCalls int
	pkiCalls      int
	lastMandate   string
	capacityID    string
	pkiID         string
}

func (b *l313Backend) ListOutbox(_ context.Context, hostID string) ([]ReleaseOutboxEntry, error) {
	if hostID == "" {
		return nil, ErrReleaseTransportDenied
	}
	b.indexCalls++
	return append([]ReleaseOutboxEntry(nil), b.entries...), nil
}

func (b *l313Backend) ReadOutboxEnvelope(_ context.Context, hostID, id string) ([]byte, error) {
	if hostID == "" || b.outbox == nil {
		return nil, ErrReleaseTransportDenied
	}
	b.readCalls++
	return append([]byte(nil), b.outbox[id]...), nil
}

func (b *l313Backend) VerifyAndCacheRecord(_ context.Context, hostID string, request AddressedRecordRequest) (ReleaseRecordCacheResult, error) {
	if hostID == "" || !validDigestID(request.RecordID) {
		return ReleaseRecordCacheResult{}, ErrReleaseTransportDenied
	}
	b.cacheCalls++
	if b.cached == nil {
		b.cached = make(map[string][]byte)
	}
	if existing, ok := b.cached[request.RecordID]; ok {
		if !byteSlicesEqual(existing, request.Envelope) {
			return ReleaseRecordCacheResult{}, ErrReleaseTransportDenied
		}
		return ReleaseRecordCacheResult{RecordID: request.RecordID, Disposition: ReleaseRecordAlreadyPresent}, nil
	}
	b.cached[request.RecordID] = append([]byte(nil), request.Envelope...)
	return ReleaseRecordCacheResult{RecordID: request.RecordID, Disposition: ReleaseRecordCached}, nil
}

func (b *l313Backend) BeginTimeProbe(_ context.Context, hostID string) (TimeProbeChallenge, error) {
	if hostID == "" {
		return TimeProbeChallenge{}, ErrReleaseTransportDenied
	}
	b.beginCalls++
	return TimeProbeChallenge{ID: "challenge-1234567890", Nonce: "fixture-only"}, nil
}

func (b *l313Backend) AcceptTimeProbe(_ context.Context, hostID string, request TimeProbeAcceptRequest) error {
	if hostID == "" || !validOpaqueID(request.ChallengeID) || len(request.Response) == 0 {
		return ErrReleaseTransportDenied
	}
	b.acceptCalls++
	return nil
}

func (b *l313Backend) AttestCapacity(_ context.Context, hostID string) (string, error) {
	if hostID == "" {
		return "", ErrReleaseTransportDenied
	}
	b.capacityCalls++
	return b.capacityID, nil
}

func (b *l313Backend) AttestPKIRequest(_ context.Context, hostID string, request PKIRequestAttestationRequest) (string, error) {
	if hostID == "" || !validOpaqueID(request.MandateID) {
		return "", ErrReleaseTransportDenied
	}
	b.pkiCalls++
	b.lastMandate = request.MandateID
	return b.pkiID, nil
}

func l313AgentIdentity() authorityAgentIdentity {
	return authorityAgentIdentity{UID: 0, LaunchdLabel: "runner-release-authority-agent",
		CodeRequirement: `identifier "org.example.runner-release-authority-agent" and anchor apple generic and certificate leaf[subject.OU] = "TEAMID1234"`}
}

type l313CertFixture struct {
	server     tls.Certificate
	client     tls.Certificate
	roots      *x509.CertPool
	caTemplate *x509.Certificate
	caDER      []byte
	caPrivate  ed25519.PrivateKey
}

func l313TLSFixture(t *testing.T, hostID, clientURI string) l313CertFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "L313 fixture CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, public, private)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caTemplate)
	host := releaseTransportHosts[hostID]
	server := l313IssueLeaf(t, caTemplate, caDER, private, host.serverURI, []net.IP{net.ParseIP(host.publicIP)}, x509.ExtKeyUsageServerAuth)
	client := l313IssueLeaf(t, caTemplate, caDER, private, clientURI, nil, x509.ExtKeyUsageClientAuth)
	return l313CertFixture{server: server, client: client, roots: roots, caTemplate: caTemplate, caDER: caDER, caPrivate: private}
}

func l313IssueLeaf(t *testing.T, ca *x509.Certificate, caDER []byte, caPrivate ed25519.PrivateKey,
	uriText string, ips []net.IP, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(uriText)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "L313 fixture leaf"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, URIs: []*url.URL{uri}, IPAddresses: ips}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, public, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: private, Leaf: leaf}
}

func l313IdentityForCertificate(certificate *x509.Certificate, uri string) ReleaseTransportPeerIdentity {
	return ReleaseTransportPeerIdentity{URISAN: uri, CertificateFingerprint: releaseCertificateFingerprint(certificate),
		SPKIFingerprint: releaseSPKIFingerprint(certificate)}
}
