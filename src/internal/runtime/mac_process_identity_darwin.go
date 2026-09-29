//go:build darwin && cgo

package runtime

/*
#cgo LDFLAGS: -lproc
#include <stdint.h>
#include <sys/proc_info.h>
#include <libproc.h>

static int runner_process_start_identity(int pid, uint64_t *identity) {
	struct proc_bsdinfo info;
	int result = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (result != sizeof(info)) {
		return -1;
	}
	*identity = ((uint64_t)info.pbi_start_tvsec * 1000000) + info.pbi_start_tvusec;
	return 0;
}
*/
import "C"

import (
	"fmt"
	"strconv"
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
