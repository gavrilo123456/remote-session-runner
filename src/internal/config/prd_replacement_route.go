package config

import (
	"fmt"
	"path/filepath"
	"reflect"

	"gopkg.in/yaml.v3"
)

const (
	prdReplacementLinuxOldPublicIP  = "129.151.232.40"
	prdReplacementLinuxOldPrivateIP = "10.0.0.200"
)

// ValidatePRDReplacementLinuxRoute validates a V2 PRD route transition for
// the existing linux-host identity. The only permitted configuration edits
// are the paired direct endpoint and queued SSH address on Mac, and the
// corresponding direct endpoint and bind address on Linux. TLS trust paths,
// SSH key paths, service/database paths, policies, mailbox roots and all
// other configuration values must remain identical. Certificate and host-key
// material still require their own fingerprint and trust checks.
func ValidatePRDReplacementLinuxRoute(currentMac, candidateMac, currentLinux, candidateLinux []byte) error {
	if !isV2HostConfiguration(currentMac, HostKindMac) || !isV2HostConfiguration(candidateMac, HostKindMac) ||
		!isV2HostConfiguration(currentLinux, HostKindLinux) || !isV2HostConfiguration(candidateLinux, HostKindLinux) {
		return fmt.Errorf("%w: expected valid V2 Mac and Linux documents", ErrInvalidLaneProfiles)
	}
	macBefore, err := decodeFileDocument(currentMac)
	if err != nil || macBefore.RemoteHosts == nil {
		return fmt.Errorf("%w: invalid current Mac route document", ErrInvalidLaneProfiles)
	}
	macAfter, err := decodeFileDocument(candidateMac)
	if err != nil || macAfter.RemoteHosts == nil {
		return fmt.Errorf("%w: invalid candidate Mac route document", ErrInvalidLaneProfiles)
	}
	linuxBefore, err := decodeFileDocument(currentLinux)
	if err != nil || linuxBefore.Linux == nil {
		return fmt.Errorf("%w: invalid current Linux route document", ErrInvalidLaneProfiles)
	}
	linuxAfter, err := decodeFileDocument(candidateLinux)
	if err != nil || linuxAfter.Linux == nil {
		return fmt.Errorf("%w: invalid candidate Linux route document", ErrInvalidLaneProfiles)
	}

	oldURL := "https://" + prdReplacementLinuxOldPublicIP + ":8443"
	newURL := "https://" + SimpleReplacementLinuxPublicIP + ":8443"
	oldMacRoute, oldOK := macBefore.RemoteHosts["linux-host"]
	newMacRoute, newOK := macAfter.RemoteHosts["linux-host"]
	if !oldOK || !newOK || oldMacRoute.Account != LinuxAccount || oldMacRoute.QueuedBridge == nil ||
		oldMacRoute.DirectEndpoint == nil || newMacRoute.QueuedBridge == nil || newMacRoute.DirectEndpoint == nil ||
		oldMacRoute.QueuedBridge.Host != prdReplacementLinuxOldPublicIP || oldMacRoute.QueuedBridge.Port != 22 ||
		oldMacRoute.DirectEndpoint.URL != oldURL || newMacRoute.QueuedBridge.Host != SimpleReplacementLinuxPublicIP ||
		newMacRoute.QueuedBridge.Port != 22 || newMacRoute.DirectEndpoint.URL != newURL {
		return fmt.Errorf("%w: Mac route does not match the approved address transition", ErrInvalidLaneProfiles)
	}

	oldBind := prdReplacementLinuxOldPrivateIP + ":8443"
	newBind := PRDReplacementLinuxPrivateIP + ":8443"
	if linuxBefore.Linux.Account != LinuxAccount || linuxBefore.Linux.RemoteTargetProfile != "linux-host" ||
		linuxBefore.Linux.DirectHTTPSBind != oldBind || linuxBefore.Linux.DirectPublicEndpoint != oldURL ||
		linuxAfter.Linux.DirectHTTPSBind != newBind || linuxAfter.Linux.DirectPublicEndpoint != newURL {
		return fmt.Errorf("%w: Linux route does not match the approved address transition", ErrInvalidLaneProfiles)
	}

	// Only the address fields named above may differ. Comparing decoded full
	// documents preserves mailbox identities, credentials, routes and policy.
	macExpected := macBefore
	macExpected.RemoteHosts = cloneRemoteHostDocuments(macBefore.RemoteHosts)
	macExpected.RemoteHosts["linux-host"].QueuedBridge.Host = SimpleReplacementLinuxPublicIP
	macExpected.RemoteHosts["linux-host"].DirectEndpoint.URL = newURL
	if !reflect.DeepEqual(macExpected, macAfter) {
		return fmt.Errorf("%w: unrelated Mac configuration field changed", ErrInvalidLaneProfiles)
	}
	linuxExpected := linuxBefore
	linuxCopy := *linuxBefore.Linux
	linuxExpected.Linux = &linuxCopy
	linuxExpected.Linux.DirectHTTPSBind = newBind
	linuxExpected.Linux.DirectPublicEndpoint = newURL
	if !reflect.DeepEqual(linuxExpected, linuxAfter) {
		return fmt.Errorf("%w: unrelated Linux configuration field changed", ErrInvalidLaneProfiles)
	}
	return nil
}

