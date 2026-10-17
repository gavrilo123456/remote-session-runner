//go:build darwin && cgo

package release

/*
#cgo LDFLAGS: -lbsm -framework Security -framework CoreFoundation
#include <errno.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <bsm/libbsm.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/SecCode.h>
#include <Security/SecRequirement.h>

static int rsr_peer_audit_token(int fd, uint32_t values[8]) {
	audit_token_t token;
	socklen_t length = sizeof(token);
	if (getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, &token, &length) != 0) return errno;
	if (length != sizeof(token)) return EINVAL;
	memcpy(values, &token, sizeof(token));
	return 0;
}

static pid_t rsr_audit_pid(const uint32_t values[8]) {
	audit_token_t token;
	memcpy(&token, values, sizeof(token));
	return audit_token_to_pid(token);
}

static uid_t rsr_audit_euid(const uint32_t values[8]) {
	audit_token_t token;
	memcpy(&token, values, sizeof(token));
	return audit_token_to_euid(token);
}

static int rsr_audit_code_valid(const uint32_t values[8], const char *text) {
	audit_token_t token;
	CFDataRef audit = NULL;
	CFStringRef requirementText = NULL;
	CFDictionaryRef attributes = NULL;
	SecCodeRef code = NULL;
	SecRequirementRef requirement = NULL;
	OSStatus status = errSecParam;
	memcpy(&token, values, sizeof(token));
	audit = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)&token, sizeof(token));
	requirementText = CFStringCreateWithCString(kCFAllocatorDefault, text, kCFStringEncodingUTF8);
	if (!audit || !requirementText) goto cleanup;
	const void *keys[] = { kSecGuestAttributeAudit };
	const void *vals[] = { audit };
	attributes = CFDictionaryCreate(kCFAllocatorDefault, keys, vals, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (!attributes) goto cleanup;
	status = SecRequirementCreateWithString(requirementText, kSecCSDefaultFlags, &requirement);
	if (status != errSecSuccess || !requirement) goto cleanup;
	status = SecCodeCopyGuestWithAttributes(NULL, attributes, kSecCSDefaultFlags, &code);
	if (status != errSecSuccess || !code) goto cleanup;
	status = SecCodeCheckValidity(code, kSecCSDefaultFlags, requirement);
cleanup:
	if (requirement) CFRelease(requirement);
	if (code) CFRelease(code);
	if (attributes) CFRelease(attributes);
	if (requirementText) CFRelease(requirementText);
	if (audit) CFRelease(audit);
	return status == errSecSuccess;
}
*/
import "C"

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

var launchdPIDLine = regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)\s*$`)

func inspectUnixPeer(conn *net.UnixConn, expectation peerExpectation) (observedPeer, error) {
	if expectation.goos != "darwin" {
		return observedPeer{}, fmt.Errorf("%w: platform or Mac peer expectation mismatch", ErrSecurityEndpointDenied)
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
	if expectation.kind == SecurityEndpointObserve {
		return inspectMacRunnerUIDPeer(fdCopy)
	}
	if !validMacPeerExpectation(expectation) {
		return observedPeer{}, fmt.Errorf("%w: incomplete Mac code identity expectation", ErrSecurityEndpointDenied)
	}
	var token [8]C.uint32_t
	if status := C.rsr_peer_audit_token(C.int(fdCopy), &token[0]); status != 0 {
		return observedPeer{}, fmt.Errorf("%w: LOCAL_PEERTOKEN unavailable (errno %d)", ErrSecurityEndpointDenied, int(status))
	}
	peer := observedPeer{}
	for i := range token {
		peer.auditToken[i] = uint32(token[i])
	}
	peer.pid = int(C.rsr_audit_pid(&token[0]))
	peer.uid = uint32(C.rsr_audit_euid(&token[0]))
	if peer.pid <= 0 {
		return peer, fmt.Errorf("%w: invalid audit-token PID", ErrSecurityEndpointDenied)
	}
	requirement := C.CString(expectation.codeRequirement)
	defer C.free(unsafe.Pointer(requirement))
	labelOK := C.rsr_audit_code_valid(&token[0], requirement)
	labelPID, labelErr := launchdPIDForLabel(expectation.launchdLabel)
	if labelOK == 0 {
		return peer, fmt.Errorf("%w: audit-token code identity failed Security.framework validation", ErrSecurityEndpointDenied)
	}
	peer.codeRequirement = expectation.codeRequirement
	peer.codeValid = true
	if labelErr != nil || labelPID != peer.pid {
		return peer, fmt.Errorf("%w: audit-token peer is not the named launchd job", ErrSecurityEndpointDenied)
	}
	peer.launchdLabel = expectation.launchdLabel
	return peer, nil
}

func inspectAuthorityAgentPeer(conn *net.UnixConn, expected authorityAgentIdentity) (authorityAgentEvidence, error) {
	if expected.UID != 0 || expected.LaunchdLabel != "runner-release-authority-agent" ||
		!validMacPeerExpectation(peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
			launchdLabel: expected.LaunchdLabel, codeRequirement: expected.CodeRequirement}) {
		return authorityAgentEvidence{}, fmt.Errorf("%w: invalid release-authority agent identity contract", ErrSecurityEndpointDenied)
	}
	peer, err := inspectUnixPeer(conn, peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
		launchdLabel: expected.LaunchdLabel, codeRequirement: expected.CodeRequirement})
	if err != nil || !peerAllowed(peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin",
		launchdLabel: expected.LaunchdLabel, codeRequirement: expected.CodeRequirement}, peer) || peer.uid != expected.UID {
		return authorityAgentEvidence{}, fmt.Errorf("%w: authenticated agent peer required", ErrSecurityEndpointDenied)
	}
	return authorityAgentEvidence{UID: peer.uid, PID: peer.pid, AuditTokenValidated: peer.codeValid,
		LaunchdLabel: peer.launchdLabel, CodeRequirement: peer.codeRequirement}, nil
}

func inspectMacRunnerUIDPeer(fd int) (observedPeer, error) {
	credentials, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil || credentials == nil {
		return observedPeer{}, fmt.Errorf("%w: LOCAL_PEERCRED unavailable: %v", ErrSecurityEndpointDenied, err)
	}
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil || pid <= 0 {
		return observedPeer{}, fmt.Errorf("%w: LOCAL_PEERPID unavailable: %v", ErrSecurityEndpointDenied, err)
	}
	return observedPeer{uid: credentials.Uid, pid: pid}, nil
}

func launchdPIDForLabel(label string) (int, error) {
	if !macLaunchdLabelPattern.MatchString(label) {
		return 0, fmt.Errorf("invalid launchd label")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/launchctl", "print", "system/"+label).Output()
	if err != nil {
		return 0, err
	}
	matches := launchdPIDLine.FindAllStringSubmatch(string(output), -1)
	if len(matches) != 1 {
		return 0, fmt.Errorf("launchd label is not running with one exact PID")
	}
	pid, err := strconv.Atoi(matches[0][1])
	if err != nil || pid <= 0 || strings.Contains(string(output), "state = waiting") || strings.Contains(string(output), "state = not running") {
		return 0, fmt.Errorf("launchd label has no active process")
	}
	return pid, nil
}
