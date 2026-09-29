//go:build darwin && cgo

package p141fixture

/*
#cgo LDFLAGS: -lproc
#include <errno.h>
#include <libproc.h>
#include <stdlib.h>
#include <sys/proc_info.h>
#include <sys/types.h>

static long long rsr_process_group_rss(int self_pid, const int *groups, int group_count, int *found_groups, int *error_code) {
	for (int attempt = 0; attempt < 3; attempt++) {
		int needed = proc_listpids(PROC_ALL_PIDS, 0, NULL, 0);
		if (needed <= 0) {
			*error_code = errno;
			return -1;
		}
		int capacity = needed + (int)(64 * sizeof(pid_t));
		pid_t *pids = (pid_t *)calloc(1, (size_t)capacity);
		if (pids == NULL) {
			*error_code = ENOMEM;
			return -1;
		}
		int bytes = proc_listpids(PROC_ALL_PIDS, 0, pids, capacity);
		if (bytes < 0 || bytes > capacity) {
			free(pids);
			continue;
		}
		long long total = 0;
		int self_seen = 0;
		for (int index = 0; index < bytes / (int)sizeof(pid_t); index++) {
			pid_t pid = pids[index];
			if (pid <= 0) continue;
			struct proc_bsdinfo bsd;
			int bsd_bytes = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &bsd, sizeof(bsd));
			if (bsd_bytes != sizeof(bsd)) continue;
			int group_index = -1;
			for (int group = 0; group < group_count; group++) {
				if (bsd.pbi_pgid == groups[group]) {
					group_index = group;
					break;
				}
			}
			if (pid != self_pid && group_index < 0) continue;
			struct proc_taskinfo task;
			int task_bytes = proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &task, sizeof(task));
			if (task_bytes != sizeof(task)) continue;
			total += (long long)task.pti_resident_size;
			if (pid == self_pid) self_seen = 1;
			if (group_index >= 0) found_groups[group_index] = 1;
		}
		free(pids);
		if (!self_seen) {
			*error_code = ESRCH;
			return -1;
		}
		*error_code = 0;
		return total;
	}
	*error_code = EAGAIN;
	return -1;
}
*/
import "C"

import (
	"fmt"
)

func processRSSBytes(processID int, groups map[int]struct{}) (int64, error) {
	groupIDs := make([]C.int, 0, len(groups))
	for groupID := range groups {
		groupIDs = append(groupIDs, C.int(groupID))
	}
	foundGroups := make([]C.int, len(groupIDs))
	var groupPointer, foundPointer *C.int
	if len(groupIDs) != 0 {
		groupPointer = &groupIDs[0]
		foundPointer = &foundGroups[0]
	}
	var errorCode C.int
	rss := C.rsr_process_group_rss(C.int(processID), groupPointer, C.int(len(groupIDs)), foundPointer, &errorCode)
	if rss < 0 {
		return 0, fmt.Errorf("sample fixture process RSS via proc_pidinfo: errno=%d", int(errorCode))
	}
	for index, found := range foundGroups {
		if found == 0 {
			return 0, fmt.Errorf("sample fixture process RSS: process group %d was not visible", int(groupIDs[index]))
		}
	}
	if rss <= 0 {
		return 0, fmt.Errorf("P141 process RSS is empty for test PID %d", processID)
	}
	return int64(rss), nil
}
