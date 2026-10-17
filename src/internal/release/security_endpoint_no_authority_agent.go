//go:build !darwin || !cgo

package release

import (
	"fmt"
	"net"
)

func inspectAuthorityAgentPeer(*net.UnixConn, authorityAgentIdentity) (authorityAgentEvidence, error) {
	return authorityAgentEvidence{}, fmt.Errorf("%w: Mac authority-agent peer inspection requires Darwin Security.framework support", ErrSecurityEndpointUnavailable)
}
