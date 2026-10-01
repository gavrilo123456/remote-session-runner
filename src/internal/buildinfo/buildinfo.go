// Package buildinfo exposes the source revision embedded in a Runner binary.
//
// Release installers set SourceRevision with Go's -ldflags -X option. The
// default marks ordinary developer and test builds as unattested rather than
// pretending they attest to a checked-in source revision.
package buildinfo

const unattestedRevision = "unattested"

// SourceRevision is intentionally a string variable so Go's linker can set it
// with -X remote-session-runner/src/internal/buildinfo.SourceRevision=<sha>.
// Do not set it from runtime configuration or request input.
var SourceRevision = unattestedRevision

// Revision returns the only revision that may be reported by Runner health
// and version surfaces. A malformed linker value is reported as unattested
// so it cannot be mistaken for a source-commit attestation.
func Revision() string {
	return normalizeRevision(SourceRevision)
}

func normalizeRevision(value string) string {
	if isFullLowerHexCommit(value) {
		return value
	}
	return unattestedRevision
}

func isFullLowerHexCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