// ValidateFreshPRDReplacementLinuxRoute validates the narrow Mac PRD route
// change used when linux-host is a genuinely fresh replacement. It deliberately
// does not require or infer any configuration from the retired worker. The Mac
// document may change only the paired linux-host endpoint and queued SSH host;
// the fresh Linux document must use the canonical replacement namespace and
// addresses. Certificate and SSH key material are verified by the installing
// command, not inferred from configuration text.
func ValidateFreshPRDReplacementLinuxRoute(currentMac, candidateMac, candidateLinux []byte) error {
	if !isV2HostConfiguration(currentMac, HostKindMac) || !isV2HostConfiguration(candidateMac, HostKindMac) ||
		!isV2HostConfiguration(candidateLinux, HostKindLinux) {
		return fmt.Errorf("%w: expected V2 PRD Mac and fresh Linux documents", ErrInvalidLaneProfiles)
	}
	macBefore, err := decodeFileDocument(currentMac)
	if err != nil || macBefore.RemoteHosts == nil {
		return fmt.Errorf("%w: invalid current Mac route document", ErrInvalidLaneProfiles)
	}
	macAfter, err := decodeFileDocument(candidateMac)
	if err != nil || macAfter.RemoteHosts == nil {
		return fmt.Errorf("%w: invalid candidate Mac route document", ErrInvalidLaneProfiles)
	}
	linuxAfter, err := parse(candidateLinux)
	if err != nil || linuxAfter.SchemaVersion() != VersionV2 || linuxAfter.Kind() != HostKindLinux ||
		linuxAfter.ReleaseLane() != ReleaseLanePRD {
		return fmt.Errorf("%w: invalid fresh replacement Linux configuration", ErrInvalidLaneProfiles)
	}
	linuxDocument, err := decodeFileDocument(candidateLinux)
	if err != nil || linuxDocument.Linux == nil {
		return fmt.Errorf("%w: invalid fresh replacement Linux document", ErrInvalidLaneProfiles)
	}
	linux, ok := linuxAfter.LinuxSettings()
	if !ok || linux.Account != LinuxAccount || linux.ServiceRoot != LinuxServiceRoot ||
		linux.Database != filepath.Join(LinuxServiceRoot, "state", "remote.db") ||
		linux.PrivateSocket != filepath.Join(LinuxServiceRoot, "run", "runnerd.sock") ||
		linux.Workspaces != filepath.Join(LinuxServiceRoot, "workspaces") ||
		linux.ScriptTempRoot != filepath.Join(LinuxServiceRoot, "tmp", "scripts") ||
		linux.Backups != filepath.Join(LinuxServiceRoot, "backups") ||
		linux.DirectHTTPSBind != PRDReplacementLinuxPrivateIP+":8443" ||
		linux.DirectPublicEndpoint != "https://"+SimpleReplacementLinuxPublicIP+":8443" ||
		linux.ServerCertificate != filepath.Join(LinuxServiceRoot, "secrets", "server.pem") ||
		linux.ClientCA != filepath.Join(LinuxServiceRoot, "secrets", "client-ca.pem") ||
		linux.ClientPrincipalMap != filepath.Join(LinuxServiceRoot, "config", "client-principals.yaml") ||
		linux.TLSMinVersion != "1.3" || linux.RuntimeAdapter != "linux-host-process" ||
		linuxDocument.Linux.RemoteTargetProfile != "linux-host" {
		return fmt.Errorf("%w: fresh Linux configuration does not match the fixed replacement PRD namespace", ErrInvalidLaneProfiles)
	}

	oldURL := "https://" + prdReplacementLinuxOldPublicIP + ":8443"
	newURL := "https://" + SimpleReplacementLinuxPublicIP + ":8443"
	oldRoute, oldOK := macBefore.RemoteHosts["linux-host"]
	newRoute, newOK := macAfter.RemoteHosts["linux-host"]
	if !oldOK || !newOK || oldRoute.Account != LinuxAccount || oldRoute.QueuedBridge == nil ||
		oldRoute.DirectEndpoint == nil || newRoute.QueuedBridge == nil || newRoute.DirectEndpoint == nil ||
		oldRoute.QueuedBridge.Host != prdReplacementLinuxOldPublicIP || oldRoute.QueuedBridge.Port != 22 ||
		oldRoute.DirectEndpoint.URL != oldURL || newRoute.QueuedBridge.Host != SimpleReplacementLinuxPublicIP ||
		newRoute.QueuedBridge.Port != 22 || newRoute.DirectEndpoint.URL != newURL {
		return fmt.Errorf("%w: Mac route does not match the fresh replacement address transition", ErrInvalidLaneProfiles)
	}
	macExpected := macBefore
	macExpected.RemoteHosts = cloneRemoteHostDocuments(macBefore.RemoteHosts)
	macExpected.RemoteHosts["linux-host"].QueuedBridge.Host = SimpleReplacementLinuxPublicIP
	macExpected.RemoteHosts["linux-host"].DirectEndpoint.URL = newURL
	if !reflect.DeepEqual(macExpected, macAfter) {
		return fmt.Errorf("%w: unrelated Mac configuration field changed", ErrInvalidLaneProfiles)
	}
	return nil
}

