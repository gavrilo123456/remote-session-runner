package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"remote-session-runner/src/internal/lanetests"
)

func main() {
	unit := flag.String("unit", "", "exact LANES unit ID")
	tier := flag.String("tier", lanetests.HermeticTier, "exact proof tier (H or N)")
	flag.Parse()
	if *unit == "" || flag.NArg() != 0 || (*tier != lanetests.HermeticTier && *tier != lanetests.NativeTier) {
		fmt.Fprintln(os.Stderr, "usage: test-lanes --unit LANES-<unit> [--tier H|N]")
		os.Exit(2)
	}
	root, err := lanetests.FindRepoRoot(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	goBinary := os.Getenv("LANES_TEST_GO")
	if goBinary == "" {
		fmt.Fprintln(os.Stderr, "LANES_TEST_GO must name the repository Go 1.27.1 toolchain")
		os.Exit(2)
	}
	manifest := filepath.Join(root, "040-implementation-evidence", "LANES-test-coverage.json")
	handbook := filepath.Join(root, "040-improvments", "040-dev-qas-prd-three-machine-lanes", "020-dev-qas-prd-three-machine-lanes-execution-handbook.md")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	run := lanetests.RunHermetic
	if *tier == lanetests.NativeTier {
		run = lanetests.RunNative
	}
	if err := run(ctx, root, manifest, handbook, goBinary, *unit, os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}
