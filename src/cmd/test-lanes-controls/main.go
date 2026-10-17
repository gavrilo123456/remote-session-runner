package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"remote-session-runner/src/internal/lanefixtures"
	"remote-session-runner/src/internal/lanetests"
)

func main() {
	unit := flag.String("unit", "", "exact F-tier LANES unit (L2-07 or L2-08)")
	host := flag.String("host", "", "exact canonical host ID")
	fixture := flag.String("fixture-manifest", "", "owner-approved fixture manifest outside the checkout")
	approvedSHA := flag.String("approved-sha256", "", "exact manifest SHA-256 recorded in the A01 owner approval")
	flag.Parse()
	if *unit == "" || *host == "" || *fixture == "" || *approvedSHA == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: test-lanes-controls --unit LANES-L2-07|LANES-L2-08 --host <canonical-host> --fixture-manifest <approved-path> --approved-sha256 sha256:<digest>")
		os.Exit(2)
	}
	root, err := lanetests.FindRepoRoot(".")
	if err != nil {
		fail(err)
	}
	goBinary := os.Getenv("LANES_TEST_GO")
	if goBinary == "" {
		fail(fmt.Errorf("LANES_TEST_GO must name the repository Go 1.27.1 toolchain"))
	}
	manifest := filepath.Join(root, "040-implementation-evidence", "LANES-test-coverage.json")
	handbook := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	if runtime.GOOS == "linux" {
		approved, err := lanefixtures.LoadApproved(*fixture, *approvedSHA, root, *unit, *host, time.Now())
		if err != nil {
			fail(err)
		}
		if err := lanefixtures.VerifyBeforeScope(approved); err != nil {
			fail(fmt.Errorf("fixture preflight before bounded scope: %w", err))
		}
		executable, err := os.Executable()
		if err != nil {
			fail(err)
		}
		reexecuted, err := lanefixtures.RunLinuxControlsInBoundedScope(approved, executable, os.Args[1:])
		if err != nil {
			fail(err)
		}
		if reexecuted {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	if err := lanetests.RunControls(ctx, root, manifest, handbook, goBinary, *unit, *host, *fixture, *approvedSHA, os.Stdout, nil); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "FAIL:", err)
	os.Exit(1)
}
