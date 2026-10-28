package config

import (
	"strings"
	"testing"
)

func TestBuildFreshPRDReplacementLinuxConfigPreservesPolicyAndTargetsReplacement(t *testing.T) {
	for _, template := range []struct {
		name string
		data string
	}{
		{name: "sandbox", data: replacementPRDLinuxTemplate("sandbox-host", "10.0.0.14:8443", "https://132.226.205.205:8443")},
		{name: "factory", data: replacementPRDLinuxTemplate("factory-sandbox", "10.0.0.30:8443", "https://145.241.255.66:8443")},
	} {
		t.Run(template.name, func(t *testing.T) {
			before := []byte(template.data)
			after, err := BuildFreshPRDReplacementLinuxConfig(before)
			if err != nil {
				t.Fatalf("build replacement config: %v", err)
			}
			if err := ValidateFreshPRDReplacementLinuxConfig(before, after); err != nil {
				t.Fatalf("validate replacement config: %v", err)
			}
			loaded, err := ParseBytes(after)
			if err != nil || loaded.SchemaVersion() != VersionV2 || loaded.Kind() != HostKindLinux || loaded.ReleaseLane() != ReleaseLanePRD {
				t.Fatalf("replacement identity: config=%+v err=%v", loaded, err)
			}
			document, err := decodeFileDocument(after)
			if err != nil || document.Linux == nil || document.Linux.RemoteTargetProfile != "linux-host" ||
				document.Linux.DirectHTTPSBind != "10.0.0.228:8443" || document.Linux.DirectPublicEndpoint != "https://79.72.94.59:8443" {
				t.Fatalf("replacement Linux route = %+v err=%v", document.Linux, err)
			}
			if len(document.EnvironmentRegistry) != 1 {
				t.Fatalf("environment count = %d, want 1", len(document.EnvironmentRegistry))
			}
			for name, environment := range document.EnvironmentRegistry {
				if environment.EffectiveAccount != LinuxAccount || environment.BaseSystem != replacementLinuxBaseSystem ||
					!sameTargets(environment.AllowedTargets, targetDocument{Kind: "remote", Profile: "linux-host"}) ||
					len(environment.AllowedControllers) != 2 || len(environment.AllowedSourceModes) != 1 || environment.AllowedSourceModes[0] != "empty" {
					t.Fatalf("environment %q changed beyond reviewed host identity: %+v", name, environment)
				}
			}
			if strings.Contains(string(after), "129.151.232.40") || strings.Contains(string(after), "132.226.205.205") || strings.Contains(string(after), "145.241.255.66") {
				t.Fatal("replacement config retains a retired or template endpoint")
			}
		})
	}
}

