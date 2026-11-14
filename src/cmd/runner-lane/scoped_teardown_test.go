package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"remote-session-runner/src/internal/laneinstall"
)

const scopedTestPair = "sess-00000000000000000000000000000001:cmd-00000000000000000000000000000001"

type scopedCommandFixture struct {
	called  int
	request laneinstall.CatalogPRDScopedTeardownRequest
	plan    bool
	fail    bool
}

func (f *scopedCommandFixture) RunScopedTeardown(_ context.Context, request laneinstall.CatalogPRDScopedTeardownRequest, plan bool, stdout, _ io.Writer) error {
	f.called++
	f.request, f.plan = request, plan
	if f.fail {
		return errors.New("scoped fixture refused preservation drift")
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"mode": "scoped_completed_teardown", "applied": !plan,
		"preserved_inventory_sha256": request.ExpectedPreservedInventorySHA256,
		"preserved_ownership_sha256": request.ExpectedPreservedOwnershipSHA256,
		"target_ownership_sha256":    request.ExpectedTargetOwnershipSHA256,
	})
}

func scopedCommandArgs(mode string) []string {
	args := []string{"catalog-prd-recover-scoped-teardown", "--host", "mac-control-plane",
		"--teardown-pair", scopedTestPair, "--release-id", "rr-scoped-fixture",
		"--release-sha256", strings.Repeat("a", 64), "--bundle-sha256", strings.Repeat("b", 64)}
	if mode != "" {
		args = append(args, "--"+mode)
	}
	if mode == "apply" {
		args = append(args, "--configuration-sha256", strings.Repeat("c", 64),
			"--backup-dir", "/private/scoped-fixture-backup", "--backup-manifest-sha256", strings.Repeat("d", 64),
			"--preserved-inventory-sha256", strings.Repeat("e", 64), "--preserved-ownership-sha256", strings.Repeat("f", 64),
			"--target-ownership-sha256", strings.Repeat("1", 64))
	}
	return args
}

func TestScopedTeardownCommandForwardsExactBoundRequest(t *testing.T) {
	for _, mode := range []string{"plan", "apply"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &scopedCommandFixture{}
			scopedTeardownCommandFixtureForTests = fixture
			t.Cleanup(func() { scopedTeardownCommandFixtureForTests = nil })
			var stdout, stderr bytes.Buffer
			if status := run(scopedCommandArgs(mode), &stdout, &stderr); status != 0 {
				t.Fatalf("status=%d stderr=%s", status, stderr.String())
			}
			if fixture.called != 1 || fixture.plan != (mode == "plan") || fixture.request.TeardownPair != scopedTestPair ||
				fixture.request.ReleaseID != "rr-scoped-fixture" || fixture.request.ExpectedReleaseSHA256 != strings.Repeat("a", 64) ||
				fixture.request.ExpectedBundleSHA256 != strings.Repeat("b", 64) {
				t.Fatalf("wrong request: %+v", fixture)
			}
			if mode == "apply" && (fixture.request.ExpectedPreservedInventorySHA256 != strings.Repeat("e", 64) ||
				fixture.request.ExpectedPreservedOwnershipSHA256 != strings.Repeat("f", 64) ||
				fixture.request.ExpectedTargetOwnershipSHA256 != strings.Repeat("1", 64) ||
				fixture.request.ExpectedConfigurationSHA256 != strings.Repeat("c", 64) ||
				fixture.request.ExpectedBackupManifestSHA != strings.Repeat("d", 64)) {
				t.Fatal("apply lost its explicit preservation/configuration/backup bindings")
			}
		})
	}
}

