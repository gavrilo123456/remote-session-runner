//go:build darwin && cgo

package runtime

/*
#include <errno.h>
#include <libproc.h>
#include <sys/proc.h>
#include <sys/proc_info.h>

// This is a read-only native observation. In particular it does not use
// kill(pid, 0), and no signalling function is reachable from this helper.
static int runner_scoped_root_absent(int pid, int *errno_out) {
	struct proc_bsdinfo info;
	errno = 0;
	int size = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (size == sizeof(info)) return 0;
	*errno_out = errno;
	if (size == 0 && errno == ESRCH) return 1;
	return -1;
}
*/
import "C"

func inspectMacScopedRootAbsent(pid int) (bool, error) {
	if pid <= 0 {
		return false, scopedMacRefusal("invalid target PID")
	}
	var errno C.int
	result := C.runner_scoped_root_absent(C.int(pid), &errno)
	if result < 0 {
		return false, scopedMacRefusal("target root is uninspectable")
	}
	return result == 1, nil
}
