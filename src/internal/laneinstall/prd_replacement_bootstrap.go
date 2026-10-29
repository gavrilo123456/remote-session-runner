package laneinstall

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshdeploy"
)

const prdReplacementBootstrapEvidenceMaxAge = 2 * time.Minute

// PRDReplacementBootstrapInput is the fully verified, in-memory input to a
// first installation on the replacement Linux worker. It contains secrets
// only so the caller can write their fixed owner-only destinations; evidence
// records contain hashes and identities, never these bytes.
type PRDReplacementBootstrapInput struct {
	OperationID           string
	TemplateHostID        string
	TemplateConfiguration []byte
	MacConfiguration      []byte
	Absence               CatalogNamespaceAbsenceReport
	ReleaseManifest       CatalogReleaseManifest
	Bundle                SimpleBundle
	ExpectedReleaseSHA256 string
	ServerCertificatePEM  []byte
	ServerPrivateKeyPEM   []byte
	ServerCSRPEM          []byte
	ServerCAPEM           []byte
	ClientCertificatePEM  []byte
	ClientCAPEM           []byte
	ClientPrincipalMap    []byte
	DispatcherPublicKey   string
	SSHHostPublicKey      string
	Now                   time.Time
}

// PRDReplacementBootstrapRequest is the private operator input accepted by the
// public bootstrap command. Release bytes and namespace-absence evidence are
// deliberately omitted: the target host loads the sealed release locally and
// captures fresh absence evidence itself. Certificate and key fields are
// transported only over the pinned host-RPC stdin channel and are never
// included in operation records or command output.
type PRDReplacementBootstrapRequest struct {
	OperationID           string `json:"operation_id"`
	TemplateHostID        string `json:"template_host_id"`
	TemplateConfiguration []byte `json:"template_configuration"`
	MacConfiguration      []byte `json:"mac_configuration"`
	ReleaseID             string `json:"release_id"`
	ExpectedReleaseSHA256 string `json:"expected_release_sha256"`
	ServerCertificatePEM  []byte `json:"server_certificate_pem"`
	ServerCSR256          string `json:"server_csr_sha256"`
	ServerCAPEM           []byte `json:"server_ca_pem"`
	ClientCertificatePEM  []byte `json:"client_certificate_pem"`
	ClientCAPEM           []byte `json:"client_ca_pem"`
	ClientPrincipalMap    []byte `json:"client_principal_map"`
	DispatcherPublicKey   string `json:"dispatcher_public_key"`
	SSHHostPublicKey      string `json:"ssh_host_public_key"`
	ApprovalSHA256        string `json:"approval_sha256"`
	PublisherOwner        string `json:"publisher_owner"`
	PublisherControl      string `json:"publisher_control"`
	PublishersPaused      bool   `json:"publishers_paused"`
	NoNewAccepted         bool   `json:"no_new_accepted"`
}

// PRDReplacementBootstrapPlan contains fixed paths and bytes derived from the
// canonical replacement namespace. It cannot be redirected to another host,
// lane, runtime root, release or account.
type PRDReplacementBootstrapPlan struct {
	OperationID                  string
	TemplateHostID               string
	TemplateConfigSHA256         string
	MacConfigSHA256              string
	AbsenceEvidenceSHA256        string
	Absence                      CatalogNamespaceAbsenceReport
	Release                      CatalogReleaseIdentity
	SourceRevision               string
	Namespace                    config.LaneNamespace
	ReleaseCatalogRoot           string
	ReleaseBundleRoot            string
	RunnerUnit                   string
	Configuration                []byte
	ConfigurationSHA256          string
	MacConfiguration             []byte
	MacCandidateSHA256           string
	MacServerCAPath              string
	MacClientCertificatePath     string
	MacClientPrivateKeyPath      string
	MacDispatcherKeyPath         string
	MacKnownHostsPath            string
	LinuxClientCAPath            string
	LinuxPrincipalMapPath        string
	LinuxServerPrivateKeyPath    string
	DirectClientURISAN           string
	ServerCertificate            []byte
	ServerPrivateKey             []byte
	ServerCSR256                 string
	ServerCA                     []byte
	ClientCA                     []byte
	ClientPrincipalMap           []byte
	Bundle                       SimpleBundle
	ServiceArtifacts             []Artifact
	QueuedWrapper                Artifact
	ControllerMap                Artifact
	AuthorizedKeyLine            string
	KnownHostLine                string
	DispatcherFingerprint        string
	DispatcherOpenSSHFingerprint string
	SSHHostFingerprint           string
	InputSHA256                  string
}

