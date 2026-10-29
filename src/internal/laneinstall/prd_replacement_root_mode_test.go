package laneinstall

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapRootModeCorrectionRecordsAreAppendOnlyAndBound(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	auth := PRDBootstrapRootModeCorrectionAuthorization{
		OperationID: prdBootstrapRootModeOperationID, RequestSHA256: digestBytes([]byte("request")),
		ApprovalSHA256: digestBytes([]byte("approval")), ReleaseID: "rr-example", ReleaseSHA256: digestBytes([]byte("release")),
		ContinuationSHA256: digestBytes([]byte("continuation")),
	}
	baseline := CatalogFreshInstallBaseline{OperationID: auth.OperationID,
		ConfigurationSHA256: digestBytes([]byte("config")), DatabaseSchema: 40,
		WorkInventorySHA256: (CatalogWorkInventory{}).SHA256(), EvidenceSHA256: digestBytes([]byte("baseline"))}
	observation := CatalogPRDFreshInstallObservation{Baseline: &baseline,
		ServiceRoot:              filepath.Join("/home", "ubuntu", ".local", "share", "remote-session-runner"),
		BootstrapJournalSHA256:   digestBytes([]byte("bootstrap journal")),
		NamespaceArtifactsSHA256: digestBytes([]byte("namespace artifacts")),
		RootDevice:               8, RootInode: 10, RootUID: uint32(os.Getuid()), RootGID: uint32(os.Getgid())}
	intent := rootModeCorrectionRecord("intent", auth, observation, time.Now().UTC())
	intentPath := filepath.Join(root, "intent.json")
	if err := writePRDBootstrapRootModeCorrectionRecord(intentPath, intent); err != nil {
		t.Fatalf("write durable intent: %v", err)
	}
	loadedIntent, found, err := readPRDBootstrapRootModeCorrectionRecord(intentPath)
	if err != nil || !found || loadedIntent != intent {
		t.Fatalf("read canonical intent: found=%t equal=%t err=%v", found, loadedIntent == intent, err)
	}
	if err := writePRDBootstrapRootModeCorrectionRecord(intentPath, intent); !errors.Is(err, os.ErrExist) {
		t.Fatalf("intent was overwritten instead of append-only: %v", err)
	}
	complete := rootModeCorrectionRecord("complete", auth, observation, time.Now().UTC())
	complete.IntentSHA256 = digestBytes(mustReadFileForRootModeTest(t, intentPath))
	complete.EvidenceSHA256 = rootModeCorrectionRecordSHA256(complete)
	completePath := filepath.Join(root, "complete.json")
	if err := writePRDBootstrapRootModeCorrectionRecord(completePath, complete); err != nil {
		t.Fatalf("write completion receipt: %v", err)
	}
	loadedComplete, found, err := readPRDBootstrapRootModeCorrectionRecord(completePath)
	if err != nil || !found || loadedComplete != complete ||
		!samePRDBootstrapRootModeCorrectionBindings(loadedComplete, loadedIntent) {
		t.Fatalf("completion is not bound to its intent: found=%t err=%v", found, err)
	}
	loadedComplete.ContinuationSHA256 = digestBytes([]byte("different continuation"))
	if samePRDBootstrapRootModeCorrectionBindings(loadedComplete, loadedIntent) {
		t.Fatal("conflicting continuation matched the original root-correction operation")
	}
}

func mustReadFileForRootModeTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
