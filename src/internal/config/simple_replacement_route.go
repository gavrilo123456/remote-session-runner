package config

import (
	"reflect"
	"strconv"
)

// SimpleReplacementLinuxPublicIP is the approved replacement peer address
// used by the bounded Dev/QAS migration path.
const SimpleReplacementLinuxPublicIP = "79.72.94.59"

// ValidateSimpleMacReplacementLinuxRoute permits one narrowly scoped
// configuration transition: the Mac's existing Dev or QAS linux-host V3
// route may move from the retired address to the verified replacement. All
// policy, mailbox, filesystem, execution-context and peer-route fields must
// remain byte-semantically unchanged. The caller still verifies the new host
// key and TLS certificate against the selected route before use.
func ValidateSimpleMacReplacementLinuxRoute(current, candidate []byte, lane ReleaseLane) error {
	if lane != ReleaseLaneDev && lane != ReleaseLaneQAS {
		return ErrInvalidLaneProfiles
	}
	currentConfig, err := parse(current)
	if err != nil || currentConfig.Kind() != HostKindMac || currentConfig.ReleaseLane() != lane || currentConfig.SchemaVersion() != VersionV3 {
		return ErrInvalidLaneProfiles
	}
	candidateConfig, err := parse(candidate)
	if err != nil || candidateConfig.Kind() != HostKindMac || candidateConfig.ReleaseLane() != lane || candidateConfig.SchemaVersion() != VersionV3 {
		return ErrInvalidLaneProfiles
	}
	currentDocument, err := decodeFileDocument(current)
	if err != nil || currentDocument.LaneProfiles == nil || currentDocument.LaneProfiles.Host != HostKindMac {
		return ErrInvalidLaneProfiles
	}
	candidateDocument, err := decodeFileDocument(candidate)
	if err != nil || candidateDocument.LaneProfiles == nil || candidateDocument.LaneProfiles.Host != HostKindMac {
		return ErrInvalidLaneProfiles
	}

	namespace, err := DeriveSimpleLaneNamespace(HostKindLinux, lane, "linux-host")
	if err != nil {
		return ErrInvalidLaneProfiles
	}
	oldURL := "https://129.151.232.40:" + strconv.Itoa(namespace.DirectPort)
	newURL := "https://" + SimpleReplacementLinuxPublicIP + ":" + strconv.Itoa(namespace.DirectPort)
	oldIndex := macProfileIndex(currentDocument.LaneProfiles.MacProfiles, "linux-host")
	newIndex := macProfileIndex(candidateDocument.LaneProfiles.MacProfiles, "linux-host")
	if oldIndex < 0 || newIndex != oldIndex {
		return ErrInvalidLaneProfiles
	}
	oldProfile := currentDocument.LaneProfiles.MacProfiles[oldIndex]
	newProfile := candidateDocument.LaneProfiles.MacProfiles[newIndex]
	if oldProfile.Queued.Host != "129.151.232.40" || oldProfile.Queued.Port != 22 || oldProfile.Direct.URL != oldURL ||
		oldProfile.Direct.Name != namespace.DirectEndpointName || oldProfile.Queued.HostKeyFingerprint == "" ||
		newProfile.Queued.Host != SimpleReplacementLinuxPublicIP || newProfile.Queued.Port != 22 || newProfile.Direct.URL != newURL ||
		newProfile.Direct.Name != namespace.DirectEndpointName || newProfile.Direct.ExpectedServerCertificateFingerprint == oldProfile.Direct.ExpectedServerCertificateFingerprint ||
		newProfile.Queued.HostKeyFingerprint == oldProfile.Queued.HostKeyFingerprint {
		return ErrInvalidLaneProfiles
	}

	// Normalize only the fields whose identity necessarily changes when the
	// remote host is replaced. Deep equality then proves every unrelated
	// policy and path remains untouched.
	currentDocument.LaneProfiles = &laneProfilesDocument{
		Host: currentDocument.LaneProfiles.Host, RemoteProfile: currentDocument.LaneProfiles.RemoteProfile,
		MacProfiles: append([]V3MacRemoteProfile(nil), currentDocument.LaneProfiles.MacProfiles...),
	}
	currentDocument.LaneProfiles.MacProfiles[oldIndex].Queued.Host = newProfile.Queued.Host
	currentDocument.LaneProfiles.MacProfiles[oldIndex].Queued.HostKeyFingerprint = newProfile.Queued.HostKeyFingerprint
	currentDocument.LaneProfiles.MacProfiles[oldIndex].Direct.URL = newProfile.Direct.URL
	currentDocument.LaneProfiles.MacProfiles[oldIndex].Direct.ExpectedServerCertificateFingerprint = newProfile.Direct.ExpectedServerCertificateFingerprint
	if !reflect.DeepEqual(currentDocument, candidateDocument) {
		return ErrInvalidLaneProfiles
	}
	return nil
}

func macProfileIndex(profiles []V3MacRemoteProfile, name string) int {
	for index, profile := range profiles {
		if profile.Profile == name {
			return index
		}
	}
	return -1
}
