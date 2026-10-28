package config

import (
	"strings"
	"testing"
)

func TestValidatePRDReplacementLinuxRouteAllowsOnlyPairedAddressChanges(t *testing.T) {
	macBefore, macAfter, linuxBefore, linuxAfter := prdReplacementRouteFixtures()
	for name, candidate := range map[string][]byte{"Mac before": macBefore, "Mac after": macAfter, "Linux before": linuxBefore, "Linux after": linuxAfter} {
		if _, err := ParseBytes(candidate); err != nil {
			t.Fatalf("parse %s fixture: %v", name, err)
		}
	}
	if err := ValidatePRDReplacementLinuxRoute(macBefore, macAfter, linuxBefore, linuxAfter); err != nil {
		t.Fatalf("validate exact paired replacement route: %v", err)
	}
	if _, err := ParseBytes(macAfter); err != nil {
		t.Fatalf("replacement Mac config must remain a valid V2 config: %v", err)
	}
	if _, err := ParseBytes(linuxAfter); err != nil {
		t.Fatalf("replacement Linux config must remain a valid V2 config: %v", err)
	}
}

func TestValidatePRDReplacementLinuxRouteRejectsUnpairedOrUnrelatedChanges(t *testing.T) {
	macBefore, macAfter, linuxBefore, linuxAfter := prdReplacementRouteFixtures()
	tests := []struct {
		name                         string
		macCandidate, linuxCandidate []byte
	}{
		{name: "Mac route only", macCandidate: macAfter, linuxCandidate: linuxBefore},
		{name: "Linux route only", macCandidate: macBefore, linuxCandidate: linuxAfter},
		{name: "Mac mailbox changed", macCandidate: []byte(strings.Replace(string(macAfter), "/mailboxes/analytics", "/mailboxes/changed", 1)), linuxCandidate: linuxAfter},
		{name: "Mac trust path changed", macCandidate: []byte(strings.Replace(string(macAfter), "poc-ca.pem", "other-ca.pem", 1)), linuxCandidate: linuxAfter},
		{name: "Linux certificate path changed", macCandidate: macAfter, linuxCandidate: []byte(strings.Replace(string(linuxAfter), "server.pem", "other-server.pem", 1))},
		{name: "unexpected endpoint", macCandidate: []byte(strings.Replace(string(macAfter), "79.72.94.59:8443", "79.72.94.60:8443", 1)), linuxCandidate: linuxAfter},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePRDReplacementLinuxRoute(macBefore, test.macCandidate, linuxBefore, test.linuxCandidate); err == nil {
				t.Fatal("route transition accepted an unpaired or unrelated change")
			}
		})
	}
}

func TestValidateFreshPRDReplacementLinuxRouteDoesNotRequireRetiredWorkerConfig(t *testing.T) {
	macBefore, macAfter, _, linuxAfter := prdReplacementRouteFixtures()
	if err := ValidateFreshPRDReplacementLinuxRoute(macBefore, macAfter, linuxAfter); err != nil {
		t.Fatalf("valid fresh replacement route rejected without a retired-host config: %v", err)
	}
	tests := []struct {
		name  string
		mac   []byte
		linux []byte
	}{
		{name: "old Mac route retained", mac: macBefore, linux: linuxAfter},
		{name: "Mac mailbox changed", mac: []byte(strings.Replace(string(macAfter), "/mailboxes/analytics", "/mailboxes/changed", 1)), linux: linuxAfter},
		{name: "Mac route changed to another host", mac: []byte(strings.Replace(string(macAfter), "79.72.94.59:8443", "79.72.94.60:8443", 1)), linux: linuxAfter},
		{name: "Linux service root changed", mac: macAfter, linux: []byte(strings.Replace(string(linuxAfter), "/remote-session-runner/state/remote.db", "/other/state/remote.db", 1))},
		{name: "Linux bind changed", mac: macAfter, linux: []byte(strings.Replace(string(linuxAfter), "10.0.0.228:8443", "10.0.0.229:8443", 1))},
		{name: "Linux target profile changed", mac: macAfter, linux: []byte(strings.Replace(string(linuxAfter), "profile: linux-host", "profile: sandbox-host", 1))},
		{name: "Linux V3 document", mac: macAfter, linux: []byte(strings.Replace(string(linuxAfter), "version: 2", "version: 3", 1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateFreshPRDReplacementLinuxRoute(macBefore, test.mac, test.linux); err == nil {
				t.Fatal("fresh replacement route accepted an unrelated or invalid change")
			}
		})
	}
}

func prdReplacementRouteFixtures() (macBefore, macAfter, linuxBefore, linuxAfter []byte) {
	macBefore = []byte(macV2ConfigFixture)
	macAfterText := strings.Replace(string(macV2ConfigFixture), "host: 129.151.232.40", "host: 79.72.94.59", 1)
	macAfterText = strings.Replace(macAfterText, "url: https://129.151.232.40:8443", "url: https://79.72.94.59:8443", 1)
	macAfter = []byte(macAfterText)

	linuxText := strings.ReplaceAll(linuxV2ConfigFixture, "10.0.0.201:8443", "10.0.0.200:8443")
	linuxText = strings.ReplaceAll(linuxText, "198.51.100.20", "129.151.232.40")
	linuxText = strings.ReplaceAll(linuxText, "linux-build-host", "linux-host")
	linuxText = strings.ReplaceAll(linuxText, "linux-build-dev", "linux-dev")
	linuxBefore = []byte(linuxText)
	linuxAfterText := strings.Replace(linuxText, "10.0.0.200:8443", "10.0.0.228:8443", 1)
	linuxAfterText = strings.Replace(linuxAfterText, "https://129.151.232.40:8443", "https://79.72.94.59:8443", 1)
	linuxAfter = []byte(linuxAfterText)
	return
}
