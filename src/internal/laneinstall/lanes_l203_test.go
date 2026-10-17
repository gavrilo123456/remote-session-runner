package laneinstall

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestLanesL203EndpointMapsAndValidMaterial(t *testing.T) {
	plans, err := BuildDirectTLSPlans()
	if err != nil || len(plans) != 9 {
		t.Fatalf("direct endpoint plans=%d err=%v, want nine fixed tuples", len(plans), err)
	}
	wantIPs := map[string]string{
		"linux-host": "129.151.232.40", "sandbox-host": "132.226.205.205", "factory-sandbox": "145.241.255.66",
	}
	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		key := string(plan.Lane) + "/" + plan.Profile
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate direct endpoint %s", key)
		}
		seen[key] = struct{}{}
		port := "8443"
		if plan.Lane == config.ReleaseLaneDev {
			port = "8444"
		} else if plan.Lane == config.ReleaseLaneQAS {
			port = "8445"
		}
		if plan.PublicIP != wantIPs[plan.Profile] || plan.PublicURL != "https://"+plan.PublicIP+":"+port || plan.Bind == "" ||
			plan.ClientURISAN != laneTLSURI(plan.Lane, "controller", plan.Profile) ||
			plan.ServerURISAN != laneTLSURI(plan.Lane, "server", plan.Profile) ||
			plan.ControllerID != "runner-controller-"+string(plan.Lane)+"-"+plan.Profile || plan.ControllerType != "direct_mtls" {
			t.Errorf("fixed direct endpoint %s = %+v", key, plan)
		}
	}
	management := BuildReleaseTransportTLSPlans()
	if len(management) != 3 {
		t.Fatalf("management endpoint count=%d, want three Ubuntu :9443 routes", len(management))
	}
	for _, plan := range management {
		parsed, err := url.Parse(plan.PublicURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != plan.PublicIP || parsed.Port() != "9443" ||
			plan.Bind == "" || plan.ClientURISAN != releaseAuthorityURI ||
			plan.ServerURISAN != "urn:remote-session-runner:release-transport:host:"+plan.HostID {
			t.Errorf("management TLS map entry = %+v err=%v", plan, err)
		}
	}

	plan, err := BuildDirectTLSPlan(config.ReleaseLaneDev, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	bundle := l203DirectBundle(t, plan, nil, nil)
	if err := ValidateDirectTLSMaterial(bundle.Plan, bundle.Fingerprints, bundle.Material); err != nil {
		t.Fatalf("valid test-only direct TLS material rejected: %v", err)
	}
	dnsBundle := l203DirectBundleWithDNS(t, plan, "linux-poc-dev.example.test")
	if err := ValidateDirectTLSMaterial(dnsBundle.Plan, dnsBundle.Fingerprints, dnsBundle.Material); err != nil {
		t.Fatalf("optional DNS SAN alongside the required IP and URI SANs rejected: %v", err)
	}
	if _, err := BuildDirectTLSClientConfig(plan, bundle.Fingerprints, bundle.Material); err != nil {
		t.Fatalf("valid fixed-IP mTLS client config rejected: %v", err)
	}
	clientOnly := bundle.Material
	clientOnly.ClientCAPEM = nil // Mac client carries its leaf and server trust, not the Linux client-CA bundle.
	if _, err := BuildDirectTLSClientConfig(plan, bundle.Fingerprints, clientOnly); err != nil {
		t.Fatalf("fixed-IP Mac client config required the server-side client CA: %v", err)
	}
	prdPlan, err := BuildDirectTLSPlan(config.ReleaseLanePRD, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildDirectTLSClientConfig(prdPlan, bundle.Fingerprints, bundle.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("PRD TLS client construction error=%v, want protected-route refusal", err)
	}
	managementPlan := management[0]
	managementMaterial, managementFingerprints := l203Material(t, managementPlan.PublicIP, managementPlan.ClientURISAN, managementPlan.ServerURISAN, "", "", nil, nil)
	if err := ValidateReleaseTransportTLSMaterial(managementPlan, managementFingerprints, managementMaterial); err != nil {
		t.Fatalf("valid test-only management TLS material rejected: %v", err)
	}
	if _, err := BuildReleaseTransportTLSClientConfig(managementPlan, managementFingerprints, managementMaterial); err != nil {
		t.Fatalf("valid fixed-IP management client config rejected: %v", err)
	}
	wrongManagementPlan := managementPlan
	wrongManagementPlan.PublicIP = "127.0.0.1"
	if _, err := BuildReleaseTransportTLSClientConfig(wrongManagementPlan, managementFingerprints, managementMaterial); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("caller-selected management client route error=%v, want rejection", err)
	}
}

func TestLanesL203RejectsWrongSANKeyChainAndControllerMap(t *testing.T) {
	plan, err := BuildDirectTLSPlan(config.ReleaseLaneDev, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	valid := l203DirectBundle(t, plan, nil, nil)
	tests := []struct {
		name   string
		bundle DirectTLSBundle
	}{
		{name: "server URI SAN", bundle: l203DirectBundleWithSAN(t, plan, "urn:remote-session-runner:lane:qas:server:linux-host", plan.PublicIP, plan.ClientURISAN)},
		{name: "server literal IP SAN", bundle: l203DirectBundleWithSAN(t, plan, plan.ServerURISAN, "203.0.113.19", plan.ClientURISAN)},
		{name: "foreign client URI SAN", bundle: l203DirectBundleWithSAN(t, plan, plan.ServerURISAN, plan.PublicIP, "urn:remote-session-runner:lane:qas:controller:linux-host")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateDirectTLSMaterial(test.bundle.Plan, test.bundle.Fingerprints, test.bundle.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
				t.Fatalf("ValidateDirectTLSMaterial error=%v, want safe material rejection", err)
			}
		})
	}

	wrongMap := valid
	wrongMap.Material.PrincipalMapYAML = []byte("version: 1\nprincipals:\n  - uri_san: " + plan.ClientURISAN + "\n    controller_type: direct_mtls\n    controller_id: runner-controller-qas-linux-host\n")
	if err := ValidateDirectTLSMaterial(plan, wrongMap.Fingerprints, wrongMap.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("foreign controller ID error=%v, want rejection", err)
	}
	duplicateMap := valid
	duplicateMap.Material.PrincipalMapYAML = []byte("version: 1\nprincipals:\n  - uri_san: " + plan.ClientURISAN + "\n    controller_type: direct_mtls\n    controller_id: " + plan.ControllerID + "\n  - uri_san: " + plan.ClientURISAN + "\n    controller_type: direct_mtls\n    controller_id: " + plan.ControllerID + "\n")
	if err := ValidateDirectTLSMaterial(plan, duplicateMap.Fingerprints, duplicateMap.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("duplicate principal map error=%v, want rejection", err)
	}

	other := l203DirectBundle(t, plan, nil, nil)
	wrongKey := valid
	wrongKey.Material.ServerPrivateKeyPEM = other.Material.ServerPrivateKeyPEM
	if err := ValidateDirectTLSMaterial(plan, wrongKey.Fingerprints, wrongKey.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("server key mismatch error=%v, want rejection", err)
	}
	wrongCA := valid
	wrongCA.Material.ServerCAPEM = append(append([]byte(nil), valid.Material.ServerCAPEM...), other.Material.ServerCAPEM...)
	wrongCA.Fingerprints.ServerCA = other.Fingerprints.ServerCA
	if err := ValidateDirectTLSMaterial(plan, wrongCA.Fingerprints, wrongCA.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("server CA mismatch error=%v, want rejection", err)
	}
	wrongFingerprint := valid
	wrongFingerprint.Fingerprints.ClientSPKI = wrongFingerprint.Fingerprints.ServerSPKI
	if err := ValidateDirectTLSMaterial(plan, wrongFingerprint.Fingerprints, wrongFingerprint.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("client SPKI mismatch error=%v, want rejection", err)
	}
	expired := l203DirectBundle(t, plan, nil, nil)
	serverAuthority, clientAuthority := l203Authority(t), l203Authority(t)
	expired.Material.ServerCertificatePEM, expired.Material.ServerPrivateKeyPEM = l203Leaf(t, serverAuthority, plan.ServerURISAN, plan.PublicIP, nil, x509.ExtKeyUsageServerAuth, time.Now().Add(-time.Hour))
	expired.Material.ServerCAPEM = serverAuthority.pem
	expired.Fingerprints.ServerCertificate, expired.Fingerprints.ServerSPKI = l203LeafFingerprints(t, expired.Material.ServerCertificatePEM)
	expired.Fingerprints.ServerCA = laneCertFingerprint(serverAuthority.certificate)
	expired.Material.ClientCertificatePEM, expired.Material.ClientPrivateKeyPEM = l203Leaf(t, clientAuthority, plan.ClientURISAN, "", nil, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	expired.Material.ClientCAPEM = clientAuthority.pem
	expired.Fingerprints.ClientCertificate, expired.Fingerprints.ClientSPKI = l203LeafFingerprints(t, expired.Material.ClientCertificatePEM)
	expired.Fingerprints.ClientCA = laneCertFingerprint(clientAuthority.certificate)
	if err := ValidateDirectTLSMaterial(plan, expired.Fingerprints, expired.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("expired server leaf error=%v, want rejection", err)
	}

	foreignPlan := plan
	foreignPlan.Lane = config.ReleaseLaneQAS
	if err := ValidateDirectTLSMaterial(foreignPlan, valid.Fingerprints, valid.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("noncanonical lane plan error=%v, want rejection", err)
	}
	if err := ValidateReleaseTransportTLSMaterial(ReleaseTransportTLSPlan{HostID: "arbitrary", PublicIP: "127.0.0.1"}, valid.Fingerprints, valid.Material); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("caller-selected management endpoint error=%v, want rejection", err)
	}
}

func TestLanesL203RejectsCrossLaneAndManagementTrustReuse(t *testing.T) {
	var direct []DirectTLSBundle
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			plan, err := BuildDirectTLSPlan(lane, profile)
			if err != nil {
				t.Fatal(err)
			}
			direct = append(direct, l203DirectBundle(t, plan, nil, nil))
		}
	}
	if err := ValidateDirectTLSLaneSet(direct); err != nil {
		t.Fatalf("six isolated test-only Dev/QAS bundles rejected: %v", err)
	}

	managementPlans := BuildReleaseTransportTLSPlans()
	managementServerCA, managementClientCA := l203Authority(t), l203Authority(t)
	var management []ReleaseTransportTLSBundle
	for _, plan := range managementPlans {
		material, fingerprints := l203Material(t, plan.PublicIP, plan.ClientURISAN, plan.ServerURISAN, "", "", managementServerCA, managementClientCA)
		management = append(management, ReleaseTransportTLSBundle{Plan: plan, Fingerprints: fingerprints, Material: material})
	}
	if err := ValidateDirectManagementTrustSeparation(direct, management); err != nil {
		t.Fatalf("independent management trust domain rejected: %v", err)
	}

	validDirect := append([]DirectTLSBundle(nil), direct...)
	devPlan, _ := BuildDirectTLSPlan(config.ReleaseLaneDev, "linux-host")
	devServerCA, devClientCA := l203Authority(t), l203Authority(t)
	direct[0] = l203DirectBundle(t, devPlan, devServerCA, devClientCA)
	qasPlan, _ := BuildDirectTLSPlan(config.ReleaseLaneQAS, "linux-host")
	direct[3] = l203DirectBundle(t, qasPlan, devServerCA, devClientCA)
	if err := ValidateDirectTLSLaneSet(direct); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("cross-lane CA/certificate reuse error=%v, want rejection", err)
	}
	if err := ValidateDirectManagementTrustSeparation(direct, management); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("invalid direct lane set passed management separation: %v", err)
	}
	direct = validDirect

	// A management leaf chained to a direct lane CA is still rejected even
	// though its endpoint and URIs match the management table.
	management[0].Material.ServerCAPEM = direct[0].Material.ServerCAPEM
	management[0].Fingerprints.ServerCA = direct[0].Fingerprints.ServerCA
	management[0].Material.ServerCertificatePEM, management[0].Material.ServerPrivateKeyPEM = l203Leaf(t, devServerCA,
		management[0].Plan.ServerURISAN, management[0].Plan.PublicIP, nil, x509.ExtKeyUsageServerAuth, time.Now().Add(time.Hour))
	management[0].Fingerprints.ServerCertificate, management[0].Fingerprints.ServerSPKI = l203LeafFingerprints(t, management[0].Material.ServerCertificatePEM)
	if err := ValidateDirectManagementTrustSeparation(direct, management); !errors.Is(err, ErrInvalidLaneTLSMaterial) {
		t.Fatalf("management/direct CA reuse error=%v, want rejection", err)
	}
}