// BuildFreshPRDReplacementMacRoute returns a V2 PRD Mac candidate that changes
// only the linux-host direct URL and queued SSH address to the verified
// replacement endpoint. It does not write configuration or known_hosts; the
// caller must still bind the returned bytes to a stopped, backed-up PRD route
// transition and independently verify the replacement SSH host key.
func BuildFreshPRDReplacementMacRoute(currentMac []byte) ([]byte, error) {
	if !isV2HostConfiguration(currentMac, HostKindMac) {
		return nil, fmt.Errorf("%w: expected a valid V2 Mac PRD document", ErrInvalidLaneProfiles)
	}
	before, err := decodeFileDocument(currentMac)
	if err != nil || before.RemoteHosts == nil {
		return nil, fmt.Errorf("%w: invalid current Mac route document", ErrInvalidLaneProfiles)
	}
	oldURL := "https://" + prdReplacementLinuxOldPublicIP + ":8443"
	route, ok := before.RemoteHosts["linux-host"]
	if !ok || route.Account != LinuxAccount || route.QueuedBridge == nil || route.DirectEndpoint == nil ||
		route.QueuedBridge.Host != prdReplacementLinuxOldPublicIP || route.QueuedBridge.Port != 22 ||
		route.DirectEndpoint.URL != oldURL || route.QueuedBridge.KnownHosts == "" || route.QueuedBridge.PrivateKey == "" ||
		route.DirectEndpoint.ServerCA == "" || route.DirectEndpoint.ClientCertificate == "" ||
		route.DirectEndpoint.ClientPrivateKey == "" {
		return nil, fmt.Errorf("%w: current Mac PRD route is not the exact retired linux-host route", ErrInvalidLaneProfiles)
	}

	after := before
	after.RemoteHosts = cloneRemoteHostDocuments(before.RemoteHosts)
	replacement := after.RemoteHosts["linux-host"]
	replacement.QueuedBridge.Host = SimpleReplacementLinuxPublicIP
	replacement.DirectEndpoint.URL = "https://" + SimpleReplacementLinuxPublicIP + ":8443"
	after.RemoteHosts["linux-host"] = replacement

	// Edit only the two approved YAML scalar nodes. Re-encoding the full typed
	// document would materialize omitted V2 defaults as zero-value structs and
	// could turn an otherwise valid legacy file into an invalid one.
	var document yaml.Node
	if err := yaml.Unmarshal(currentMac, &document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, fmt.Errorf("%w: decode current Mac route syntax tree", ErrInvalidLaneProfiles)
	}
	root := document.Content[0]
	remoteHosts, ok := yamlMappingValue(root, "remote_hosts")
	if !ok {
		return nil, fmt.Errorf("%w: current Mac route lacks remote_hosts", ErrInvalidLaneProfiles)
	}
	linuxHost, ok := yamlMappingValue(remoteHosts, "linux-host")
	if !ok {
		return nil, fmt.Errorf("%w: current Mac route lacks linux-host", ErrInvalidLaneProfiles)
	}
	queuedBridge, ok := yamlMappingValue(linuxHost, "queued_bridge")
	if !ok {
		return nil, fmt.Errorf("%w: current Mac route lacks queued_bridge", ErrInvalidLaneProfiles)
	}
	queuedHost, ok := yamlMappingValue(queuedBridge, "host")
	if !ok || queuedHost.Kind != yaml.ScalarNode || queuedHost.Tag != "!!str" {
		return nil, fmt.Errorf("%w: current Mac route has invalid queued host scalar", ErrInvalidLaneProfiles)
	}
	directEndpoint, ok := yamlMappingValue(linuxHost, "direct_endpoint")
	if !ok {
		return nil, fmt.Errorf("%w: current Mac route lacks direct_endpoint", ErrInvalidLaneProfiles)
	}
	directURL, ok := yamlMappingValue(directEndpoint, "url")
	if !ok || directURL.Kind != yaml.ScalarNode || directURL.Tag != "!!str" {
		return nil, fmt.Errorf("%w: current Mac route has invalid direct URL scalar", ErrInvalidLaneProfiles)
	}
	queuedHost.Value = SimpleReplacementLinuxPublicIP
	directURL.Value = "https://" + SimpleReplacementLinuxPublicIP + ":8443"
	candidate, err := yaml.Marshal(&document)
	if err != nil {
		return nil, fmt.Errorf("%w: encode replacement Mac route candidate", ErrInvalidLaneProfiles)
	}
	parsed, err := parse(candidate)
	if err != nil {
		return nil, fmt.Errorf("%w: parse serialized Mac route candidate: %v", ErrInvalidLaneProfiles, err)
	}
	if parsed.SchemaVersion() != VersionV2 || parsed.Kind() != HostKindMac || parsed.ReleaseLane() != ReleaseLanePRD {
		return nil, fmt.Errorf("%w: serialized Mac route candidate is not a valid V2 PRD document", ErrInvalidLaneProfiles)
	}
	decoded, err := decodeFileDocument(candidate)
	if err != nil {
		return nil, fmt.Errorf("%w: decode serialized Mac route candidate: %v", ErrInvalidLaneProfiles, err)
	}
	if !reflect.DeepEqual(after, decoded) {
		return nil, fmt.Errorf("%w: serialized Mac route candidate changed unrelated configuration", ErrInvalidLaneProfiles)
	}
	return candidate, nil
}

func yamlMappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Kind == yaml.ScalarNode && mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}

func isV2HostConfiguration(data []byte, host HostKind) bool {
	parsed, err := parse(data)
	return err == nil && parsed.SchemaVersion() == VersionV2 && parsed.Kind() == host &&
		parsed.ReleaseLane() == ReleaseLanePRD
}

func cloneRemoteHostDocuments(source map[string]remoteHostDocument) map[string]remoteHostDocument {
	cloned := make(map[string]remoteHostDocument, len(source))
	for name, host := range source {
		copyHost := host
		if host.QueuedBridge != nil {
			bridge := *host.QueuedBridge
			copyHost.QueuedBridge = &bridge
		}
		if host.DirectEndpoint != nil {
			endpoint := *host.DirectEndpoint
			copyHost.DirectEndpoint = &endpoint
		}
		cloned[name] = copyHost
	}
	return cloned
}
