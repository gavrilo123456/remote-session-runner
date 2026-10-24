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
