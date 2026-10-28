package laneinstall

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/config"
)

var ErrInvalidLaneTLSMaterial = errors.New("invalid lane TLS material")

const releaseAuthorityURI = "urn:remote-session-runner:release-authority:mac-control-plane"

// DirectTLSPlan is the derived public and private identity for one direct
// endpoint. It contains no caller-selected address, path, or credential.
type DirectTLSPlan struct {
	Lane           config.ReleaseLane
	Profile        string
	Name           string
	PublicURL      string
	PublicIP       string
	Bind           string
	ClientURISAN   string
	ServerURISAN   string
	ControllerID   string
	ControllerType string
	routeProof     [32]byte
}

// ReleaseTransportTLSPlan describes one Ubuntu management listener. The Mac
// authority is the only TLS client; the Mac-local transport uses its fixed
// Unix socket and has no server certificate or public listener.
type ReleaseTransportTLSPlan struct {
	HostID       string
	PublicURL    string
	PublicIP     string
	Bind         string
	ClientURISAN string
	ServerURISAN string
}

// TLSMaterial contains an endpoint's certificates, private keys, trust roots,
// and optional exact direct-controller map. The validator never logs or
// returns material bytes.
type TLSMaterial struct {
	ServerCertificatePEM []byte
	ServerPrivateKeyPEM  []byte
	ServerCAPEM          []byte
	ClientCertificatePEM []byte
	ClientPrivateKeyPEM  []byte
	ClientCAPEM          []byte
	PrincipalMapYAML     []byte
}

// TLSFingerprints pins both leaves, their public keys, and the two trust
// bundles. Values are safe lowercase SHA-256 fingerprints, not key contents.
type TLSFingerprints struct {
	ServerCertificate string
	ServerSPKI        string
	ServerCA          string
	ClientCertificate string
	ClientSPKI        string
	ClientCA          string
}

// DirectTLSBundle is one validated direct endpoint's public-only assertion
// set and fixture/material input.
type DirectTLSBundle struct {
	Plan         DirectTLSPlan
	Fingerprints TLSFingerprints
	Material     TLSMaterial
}

// ReleaseTransportTLSBundle is one management endpoint's material set.
type ReleaseTransportTLSBundle struct {
	Plan         ReleaseTransportTLSPlan
	Fingerprints TLSFingerprints
	Material     TLSMaterial
}

