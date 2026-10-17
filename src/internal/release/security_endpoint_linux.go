//go:build linux

package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func inspectUnixPeer(conn *net.UnixConn, expectation peerExpectation) (observedPeer, error) {
	if expectation.goos != "linux" {
		return observedPeer{}, fmt.Errorf("%w: platform expectation mismatch", ErrSecurityEndpointDenied)
	}
	var peer observedPeer
	raw, err := conn.SyscallConn()
	if err != nil {
		return peer, err
	}
	fdCopy := -1
	var inspectErr error
	controlErr := raw.Control(func(fd uintptr) {
		fdCopy, inspectErr = unix.Dup(int(fd))
		if inspectErr == nil {
			unix.CloseOnExec(fdCopy)
		}
	})
	if controlErr != nil {
		return observedPeer{}, controlErr
	}
	if inspectErr != nil {
		return observedPeer{}, inspectErr
	}
	defer unix.Close(fdCopy)
	return inspectLinuxPeerFD(fdCopy, expectation)
}

func inspectLinuxPeerFD(fd int, expectation peerExpectation) (observedPeer, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred == nil || cred.Pid <= 0 {
		return observedPeer{}, fmt.Errorf("%w: unavailable Linux socket peer credentials: %v", ErrSecurityEndpointDenied, err)
	}
	peer := observedPeer{uid: cred.Uid, pid: int(cred.Pid)}
	if expectation.kind == SecurityEndpointObserve {
		return peer, nil
	}
	if expectation.kind != SecurityEndpointRelease || !validLinuxTransportExpectation(expectation) {
		return observedPeer{}, fmt.Errorf("%w: invalid Linux endpoint policy", ErrSecurityEndpointDenied)
	}
	pidfd, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return observedPeer{}, fmt.Errorf("%w: kernel cannot pin the release transport peer pid: %v", ErrSecurityEndpointDenied, err)
	}
	defer unix.Close(pidfd)
	if err := unix.PidfdSendSignal(pidfd, 0, nil, 0); err != nil {
		return observedPeer{}, fmt.Errorf("%w: release transport peer exited", ErrSecurityEndpointDenied)
	}
	serviceAccount, err := linuxDedicatedServiceAccount(cred.Uid, expectation.transportUser)
	if err != nil || !serviceAccount {
		return observedPeer{}, fmt.Errorf("%w: peer does not use the dedicated non-login service account", ErrSecurityEndpointDenied)
	}
	cgroup, err := linuxUnifiedCgroup(cred.Pid)
	if err != nil || cgroup != expectation.transportControlGroup {
		return observedPeer{}, fmt.Errorf("%w: peer cgroup does not match the fixed transport unit", ErrSecurityEndpointDenied)
	}
	unitPID, err := linuxSystemdMainPID(expectation.transportUnit)
	if err != nil || unitPID != int(cred.Pid) {
		return observedPeer{}, fmt.Errorf("%w: peer is not the main process of the fixed systemd unit", ErrSecurityEndpointDenied)
	}
	path, fileUID, mode, dev, inode, digest, err := linuxPeerExecutable(cred.Pid, expectation.transportExecutable)
	if err != nil {
		return observedPeer{}, fmt.Errorf("%w: installed transport executable identity could not be verified", ErrSecurityEndpointDenied)
	}
	if err := unix.PidfdSendSignal(pidfd, 0, nil, 0); err != nil {
		return observedPeer{}, fmt.Errorf("%w: release transport peer exited during identity check", ErrSecurityEndpointDenied)
	}
	cgroupAgain, err := linuxUnifiedCgroup(cred.Pid)
	if err != nil || cgroupAgain != cgroup {
		return observedPeer{}, fmt.Errorf("%w: peer changed cgroup during identity check", ErrSecurityEndpointDenied)
	}
	peer.unit = expectation.transportUnit
	peer.controlGroup = cgroup
	peer.executablePath = path
	peer.executableUID = fileUID
	peer.executableMode = mode
	peer.executableDev = dev
	peer.executableInode = inode
	peer.executableHash = digest
	peer.serviceAccount = true
	return peer, nil
}