// PRDReplacementBootstrapFile is one fixed owner-only file needed to prepare
// the new Linux PRD namespace. The selected bundle itself remains in the
// immutable release catalog and is not copied or activated here.
type PRDReplacementBootstrapFile struct {
	InstallPath string
	Mode        uint32
	Contents    []byte
}

// BuildPRDReplacementBootstrapPlan validates a fresh replacement namespace,
// a sealed Linux-host bundle, the reviewed existing-worker policy template,
// and exact TLS/SSH public identities. It performs no writes and does not
// contact a service.
func BuildPRDReplacementBootstrapPlan(input PRDReplacementBootstrapInput) (PRDReplacementBootstrapPlan, error) {
	var result PRDReplacementBootstrapPlan
	if !validCatalogOperationID(input.OperationID) ||
		(input.TemplateHostID != "sandbox-host" && input.TemplateHostID != "factory-sandbox") ||
		!bundleDigestPattern.MatchString(input.ExpectedReleaseSHA256) || input.Now.IsZero() {
		return result, ErrCatalogOperationInvalid
	}
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		return result, ErrInvalidPRDUpgradePlan
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(input.Absence, "linux-host", config.ReleaseLanePRD,
		input.Absence.SelectedLane, plan.Namespace); err != nil || input.Absence.State != "absent" {
		return result, errors.Join(ErrCatalogOperationInvalid, err)
	}
	observedAt, err := time.Parse(time.RFC3339Nano, input.Absence.ObservedAtUTC)
	if err != nil || input.Now.Before(observedAt) || input.Now.Sub(observedAt) > prdReplacementBootstrapEvidenceMaxAge {
		return result, ErrCatalogOperationInvalid
	}
	template, err := config.ParseBytes(input.TemplateConfiguration)
	if err != nil || template.Kind() != config.HostKindLinux || template.ReleaseLane() != config.ReleaseLanePRD {
		return result, config.ErrInvalidLaneProfiles
	}
	templateHost, err := config.PRDReplacementLinuxTemplateHost(input.TemplateConfiguration)
	if err != nil || templateHost != input.TemplateHostID {
		return result, config.ErrInvalidLaneProfiles
	}
	configuration, err := config.BuildFreshPRDReplacementLinuxConfig(input.TemplateConfiguration)
	if err != nil {
		return result, err
	}
	loaded, err := config.ParseBytes(configuration)
	if err != nil || loaded.Kind() != config.HostKindLinux || loaded.ReleaseLane() != config.ReleaseLanePRD {
		return result, config.ErrInvalidLaneProfiles
	}
	linux, ok := loaded.LinuxSettings()
	if !ok || linux.DirectHTTPSBind != config.PRDReplacementLinuxPrivateIP+":8443" ||
		linux.DirectPublicEndpoint != "https://"+config.SimpleReplacementLinuxPublicIP+":8443" {
		return result, config.ErrInvalidLaneProfiles
	}
	releaseManifestBytes, err := CatalogReleaseManifestBytes(input.ReleaseManifest)
	if err != nil {
		return result, ErrInvalidCatalogRelease
	}
	releaseDigest := sha256.Sum256(releaseManifestBytes)
	releaseSHA256 := hex.EncodeToString(releaseDigest[:])
	releaseHost, found := catalogHostEntry(input.ReleaseManifest, "linux-host")
	if !found || input.ReleaseManifest.ReleaseID == "" || releaseSHA256 != input.ExpectedReleaseSHA256 ||
		releaseHost.BundleSHA256 != input.Bundle.Manifest.BundleSHA256 ||
		input.ReleaseManifest.SourceRevision != input.Bundle.Manifest.SourceRevision || ValidateSimpleBundle(input.Bundle) != nil ||
		input.Bundle.Manifest.HostID != "linux-host" || input.Bundle.Manifest.GOOS != "linux" || input.Bundle.Manifest.GOARCH != "amd64" {
		return result, ErrInvalidCatalogRelease
	}
	releaseIdentity := CatalogReleaseIdentity{ReleaseID: input.ReleaseManifest.ReleaseID, ReleaseSHA256: releaseSHA256,
		HostID: "linux-host", BundleSHA256: releaseHost.BundleSHA256}
	if err := ValidatePRDReplacementServerMaterial(input.ServerCertificatePEM, input.ServerPrivateKeyPEM, input.ServerCAPEM, input.Now); err != nil {
		return result, err
	}
	if err := validatePRDReplacementServerCSRKeyPair(input.ServerCSRPEM, input.ServerPrivateKeyPEM); err != nil {
		return result, err
	}
	macIdentity, err := config.InspectPRDReplacementMacLegacyIdentity(input.MacConfiguration)
	if err != nil {
		return result, config.ErrInvalidLaneProfiles
	}
	if err := validatePRDReplacementLegacyClientMaterial(input.ClientCertificatePEM, input.ClientCAPEM, input.ClientPrincipalMap, input.Now); err != nil {
		return result, ErrInvalidLaneTLSMaterial
	}
	macCandidate, err := config.BuildFreshPRDReplacementMacRoute(input.MacConfiguration)
	if err != nil || config.ValidateFreshPRDReplacementLinuxRoute(input.MacConfiguration, macCandidate, configuration) != nil {
		return result, config.ErrInvalidLaneProfiles
	}
	dispatcher, err := parseEd25519PublicKey(input.DispatcherPublicKey)
	if err != nil {
		return result, fmt.Errorf("invalid replacement dispatcher public key: %w", err)
	}
	hostKey, err := parseEd25519PublicKey(input.SSHHostPublicKey)
	if err != nil {
		return result, fmt.Errorf("invalid replacement SSH host public key: %w", err)
	}
	queued, err := buildPRDReplacementQueuedSSHArtifacts(dispatcher, hostKey)
	if err != nil {
		return result, err
	}
	serviceArtifacts, err := BuildPRDCatalogServiceArtifacts("linux-host")
	if err != nil {
		return result, err
	}
	templateDigest := digestBytes(input.TemplateConfiguration)
	macConfigDigest := digestBytes(input.MacConfiguration)
	configurationDigest := digestBytes(configuration)
	macCandidateDigest := digestBytes(macCandidate)
	absenceDigest := input.Absence.EvidenceSHA256
	inputDigest, err := prdReplacementBootstrapInputDigest(input, configuration)
	if err != nil {
		return result, err
	}
	return PRDReplacementBootstrapPlan{
		OperationID: input.OperationID, TemplateHostID: input.TemplateHostID,
		TemplateConfigSHA256: templateDigest, MacConfigSHA256: macConfigDigest, AbsenceEvidenceSHA256: absenceDigest,
		Release: releaseIdentity, SourceRevision: input.ReleaseManifest.SourceRevision,
		Namespace: plan.Namespace, ReleaseCatalogRoot: filepath.Join("/home", config.LinuxAccount, "projects-runtime", "runner-releases"),
		ReleaseBundleRoot: filepath.Join("/home", config.LinuxAccount, "projects-runtime", "runner-releases", input.ReleaseManifest.ReleaseID, "linux-host"),
		RunnerUnit:        plan.Namespace.RunnerUnit, Absence: input.Absence,
		Configuration: configuration, ConfigurationSHA256: configurationDigest,
		MacConfiguration: macCandidate, MacCandidateSHA256: macCandidateDigest,
		MacServerCAPath: macIdentity.ServerCAPath, MacClientCertificatePath: macIdentity.ClientCertificatePath,
		MacClientPrivateKeyPath: macIdentity.ClientPrivateKeyPath, MacDispatcherKeyPath: macIdentity.DispatcherKeyPath,
		MacKnownHostsPath: macIdentity.KnownHostsPath, LinuxClientCAPath: linux.ClientCA,
		LinuxServerPrivateKeyPath: filepath.Join(plan.Namespace.ServiceRoot, "secrets", "server.key"),
		LinuxPrincipalMapPath:     linux.ClientPrincipalMap, DirectClientURISAN: prdReplacementLegacyDirectURI,
		ServerCertificate: append([]byte(nil), input.ServerCertificatePEM...),
		ServerPrivateKey:  append([]byte(nil), input.ServerPrivateKeyPEM...), ServerCSR256: digestBytes(input.ServerCSRPEM),
		ServerCA: append([]byte(nil), input.ServerCAPEM...), ClientCA: append([]byte(nil), input.ClientCAPEM...),
		ClientPrincipalMap: append([]byte(nil), input.ClientPrincipalMap...), Bundle: input.Bundle,
		ServiceArtifacts: serviceArtifacts, QueuedWrapper: queued.wrapper, ControllerMap: queued.controllerMap,
		AuthorizedKeyLine: queued.authorizedKeyLine, KnownHostLine: queued.knownHostLine,
		DispatcherFingerprint: queued.dispatcherFingerprint, DispatcherOpenSSHFingerprint: queued.dispatcherOpenSSHFingerprint,
		SSHHostFingerprint: queued.hostFingerprint, InputSHA256: inputDigest,
	}, nil
}