// BuildDirectTLSPlans returns the complete fixed endpoint table, including
// the existing PRD mapping. It derives all values from the namespace registry;
// it does not install or contact a listener.
func BuildDirectTLSPlans() ([]DirectTLSPlan, error) {
	plans := make([]DirectTLSPlan, 0, 9)
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS, config.ReleaseLanePRD} {
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			plan, err := BuildDirectTLSPlan(lane, profile)
			if err != nil {
				return nil, ErrInvalidLaneTLSMaterial
			}
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

// BuildDirectTLSPlan returns the fixed direct endpoint and identity for one
// lane/profile pair. PRD is represented for compatibility checks; callers
// must still follow the separate PRD maintenance gates before changing it.
func BuildDirectTLSPlan(lane config.ReleaseLane, profile string) (DirectTLSPlan, error) {
	namespace, err := config.DeriveLaneNamespace(config.HostKindLinux, lane, profile)
	if err != nil {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	return buildDirectTLSPlanFromRoute(lane, profile, namespace.DirectEndpointURL, namespace.DirectHTTPSBind)
}

// BuildDirectTLSPlanFromProfiles carries the explicit V3 route selected by the
// paired Mac and Linux configs into the certificate plan. The pair validator
// binds the same URL, SSH host identity, controller pins, and lane SANs before
// any TLS material can be issued for a non-default address.
func BuildDirectTLSPlanFromProfiles(lane config.ReleaseLane, profile string, mac, linux config.ValidatedV3Profiles) (DirectTLSPlan, error) {
	if mac.Lane() != lane || linux.Lane() != lane ||
		config.ValidateV3MacLinuxProfilePair(mac, linux, profile) != nil {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	macProfile, ok := mac.MacProfile(profile)
	linuxProfile, linuxOK := linux.LinuxProfileBinding()
	if !ok || !linuxOK {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	return buildDirectTLSPlanFromRoute(lane, profile, macProfile.Direct.URL, linuxProfile.Direct.Bind)
}

func buildDirectTLSPlanFromRoute(lane config.ReleaseLane, profile, publicURL, bind string) (DirectTLSPlan, error) {
	namespace, err := config.DeriveSimpleLaneNamespace(config.HostKindLinux, lane, profile)
	if err != nil {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	endpoint, err := url.Parse(publicURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Opaque != "" ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawPath != "" || endpoint.String() != publicURL {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	publicIP := net.ParseIP(endpoint.Hostname())
	if publicIP == nil || publicIP.To4() == nil || publicIP.String() != endpoint.Hostname() ||
		endpoint.Path != "" || endpoint.Port() != fmt.Sprint(namespace.DirectPort) {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	bindHost, bindPort, err := net.SplitHostPort(bind)
	if err != nil || !config.SimpleLaneRouteAddressAllowedForLane(profile, lane, publicIP.String(), bindHost) || bindPort != fmt.Sprint(namespace.DirectPort) {
		return DirectTLSPlan{}, ErrInvalidLaneTLSMaterial
	}
	plan := DirectTLSPlan{
		Lane: lane, Profile: profile, Name: namespace.DirectEndpointName,
		PublicURL: publicURL, PublicIP: publicIP.String(), Bind: bind,
		ClientURISAN:   laneTLSURI(lane, "controller", profile),
		ServerURISAN:   laneTLSURI(lane, "server", profile),
		ControllerID:   "runner-controller-" + string(lane) + "-" + profile,
		ControllerType: "direct_mtls",
	}
	plan.routeProof = directTLSPlanProof(plan)
	return plan, nil
}

// BuildReleaseTransportTLSPlans returns the fixed :9443 management routes.
// There is deliberately no Mac public route in this table.
func BuildReleaseTransportTLSPlans() []ReleaseTransportTLSPlan {
	return []ReleaseTransportTLSPlan{
		{HostID: "linux-host", PublicURL: "https://129.151.232.40:9443", PublicIP: "129.151.232.40", Bind: "10.0.0.200:9443", ClientURISAN: releaseAuthorityURI, ServerURISAN: "urn:remote-session-runner:release-transport:host:linux-host"},
		{HostID: "sandbox-host", PublicURL: "https://132.226.205.205:9443", PublicIP: "132.226.205.205", Bind: "10.0.0.14:9443", ClientURISAN: releaseAuthorityURI, ServerURISAN: "urn:remote-session-runner:release-transport:host:sandbox-host"},
		{HostID: "factory-sandbox", PublicURL: "https://145.241.255.66:9443", PublicIP: "145.241.255.66", Bind: "10.0.0.30:9443", ClientURISAN: releaseAuthorityURI, ServerURISAN: "urn:remote-session-runner:release-transport:host:factory-sandbox"},
	}
}

// ValidateDirectTLSMaterial verifies both certificate chains, exact endpoint
// identities, keypairs, safe fingerprints, and the one-entry principal map
// for a fixed direct lane endpoint.
func ValidateDirectTLSMaterial(plan DirectTLSPlan, fingerprints TLSFingerprints, material TLSMaterial) error {
	if !validDirectTLSPlan(plan) || plan.Lane == config.ReleaseLanePRD || plan.ControllerType != "direct_mtls" {
		return ErrInvalidLaneTLSMaterial
	}
	if err := validateMutualTLSMaterial(plan.PublicIP, plan.ClientURISAN, plan.ServerURISAN, fingerprints, material); err != nil {
		return err
	}
	return validateDirectPrincipalMap(material.PrincipalMapYAML, plan)
}

// ValidateReleaseTransportTLSMaterial validates the distinct management
// trust domain against the fixed authority and host URI/IP identities.
func ValidateReleaseTransportTLSMaterial(plan ReleaseTransportTLSPlan, fingerprints TLSFingerprints, material TLSMaterial) error {
	var canonical *ReleaseTransportTLSPlan
	for _, candidate := range BuildReleaseTransportTLSPlans() {
		if candidate.HostID == plan.HostID {
			canonical = &candidate
			break
		}
	}
	if canonical == nil || *canonical != plan || len(material.PrincipalMapYAML) != 0 {
		return ErrInvalidLaneTLSMaterial
	}
	return validateMutualTLSMaterial(plan.PublicIP, plan.ClientURISAN, plan.ServerURISAN, fingerprints, material)
}

// ValidateDirectTLSLaneSet validates every Dev/QAS profile and proves that
// leaf certificates, CA certificates, public keys, and controller identities
// do not cross the lane boundary. PRD is excluded because this unit must not
// mutate or reissue its existing material.
func ValidateDirectTLSLaneSet(bundles []DirectTLSBundle) error {
	want := make(map[string]DirectTLSPlan, 6)
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			plan, err := BuildDirectTLSPlan(lane, profile)
			if err != nil {
				return ErrInvalidLaneTLSMaterial
			}
			want[string(lane)+"\x00"+profile] = plan
		}
	}
	if len(bundles) != len(want) {
		return ErrInvalidLaneTLSMaterial
	}
	seen := make(map[string]struct{}, len(bundles))
	certificateByLane := make(map[config.ReleaseLane]map[string]struct{})
	keyByLane := make(map[config.ReleaseLane]map[string]struct{})
	profileMaterial := make(map[string]TLSMaterial)
	for _, bundle := range bundles {
		key := string(bundle.Plan.Lane) + "\x00" + bundle.Plan.Profile
		canonical, exists := want[key]
		if !exists || bundle.Plan != canonical {
			return ErrInvalidLaneTLSMaterial
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidLaneTLSMaterial
		}
		seen[key] = struct{}{}
		if err := ValidateDirectTLSMaterial(bundle.Plan, bundle.Fingerprints, bundle.Material); err != nil {
			return ErrInvalidLaneTLSMaterial
		}
		certificates, publicKeys, err := materialFingerprints(bundle.Material)
		if err != nil {
			return ErrInvalidLaneTLSMaterial
		}
		if certificateByLane[bundle.Plan.Lane] == nil {
			certificateByLane[bundle.Plan.Lane] = make(map[string]struct{})
			keyByLane[bundle.Plan.Lane] = make(map[string]struct{})
		}
		for _, fingerprint := range certificates {
			certificateByLane[bundle.Plan.Lane][fingerprint] = struct{}{}
		}
		for _, fingerprint := range publicKeys {
			keyByLane[bundle.Plan.Lane][fingerprint] = struct{}{}
		}
		profileMaterial[key] = bundle.Material
	}
	if len(seen) != len(want) {
		return ErrInvalidLaneTLSMaterial
	}
	for leftLane, leftCertificates := range certificateByLane {
		for rightLane, rightCertificates := range certificateByLane {
			if leftLane >= rightLane {
				continue
			}
			for fingerprint := range leftCertificates {
				if _, reused := rightCertificates[fingerprint]; reused {
					return ErrInvalidLaneTLSMaterial
				}
			}
			for fingerprint := range keyByLane[leftLane] {
				if _, reused := keyByLane[rightLane][fingerprint]; reused {
					return ErrInvalidLaneTLSMaterial
				}
			}
		}
	}
	profiles := []string{"linux-host", "sandbox-host", "factory-sandbox"}
	for lane := range certificateByLane {
		for profileIndex, profile := range profiles {
			material := profileMaterial[string(lane)+"\x00"+profile]
			server, _, _, err := parseTLSKeyPair(material.ServerCertificatePEM, material.ServerPrivateKeyPEM)
			if err != nil {
				return ErrInvalidLaneTLSMaterial
			}
			client, _, _, err := parseTLSKeyPair(material.ClientCertificatePEM, material.ClientPrivateKeyPEM)
			if err != nil || server.Leaf == nil || client.Leaf == nil {
				return ErrInvalidLaneTLSMaterial
			}
			for existingIndex, existingProfile := range profiles {
				if existingIndex >= profileIndex {
					continue
				}
				existing := profileMaterial[string(lane)+"\x00"+existingProfile]
				existingServer, _, _, err := parseTLSKeyPair(existing.ServerCertificatePEM, existing.ServerPrivateKeyPEM)
				if err != nil {
					return ErrInvalidLaneTLSMaterial
				}
				existingClient, _, _, err := parseTLSKeyPair(existing.ClientCertificatePEM, existing.ClientPrivateKeyPEM)
				if err != nil || existingServer.Leaf == nil || existingClient.Leaf == nil ||
					laneCertFingerprint(server.Leaf) == laneCertFingerprint(existingServer.Leaf) ||
					laneSPKIFingerprint(server.Leaf) == laneSPKIFingerprint(existingServer.Leaf) ||
					laneCertFingerprint(client.Leaf) == laneCertFingerprint(existingClient.Leaf) ||
					laneSPKIFingerprint(client.Leaf) == laneSPKIFingerprint(existingClient.Leaf) {
					return ErrInvalidLaneTLSMaterial
				}
			}
		}
	}
	return nil
}

// ValidateDirectManagementTrustSeparation checks that a complete Dev/QAS
// direct set and the fixed :9443 management set use disjoint certificates and
// public keys. Management endpoints may share their own management CA.
func ValidateDirectManagementTrustSeparation(direct []DirectTLSBundle, management []ReleaseTransportTLSBundle) error {
	if err := ValidateDirectTLSLaneSet(direct); err != nil || len(management) != len(BuildReleaseTransportTLSPlans()) {
		return ErrInvalidLaneTLSMaterial
	}
	managementByHost := make(map[string]struct{}, len(management))
	directCertificates, directKeys := make(map[string]struct{}), make(map[string]struct{})
	for _, bundle := range direct {
		certificates, keys, err := materialFingerprints(bundle.Material)
		if err != nil {
			return ErrInvalidLaneTLSMaterial
		}
		for _, fingerprint := range certificates {
			directCertificates[fingerprint] = struct{}{}
		}
		for _, fingerprint := range keys {
			directKeys[fingerprint] = struct{}{}
		}
	}
	for _, bundle := range management {
		if err := ValidateReleaseTransportTLSMaterial(bundle.Plan, bundle.Fingerprints, bundle.Material); err != nil {
			return ErrInvalidLaneTLSMaterial
		}
		if _, duplicate := managementByHost[bundle.Plan.HostID]; duplicate {
			return ErrInvalidLaneTLSMaterial
		}
		managementByHost[bundle.Plan.HostID] = struct{}{}
		certificates, keys, err := materialFingerprints(bundle.Material)
		if err != nil {
			return ErrInvalidLaneTLSMaterial
		}
		for _, fingerprint := range certificates {
			if _, reused := directCertificates[fingerprint]; reused {
				return ErrInvalidLaneTLSMaterial
			}
		}
		for _, fingerprint := range keys {
			if _, reused := directKeys[fingerprint]; reused {
				return ErrInvalidLaneTLSMaterial
			}
		}
	}
	if len(managementByHost) != len(BuildReleaseTransportTLSPlans()) {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

func materialFingerprints(material TLSMaterial) ([]string, []string, error) {
	server, _, serverChain, err := parseTLSKeyPair(material.ServerCertificatePEM, material.ServerPrivateKeyPEM)
	if err != nil {
		return nil, nil, ErrInvalidLaneTLSMaterial
	}
	client, _, clientChain, err := parseTLSKeyPair(material.ClientCertificatePEM, material.ClientPrivateKeyPEM)
	if err != nil {
		return nil, nil, ErrInvalidLaneTLSMaterial
	}
	if _, _, err := parseLaneCertificates(material.ServerCAPEM); err != nil {
		return nil, nil, ErrInvalidLaneTLSMaterial
	}
	if _, _, err := parseLaneCertificates(material.ClientCAPEM); err != nil {
		return nil, nil, ErrInvalidLaneTLSMaterial
	}
	certificates := make([]string, 0, len(server.Certificate)+len(client.Certificate)+len(serverChain)+len(clientChain))
	keys := make([]string, 0, len(server.Certificate)+len(client.Certificate))
	appendCertificate := func(certificate *x509.Certificate) {
		certificates = append(certificates, laneCertFingerprint(certificate))
		keys = append(keys, laneSPKIFingerprint(certificate))
	}
	for _, der := range server.Certificate {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, nil, ErrInvalidLaneTLSMaterial
		}
		appendCertificate(certificate)
	}
	for _, der := range client.Certificate {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, nil, ErrInvalidLaneTLSMaterial
		}
		appendCertificate(certificate)
	}
	for _, certificate := range append(serverChain, clientChain...) {
		appendCertificate(certificate)
	}
	for _, rootPEM := range [][]byte{material.ServerCAPEM, material.ClientCAPEM} {
		_, roots, err := parseLaneCertificates(rootPEM)
		if err != nil {
			return nil, nil, ErrInvalidLaneTLSMaterial
		}
		for _, root := range roots {
			appendCertificate(root)
		}
	}
	return certificates, keys, nil
}

// ValidateMutualTLSMaterial performs local identity validation. It does not
// bind, contact, install, or start a network listener.
func validateMutualTLSMaterial(publicIP, clientURI, serverURI string, fingerprints TLSFingerprints, material TLSMaterial) error {
	if net.ParseIP(publicIP) == nil || !validLaneURI(clientURI) || !validLaneURI(serverURI) ||
		!validLaneFingerprint(fingerprints.ServerCertificate) || !validLaneFingerprint(fingerprints.ServerSPKI) ||
		!validLaneFingerprint(fingerprints.ServerCA) || !validLaneFingerprint(fingerprints.ClientCertificate) ||
		!validLaneFingerprint(fingerprints.ClientSPKI) || !validLaneFingerprint(fingerprints.ClientCA) {
		return ErrInvalidLaneTLSMaterial
	}
	serverPair, serverLeaf, serverChain, err := parseTLSKeyPair(material.ServerCertificatePEM, material.ServerPrivateKeyPEM)
	if err != nil || !laneCertificateMatches(serverLeaf, fingerprints.ServerCertificate, fingerprints.ServerSPKI) ||
		!exactServerEndpointSANs(serverLeaf, serverURI, publicIP) ||
		!verifyLaneCertificate(serverLeaf, serverChain, material.ServerCAPEM, fingerprints.ServerCA, x509.ExtKeyUsageServerAuth, publicIP) {
		return ErrInvalidLaneTLSMaterial
	}
	clientPair, clientLeaf, clientChain, err := parseTLSKeyPair(material.ClientCertificatePEM, material.ClientPrivateKeyPEM)
	if err != nil || !laneCertificateMatches(clientLeaf, fingerprints.ClientCertificate, fingerprints.ClientSPKI) ||
		!exactCertificateURIs(clientLeaf, clientURI) || len(clientLeaf.IPAddresses) != 0 || len(clientLeaf.DNSNames) != 0 ||
		!verifyLaneCertificate(clientLeaf, clientChain, material.ClientCAPEM, fingerprints.ClientCA, x509.ExtKeyUsageClientAuth, "") {
		return ErrInvalidLaneTLSMaterial
	}
	if len(serverPair.Certificate) == 0 || len(clientPair.Certificate) == 0 {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

// BuildDirectTLSClientConfig creates a TLS 1.3 client configuration for the
// canonical lane/profile route. It loads no files and opens no socket.
func BuildDirectTLSClientConfig(plan DirectTLSPlan, fingerprints TLSFingerprints, material TLSMaterial) (*tls.Config, error) {
	if !validDirectTLSPlan(plan) || plan.Lane == config.ReleaseLanePRD {
		return nil, ErrInvalidLaneTLSMaterial
	}
	return buildMutualTLSClientConfig(plan.PublicIP, plan.ClientURISAN, plan.ServerURISAN, fingerprints, material)
}

func validDirectTLSPlan(plan DirectTLSPlan) bool {
	if plan.routeProof == ([32]byte{}) || plan.routeProof != directTLSPlanProof(plan) {
		return false
	}
	canonical, err := buildDirectTLSPlanFromRoute(plan.Lane, plan.Profile, plan.PublicURL, plan.Bind)
	return err == nil && canonical == plan
}

func directTLSPlanProof(plan DirectTLSPlan) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		plan.Lane, plan.Profile, plan.Name, plan.PublicURL, plan.PublicIP, plan.Bind,
		plan.ClientURISAN, plan.ServerURISAN+"\x00"+plan.ControllerID+"\x00"+plan.ControllerType)))
}

// BuildReleaseTransportTLSClientConfig creates a TLS 1.3 client configuration
// for one canonical Mac-authority-to-host management route.
func BuildReleaseTransportTLSClientConfig(plan ReleaseTransportTLSPlan, fingerprints TLSFingerprints, material TLSMaterial) (*tls.Config, error) {
	canonical := false
	for _, candidate := range BuildReleaseTransportTLSPlans() {
		if candidate == plan {
			canonical = true
			break
		}
	}
	if !canonical {
		return nil, ErrInvalidLaneTLSMaterial
	}
	return buildMutualTLSClientConfig(plan.PublicIP, plan.ClientURISAN, plan.ServerURISAN, fingerprints, material)
}

// buildMutualTLSClientConfig verifies the ordinary chain/IP SAN and additionally
// pins the expected server URI/certificate/SPKI identity.
func buildMutualTLSClientConfig(publicIP, clientURI, serverURI string, fingerprints TLSFingerprints, material TLSMaterial) (*tls.Config, error) {
	if net.ParseIP(publicIP) == nil || !validLaneURI(clientURI) || !validLaneURI(serverURI) ||
		!validLaneFingerprint(fingerprints.ClientCertificate) || !validLaneFingerprint(fingerprints.ClientSPKI) ||
		!validLaneFingerprint(fingerprints.ServerCertificate) || !validLaneFingerprint(fingerprints.ServerSPKI) ||
		!validLaneFingerprint(fingerprints.ServerCA) {
		return nil, ErrInvalidLaneTLSMaterial
	}
	clientPair, clientLeaf, _, err := parseTLSKeyPair(material.ClientCertificatePEM, material.ClientPrivateKeyPEM)
	if err != nil || !laneCertificateMatches(clientLeaf, fingerprints.ClientCertificate, fingerprints.ClientSPKI) ||
		!exactCertificateURIs(clientLeaf, clientURI) || !validCertificateAtNow(clientLeaf, x509.ExtKeyUsageClientAuth) {
		return nil, ErrInvalidLaneTLSMaterial
	}
	roots, rootCertificates, err := parseLaneCertificates(material.ServerCAPEM)
	if err != nil {
		return nil, ErrInvalidLaneTLSMaterial
	}
	foundExpectedRoot := false
	for _, root := range rootCertificates {
		if laneCertFingerprint(root) == fingerprints.ServerCA {
			foundExpectedRoot = true
		}
	}
	if !foundExpectedRoot || len(clientPair.Certificate) == 0 {
		return nil, ErrInvalidLaneTLSMaterial
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   publicIP,
		RootCAs:      roots,
		Certificates: []tls.Certificate{clientPair},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrInvalidLaneTLSMaterial
			}
			server := state.PeerCertificates[0]
			if !laneCertificateMatches(server, fingerprints.ServerCertificate, fingerprints.ServerSPKI) ||
				!exactServerEndpointSANs(server, serverURI, publicIP) || !verifiedChainEndsAt(state.VerifiedChains, fingerprints.ServerCA) {
				return ErrInvalidLaneTLSMaterial
			}
			return nil
		},
	}, nil
}

type directPrincipalMap struct {
	Version    int                    `yaml:"version"`
	Principals []directPrincipalEntry `yaml:"principals"`
}

type directPrincipalEntry struct {
	URISAN         string `yaml:"uri_san"`
	ControllerType string `yaml:"controller_type"`
	ControllerID   string `yaml:"controller_id"`
}

func validateDirectPrincipalMap(contents []byte, plan DirectTLSPlan) error {
	if len(contents) == 0 || len(contents) > 64*1024 || bytes.Contains(contents, []byte("\x00")) {
		return ErrInvalidLaneTLSMaterial
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var document directPrincipalMap
	if err := decoder.Decode(&document); err != nil || document.Version != 1 || len(document.Principals) != 1 {
		return ErrInvalidLaneTLSMaterial
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidLaneTLSMaterial
	}
	entry := document.Principals[0]
	if entry.URISAN != plan.ClientURISAN || entry.ControllerType != plan.ControllerType || entry.ControllerID != plan.ControllerID {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

func parseTLSKeyPair(certificatePEM, keyPEM []byte) (tls.Certificate, *x509.Certificate, []*x509.Certificate, error) {
	if len(certificatePEM) == 0 || len(certificatePEM) > 1<<20 || len(keyPEM) == 0 || len(keyPEM) > 1<<20 {
		return tls.Certificate{}, nil, nil, ErrInvalidLaneTLSMaterial
	}
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, nil, nil, ErrInvalidLaneTLSMaterial
	}
	certificates := make([]*x509.Certificate, 0, len(pair.Certificate)-1)
	for i, der := range pair.Certificate {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return tls.Certificate{}, nil, nil, ErrInvalidLaneTLSMaterial
		}
		if i == 0 {
			pair.Leaf = certificate
		} else {
			certificates = append(certificates, certificate)
		}
	}
	return pair, pair.Leaf, certificates, nil
}

func verifyLaneCertificate(leaf *x509.Certificate, intermediates []*x509.Certificate, rootPEM []byte, expectedRoot string, usage x509.ExtKeyUsage, ip string) bool {
	roots, rootCertificates, err := parseLaneCertificates(rootPEM)
	if err != nil {
		return false
	}
	rootFound := false
	for _, root := range rootCertificates {
		if laneCertFingerprint(root) == expectedRoot {
			rootFound = true
		}
	}
	if !rootFound {
		return false
	}
	chain := x509.NewCertPool()
	for _, certificate := range intermediates {
		chain.AddCert(certificate)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: chain, CurrentTime: time.Now(), DNSName: ip, KeyUsages: []x509.ExtKeyUsage{usage}})
	if err != nil {
		return false
	}
	for _, verified := range chains {
		if len(verified) > 0 && laneCertFingerprint(verified[len(verified)-1]) == expectedRoot {
			return true
		}
	}
	return false
}

func verifiedChainEndsAt(chains [][]*x509.Certificate, expectedRoot string) bool {
	for _, chain := range chains {
		if len(chain) > 0 && laneCertFingerprint(chain[len(chain)-1]) == expectedRoot {
			return true
		}
	}
	return false
}

func validCertificateAtNow(certificate *x509.Certificate, usage x509.ExtKeyUsage) bool {
	if certificate == nil || time.Now().Before(certificate.NotBefore) || time.Now().After(certificate.NotAfter) {
		return false
	}
	if len(certificate.ExtKeyUsage) == 0 {
		return true
	}
	for _, allowed := range certificate.ExtKeyUsage {
		if allowed == usage || allowed == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func parseLaneCertificates(contents []byte) (*x509.CertPool, []*x509.Certificate, error) {
	if len(contents) == 0 || len(contents) > 1<<20 {
		return nil, nil, ErrInvalidLaneTLSMaterial
	}
	remaining := contents
	pool := x509.NewCertPool()
	var certificates []*x509.Certificate
	for len(bytes.TrimSpace(remaining)) > 0 {
		remaining = bytes.TrimSpace(remaining)
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, nil, ErrInvalidLaneTLSMaterial
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, nil, ErrInvalidLaneTLSMaterial
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, ErrInvalidLaneTLSMaterial
		}
		certificates = append(certificates, certificate)
		pool.AddCert(certificate)
		remaining = rest
	}
	if len(certificates) == 0 {
		return nil, nil, ErrInvalidLaneTLSMaterial
	}
	return pool, certificates, nil
}

func exactCertificateURIs(certificate *x509.Certificate, expected string) bool {
	names, ok := subjectAltNames(certificate)
	return ok && len(names) == 1 && names[0].Class == 2 && names[0].Tag == 6 && string(names[0].Bytes) == expected &&
		len(certificate.URIs) == 1 && certificate.URIs[0].String() == expected
}

func exactServerEndpointSANs(certificate *x509.Certificate, expectedURI, expectedIP string) bool {
	names, ok := subjectAltNames(certificate)
	if !ok || len(certificate.IPAddresses) != 1 || len(certificate.URIs) != 1 || certificate.URIs[0].String() != expectedURI {
		return false
	}
	uriCount, ipCount := 0, 0
	for _, name := range names {
		switch {
		case name.Class == 2 && name.Tag == 2: // Optional DNS SAN; the fixed URL still verifies the IP SAN.
			continue
		case name.Class == 2 && name.Tag == 6:
			if string(name.Bytes) != expectedURI {
				return false
			}
			uriCount++
		case name.Class == 2 && name.Tag == 7:
			ipCount++
		default:
			return false
		}
	}
	if uriCount != 1 || ipCount != 1 {
		return false
	}
	actual := certificate.IPAddresses[0]
	want := net.ParseIP(expectedIP)
	return want != nil && actual.Equal(want)
}

func subjectAltNames(certificate *x509.Certificate) ([]asn1.RawValue, bool) {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			var names []asn1.RawValue
			rest, err := asn1.Unmarshal(extension.Value, &names)
			return names, err == nil && len(rest) == 0 && len(names) > 0
		}
	}
	return nil, false
}

func laneCertificateMatches(certificate *x509.Certificate, certificateFingerprint, spkiFingerprint string) bool {
	return laneCertFingerprint(certificate) == certificateFingerprint && laneSPKIFingerprint(certificate) == spkiFingerprint
}

func laneCertFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.Raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func laneSPKIFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validLaneFingerprint(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func validLaneURI(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "urn" && parsed.Opaque != "" && parsed.User == nil && parsed.Host == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func laneTLSURI(lane config.ReleaseLane, role, profile string) string {
	return "urn:remote-session-runner:lane:" + string(lane) + ":" + role + ":" + profile
}
