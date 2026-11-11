//go:build darwin && cgo

package runtime

/*
#include <errno.h>
#include <stdint.h>
#include <libproc.h>
#include <sys/proc.h>
#include <sys/proc_info.h>
#include <sys/sysctl.h>

typedef struct {
	int pid;
	int ppid;
	int pgid;
	int uid;
	int status;
	uint64_t start;
} runner_cleanup_member;

static int runner_cleanup_list_group(int pgid, pid_t *pids, int bytes, int *error_out) {
	errno = 0;
	int result = proc_listpgrppids((pid_t)pgid, pids, bytes);
	*error_out = errno;
	return result;
}

// Full BSD information commonly rejects zombies with ESRCH on Darwin. A
// positive KERN_PROC_PID snapshot supplies the missing status and ownership;
// ESRCH alone is never converted into a synthetic zombie in normal cleanup.
static int runner_cleanup_inspect_member(int pid, runner_cleanup_member *out, int *error_out) {
	struct proc_bsdinfo info;
	errno = 0;
	int result = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (result == sizeof(info)) {
		out->pid = info.pbi_pid;
		out->ppid = info.pbi_ppid;
		out->pgid = info.pbi_pgid;
		out->uid = info.pbi_uid;
		out->status = info.pbi_status;
		out->start = (uint64_t)info.pbi_start_tvsec * 1000000 + info.pbi_start_tvusec;
		return 1;
	}
	if (result != 0 || errno != ESRCH) {
		*error_out = errno;
		return -1;
	}
	struct kinfo_proc kernel_info;
	int mib[4] = { CTL_KERN, KERN_PROC, KERN_PROC_PID, pid };
	size_t length = sizeof(kernel_info);
	errno = 0;
	if (sysctl(mib, 4, &kernel_info, &length, NULL, 0) != 0) {
		*error_out = errno;
		return -1;
	}
	if (length == 0) {
		*error_out = ESRCH;
		return 0;
	}
	if (length != sizeof(kernel_info)) {
		*error_out = 0;
		return -1;
	}
	out->pid = kernel_info.kp_proc.p_pid;
	out->ppid = kernel_info.kp_eproc.e_ppid;
	out->pgid = kernel_info.kp_eproc.e_pgid;
	out->uid = kernel_info.kp_eproc.e_ucred.cr_uid;
	out->status = kernel_info.kp_proc.p_stat;
	out->start = (uint64_t)kernel_info.kp_proc.p_starttime.tv_sec * 1000000 + kernel_info.kp_proc.p_starttime.tv_usec;
	return 1;
}
*/
import "C"

import (
	"fmt"
	"slices"
	"strconv"
	"unsafe"

	"remote-session-runner/src/internal/domain"
)

// inspectMacCleanupGroupMembers is intentionally separate from the historical
// lost-recovery inspector. It supplies positive native status/ownership proof
// for normal post-reap cleanup without changing any recovery signal semantics.
func inspectMacCleanupGroupMembers(groupID int) ([]domain.RuntimeCleanupMember, error) {
	return sampleMacCleanupGroup(groupID, listMacCleanupGroupPIDs, readMacCleanupGroupMember)
}

func readMacCleanupGroupMember(pid int) (domain.RuntimeCleanupMember, bool, error) {
	var info C.runner_cleanup_member
	var errno C.int
	result := C.runner_cleanup_inspect_member(C.int(pid), &info, &errno)
	if result == 0 {
		return domain.RuntimeCleanupMember{}, false, nil
	}
	if result != 1 {
		return domain.RuntimeCleanupMember{}, false, macCleanupInspectionFailure("group_inspection_failed", macProcessInspectionError("inspect cleanup group member", errno))
	}
	if int(info.pid) != pid {
		return domain.RuntimeCleanupMember{}, false, macCleanupInspectionFailure("group_identity_changed", ErrRuntimeOwnershipRecord)
	}
	member := domain.RuntimeCleanupMember{PID: int(info.pid), ParentPID: int(info.ppid),
		ProcessGroupID: int(info.pgid), UID: int(info.uid), Status: uint32(info.status)}
	if info.start != 0 {
		member.ProcessStartIdentity = strconv.FormatUint(uint64(info.start), 10)
	}
	return member, true, nil
}

func listMacCleanupGroupPIDs(groupID int) ([]int, error) {
	if groupID <= 0 {
		return nil, macCleanupInspectionFailure("group_identity_changed", ErrRuntimeOwnershipRecord)
	}
	for capacity := 64; capacity <= 4096; capacity *= 2 {
		pids := make([]C.pid_t, capacity)
		var errno C.int
		listed := int(C.runner_cleanup_list_group(C.int(groupID), &pids[0], C.int(len(pids)*int(unsafe.Sizeof(pids[0]))), &errno))
		if listed < 0 || errno != 0 {
			return nil, macCleanupInspectionFailure("group_inspection_failed", macProcessInspectionError("list cleanup group", errno))
		}
		if listed > capacity {
			return nil, macCleanupInspectionFailure("group_observation_incomplete", ErrRuntimeOwnershipRecord)
		}
		if listed == capacity {
			continue
		}
		members := make([]int, 0, listed)
		seen := make(map[int]bool, listed)
		for _, pid := range pids[:listed] {
			if pid <= 0 || seen[int(pid)] {
				return nil, macCleanupInspectionFailure("group_observation_incomplete", ErrRuntimeOwnershipRecord)
			}
			seen[int(pid)] = true
			members = append(members, int(pid))
		}
		slices.Sort(members)
		return members, nil
	}
	return nil, macCleanupInspectionFailure("group_observation_incomplete", fmt.Errorf("%w: bounded cleanup snapshot is full", ErrRuntimeOwnershipRecord))
}