// PRDReplacementBootstrapFiles returns the only owner-managed configuration
// and trust files the fresh install may create before catalog selection. The
// executable entrypoint and forced-command wrapper are installed by the
// selector transaction after its durable prepared journal; the unit, selector,
// database, and authorized_keys each have separate guarded procedures.
func PRDReplacementBootstrapFiles(plan PRDReplacementBootstrapPlan) ([]PRDReplacementBootstrapFile, error) {
	expected, err := BuildPRDUpgradePlan("linux-host")
	if err != nil || plan.OperationID == "" || plan.Release.HostID != "linux-host" ||
		plan.Namespace != expected.Namespace || plan.ReleaseCatalogRoot != filepath.Join("/home", config.LinuxAccount, "projects-runtime", "runner-releases") ||
		plan.ReleaseBundleRoot != filepath.Join(plan.ReleaseCatalogRoot, plan.Release.ReleaseID, "linux-host") ||
		plan.RunnerUnit != expected.Namespace.RunnerUnit || plan.LinuxServerPrivateKeyPath != filepath.Join(expected.Namespace.ServiceRoot, "secrets", "server.key") ||
		plan.Absence.EvidenceSHA256 != plan.AbsenceEvidenceSHA256 || !bundleDigestPattern.MatchString(plan.AbsenceEvidenceSHA256) ||
		!bundleDigestPattern.MatchString(plan.InputSHA256) || !bundleDigestPattern.MatchString(plan.ConfigurationSHA256) ||
		digestBytes(plan.Configuration) != plan.ConfigurationSHA256 || ValidateSimpleBundle(plan.Bundle) != nil ||
		plan.Bundle.Manifest.BundleSHA256 != plan.Release.BundleSHA256 || plan.Bundle.Manifest.HostID != "linux-host" {
		return nil, ErrInvalidCatalogRelease
	}
	if err := validateCatalogNamespaceAbsenceReportForNamespace(plan.Absence, "linux-host", config.ReleaseLanePRD,
		plan.Absence.SelectedLane, expected.Namespace); err != nil || plan.Absence.State != "absent" {
		return nil, errors.Join(ErrInvalidCatalogRelease, err)
	}
	if plan.ControllerMap.InstallPath != filepath.Join(expected.Namespace.ServiceRoot, "config", "ssh-controller-map.yaml") ||
		plan.ControllerMap.Mode != 0o600 || plan.ControllerMap.Contents == "" ||
		plan.QueuedWrapper.InstallPath != filepath.Join(expected.Namespace.Binaries, "runner-ssh-bridge-forced.sh") ||
		plan.QueuedWrapper.Mode != 0o700 || plan.QueuedWrapper.Contents == "" {
		return nil, ErrInvalidCatalogRelease
	}
	files := []PRDReplacementBootstrapFile{
		{InstallPath: expected.Namespace.ConfigFile, Mode: 0o600, Contents: append([]byte(nil), plan.Configuration...)},
		{InstallPath: filepath.Join(expected.Namespace.ServiceRoot, "secrets", "server.pem"), Mode: 0o600, Contents: append([]byte(nil), plan.ServerCertificate...)},
		{InstallPath: plan.LinuxServerPrivateKeyPath, Mode: 0o600, Contents: append([]byte(nil), plan.ServerPrivateKey...)},
		{InstallPath: plan.LinuxClientCAPath, Mode: 0o600, Contents: append([]byte(nil), plan.ClientCA...)},
		{InstallPath: plan.LinuxPrincipalMapPath, Mode: 0o600, Contents: append([]byte(nil), plan.ClientPrincipalMap...)},
		{InstallPath: plan.ControllerMap.InstallPath, Mode: plan.ControllerMap.Mode, Contents: []byte(plan.ControllerMap.Contents)},
	}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !pathContained(expected.Namespace.ServiceRoot, file.InstallPath) || filepath.Clean(file.InstallPath) != file.InstallPath ||
			file.Mode != 0o600 && file.Mode != 0o700 || len(file.Contents) == 0 {
			return nil, ErrInvalidCatalogRelease
		}
		if _, ok := seen[file.InstallPath]; ok {
			return nil, ErrInvalidCatalogRelease
		}
		seen[file.InstallPath] = struct{}{}
	}
	return files, nil
}