func linuxDedicatedServiceAccount(uid uint32, expectedName string) (bool, error) {
	if expectedName != "runner-release-transport" || uid == 0 {
		return false, nil
	}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return false, err
	}
	var foundName, foundUID bool
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		fields := bytes.Split(line, []byte{':'})
		if len(fields) != 7 {
			continue
		}
		name := string(fields[0])
		parsedUID, parseErr := strconv.ParseUint(string(fields[2]), 10, 32)
		if parseErr != nil {
			continue
		}
		if name == expectedName {
			if uint32(parsedUID) != uid || (string(fields[6]) != "/usr/sbin/nologin" && string(fields[6]) != "/sbin/nologin" && string(fields[6]) != "/bin/false") {
				return false, nil
			}
			foundName = true
		}
		if uint32(parsedUID) == uid {
			if name != expectedName {
				return false, nil
			}
			foundUID = true
		}
	}
	return foundName && foundUID, nil
}

func linuxUnifiedCgroup(pid int32) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(int(pid)), "cgroup"))
	if err != nil {
		return "", err
	}
	var unified string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || fields[0] != "0" || fields[1] != "" || !filepath.IsAbs(fields[2]) || filepath.Clean(fields[2]) != fields[2] {
			continue
		}
		if unified != "" {
			return "", errors.New("multiple unified cgroup records")
		}
		unified = fields[2]
	}
	if unified == "" {
		return "", errors.New("unified cgroup-v2 membership is absent")
	}
	return unified, nil
}

func linuxSystemdMainPID(unit string) (int, error) {
	if !linuxServiceUnitPattern.MatchString(unit) {
		return 0, errors.New("invalid systemd unit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=MainPID", "--value", unit).Output()
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(output))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return 0, errors.New("systemd returned invalid MainPID")
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return 0, errors.New("systemd MainPID is not a live process ID")
	}
	return pid, nil
}

func linuxPeerExecutable(pid int32, expectedPath string) (string, uint32, os.FileMode, uint64, uint64, string, error) {
	if !filepath.IsAbs(expectedPath) || filepath.Clean(expectedPath) != expectedPath {
		return "", 0, 0, 0, 0, "", errors.New("invalid expected executable path")
	}
	procPath := filepath.Join("/proc", strconv.Itoa(int(pid)), "exe")
	actualPath, err := os.Readlink(procPath)
	if err != nil || actualPath != expectedPath {
		return "", 0, 0, 0, 0, "", errors.New("peer executable path mismatch")
	}
	processFile, err := os.Open(procPath)
	if err != nil {
		return "", 0, 0, 0, 0, "", err
	}
	defer processFile.Close()
	processInfo, err := processFile.Stat()
	if err != nil {
		return "", 0, 0, 0, 0, "", err
	}
	processStat, ok := processInfo.Sys().(*syscall.Stat_t)
	if !ok || !processInfo.Mode().IsRegular() || processStat.Uid != 0 || processInfo.Mode().Perm()&0o022 != 0 || processInfo.Mode()&0o7000 != 0 {
		return "", 0, 0, 0, 0, "", errors.New("peer executable is not an immutable root-owned regular file")
	}
	if err := linuxRootOwnedPath(expectedPath); err != nil {
		return "", 0, 0, 0, 0, "", err
	}
	pathFile, err := os.Open(expectedPath)
	if err != nil {
		return "", 0, 0, 0, 0, "", err
	}
	defer pathFile.Close()
	pathInfo, err := pathFile.Stat()
	if err != nil {
		return "", 0, 0, 0, 0, "", err
	}
	pathStat, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || pathStat.Dev != processStat.Dev || pathStat.Ino != processStat.Ino || pathStat.Uid != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm()&0o022 != 0 || pathInfo.Mode()&0o7000 != 0 {
		return "", 0, 0, 0, 0, "", errors.New("installed executable path no longer identifies the peer's executable inode")
	}
	if _, err := processFile.Seek(0, io.SeekStart); err != nil {
		return "", 0, 0, 0, 0, "", err
	}
	if processInfo.Size() <= 0 {
		return "", 0, 0, 0, 0, "", errors.New("peer executable could not be measured")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, processFile); err != nil {
		return "", 0, 0, 0, 0, "", errors.New("peer executable could not be measured")
	}
	return actualPath, processStat.Uid, processInfo.Mode(), uint64(processStat.Dev), processStat.Ino, hex.EncodeToString(digest.Sum(nil)), nil
}

func linuxRootOwnedPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid executable path")
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			return errors.New("empty executable path component")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("executable path contains a missing component or symlink")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("executable path component is not root-owned and immutable")
		}
		if current != path && !info.IsDir() {
			return errors.New("executable path component is not a directory")
		}
	}
	return nil
}
