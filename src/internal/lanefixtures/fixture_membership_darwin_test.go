//go:build darwin

package lanefixtures

import (
	"fmt"
	"testing"
)

func verifyFixtureWorkloadMembership(_ *testing.T, _ Manifest) error {
	return fmt.Errorf("Linux fixture workload membership is not available on macOS")
}
