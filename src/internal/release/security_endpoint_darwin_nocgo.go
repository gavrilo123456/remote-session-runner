//go:build darwin && !cgo

package release

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func inspectUnixPeer(conn *net.UnixConn, expectation peerExpectation) (observedPeer, error) {
	if expectation.goos != "darwin" || expectation.kind != SecurityEndpointObserve {
		return observedPeer{}, fmt.Errorf("%w: macOS audit-token and code validation require cgo Security.framework support", ErrSecurityEndpointUnavailable)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return observedPeer{}, err
	}
	fdCopy := -1
	var copyErr error
	controlErr := raw.Control(func(fd uintptr) {
		fdCopy, copyErr = unix.Dup(int(fd))
		if copyErr == nil {
			unix.CloseOnExec(fdCopy)
		}
	})
	if controlErr != nil {
		return observedPeer{}, controlErr
	}
	if copyErr != nil {
		return observedPeer{}, copyErr
	}
	defer unix.Close(fdCopy)
	credentials, err := unix.GetsockoptXucred(fdCopy, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil || credentials == nil {
		return observedPeer{}, fmt.Errorf("%w: LOCAL_PEERCRED unavailable: %v", ErrSecurityEndpointDenied, err)
	}
	pid, err := unix.GetsockoptInt(fdCopy, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil || pid <= 0 {
		return observedPeer{}, fmt.Errorf("%w: LOCAL_PEERPID unavailable: %v", ErrSecurityEndpointDenied, err)
	}
	return observedPeer{uid: credentials.Uid, pid: pid}, nil
}
