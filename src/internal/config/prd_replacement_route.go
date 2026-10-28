package config

import (
	"fmt"
	"reflect"
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