func TestLanesL203LocalMutualTLSIdentityNegatives(t *testing.T) {
	devPlan, err := BuildDirectTLSPlan(config.ReleaseLaneDev, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	devServerCA, devClientCA := l203Authority(t), l203Authority(t)
	dev := l203DirectBundle(t, devPlan, devServerCA, devClientCA)
	clientConfig, err := BuildDirectTLSClientConfig(devPlan, dev.Fingerprints, dev.Material)
	if err != nil {
		t.Fatalf("build fixed-IP mTLS client: %v", err)
	}
	if err := l203LocalHandshake(t, dev.Material, clientConfig, devPlan.ClientURISAN, dev.Fingerprints.ClientCertificate); err != nil {
		t.Fatalf("valid local TLS handshake failed: %v", err)
	}

	qasPlan, err := BuildDirectTLSPlan(config.ReleaseLaneQAS, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	qas := l203DirectBundle(t, qasPlan, nil, nil)
	qasClientForDevServer := qas.Material
	qasClientForDevServer.ServerCAPEM = dev.Material.ServerCAPEM
	foreignClient := l203ClientConfigForServer(t, devPlan, dev.Fingerprints, qasClientForDevServer)
	if err := l203LocalHandshake(t, dev.Material, foreignClient, devPlan.ClientURISAN, dev.Fingerprints.ClientCertificate); err == nil {
		t.Fatal("Dev endpoint accepted QAS client chain/identity")
	}

	wrongURIServer := l203DirectBundleWithSAN(t, devPlan, "urn:remote-session-runner:lane:dev:server:other-profile", devPlan.PublicIP, devPlan.ClientURISAN)
	wrongURIFingerprints := wrongURIServer.Fingerprints
	wrongURIClient, err := BuildDirectTLSClientConfig(devPlan, wrongURIFingerprints, wrongURIServer.Material)
	if err != nil {
		t.Fatalf("build pinning client for wrong-URI test server: %v", err)
	}
	if err := l203LocalHandshake(t, wrongURIServer.Material, wrongURIClient, devPlan.ClientURISAN, wrongURIFingerprints.ClientCertificate); err == nil {
		t.Fatal("client accepted a trusted/IP-valid server with a foreign URI SAN")
	}

	wrongIPServer := l203DirectBundleWithSAN(t, devPlan, devPlan.ServerURISAN, "203.0.113.99", devPlan.ClientURISAN)
	wrongIPFingerprints := wrongIPServer.Fingerprints
	wrongIPClient, err := BuildDirectTLSClientConfig(devPlan, wrongIPFingerprints, wrongIPServer.Material)
	if err != nil {
		t.Fatalf("build pinning client for wrong-IP test server: %v", err)
	}
	if err := l203LocalHandshake(t, wrongIPServer.Material, wrongIPClient, devPlan.ClientURISAN, wrongIPFingerprints.ClientCertificate); err == nil {
		t.Fatal("client accepted a trusted server with the wrong literal-IP SAN")
	}

	wrongClientCertificate, wrongClientKey := l203Leaf(t, devClientCA, "urn:remote-session-runner:lane:dev:controller:other-profile", "", nil, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	wrongClient := dev
	wrongClient.Material.ClientCertificatePEM = wrongClientCertificate
	wrongClient.Material.ClientPrivateKeyPEM = wrongClientKey
	wrongClient.Fingerprints.ClientCertificate, wrongClient.Fingerprints.ClientSPKI = l203LeafFingerprints(t, wrongClientCertificate)
	clientPair, _, _, err := parseTLSKeyPair(wrongClient.Material.ClientCertificatePEM, wrongClient.Material.ClientPrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	wrongClientConfig := l203ClientConfigForServer(t, devPlan, dev.Fingerprints, dev.Material)
	wrongClientConfig.Certificates = []tls.Certificate{clientPair}
	if err := l203LocalHandshake(t, dev.Material, wrongClientConfig, devPlan.ClientURISAN, wrongClient.Fingerprints.ClientCertificate); err == nil {
		t.Fatal("server accepted a trusted client with a foreign controller URI SAN")
	}
}

type l203AuthorityFixture struct {
	certificate *x509.Certificate
	privateKey  *ecdsa.PrivateKey
	pem         []byte
}

func l203Authority(t *testing.T) *l203AuthorityFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial := l203Serial(t)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "L2-03 test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &l203AuthorityFixture{certificate: certificate, privateKey: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func l203Material(t *testing.T, publicIP, clientURI, serverURI, controllerType, controllerID string, serverAuthority, clientAuthority *l203AuthorityFixture) (TLSMaterial, TLSFingerprints) {
	t.Helper()
	if serverAuthority == nil {
		serverAuthority = l203Authority(t)
	}
	if clientAuthority == nil {
		clientAuthority = l203Authority(t)
	}
	serverCertificatePEM, serverPrivateKeyPEM := l203Leaf(t, serverAuthority, serverURI, publicIP, nil, x509.ExtKeyUsageServerAuth, time.Now().Add(time.Hour))
	clientCertificatePEM, clientPrivateKeyPEM := l203Leaf(t, clientAuthority, clientURI, "", nil, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	serverCert, _ := x509.ParseCertificate(pemBlockBytes(t, serverCertificatePEM))
	clientCert, _ := x509.ParseCertificate(pemBlockBytes(t, clientCertificatePEM))
	fingerprints := TLSFingerprints{
		ServerCertificate: laneCertFingerprint(serverCert), ServerSPKI: laneSPKIFingerprint(serverCert), ServerCA: laneCertFingerprint(serverAuthority.certificate),
		ClientCertificate: laneCertFingerprint(clientCert), ClientSPKI: laneSPKIFingerprint(clientCert), ClientCA: laneCertFingerprint(clientAuthority.certificate),
	}
	material := TLSMaterial{
		ServerCertificatePEM: serverCertificatePEM, ServerPrivateKeyPEM: serverPrivateKeyPEM, ServerCAPEM: serverAuthority.pem,
		ClientCertificatePEM: clientCertificatePEM, ClientPrivateKeyPEM: clientPrivateKeyPEM, ClientCAPEM: clientAuthority.pem,
	}
	if controllerType != "" || controllerID != "" {
		material.PrincipalMapYAML = []byte("version: 1\nprincipals:\n  - uri_san: " + clientURI + "\n    controller_type: " + controllerType + "\n    controller_id: " + controllerID + "\n")
	}
	return material, fingerprints
}

func l203DirectBundle(t *testing.T, plan DirectTLSPlan, serverAuthority, clientAuthority *l203AuthorityFixture) DirectTLSBundle {
	t.Helper()
	material, fingerprints := l203Material(t, plan.PublicIP, plan.ClientURISAN, plan.ServerURISAN, plan.ControllerType, plan.ControllerID, serverAuthority, clientAuthority)
	return DirectTLSBundle{Plan: plan, Fingerprints: fingerprints, Material: material}
}

func l203DirectBundleWithSAN(t *testing.T, plan DirectTLSPlan, serverURI, serverIP, clientURI string) DirectTLSBundle {
	t.Helper()
	serverAuthority, clientAuthority := l203Authority(t), l203Authority(t)
	serverCertificatePEM, serverPrivateKeyPEM := l203Leaf(t, serverAuthority, serverURI, serverIP, nil, x509.ExtKeyUsageServerAuth, time.Now().Add(time.Hour))
	clientCertificatePEM, clientPrivateKeyPEM := l203Leaf(t, clientAuthority, clientURI, "", nil, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	return l203BundleFromLeaves(t, plan, serverAuthority, clientAuthority, serverCertificatePEM, serverPrivateKeyPEM, clientCertificatePEM, clientPrivateKeyPEM)
}

func l203DirectBundleWithDNS(t *testing.T, plan DirectTLSPlan, dns string) DirectTLSBundle {
	t.Helper()
	serverAuthority, clientAuthority := l203Authority(t), l203Authority(t)
	serverCertificatePEM, serverPrivateKeyPEM := l203Leaf(t, serverAuthority, plan.ServerURISAN, plan.PublicIP, []string{dns}, x509.ExtKeyUsageServerAuth, time.Now().Add(time.Hour))
	clientCertificatePEM, clientPrivateKeyPEM := l203Leaf(t, clientAuthority, plan.ClientURISAN, "", nil, x509.ExtKeyUsageClientAuth, time.Now().Add(time.Hour))
	return l203BundleFromLeaves(t, plan, serverAuthority, clientAuthority, serverCertificatePEM, serverPrivateKeyPEM, clientCertificatePEM, clientPrivateKeyPEM)
}

func l203BundleFromLeaves(t *testing.T, plan DirectTLSPlan, serverAuthority, clientAuthority *l203AuthorityFixture, serverCertificatePEM, serverPrivateKeyPEM, clientCertificatePEM, clientPrivateKeyPEM []byte) DirectTLSBundle {
	t.Helper()
	serverCert, err := x509.ParseCertificate(pemBlockBytes(t, serverCertificatePEM))
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := x509.ParseCertificate(pemBlockBytes(t, clientCertificatePEM))
	if err != nil {
		t.Fatal(err)
	}
	material := TLSMaterial{
		ServerCertificatePEM: serverCertificatePEM, ServerPrivateKeyPEM: serverPrivateKeyPEM, ServerCAPEM: serverAuthority.pem,
		ClientCertificatePEM: clientCertificatePEM, ClientPrivateKeyPEM: clientPrivateKeyPEM, ClientCAPEM: clientAuthority.pem,
		PrincipalMapYAML: []byte("version: 1\nprincipals:\n  - uri_san: " + plan.ClientURISAN + "\n    controller_type: " + plan.ControllerType + "\n    controller_id: " + plan.ControllerID + "\n"),
	}
	fingerprints := TLSFingerprints{
		ServerCertificate: laneCertFingerprint(serverCert), ServerSPKI: laneSPKIFingerprint(serverCert), ServerCA: laneCertFingerprint(serverAuthority.certificate),
		ClientCertificate: laneCertFingerprint(clientCert), ClientSPKI: laneSPKIFingerprint(clientCert), ClientCA: laneCertFingerprint(clientAuthority.certificate),
	}
	return DirectTLSBundle{Plan: plan, Fingerprints: fingerprints, Material: material}
}

func l203Leaf(t *testing.T, authority *l203AuthorityFixture, uri, ip string, dnsNames []string, usage x509.ExtKeyUsage, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	parsedURI, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: l203Serial(t), Subject: pkix.Name{CommonName: "L2-03 test leaf"},
		NotBefore: now.Add(-time.Minute), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{usage}, BasicConstraintsValid: true,
		URIs: []*url.URL{parsedURI}, DNSNames: dnsNames,
	}
	if ip != "" {
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil {
			t.Fatalf("invalid test IP %q", ip)
		}
		template.IPAddresses = []net.IP{parsedIP}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority.certificate, &key.PublicKey, authority.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func l203Serial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	return serial.Add(serial, big.NewInt(1))
}

func pemBlockBytes(t *testing.T, contents []byte) []byte {
	t.Helper()
	block, rest := pem.Decode(contents)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("test PEM is malformed")
	}
	return block.Bytes
}

func l203LeafFingerprints(t *testing.T, contents []byte) (string, string) {
	t.Helper()
	certificate, err := x509.ParseCertificate(pemBlockBytes(t, contents))
	if err != nil {
		t.Fatal(err)
	}
	return laneCertFingerprint(certificate), laneSPKIFingerprint(certificate)
}

func l203ClientConfigForServer(t *testing.T, plan DirectTLSPlan, fingerprints TLSFingerprints, clientMaterial TLSMaterial) *tls.Config {
	t.Helper()
	clientPair, _, _, err := parseTLSKeyPair(clientMaterial.ClientCertificatePEM, clientMaterial.ClientPrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots, _, err := parseLaneCertificates(clientMaterial.ServerCAPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: plan.PublicIP, RootCAs: roots, Certificates: []tls.Certificate{clientPair},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrInvalidLaneTLSMaterial
			}
			leaf := state.PeerCertificates[0]
			if !laneCertificateMatches(leaf, fingerprints.ServerCertificate, fingerprints.ServerSPKI) ||
				!exactServerEndpointSANs(leaf, plan.ServerURISAN, plan.PublicIP) {
				return ErrInvalidLaneTLSMaterial
			}
			return nil
		},
	}
}

func l203LocalHandshake(t *testing.T, serverMaterial TLSMaterial, clientConfig *tls.Config, expectedClientURI, expectedClientFingerprint string) error {
	t.Helper()
	serverPair, _, _, err := parseTLSKeyPair(serverMaterial.ServerCertificatePEM, serverMaterial.ServerPrivateKeyPEM)
	if err != nil {
		return err
	}
	clientRoots, _, err := parseLaneCertificates(serverMaterial.ClientCAPEM)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	serverConfig := &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverPair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || !exactCertificateURIs(state.PeerCertificates[0], expectedClientURI) ||
				laneCertFingerprint(state.PeerCertificates[0]) != expectedClientFingerprint {
				return ErrInvalidLaneTLSMaterial
			}
			return nil
		},
	}
	serverResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		secure := tls.Server(connection, serverConfig)
		serverErr := secure.Handshake()
		_ = secure.Close()
		serverResult <- serverErr
	}()
	connection, err := tls.Dial("tcp", listener.Addr().String(), clientConfig)
	if err == nil {
		_ = connection.Close()
	}
	serverErr := <-serverResult
	if err == nil && serverErr == nil {
		return nil
	}
	return errors.Join(err, serverErr)
}

func TestLanesL203FingerprintShape(t *testing.T) {
	digest := sha256.Sum256([]byte("l2-03 identity"))
	fingerprint := "sha256:" + hex.EncodeToString(digest[:])
	if !validLaneFingerprint(fingerprint) || validLaneFingerprint("SHA256:"+hex.EncodeToString(digest[:])) {
		t.Fatal("fingerprint validation did not enforce lowercase SHA-256 form")
	}
}