const prdReplacementLegacyDirectURI = "urn:remote-session-runner:controller:runner-tomasz-direct"

type prdReplacementQueuedArtifacts struct {
	wrapper                      Artifact
	controllerMap                Artifact
	authorizedKeyLine            string
	knownHostLine                string
	dispatcherFingerprint        string
	dispatcherOpenSSHFingerprint string
	hostFingerprint              string
}

func buildPRDReplacementQueuedSSHArtifacts(dispatcher, hostKey parsedSSHKey) (prdReplacementQueuedArtifacts, error) {
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil || dispatcher.keyType != "ssh-ed25519" || hostKey.keyType != "ssh-ed25519" {
		return prdReplacementQueuedArtifacts{}, ErrInvalidSimpleBundle
	}
	namespace := plan.Namespace
	wrapperPath := filepath.Join(namespace.Binaries, "runner-ssh-bridge-forced.sh")
	controllerMapPath := filepath.Join(namespace.ServiceRoot, "config", "ssh-controller-map.yaml")
	entry := sshdeploy.Entry{PublicKey: dispatcher.keyType + " " + dispatcher.keyData,
		WrapperPath: wrapperPath, Comment: "runner-dispatcher-linux-host-prd"}
	keyLine, err := entry.Render()
	if err != nil {
		return prdReplacementQueuedArtifacts{}, err
	}
	openSSH := opensshFingerprint(dispatcher.blob)
	dispatcherFingerprint := lowerHexFingerprint(dispatcher.blob)
	controllerMap := fmt.Sprintf("version: 1\nkeys:\n  %q:\n    controller_type: %s\n    controller_id: %q\n",
		openSSH, domain.ControllerTypeQueuedMac, config.MacAccount)
	knownHost := fmt.Sprintf("%s %s %s\n", config.SimpleReplacementLinuxPublicIP, hostKey.keyType, hostKey.keyData)
	return prdReplacementQueuedArtifacts{
		wrapper: Artifact{Kind: ArtifactSSHWrapper, InstallPath: wrapperPath, Mode: 0o700, Owner: config.LinuxAccount,
			Group: config.LinuxAccount, Contents: renderPRDCatalogQueuedSSHWrapper(namespace, "linux-host")},
		controllerMap: Artifact{Kind: ArtifactSSHControllerMap, InstallPath: controllerMapPath, Mode: 0o600,
			Owner: config.LinuxAccount, Group: config.LinuxAccount, Contents: controllerMap},
		authorizedKeyLine: keyLine + "\n", knownHostLine: knownHost,
		dispatcherFingerprint: dispatcherFingerprint, dispatcherOpenSSHFingerprint: openSSH,
		hostFingerprint: lowerHexFingerprint(hostKey.blob),
	}, nil
}

