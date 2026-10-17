//go:build darwin

package lanefixtures

import "testing"

func TestPhysicalWholeDiskRequiresVerifiedNonImageDevice(t *testing.T) {
	for _, tc := range []struct {
		name, kind, whole, internal, wholeDisk string
		wantErr                                bool
	}{
		{"external physical whole disk", "Physical", "disk4", "false", "true", false},
		{"disk image", "Virtual", "disk4", "false", "true", true},
		{"unknown kind", "Unknown", "disk0", "false", "true", true},
		{"internal physical disk", "Physical", "disk0", "true", "true", true},
		{"missing parent", "Physical", "", "false", "true", true},
		{"partition is not whole disk", "Physical", "disk0s2", "false", "true", true},
		{"path is not a device identifier", "Physical", "/dev/disk0", "false", "true", true},
		{"whole-disk property missing", "Physical", "disk4", "false", "false", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := physicalWholeDisk(tc.kind, tc.whole, tc.internal, tc.wholeDisk)
			if (err != nil) != tc.wantErr {
				t.Fatalf("physicalWholeDisk(%q, %q, %q, %q) error = %v", tc.kind, tc.whole, tc.internal, tc.wholeDisk, err)
			}
			if !tc.wantErr && got != tc.whole {
				t.Fatalf("physicalWholeDisk returned %q, want %q", got, tc.whole)
			}
		})
	}
}

func TestMacFixtureDiskPlacementExcludesProductionAndSeparatesBackup(t *testing.T) {
	cases := []struct {
		name, role, container, physical, fixtureContainer, fixturePhysical, dataContainer, dataPhysical string
		wantErr                                                                                         bool
	}{
		{"test volume on isolated fixture disk", "dev", "disk6", "disk2", "disk6", "disk2", "disk3", "disk0", false},
		{"test volume on production Data container", "dev", "disk3", "disk2", "disk6", "disk2", "disk3", "disk0", true},
		{"test volume on production physical disk", "qas", "disk6", "disk0", "disk6", "disk2", "disk3", "disk0", true},
		{"backup on separate physical disk", "backup", "disk7", "disk4", "disk6", "disk2", "disk3", "disk0", false},
		{"backup shares test physical disk", "backup", "disk7", "disk2", "disk6", "disk2", "disk3", "disk0", true},
		{"backup on production disk", "backup", "disk7", "disk0", "disk6", "disk2", "disk3", "disk0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMacPhysicalPlacement(tc.role, tc.container, tc.physical, tc.fixtureContainer, tc.fixturePhysical, tc.dataContainer, tc.dataPhysical)
			if (err != nil) != tc.wantErr {
				t.Fatalf("placement validation error = %v", err)
			}
		})
	}
}
