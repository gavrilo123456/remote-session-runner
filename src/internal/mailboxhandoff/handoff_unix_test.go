//go:build darwin || linux

package mailboxhandoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

type fixture struct {
	base, path, digest string
	receipt            Receipt
	definitions        []Definition
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "prd")
	target := filepath.Join(base, "qas")
	root := filepath.Join(base, "project", "mailbox-")
	for _, path := range []string{source, target, root, filepath.Join(target, "staging", "mailbox-handoffs"), filepath.Join(target, "mailbox")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var stat unix.Stat_t
	if err := unix.Stat(root, &stat); err != nil {
		t.Fatal(err)
	}
	r := Receipt{
		Version: Version, OperationID: strings.Repeat("a", 32), SourceLane: "prd", TargetLane: "qas",
		SourceServiceRoot: source, TargetServiceRoot: target,
		SourceConfigSHA256: strings.Repeat("b", 64), TargetConfigSHA256: strings.Repeat("c", 64), Phase: "active",
		EnvironmentAliases: map[string]string{"linux-dev": "linux-host-qas"},
		Mailboxes: []Mailbox{{SourceID: "project", TargetID: "project", Root: root, Device: uint64(stat.Dev), Inode: uint64(stat.Ino),
			ArchiveRelative:   ".runner-mailbox-handoffs/" + strings.Repeat("a", 32) + "/prd",
			BlockedRequestIDs: []string{"req-prd-history"}, BlockedIdempotencySHA256: []string{SHA256([]byte("private-old-key"))}}},
	}
	f := fixture{base: base, path: ReceiptPath(r), receipt: r, definitions: []Definition{{ID: "default", Root: filepath.Join(target, "mailbox")}, {ID: "project", Root: root}}}
	f.writeReceipt(t)
	m := r.Mailboxes[0]
	if err := WriteMarker(root, Marker{OperationID: r.OperationID, OwnerLane: "qas", ReceiptSHA256: f.digest, Device: m.Device, Inode: m.Inode}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) writeReceipt(t *testing.T) {
	t.Helper()
	raw, err := MarshalReceipt(f.receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.digest = SHA256(raw)
}

func (f fixture) acquire() (*Lease, error) {
	return Acquire(f.path, "qas", f.receipt.TargetServiceRoot, f.receipt.TargetConfigSHA256, f.definitions)
}

func TestHandoffLeaseOwnsRootsAndFencesHistoricalIdentities(t *testing.T) {
	f := newFixture(t)
	l, err := f.acquire()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if !l.Covers("project") || l.Covers("default") {
		t.Fatal("transferred root coverage is incorrect")
	}
	for _, id := range []string{"project", "default"} {
		if err := l.Check(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.CheckRequest("project", "req-fresh", "new-key"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(l.CheckRequest("project", "req-prd-history", "new-key"), ErrReplay) {
		t.Fatal("historical request was not fenced")
	}
	if !errors.Is(l.CheckRequest("project", "req-fresh", "private-old-key"), ErrReplay) {
		t.Fatal("historical idempotency key was not fenced")
	}
	if !errors.Is(l.CheckACK("project", "req-prd-history"), ErrReplay) {
		t.Fatal("historical ACK was not fenced")
	}
	if got, err := l.ResolveEnvironment("project", "linux-dev"); err != nil || got != "linux-host-qas" {
		t.Fatalf("alias=%q error=%v", got, err)
	}
	if got, err := l.ResolveEnvironment("default", "linux-dev"); err != nil || got != "linux-dev" {
		t.Fatalf("native alias=%q error=%v", got, err)
	}
	if _, err := f.acquire(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second owner error=%v", err)
	}
	if err := l.Check("unknown"); !errors.Is(err, ErrOwnership) {
		t.Fatalf("unknown mailbox error=%v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Check("project"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed check error=%v", err)
	}
	again, err := f.acquire()
	if err != nil {
		t.Fatalf("released root could not be reacquired: %v", err)
	}
	_ = again.Close()
	raw, _ := os.ReadFile(f.path)
	if strings.Contains(string(raw), "private-old-key") {
		t.Fatal("raw idempotency key leaked to receipt")
	}
}

func TestHandoffFailsClosedForIdentityConfigurationAndPhaseMismatch(t *testing.T) {
	for _, tc := range []string{"lane", "service-root", "configuration", "phase", "root-identity", "missing-external", "extra-receipt-root"} {
		t.Run(tc, func(t *testing.T) {
			f := newFixture(t)
			lane, serviceRoot, digest := "qas", f.receipt.TargetServiceRoot, f.receipt.TargetConfigSHA256
			switch tc {
			case "lane":
				lane = "prd"
			case "service-root":
				serviceRoot = f.receipt.SourceServiceRoot
			case "configuration":
				digest = strings.Repeat("d", 64)
			case "phase":
				f.receipt.Phase = "returning"
				f.writeReceipt(t)
			case "root-identity":
				root := f.receipt.Mailboxes[0].Root
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing-external":
				f.definitions = append(f.definitions, Definition{ID: "other", Root: filepath.Join(f.base, "other")})
			case "extra-receipt-root":
				f.definitions = f.definitions[:1]
			}
			if lease, err := Acquire(f.path, lane, serviceRoot, digest, f.definitions); err == nil {
				_ = lease.Close()
				t.Fatal("mismatch acquired ownership")
			}
		})
	}
}

func TestHandoffRevalidatesReceiptMarkerAndRootBeforeFurtherWork(t *testing.T) {
	for _, change := range []string{"receipt", "receipt-mode", "receipt-hardlink", "receipt-symlink", "receipt-parent-symlink", "service-root-mode", "marker", "lock", "root"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			lease, err := f.acquire()
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			root := f.receipt.Mailboxes[0].Root
			switch change {
			case "receipt":
				f.receipt.Phase = "returning"
				f.writeReceipt(t)
			case "receipt-mode":
				if err := os.Chmod(f.path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "receipt-hardlink":
				if err := os.Link(f.path, f.path+"-linked"); err != nil {
					t.Fatal(err)
				}
			case "receipt-symlink", "receipt-parent-symlink":
				path := f.path
				if change == "receipt-parent-symlink" {
					path = filepath.Dir(path)
				}
				if err := os.Rename(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatal(err)
				}
			case "service-root-mode":
				if err := os.Chmod(f.receipt.TargetServiceRoot, 0o755); err != nil {
					t.Fatal(err)
				}
			case "marker":
				if err := os.WriteFile(filepath.Join(root, MarkerFileName), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "lock":
				if err := os.Rename(filepath.Join(root, LockFileName), filepath.Join(root, LockFileName+"-old")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, LockFileName), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := lease.Check("project"); !errors.Is(err, ErrOwnership) {
				t.Fatalf("changed %s error=%v", change, err)
			}
			if _, err := lease.ResolveEnvironment("project", "linux-dev"); !errors.Is(err, ErrOwnership) {
				t.Fatalf("changed alias error=%v", err)
			}
		})
	}
}

func TestHandoffLeaseHashesEveryCheckDespiteUnchangedSizeAndMtime(t *testing.T) {
	f := newFixture(t)
	lease, err := f.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	before, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(raw), `"phase": "active"`, `"phase": "return"`, 1)
	if changed == string(raw) || len(changed) != len(raw) {
		t.Fatal("tamper fixture did not preserve receipt length")
	}
	if err := os.WriteFile(f.path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(f.path)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("tamper fixture metadata changed: after=%v error=%v", after, err)
	}
	if err := lease.Check("project"); !errors.Is(err, ErrOwnership) {
		t.Fatalf("same-size same-mtime receipt tamper accepted: %v", err)
	}
	if err := lease.CheckRequest("project", "req-new", "new-key"); !errors.Is(err, ErrOwnership) {
		t.Fatalf("tampered receipt admitted new request: %v", err)
	}
}

func TestHandoffLargeReceiptChecksDoNotRebuildHistoricalIndexes(t *testing.T) {
	small := newFixture(t)
	smallLease, err := small.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer smallLease.Close()
	large := newFixture(t)
	mailbox := &large.receipt.Mailboxes[0]
	mailbox.BlockedRequestIDs = make([]string, 5694)
	mailbox.BlockedIdempotencySHA256 = make([]string, 5164)
	for i := range mailbox.BlockedRequestIDs {
		mailbox.BlockedRequestIDs[i] = fmt.Sprintf("req-%06d-%s", i, strings.Repeat("history", 7))
	}
	for i := range mailbox.BlockedIdempotencySHA256 {
		mailbox.BlockedIdempotencySHA256[i] = SHA256([]byte(fmt.Sprintf("history-key-%06d", i)))
	}
	large.writeReceipt(t)
	if info, err := os.Stat(large.path); err != nil || info.Size() < 700<<10 {
		t.Fatalf("large receipt fixture too small: info=%v error=%v", info, err)
	}
	if err := WriteMarker(mailbox.Root, Marker{OperationID: large.receipt.OperationID, OwnerLane: "qas", ReceiptSHA256: large.digest,
		Device: mailbox.Device, Inode: mailbox.Inode}); err != nil {
		t.Fatal(err)
	}
	largeLease, err := large.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer largeLease.Close()
	// Full acquisition still builds the replay fences from every history
	// entry. Repeated idle checks must not allocate one object per entry.
	if err := largeLease.CheckRequest("project", mailbox.BlockedRequestIDs[len(mailbox.BlockedRequestIDs)-1], "fresh-key"); !errors.Is(err, ErrReplay) {
		t.Fatalf("last historical request was not fenced: %v", err)
	}
	if err := largeLease.CheckRequest("project", "req-fresh", "history-key-005163"); !errors.Is(err, ErrReplay) {
		t.Fatalf("last historical key was not fenced: %v", err)
	}
	checkAllocs := func(lease *Lease) float64 {
		return testing.AllocsPerRun(3, func() {
			if err := lease.Check("project"); err != nil {
				t.Fatal(err)
			}
		})
	}
	smallAllocs, largeAllocs := checkAllocs(smallLease), checkAllocs(largeLease)
	t.Logf("ownership check allocations: small=%.0f large=%.0f", smallAllocs, largeAllocs)
	// Parsing/rebuilding >10,000 history entries grows by thousands instead.
	if largeAllocs > smallAllocs+64 {
		t.Fatalf("ownership check allocations scale with historical entries: small=%.0f large=%.0f", smallAllocs, largeAllocs)
	}
	benchmark := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := largeLease.Check("project"); err != nil {
				b.Fatal(err)
			}
		}
	})
	t.Logf("large receipt guard: %s %s", benchmark.String(), benchmark.MemString())
	// Guard calls must not allocate history-sized byte buffers, even if they
	// avoid decoding the JSON. The lease owns one reusable streaming buffer.
	if benchmark.AllocedBytesPerOp() > 64<<10 {
		t.Fatalf("large receipt check allocated %d bytes per call", benchmark.AllocedBytesPerOp())
	}
}

func TestHandoffRejectsSymlinksHardlinksAndUnsafeModes(t *testing.T) {
	for _, change := range []string{"receipt-symlink", "marker-symlink", "lock-symlink", "ancestor-symlink", "receipt-hardlink", "receipt-mode", "root-mode", "ancestor-mode"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			root := f.receipt.Mailboxes[0].Root
			switch change {
			case "receipt-symlink", "marker-symlink", "lock-symlink":
				path := f.path
				if change == "marker-symlink" {
					path = filepath.Join(root, MarkerFileName)
				}
				if change == "lock-symlink" {
					path = filepath.Join(root, LockFileName)
				}
				if err := os.Rename(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatal(err)
				}
			case "ancestor-symlink":
				parent := filepath.Dir(root)
				if err := os.Rename(parent, parent+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-real", parent); err != nil {
					t.Fatal(err)
				}
			case "receipt-hardlink":
				if err := os.Link(f.path, f.path+"-copy"); err != nil {
					t.Fatal(err)
				}
			case "receipt-mode":
				if err := os.Chmod(f.path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "root-mode":
				if err := os.Chmod(root, 0o755); err != nil {
					t.Fatal(err)
				}
			case "ancestor-mode":
				if err := os.Chmod(filepath.Dir(root), 0o770); err != nil {
					t.Fatal(err)
				}
			}
			if lease, err := f.acquire(); err == nil {
				_ = lease.Close()
				t.Fatal("unsafe boundary acquired")
			}
		})
	}
}

func TestHandoffMarkerTransitionsRequireNoRuntimeOwner(t *testing.T) {
	f := newFixture(t)
	lease, err := f.acquire()
	if err != nil {
		t.Fatal(err)
	}
	m := f.receipt.Mailboxes[0]
	marker := Marker{OperationID: f.receipt.OperationID, OwnerLane: "none", ReceiptSHA256: f.digest, Device: m.Device, Inode: m.Inode}
	if err := WriteMarker(m.Root, marker); !errors.Is(err, ErrBusy) {
		t.Fatalf("live owner marker update error=%v", err)
	}
	_ = lease.Close()
	if err := WriteMarker(m.Root, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.acquire(); !errors.Is(err, ErrOwnership) {
		t.Fatalf("transition marker error=%v", err)
	}
	if exists, err := MarkerExists(m.Root); err != nil || !exists {
		t.Fatalf("marker discovery exists=%v error=%v", exists, err)
	}
	if exists, err := MarkerExists(filepath.Join(f.base, "missing")); err != nil || exists {
		t.Fatalf("absent root exists=%v error=%v", exists, err)
	}
	if exists, err := MarkerExists(filepath.Join(f.receipt.TargetServiceRoot, "mailbox")); err != nil || exists {
		t.Fatalf("native root exists=%v error=%v", exists, err)
	}
	wrong := marker
	wrong.OperationID = strings.Repeat("d", 32)
	if err := RemoveMarker(m.Root, wrong); !errors.Is(err, ErrOwnership) {
		t.Fatalf("wrong return marker error=%v", err)
	}
	for _, name := range []string{".runner-mailbox-retired", ".runner-mailbox-migration-fence"} {
		if err := os.WriteFile(filepath.Join(m.Root, name), []byte("preserved"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveMarker(m.Root, marker); err != nil {
		t.Fatal(err)
	}
	if exists, err := MarkerExists(m.Root); err != nil || exists {
		t.Fatalf("returned marker exists=%v error=%v", exists, err)
	}
	for _, name := range []string{".runner-mailbox-retired", ".runner-mailbox-migration-fence"} {
		raw, err := os.ReadFile(filepath.Join(m.Root, name))
		if err != nil || string(raw) != "preserved" {
			t.Fatalf("unrelated fence %s changed", name)
		}
	}
}

func TestHandoffReceiptRejectsAmbiguousOrEscapingMetadata(t *testing.T) {
	for _, change := range []string{"source-lane", "target-lane", "operation", "archive", "duplicate-root", "raw-key", "alias"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			r := f.receipt
			switch change {
			case "source-lane":
				r.SourceLane = "dev"
			case "target-lane":
				r.TargetLane = "prd"
			case "operation":
				r.OperationID = "../other"
			case "archive":
				r.Mailboxes[0].ArchiveRelative = "../outside"
			case "duplicate-root":
				r.Mailboxes = append(r.Mailboxes, r.Mailboxes[0])
			case "raw-key":
				r.Mailboxes[0].BlockedIdempotencySHA256 = []string{"raw-secret"}
			case "alias":
				r.EnvironmentAliases["bad\x00name"] = "qas"
			}
			if err := ValidateReceipt(r); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid receipt error=%v", err)
			}
		})
	}
}

func TestHandoffAllowsExactPRDNativeMailboxesButNoOtherServicePaths(t *testing.T) {
	for _, sourceID := range []string{"default", "analytics"} {
		t.Run(sourceID, func(t *testing.T) {
			f := newFixture(t)
			m := &f.receipt.Mailboxes[0]
			m.SourceID = sourceID
			m.TargetID = "prd-" + sourceID
			m.Root = filepath.Join(f.receipt.SourceServiceRoot, "mailboxes", sourceID)
			if sourceID == "default" {
				m.Root = filepath.Join(f.receipt.SourceServiceRoot, "mailbox")
			}
			if err := os.MkdirAll(m.Root, 0o700); err != nil {
				t.Fatal(err)
			}
			var stat unix.Stat_t
			if err := unix.Stat(m.Root, &stat); err != nil {
				t.Fatal(err)
			}
			m.Device, m.Inode = uint64(stat.Dev), uint64(stat.Ino)
			f.definitions[1] = Definition{ID: m.TargetID, Root: m.Root}
			f.writeReceipt(t)
			if err := WriteMarker(m.Root, Marker{OperationID: f.receipt.OperationID, OwnerLane: "qas", ReceiptSHA256: f.digest, Device: m.Device, Inode: m.Inode}); err != nil {
				t.Fatal(err)
			}
			lease, err := f.acquire()
			if err != nil {
				t.Fatalf("exact native source root refused: %v", err)
			}
			_ = lease.Close()
			m.Root = filepath.Join(f.receipt.SourceServiceRoot, "state")
			if err := ValidateReceipt(f.receipt); !errors.Is(err, ErrInvalid) {
				t.Fatalf("protected state root error=%v", err)
			}
		})
	}
}
