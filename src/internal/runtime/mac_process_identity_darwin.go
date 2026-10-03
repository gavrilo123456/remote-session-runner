//go:build darwin && cgo

package runtime

/*
#cgo LDFLAGS: -lproc
#include <errno.h>
#include <stdint.h>
#include <sys/proc.h>
#include <sys/proc_info.h>
#include <libproc.h>

typedef struct {
	int pid;
	int ppid;
	int pgid;
	int status;
	int uid;
} runner_short_proc;

static int runner_process_start_identity(int pid, uint64_t *identity) {
	struct proc_bsdinfo info;
	int result = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (result != sizeof(info)) {
		return -1;
	}
	*identity = ((uint64_t)info.pbi_start_tvsec * 1000000) + info.pbi_start_tvusec;
	return 0;
}

static int runner_list_pgrp_pids(int pgid, pid_t *pids, int bytes, int *errno_out) {
	errno = 0;
	int result = proc_listpgrppids((pid_t)pgid, pids, bytes);
	if (result < 0) {
		*errno_out = errno;
	}
	return result;
}

static int runner_short_proc_info(int pid, runner_short_proc *out, int *errno_out) {
	struct proc_bsdinfo info;
	errno = 0;
	int result = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (result == 0) {
		*errno_out = errno;
		return 0;
	}
	if (result != sizeof(info)) {
		*errno_out = errno;
		return -1;
	}
	out->pid = (int)info.pbi_pid;
	out->ppid = (int)info.pbi_ppid;
	out->pgid = (int)info.pbi_pgid;
	out->status = (int)info.pbi_status;
	out->uid = (int)info.pbi_uid;
	return 1;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"strconv"
	"syscall"
	"unsafe"
)

func inspectMacProcessStartIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid Mac process ID %d", pid)
	}
	var identity C.uint64_t
	if C.runner_process_start_identity(C.int(pid), &identity) != 0 || identity == 0 {
		return "", fmt.Errorf("libproc could not inspect start identity for PID %d", pid)
	}
	return strconv.FormatUint(uint64(identity), 10), nil
}

// inspectMacProcessGroupMembers returns one bounded process-group snapshot.
// A malformed or unavailable member fails closed; callers that intend to
// signal take their own fresh snapshot immediately before that signal.
func inspectMacProcessGroupMembers(processGroupID int) ([]macProcessGroupMember, error) {
	if processGroupID <= 0 {
		return nil, fmt.Errorf("invalid Mac process group ID %d", processGroupID)
	}
	const (
		initialCapacity = 64
		maxCapacity     = 4096
	)
	capacity := initialCapacity
	for {
		pids := make([]C.pid_t, capacity)
		var errno C.int
		bytes := int(unsafe.Sizeof(pids[0])) * len(pids)
		listed := int(C.runner_list_pgrp_pids(C.int(processGroupID), &pids[0], C.int(bytes), &errno))
		if listed < 0 {
			return nil, macProcessInspectionError("list process group", errno)
		}
		if listed > len(pids) {
			return nil, fmt.Errorf("list Mac process group %d: invalid member count %d", processGroupID, listed)
		}
		if listed == len(pids) {
			if capacity >= maxCapacity {
				return nil, fmt.Errorf("list Mac process group %d: bounded member snapshot is full", processGroupID)
			}
			capacity *= 2
			continue
		}

		members := make([]macProcessGroupMember, 0, listed)
		for _, pid := range pids[:listed] {
			if pid <= 0 {
				return nil, fmt.Errorf("inspect Mac process group %d: invalid listed PID %d", processGroupID, pid)
			}
			var info C.runner_short_proc
			errno = 0
			inspectResult := C.runner_short_proc_info(C.int(pid), &info, &errno)
			if inspectResult == 0 {
				// Darwin lists an unreaped zombie in proc_listpgrppids but
				// reports no BSD information for it (usually ESRCH). It cannot
				// execute, so preserve that specific absence as a zombie member.
				// A zero-length answer without ESRCH is uninspectable, not proof.
				if !errors.Is(syscall.Errno(errno), syscall.ESRCH) {
					return nil, macProcessInspectionError("inspect process-group member", errno)
				}
				members = append(members, macProcessGroupMember{PID: int(pid), ProcessGroupID: processGroupID, Status: macProcessStatusZombie})
				continue
			}
			if inspectResult < 0 {
				return nil, macProcessInspectionError("inspect process-group member", errno)
			}
			member := macProcessGroupMember{
				PID:            int(info.pid),
				ParentPID:      int(info.ppid),
				ProcessGroupID: int(info.pgid),
				UID:            int(info.uid),
				Status:         uint32(info.status),
			}
			if member.PID != int(pid) || member.ProcessGroupID != processGroupID {
				return nil, fmt.Errorf("inspect Mac process group %d: member identity changed", processGroupID)
			}
			members = append(members, member)
		}
		return members, nil
	}
}

func macProcessInspectionError(operation string, errno C.int) error {
	if errno != 0 {
		return fmt.Errorf("%s: %w", operation, syscall.Errno(errno))
	}
	return fmt.Errorf("%s: libproc returned incomplete process information", operation)
}