func TestScopedTeardownCommandRefusesAmbiguousOrUnboundInputBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing mode", scopedCommandArgs("")},
		{"both modes", append(scopedCommandArgs("apply"), "--plan")},
		{"duplicate target", append(scopedCommandArgs("plan"), "--teardown-pair", scopedTestPair)},
		{"foreign host", append(scopedCommandArgs("plan"), "--host", "linux-host")},
		{"legacy lost mode", append(scopedCommandArgs("plan"), "--lost-pair", scopedTestPair)},
		{"bad pair", append(scopedCommandArgs("plan")[:3], "--teardown-pair", "not-a-pair", "--release-id", "rr-scoped-fixture", "--release-sha256", strings.Repeat("a", 64), "--bundle-sha256", strings.Repeat("b", 64), "--plan")},
		{"invalid digest", append(scopedCommandArgs("plan"), "--bundle-sha256", "not-a-sha256")},
		{"apply without package", append(scopedCommandArgs("plan")[:len(scopedCommandArgs("plan"))-1], "--apply")},
		{"apply without target binding", scopedCommandArgs("apply")[:len(scopedCommandArgs("apply"))-2]},
		{"archive path without digest", append(scopedCommandArgs("apply"), "--runtime-archive-dir", "/private/original-archive")},
		{"archive digest without path", append(scopedCommandArgs("apply"), "--runtime-archive-manifest-sha256", strings.Repeat("2", 64))},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &scopedCommandFixture{}
			scopedTeardownCommandFixtureForTests = fixture
			t.Cleanup(func() { scopedTeardownCommandFixtureForTests = nil })
			var stdout, stderr bytes.Buffer
			if status := run(test.args, &stdout, &stderr); status == 0 || fixture.called != 0 || stdout.Len() != 0 {
				t.Fatalf("unsafe input reached backend: status=%d calls=%d stdout=%s", status, fixture.called, stdout.String())
			}
		})
	}
}

func TestScopedTeardownCommandPreservesOriginalArchiveBindingsOnRetry(t *testing.T) {
	fixture := &scopedCommandFixture{}
	scopedTeardownCommandFixtureForTests = fixture
	t.Cleanup(func() { scopedTeardownCommandFixtureForTests = nil })
	args := append(scopedCommandArgs("apply"), "--runtime-archive-dir", "/private/original-scoped-archive",
		"--runtime-archive-manifest-sha256", strings.Repeat("2", 64))
	var stdout, stderr bytes.Buffer
	if status := run(args, &stdout, &stderr); status != 0 || fixture.called != 1 ||
		fixture.request.RuntimeArchiveDirectory != "/private/original-scoped-archive" ||
		fixture.request.ExpectedRuntimeArchiveManifestSHA256 != strings.Repeat("2", 64) {
		t.Fatalf("retry lost original archive binding: status=%d request=%+v stderr=%s", status, fixture.request, stderr.String())
	}
}

// The environment switch exists only in this test-binary helper, never in the
// production command. It exercises public command dispatch and exact bindings.
func TestScopedTeardownCommandHelperProcess(t *testing.T) {
	if os.Getenv("RSR_SCOPED_TEST_PROCESS") != "1" {
		return
	}
	fixture := &scopedCommandFixture{fail: os.Getenv("RSR_SCOPED_TEST_REFUSE") == "1"}
	scopedTeardownCommandFixtureForTests = fixture
	for index, arg := range os.Args {
		if arg == "--" {
			os.Exit(run(os.Args[index+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func TestScopedTeardownPublicCommandFixtureAndRefusal(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		args := append([]string{"-test.run=^TestScopedTeardownCommandHelperProcess$", "--"}, scopedCommandArgs("apply")...)
		command := exec.Command(os.Args[0], args...)
		command.Env = append(os.Environ(), "RSR_SCOPED_TEST_PROCESS=1")
		if refuse {
			command.Env = append(command.Env, "RSR_SCOPED_TEST_REFUSE=1")
		}
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if refuse {
			if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "preservation drift") {
				t.Fatalf("refusal became success: err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			continue
		}
		var report map[string]any
		if err != nil || json.Unmarshal(stdout.Bytes(), &report) != nil || report["applied"] != true ||
			report["preserved_inventory_sha256"] != strings.Repeat("e", 64) || report["preserved_ownership_sha256"] != strings.Repeat("f", 64) ||
			report["target_ownership_sha256"] != strings.Repeat("1", 64) {
			t.Fatalf("lost public bindings: err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	}
}
