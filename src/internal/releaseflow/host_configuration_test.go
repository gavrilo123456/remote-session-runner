package releaseflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadHostConfigurationAcceptsReadOnlyOwnerPrivateKeys(t *testing.T) {
	path := writeTestHostConfiguration(t, t.TempDir())
	var cfg HostConfiguration
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, host := range HostOrder {
		profile := cfg.Hosts[host]
		if err := os.Chmod(profile.GitIdentityFile, 0o400); err != nil {
			t.Fatal(err)
		}
		if profile.IdentityFile != "" {
			if err := os.Chmod(profile.IdentityFile, 0o400); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := LoadHostConfiguration(filepath.Clean(path)); err != nil {
		t.Fatalf("owner-only read-only identities should be accepted: %v", err)
	}

	if err := os.Chmod(cfg.Hosts["linux-host"].IdentityFile, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHostConfiguration(path); err == nil {
		t.Fatal("identity with group access was accepted")
	}
}

func TestAllowFirstInstallPeerAbsenceIsLimitedToReplacementLinuxHost(t *testing.T) {
	path := writeTestHostConfiguration(t, t.TempDir())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg HostConfiguration
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Hosts["linux-host"] = HostProfile{HostID: "linux-host", AllowFirstInstallPeerAbsence: true,
		GitIdentityFile: cfg.Hosts["linux-host"].GitIdentityFile, GitKnownHostsFile: cfg.Hosts["linux-host"].GitKnownHostsFile,
		Repository: cfg.Hosts["linux-host"].Repository, GoBinary: cfg.Hosts["linux-host"].GoBinary,
		RuntimeRoot: cfg.Hosts["linux-host"].RuntimeRoot, Target: replacementLinuxTarget,
		IdentityFile: cfg.Hosts["linux-host"].IdentityFile, KnownHostsFile: cfg.Hosts["linux-host"].KnownHostsFile}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHostConfiguration(path); err != nil {
		t.Fatalf("Linux-only absent-PRD opt-in rejected: %v", err)
	}
	cfg.Hosts["sandbox-host"] = HostProfile{HostID: "sandbox-host", AllowFirstInstallPeerAbsence: true,
		GitIdentityFile: cfg.Hosts["sandbox-host"].GitIdentityFile, GitKnownHostsFile: cfg.Hosts["sandbox-host"].GitKnownHostsFile,
		Repository: cfg.Hosts["sandbox-host"].Repository, GoBinary: cfg.Hosts["sandbox-host"].GoBinary,
		RuntimeRoot: cfg.Hosts["sandbox-host"].RuntimeRoot, Target: cfg.Hosts["sandbox-host"].Target,
		IdentityFile: cfg.Hosts["sandbox-host"].IdentityFile, KnownHostsFile: cfg.Hosts["sandbox-host"].KnownHostsFile}
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHostConfiguration(path); err == nil {
		t.Fatal("non-Linux host was allowed to declare absent PRD peer fallback")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	profile := cfg.Hosts["linux-host"]
	profile.Target = "ubuntu@129.151.232.40"
	cfg.Hosts["linux-host"] = profile
	data, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHostConfiguration(path); err == nil {
		t.Fatal("legacy Linux target was allowed to declare replacement first-install peer absence")
	}
}