func TestBuildFreshPRDReplacementLinuxConfigRejectsUnverifiedTemplate(t *testing.T) {
	base := replacementPRDLinuxTemplate("sandbox-host", "10.0.0.14:8443", "https://132.226.205.205:8443")
	for _, test := range []struct {
		name string
		edit func(string) string
	}{
		{name: "wrong bind", edit: func(s string) string { return strings.Replace(s, "10.0.0.14:8443", "10.0.0.15:8443", 1) }},
		{name: "wrong endpoint", edit: func(s string) string {
			return strings.Replace(s, "https://132.226.205.205:8443", "https://132.226.205.206:8443", 1)
		}},
		{name: "wrong target policy", edit: func(s string) string {
			return strings.Replace(s, "profile: sandbox-host", "profile: factory-sandbox", 1)
		}},
		{name: "non-empty source policy", edit: func(s string) string {
			return strings.Replace(s, "allowed_source_modes: [empty]", "allowed_source_modes: [repository]", 1)
		}},
		{name: "unexpected controller", edit: func(s string) string { return strings.Replace(s, "id: tomasz.walczuk", "id: other-operator", 1) }},
		{name: "changed account", edit: func(s string) string {
			return strings.Replace(s, "effective_account: ubuntu", "effective_account: root", 1)
		}},
		{name: "wrong schema", edit: func(s string) string { return strings.Replace(s, "version: 2", "version: 3", 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildFreshPRDReplacementLinuxConfig([]byte(test.edit(base))); err == nil {
				t.Fatal("unverified Linux PRD template was accepted")
			}
		})
	}
}

func TestBuildFreshPRDReplacementConfigPairKeepsMacAndLinuxChangesPaired(t *testing.T) {
	macBefore := []byte(macV2ConfigFixture)
	for _, template := range []struct {
		name string
		data string
	}{
		{name: "sandbox", data: replacementPRDLinuxTemplate("sandbox-host", "10.0.0.14:8443", "https://132.226.205.205:8443")},
		{name: "factory", data: replacementPRDLinuxTemplate("factory-sandbox", "10.0.0.30:8443", "https://145.241.255.66:8443")},
	} {
		t.Run(template.name, func(t *testing.T) {
			macCandidate, linuxCandidate, err := BuildFreshPRDReplacementConfigPair(macBefore, []byte(template.data))
			if err != nil {
				t.Fatalf("build paired replacement configs: %v", err)
			}
			if err := ValidateFreshPRDReplacementLinuxRoute(macBefore, macCandidate, linuxCandidate); err != nil {
				t.Fatalf("validate paired replacement configs: %v", err)
			}
			macParsed, err := ParseBytes(macCandidate)
			if err != nil || macParsed.ReleaseLane() != ReleaseLanePRD || macParsed.Kind() != HostKindMac {
				t.Fatalf("replacement Mac config identity: config=%+v err=%v", macParsed, err)
			}
			linuxParsed, err := ParseBytes(linuxCandidate)
			if err != nil || linuxParsed.ReleaseLane() != ReleaseLanePRD || linuxParsed.Kind() != HostKindLinux {
				t.Fatalf("replacement Linux config identity: config=%+v err=%v", linuxParsed, err)
			}
			if strings.Contains(string(macCandidate), prdReplacementLinuxOldPublicIP) ||
				strings.Contains(string(linuxCandidate), prdReplacementLinuxOldPublicIP) {
				t.Fatal("paired fresh-install config retains the retired Linux endpoint")
			}
		})
	}
}

func TestBuildFreshPRDReplacementConfigPairRejectsEitherUnverifiedInput(t *testing.T) {
	macBefore := []byte(macV2ConfigFixture)
	linuxTemplate := []byte(replacementPRDLinuxTemplate("sandbox-host", "10.0.0.14:8443", "https://132.226.205.205:8443"))
	for _, test := range []struct {
		name  string
		mac   []byte
		linux []byte
	}{
		{name: "Mac endpoint already changed", mac: []byte(strings.Replace(string(macBefore), "host: 129.151.232.40", "host: 79.72.94.59", 1)), linux: linuxTemplate},
		{name: "Linux template endpoint drift", mac: macBefore, linux: []byte(strings.Replace(string(linuxTemplate), "10.0.0.14:8443", "10.0.0.15:8443", 1))},
		{name: "unrelated Mac mailbox change", mac: []byte(strings.Replace(string(macBefore), "/mailboxes/analytics", "/mailboxes/other", 1)), linux: linuxTemplate},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := BuildFreshPRDReplacementConfigPair(test.mac, test.linux); err == nil {
				t.Fatal("unverified paired replacement inputs were accepted")
			}
		})
	}
}

func replacementPRDLinuxTemplate(profile, bind, endpoint string) string {
	fixture := strings.ReplaceAll(linuxV2ConfigFixture, "linux-build-host", profile)
	fixture = strings.ReplaceAll(fixture, "10.0.0.201:8443", bind)
	fixture = strings.ReplaceAll(fixture, "https://198.51.100.20:8443", endpoint)
	fixture = strings.ReplaceAll(fixture, "linux-build-dev", "linux-prd-direct")
	fixture = strings.ReplaceAll(fixture, "Ubuntu 22.04 LTS", "Ubuntu 24.04 LTS")
	return fixture
}
