//go:build darwin && !cgo

package runtime

import "testing"

// The recovery implementation depends on Darwin's libproc view of process
// groups. Do not silently skip the required proof tests in a no-cgo build.
func TestBUG011MacLostRecoveryRequiresCGO(t *testing.T) {
	t.Fatal("BUG-011 Mac lost-runtime recovery requires cgo-enabled Darwin tests")
}
