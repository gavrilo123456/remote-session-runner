package release

import (
	"slices"
	"sort"
	"time"
)

const (
	HostManagementTransportOverlap = "overlap"
	HostManagementTransportRetire  = "retire"
)

// ValidateHostManagementTransportTransition checks the signed transport
// contract change between two host-management manifest payloads. The overlap
// step must retain every prior CA/trust assignment and admit exactly one
// bounded old peer beside each new active peer. The retirement step removes
// every old peer and leaf only after the caller has independently proved old
// leaf denial at every endpoint.
func ValidateHostManagementTransportTransition(previousRaw, nextRaw []byte, phase string, now time.Time) error {
	if ValidatePayload("host_management_manifest", previousRaw) != nil || ValidatePayload("host_management_manifest", nextRaw) != nil || now.IsZero() {
		return ErrInvalidTransportContract
	}
	var previous, next hostManagementManifestPayload
	if decodeStrict(previousRaw, &previous) != nil || decodeStrict(nextRaw, &next) != nil ||
		validateTransportContractShape(previous.TransportContract) != nil {
		return ErrInvalidTransportContract
	}
	if phase == HostManagementTransportOverlap {
		if validateTransportContractAt(next.TransportContract, now.UTC()) != nil || !containsAllTransportCAs(next.TransportContract.CAInventory, previous.TransportContract.CAInventory) ||
			!containsAllTransportTrust(next.TransportContract.TrustInventory, previous.TransportContract.TrustInventory) {
			return ErrInvalidTransportContract
		}
		oldPeers := peersByHost(previous.TransportContract.ActivePeerAllowlist)
		newPeers := peersByHost(next.TransportContract.ActivePeerAllowlist)
		oldCAIDs := make(map[string]struct{}, len(previous.TransportContract.CAInventory))
		for _, ca := range previous.TransportContract.CAInventory {
			oldCAIDs[ca.CAID] = struct{}{}
		}
		newCAIDs := make(map[string]struct{})
		activeCAIDs := make(map[string]struct{})
		expectedTrust := append([]transportTrust(nil), previous.TransportContract.TrustInventory...)
		for _, ca := range next.TransportContract.CAInventory {
			if _, existed := oldCAIDs[ca.CAID]; !existed {
				newCAIDs[ca.CAID] = struct{}{}
			}
		}
		if len(newCAIDs) == 0 {
			return ErrInvalidTransportContract
		}
		var commonDeadline string
		for _, hostID := range canonicalHostIDs {
			prior := oldPeers[hostID]
			pair := newPeers[hostID]
			if len(prior) != 1 || prior[0].OverlapNotAfter != "" || len(pair) != 2 {
				return ErrInvalidTransportContract
			}
			var active, overlap *activePeer
			for i := range pair {
				if pair[i].OverlapNotAfter == "" {
					active = &pair[i]
				} else {
					overlap = &pair[i]
				}
			}
			activeLeafCA := transportLeafCA(next.TransportContract.LeafInventory, active)
			overlapLeafCA := transportLeafCA(next.TransportContract.LeafInventory, overlap)
			if active == nil || overlap == nil || !samePeerIdentity(prior[0], *overlap) || samePeerCertificate(prior[0], *active) ||
				!transportLeafPresent(next.TransportContract.LeafInventory, *active) || !transportLeafPresent(next.TransportContract.LeafInventory, *overlap) ||
				!containsCAID(newCAIDs, activeLeafCA) || !containsCAID(oldCAIDs, overlapLeafCA) ||
				!transportTrustPresent(next.TransportContract.TrustInventory, hostID, activeLeafCA) || !transportTrustPresent(next.TransportContract.TrustInventory, hostID, overlapLeafCA) {
				return ErrInvalidTransportContract
			}
			activeCAIDs[activeLeafCA] = struct{}{}
			expectedTrust = append(expectedTrust, transportTrust{HostID: hostID, CAID: activeLeafCA})
			if commonDeadline == "" {
				commonDeadline = overlap.OverlapNotAfter
			} else if commonDeadline != overlap.OverlapNotAfter {
				return ErrInvalidTransportContract
			}
		}
		if len(next.TransportContract.LeafInventory) != 2*len(canonicalHostIDs) || !sameTransportTrustInventory(expectedTrust, next.TransportContract.TrustInventory) {
			return ErrInvalidTransportContract
		}
		if !sameTransportCAInventory(next.TransportContract.CAInventory, append(append([]transportCA(nil), previous.TransportContract.CAInventory...), onlyNewTransportCAs(next.TransportContract.CAInventory, oldCAIDs)...)) {
			return ErrInvalidTransportContract
		}
		if !sameStringSet(newCAIDs, activeCAIDs) {
			return ErrInvalidTransportContract
		}
		return nil
	}
	if phase != HostManagementTransportRetire || validateTransportContractAt(next.TransportContract, now.UTC()) != nil {
		return ErrInvalidTransportContract
	}
	oldPeers := peersByHost(previous.TransportContract.ActivePeerAllowlist)
	newPeers := peersByHost(next.TransportContract.ActivePeerAllowlist)
	oldCAIDs := make(map[string]struct{})
	for _, ca := range previous.TransportContract.CAInventory {
		oldCAIDs[ca.CAID] = struct{}{}
	}
	retiredCAIDs := make(map[string]struct{})
	expectedCAs := make([]transportCA, 0, len(previous.TransportContract.CAInventory))
	for _, ca := range previous.TransportContract.CAInventory {
		if _, found := oldCAIDs[ca.CAID]; found {
			isCurrent := false
			for _, peer := range previous.TransportContract.ActivePeerAllowlist {
				if peer.OverlapNotAfter == "" && transportLeafCA(previous.TransportContract.LeafInventory, &peer) == ca.CAID {
					isCurrent = true
				}
			}
			if !isCurrent {
				retiredCAIDs[ca.CAID] = struct{}{}
				continue
			}
		}
		expectedCAs = append(expectedCAs, ca)
	}
	expectedTrust := make([]transportTrust, 0, len(previous.TransportContract.TrustInventory))
	for _, trust := range previous.TransportContract.TrustInventory {
		if _, retired := retiredCAIDs[trust.CAID]; !retired {
			expectedTrust = append(expectedTrust, trust)
		}
	}
	for _, ca := range next.TransportContract.CAInventory {
		if _, retired := retiredCAIDs[ca.CAID]; retired {
			return ErrInvalidTransportContract
		}
	}
	for _, trust := range next.TransportContract.TrustInventory {
		if _, retired := retiredCAIDs[trust.CAID]; retired {
			return ErrInvalidTransportContract
		}
	}
	if !sameTransportCAInventory(expectedCAs, next.TransportContract.CAInventory) ||
		!sameTransportTrustInventory(expectedTrust, next.TransportContract.TrustInventory) ||
		len(next.TransportContract.LeafInventory) != len(canonicalHostIDs) {
		return ErrInvalidTransportContract
	}
	for _, hostID := range canonicalHostIDs {
		prior := oldPeers[hostID]
		current := newPeers[hostID]
		if len(prior) != 2 || len(current) != 1 || current[0].OverlapNotAfter != "" {
			return ErrInvalidTransportContract
		}
		var previousCurrent, oldOverlap *activePeer
		for i := range prior {
			if prior[i].OverlapNotAfter == "" {
				previousCurrent = &prior[i]
			} else {
				oldOverlap = &prior[i]
			}
		}
		if previousCurrent == nil || oldOverlap == nil || !samePeerCertificate(*previousCurrent, current[0]) ||
			activePeerPresent(next.TransportContract.ActivePeerAllowlist, *oldOverlap) || transportLeafPresent(next.TransportContract.LeafInventory, *oldOverlap) {
			return ErrInvalidTransportContract
		}
	}
	return nil
}

