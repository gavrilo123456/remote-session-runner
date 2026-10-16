package laneroot

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
)

func TestLanesL103DeclarativeHandoffRequiresFixedChildrenAndVolumeIdentity(t *testing.T) {
	handoff := syntheticHandoff(config.HostKindLinux, config.ReleaseLaneDev, "/var/tmp/dev-mailbox")
	if err := ValidateHandoff(handoff); err != nil {
		t.Fatalf("valid Linux handoff rejected: %v", err)
	}

	missing := cloneHandoff(handoff)
	delete(missing.ChildIdentities, "acks")
	if err := ValidateHandoff(missing); !errors.Is(err, ErrInvalidHandoff) {
		t.Errorf("missing child accepted: %v", err)
	}
	extra := cloneHandoff(handoff)
	extra.ChildIdentities["other"] = FileIdentity{Device: 41, Inode: 99}
	if err := ValidateHandoff(extra); !errors.Is(err, ErrInvalidHandoff) {
		t.Errorf("undeclared child accepted: %v", err)
	}
	aliasedChild := cloneHandoff(handoff)
	aliasedChild.ChildIdentities["acks"] = aliasedChild.ChildIdentities["inbox"]
	if err := ValidateHandoff(aliasedChild); !errors.Is(err, ErrInvalidHandoff) {
		t.Errorf("aliased child accepted: %v", err)
	}

	mac := syntheticHandoff(config.HostKindMac, config.ReleaseLaneDev, "/Volumes/dev-mailbox")
	mac.Volume = &VolumeHandoff{
		Expected: VolumeIdentity{Mountpoint: mac.Path, UUID: "0f7a62d1-71bd-4c23-9a21-010203040506", Filesystem: "apfs", Device: 41, QuotaBytes: 1 << 40},
		Observed: VolumeIdentity{Mountpoint: mac.Path, UUID: "0f7a62d1-71bd-4c23-9a21-010203040506", Filesystem: "apfs", Device: 41, QuotaBytes: 1 << 40},
	}
	if err := ValidateHandoff(mac); err != nil {
		t.Fatalf("valid Mac Dev APFS handoff rejected: %v", err)
	}
	for name, mutate := range map[string]func(*RootHandoff){
		"missing APFS identity": func(value *RootHandoff) { value.Volume = nil },
		"wrong UUID":            func(value *RootHandoff) { value.Volume.Observed.UUID = "11111111-2222-3333-4444-555555555555" },
		"wrong mountpoint":      func(value *RootHandoff) { value.Volume.Observed.Mountpoint = "/Volumes/other" },
		"wrong filesystem":      func(value *RootHandoff) { value.Volume.Observed.Filesystem = "hfs" },
		"wrong device":          func(value *RootHandoff) { value.Volume.Observed.Device++ },
		"zero quota":            func(value *RootHandoff) { value.Volume.Observed.QuotaBytes = 0 },
	} {
		bad := cloneHandoff(mac)
		mutate(&bad)
		if err := ValidateHandoff(bad); !errors.Is(err, ErrInvalidHandoff) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	prd := syntheticHandoff(config.HostKindMac, config.ReleaseLanePRD, "/var/tmp/prd-mailbox")
	prd.Volume = mac.Volume
	if err := ValidateHandoff(prd); !errors.Is(err, ErrInvalidHandoff) {
		t.Errorf("unexpected APFS volume requirement accepted for Mac PRD: %v", err)
	}
}

func TestLanesL103DeclarativeRootSetRejectsNestedAndAliasedRoots(t *testing.T) {
	first := syntheticHandoff(config.HostKindLinux, config.ReleaseLaneDev, "/var/lib/runner/dev")
	second := syntheticHandoff(config.HostKindLinux, config.ReleaseLaneQAS, "/var/lib/runner/qas")
	second.RootIdentity.Inode = 99
	roots := []ValidatedRoot{validated(first), validated(second)}
	if err := ValidateRootSet(roots); err != nil {
		t.Fatalf("disjoint roots rejected: %v", err)
	}

	for name, bad := range map[string][]ValidatedRoot{
		"same path":        {validated(first), {Host: config.HostKindLinux, Lane: config.ReleaseLaneQAS, InboxID: "qas", Path: first.Path, RootIdentity: FileIdentity{Device: 41, Inode: 40}}},
		"nested path":      {validated(first), {Host: config.HostKindLinux, Lane: config.ReleaseLaneQAS, InboxID: "qas", Path: filepath.Join(first.Path, "nested"), RootIdentity: FileIdentity{Device: 41, Inode: 40}}},
		"descriptor alias": {validated(first), {Host: config.HostKindLinux, Lane: config.ReleaseLaneQAS, InboxID: "qas", Path: "/var/lib/runner/other", RootIdentity: first.RootIdentity}},
	} {
		if err := ValidateRootSet(bad); !errors.Is(err, ErrInvalidHandoff) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestLanesL103DeclarativeAncillaryInventoryIsClosed(t *testing.T) {
	handoff := syntheticHandoff(config.HostKindLinux, config.ReleaseLanePRD, "/var/tmp/prd-mailbox")
	handoff.Layout = LayoutRetained
	handoff.AncillaryEntries = []AncillaryEntrySnapshot{{
		Name: "legacy-cache", Kind: EntryDirectory, Identity: FileIdentity{Device: 41, Inode: 20}, OwnerUID: handoff.OwnerUID, Mode: 0,
	}}
	if err := ValidateHandoff(handoff); err != nil {
		t.Fatalf("safe retained entry metadata rejected: %v", err)
	}
	for name, mutate := range map[string]func(*RootHandoff){
		"path traversal":      func(value *RootHandoff) { value.AncillaryEntries[0].Name = "../cache" },
		"reserved child name": func(value *RootHandoff) { value.AncillaryEntries[0].Name = "inbox" },
		"symlink type":        func(value *RootHandoff) { value.AncillaryEntries[0].Kind = "symlink" },
		"wrong owner":         func(value *RootHandoff) { value.AncillaryEntries[0].OwnerUID++ },
		"group writable":      func(value *RootHandoff) { value.AncillaryEntries[0].Mode = 0o020 },
		"special mode":        func(value *RootHandoff) { value.AncillaryEntries[0].Mode = 0o1000 },
		"root identity alias": func(value *RootHandoff) { value.AncillaryEntries[0].Identity = value.RootIdentity },
	} {
		bad := cloneHandoff(handoff)
		mutate(&bad)
		if err := ValidateHandoff(bad); !errors.Is(err, ErrInvalidHandoff) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestLanesL103DeclarativePathsAndLayoutsFailClosed(t *testing.T) {
	handoff := syntheticHandoff(config.HostKindLinux, config.ReleaseLaneDev, "/var/tmp/dev-mailbox")
	for name, mutate := range map[string]func(*RootHandoff){
		"noncanonical path": func(value *RootHandoff) { value.Path = "/var/tmp/../tmp/dev-mailbox" },
		"root path":         func(value *RootHandoff) { value.Path = "/" },
		"unknown layout":    func(value *RootHandoff) { value.Layout = "repair" },
		"zero owner":        func(value *RootHandoff) { value.OwnerUID = 0 },
	} {
		bad := cloneHandoff(handoff)
		mutate(&bad)
		if err := ValidateHandoff(bad); !errors.Is(err, ErrInvalidHandoff) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	retained := cloneHandoff(handoff)
	retained.Layout = LayoutRetained
	if err := ValidateHandoff(retained); err != nil {
		t.Fatalf("empty retained inventory should be valid: %v", err)
	}
}

func TestLanesL103InspectsEmptyDescriptorBackedMailboxTree(t *testing.T) {
	fixture := newRootFixture(t, nil)
	inventory, err := InspectRoot(fixture.handoff)
	if err != nil {
		rootFD, parentFD, rootName, openErr := openRootNoFollow(fixture.handoff.Path, fixture.handoff.OwnerUID)
		t.Logf("debug path=%s tmp=%s owner=%d openErr=%v", fixture.handoff.Path, os.TempDir(), fixture.handoff.OwnerUID, openErr)
		if openErr == nil {
			defer unix.Close(rootFD)
			defer unix.Close(parentFD)
			rootStat, statErr := fstat(rootFD)
			names, namesErr := readDirectoryNames(rootFD)
			t.Logf("debug name=%q root=%+v statErr=%v expected=%+v names=%v namesErr=%v", rootName, rootStat, statErr, fixture.handoff.RootIdentity, names, namesErr)
		}
		t.Fatalf("InspectRoot rejected a valid empty root: %v", err)
	}
	if inventory.Claim.RootIdentity != fixture.handoff.RootIdentity || len(inventory.ChildIdentities) != len(mailboxChildren) {
		t.Fatalf("unexpected inspected inventory: %+v", inventory)
	}
}

func TestLanesL103InspectsOpaqueRetainedEntriesWithoutTraversal(t *testing.T) {
	fixture := newRootFixture(t, map[string]string{"legacy-cache": "directory"})
	inside := filepath.Join(fixture.root, "legacy-cache", "opaque")
	if err := os.Symlink(filepath.Join(fixture.base, "missing-target"), inside); err != nil {
		t.Fatal(err)
	}
	fixture.handoff.Layout = LayoutRetained
	fixture.handoff.AncillaryEntries = []AncillaryEntrySnapshot{snapshotAncillary(t, fixture.root, "legacy-cache")}
	if _, err := InspectRoot(fixture.handoff); err != nil {
		t.Fatalf("nested opaque symlink affected top-level inventory: %v", err)
	}
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("retained bytes are opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRoot(fixture.handoff); err != nil {
		t.Fatalf("changing opaque directory contents changed its top-level identity: %v", err)
	}
}

func TestLanesL103RejectsSymlinkedRootAncestorsAndChildren(t *testing.T) {
	t.Run("root symlink", func(t *testing.T) {
		fixture := newRootFixture(t, nil)
		original := fixture.root + "-original"
		if err := os.Rename(fixture.root, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(original, fixture.root); err != nil {
			t.Fatal(err)
		}
		assertInspectionRejected(t, fixture.handoff)
	})
	t.Run("ancestor symlink", func(t *testing.T) {
		fixture := newRootFixture(t, nil)
		alias := filepath.Join(fixture.base, "alias")
		if err := os.Symlink(filepath.Dir(fixture.root), alias); err != nil {
			t.Fatal(err)
		}
		aliased := cloneHandoff(fixture.handoff)
		aliased.Path = filepath.Join(alias, filepath.Base(fixture.root))
		assertInspectionRejected(t, aliased)
	})
	t.Run("child symlink", func(t *testing.T) {
		fixture := newRootFixture(t, nil)
		child := filepath.Join(fixture.root, "acks")
		if err := os.Remove(child); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(fixture.base, "absent"), child); err != nil {
			t.Fatal(err)
		}
		assertInspectionRejected(t, fixture.handoff)
	})
}

func TestLanesL103RejectsWrongModesMissingAndUndeclaredEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, rootFixture)
	}{
		{name: "root mode", mutate: func(t *testing.T, fixture rootFixture) {
			if err := os.Chmod(fixture.root, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "child mode", mutate: func(t *testing.T, fixture rootFixture) {
			if err := os.Chmod(filepath.Join(fixture.root, "events"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing child", mutate: func(t *testing.T, fixture rootFixture) {
			if err := os.RemoveAll(filepath.Join(fixture.root, "diagnostics")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "undeclared entry", mutate: func(t *testing.T, fixture rootFixture) {
			if err := os.WriteFile(filepath.Join(fixture.root, "unlisted"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "nonempty initialized child", mutate: func(t *testing.T, fixture rootFixture) {
			if err := os.WriteFile(filepath.Join(fixture.root, "inbox", "request"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRootFixture(t, nil)
			test.mutate(t, fixture)
			assertInspectionRejected(t, fixture.handoff)
		})
	}
}

func TestLanesL103RejectsIdentityReplacementAndUnsafeAncestor(t *testing.T) {
	t.Run("root replaced at same path", func(t *testing.T) {
		fixture := newRootFixture(t, nil)
		retired := fixture.root + "-retired"
		if err := os.Rename(fixture.root, retired); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.root, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range mailboxChildren {
			if err := os.Mkdir(filepath.Join(fixture.root, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		assertInspectionRejected(t, fixture.handoff)
	})
	t.Run("ancestor writable by group and other", func(t *testing.T) {
		fixture := newRootFixture(t, nil)
		if err := os.Chmod(fixture.base, 0o777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(fixture.base, 0o700)
		assertInspectionRejected(t, fixture.handoff)
	})
	t.Run("wrong owner snapshot", func(t *testing.T) {
		fixture := newRootFixture(t, nil)
		fixture.handoff.OwnerUID++
		assertInspectionRejected(t, fixture.handoff)
	})
}

func TestLanesL103RejectsAncillaryReplacementAndTypeChanges(t *testing.T) {
	fixture := newRootFixture(t, map[string]string{"retained-index": "file"})
	fixture.handoff.Layout = LayoutRetained
	fixture.handoff.AncillaryEntries = []AncillaryEntrySnapshot{snapshotAncillary(t, fixture.root, "retained-index")}

	retired := filepath.Join(fixture.base, "retired-index")
	if err := os.Rename(filepath.Join(fixture.root, "retained-index"), retired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "retained-index"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertInspectionRejected(t, fixture.handoff)

	if err := os.Remove(filepath.Join(fixture.root, "retained-index")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(retired, filepath.Join(fixture.root, "retained-index")); err != nil {
		t.Fatal(err)
	}
	assertInspectionRejected(t, fixture.handoff)
}

type rootFixture struct {
	base    string
	root    string
	handoff RootHandoff
}

func newRootFixture(t *testing.T, ancillary map[string]string) rootFixture {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("resolve test home: %v", err)
	}
	privateParent, err := os.MkdirTemp(home, ".laneroot-test-")
	if err != nil {
		// Some constrained Mac shells cannot create a sibling in the home
		// directory. Their system temp root is already owner-private and has
		// non-writable ancestors, so use that path; Linux test hosts normally
		// take the private-home branch above.
		privateParent, err = filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			t.Fatalf("resolve secure system temp path: %v", err)
		}
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(privateParent) })
	}
	t.Setenv("TMPDIR", privateParent)
	base := t.TempDir()
	root := filepath.Join(base, "mailbox")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range mailboxChildren {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, kind := range ancillary {
		switch kind {
		case "file":
			if err := os.WriteFile(filepath.Join(root, name), []byte("opaque"), 0o600); err != nil {
				t.Fatal(err)
			}
		case "directory":
			if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown ancillary fixture kind %q", kind)
		}
	}
	owner := uint32(os.Getuid())
	rootIdentity := snapshotIdentity(t, root)
	children := make(map[string]FileIdentity, len(mailboxChildren))
	for _, name := range mailboxChildren {
		children[name] = snapshotIdentity(t, filepath.Join(root, name))
	}
	layout := LayoutInitializedEmpty
	if len(ancillary) != 0 {
		layout = LayoutRetained
	}
	return rootFixture{base: base, root: root, handoff: RootHandoff{
		Host: config.HostKindLinux, Lane: config.ReleaseLaneDev, InboxID: "dev", Path: root,
		Layout: layout, OwnerUID: owner, RootIdentity: rootIdentity, ChildIdentities: children,
	}}
}

func snapshotIdentity(t *testing.T, path string) FileIdentity {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s did not return syscall.Stat_t", path)
	}
	return FileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}
}

func snapshotAncillary(t *testing.T, root, name string) AncillaryEntrySnapshot {
	t.Helper()
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s did not return syscall.Stat_t", path)
	}
	kind := EntryRegularFile
	if info.IsDir() {
		kind = EntryDirectory
	}
	return AncillaryEntrySnapshot{Name: name, Kind: kind,
		Identity: FileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)},
		OwnerUID: stat.Uid, Mode: uint32(stat.Mode & 0o7777)}
}

func assertInspectionRejected(t *testing.T, handoff RootHandoff) {
	t.Helper()
	if _, err := InspectRoot(handoff); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("InspectRoot accepted unsafe fixture: %v", err)
	}
}

func syntheticHandoff(host config.HostKind, lane config.ReleaseLane, path string) RootHandoff {
	children := make(map[string]FileIdentity, len(mailboxChildren))
	for index, name := range mailboxChildren {
		children[name] = FileIdentity{Device: 41, Inode: uint64(index + 2)}
	}
	return RootHandoff{Host: host, Lane: lane, InboxID: string(lane), Path: path, Layout: LayoutInitializedEmpty,
		OwnerUID: 501, RootIdentity: FileIdentity{Device: 41, Inode: 1}, ChildIdentities: children}
}

func cloneHandoff(input RootHandoff) RootHandoff {
	output := input
	output.ChildIdentities = make(map[string]FileIdentity, len(input.ChildIdentities))
	for name, identity := range input.ChildIdentities {
		output.ChildIdentities[name] = identity
	}
	output.AncillaryEntries = append([]AncillaryEntrySnapshot(nil), input.AncillaryEntries...)
	if input.Volume != nil {
		volume := *input.Volume
		output.Volume = &volume
	}
	return output
}

func validated(handoff RootHandoff) ValidatedRoot {
	return ValidatedRoot{Host: handoff.Host, Lane: handoff.Lane, InboxID: handoff.InboxID, Path: handoff.Path, RootIdentity: handoff.RootIdentity}
}
