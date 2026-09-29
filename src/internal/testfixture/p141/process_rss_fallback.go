//go:build !darwin

package p141fixture

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func processRSSBytes(processID int, groups map[int]struct{}) (int64, error) {
	output, err := exec.Command("ps", "-axo", "pid=,pgid=,rss=").Output()
	if err != nil {
		return 0, fmt.Errorf("sample fixture process RSS: %w", err)
	}
	var total int64
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		pgid, groupErr := strconv.Atoi(fields[1])
		rssKB, rssErr := strconv.ParseInt(fields[2], 10, 64)
		if pidErr != nil || groupErr != nil || rssErr != nil {
			continue
		}
		if pid == processID {
			total += rssKB * 1024
			continue
		}
		if _, owned := groups[pgid]; owned {
			total += rssKB * 1024
		}
	}
	if total <= 0 {
		return 0, fmt.Errorf("P141 process RSS sample is empty for test PID %d", processID)
	}
	return total, nil
}