func validatePRDReplacementLegacyClientMaterial(certificatePEM, caPEM, principalMap []byte, now time.Time) error {
	if len(certificatePEM) == 0 || len(certificatePEM) > 1<<20 || len(caPEM) == 0 || len(caPEM) > 1<<20 || now.IsZero() {
		return ErrInvalidLaneTLSMaterial
	}
	var leaf *x509.Certificate
	var intermediates = x509.NewCertPool()
	rest := certificatePEM
	for index := 0; len(bytesTrimSpace(rest)) > 0; index++ {
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || index > 8 {
			return ErrInvalidLaneTLSMaterial
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return ErrInvalidLaneTLSMaterial
		}
		if index == 0 {
			leaf = certificate
		} else {
			intermediates.AddCert(certificate)
		}
		rest = remaining
	}
	if leaf == nil || leaf.IsCA || len(leaf.IPAddresses) != 0 || len(leaf.DNSNames) != 0 ||
		!exactCertificateURIs(leaf, prdReplacementLegacyDirectURI) || !validCertificateAtNow(leaf, x509.ExtKeyUsageClientAuth) {
		return ErrInvalidLaneTLSMaterial
	}
	roots, _, err := parseLaneCertificates(caPEM)
	if err != nil {
		return ErrInvalidLaneTLSMaterial
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates,
		CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return ErrInvalidLaneTLSMaterial
	}
	if len(principalMap) == 0 || len(principalMap) > 64*1024 || bytes.Contains(principalMap, []byte("\x00")) {
		return ErrInvalidLaneTLSMaterial
	}
	decoder := yaml.NewDecoder(bytes.NewReader(principalMap))
	decoder.KnownFields(true)
	var document directPrincipalMap
	if err := decoder.Decode(&document); err != nil || document.Version != 1 || len(document.Principals) != 1 {
		return ErrInvalidLaneTLSMaterial
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidLaneTLSMaterial
	}
	entry := document.Principals[0]
	if entry.URISAN != prdReplacementLegacyDirectURI || entry.ControllerType != "direct_mtls" || entry.ControllerID != config.MacAccount {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

func prdReplacementBootstrapInputDigest(input PRDReplacementBootstrapInput, configuration []byte) (string, error) {
	manifestBytes, err := CatalogReleaseManifestBytes(input.ReleaseManifest)
	if err != nil {
		return "", ErrInvalidCatalogRelease
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	releaseHost, found := catalogHostEntry(input.ReleaseManifest, "linux-host")
	if !found {
		return "", ErrInvalidCatalogRelease
	}
	releaseIdentity := CatalogReleaseIdentity{ReleaseID: input.ReleaseManifest.ReleaseID,
		ReleaseSHA256: hex.EncodeToString(manifestDigest[:]), HostID: "linux-host", BundleSHA256: releaseHost.BundleSHA256}
	identity := struct {
		OperationID               string                 `json:"operation_id"`
		TemplateHost              string                 `json:"template_host"`
		TemplateConfigSHA256      string                 `json:"template_config_sha256"`
		MacConfigSHA256           string                 `json:"mac_config_sha256"`
		AbsenceSHA256             string                 `json:"absence_evidence_sha256"`
		Release                   CatalogReleaseIdentity `json:"release"`
		ConfigurationSHA256       string                 `json:"configuration_sha256"`
		ServerCertificateSHA256   string                 `json:"server_certificate_sha256"`
		ServerPrivateKeySHA256    string                 `json:"server_private_key_sha256"`
		ServerCSRSHA256           string                 `json:"server_csr_sha256"`
		ServerCASHA256            string                 `json:"server_ca_sha256"`
		ClientCertificateSHA256   string                 `json:"client_certificate_sha256"`
		ClientCASHA256            string                 `json:"client_ca_sha256"`
		PrincipalMapSHA256        string                 `json:"principal_map_sha256"`
		DispatcherPublicKeySHA256 string                 `json:"dispatcher_public_key_sha256"`
		SSHHostPublicKeySHA256    string                 `json:"ssh_host_public_key_sha256"`
		BundleSHA256              string                 `json:"bundle_sha256"`
	}{
		input.OperationID, input.TemplateHostID, digestBytes(input.TemplateConfiguration), digestBytes(input.MacConfiguration),
		input.Absence.EvidenceSHA256, releaseIdentity, digestBytes(configuration),
		digestBytes(input.ServerCertificatePEM), digestBytes(input.ServerPrivateKeyPEM), digestBytes(input.ServerCSRPEM),
		digestBytes(input.ServerCAPEM), digestBytes(input.ClientCertificatePEM),
		digestBytes(input.ClientCAPEM), digestBytes(input.ClientPrincipalMap),
		digestBytes([]byte(input.DispatcherPublicKey)),
		digestBytes([]byte(input.SSHHostPublicKey)), input.Bundle.Manifest.BundleSHA256,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