func onlyNewTransportCAs(values []transportCA, priorIDs map[string]struct{}) []transportCA {
	var result []transportCA
	for _, value := range values {
		if _, existed := priorIDs[value.CAID]; !existed {
			result = append(result, value)
		}
	}
	return result
}

func sameTransportCAInventory(a, b []transportCA) bool {
	a = append([]transportCA(nil), a...)
	b = append([]transportCA(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i].CAID < a[j].CAID })
	sort.Slice(b, func(i, j int) bool { return b[i].CAID < b[j].CAID })
	return slices.Equal(a, b)
}

func sameTransportTrustInventory(a, b []transportTrust) bool {
	a = append([]transportTrust(nil), a...)
	b = append([]transportTrust(nil), b...)
	order := func(values []transportTrust) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].HostID == values[j].HostID {
				return values[i].CAID < values[j].CAID
			}
			return values[i].HostID < values[j].HostID
		})
	}
	order(a)
	order(b)
	return slices.Equal(a, b)
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if _, found := b[value]; !found {
			return false
		}
	}
	return true
}

func peersByHost(peers []activePeer) map[string][]activePeer {
	result := make(map[string][]activePeer, len(canonicalHostIDs))
	for _, peer := range peers {
		result[peer.HostID] = append(result[peer.HostID], peer)
	}
	return result
}

func samePeerIdentity(a, b activePeer) bool {
	return a.HostID == b.HostID && a.URISAN == b.URISAN && a.IPSAN == b.IPSAN
}

func samePeerCertificate(a, b activePeer) bool {
	return a.CertificateFingerprint == b.CertificateFingerprint && a.SPKIFingerprint == b.SPKIFingerprint
}

func activePeerPresent(peers []activePeer, target activePeer) bool {
	for _, peer := range peers {
		if samePeerIdentity(peer, target) && samePeerCertificate(peer, target) {
			return true
		}
	}
	return false
}

func transportLeafPresent(leaves []transportLeaf, peer activePeer) bool {
	for _, leaf := range leaves {
		if leaf.HostID == peer.HostID && leaf.URISAN == peer.URISAN && leaf.IPSAN == peer.IPSAN &&
			leaf.CertificateFingerprint == peer.CertificateFingerprint && leaf.SPKIFingerprint == peer.SPKIFingerprint {
			return true
		}
	}
	return false
}

func containsAllTransportCAs(target, previous []transportCA) bool {
	for _, old := range previous {
		found := false
		for _, candidate := range target {
			if candidate == old {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsAllTransportTrust(target, previous []transportTrust) bool {
	for _, old := range previous {
		found := false
		for _, candidate := range target {
			if candidate == old {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func transportLeafCA(leaves []transportLeaf, peer *activePeer) string {
	if peer == nil {
		return ""
	}
	for _, leaf := range leaves {
		if leaf.HostID == peer.HostID && leaf.URISAN == peer.URISAN && leaf.IPSAN == peer.IPSAN &&
			leaf.CertificateFingerprint == peer.CertificateFingerprint && leaf.SPKIFingerprint == peer.SPKIFingerprint {
			return leaf.CAID
		}
	}
	return ""
}

func containsCAID(values map[string]struct{}, id string) bool {
	_, exists := values[id]
	return id != "" && exists
}

func transportTrustPresent(trusts []transportTrust, hostID, caID string) bool {
	for _, trust := range trusts {
		if trust.HostID == hostID && trust.CAID == caID {
			return true
		}
	}
	return false
}
