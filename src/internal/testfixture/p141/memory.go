package p141fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MemoryStats struct {
	Samples                int
	BaselineAvailableBytes int64
	PeakFixtureRSSBytes    int64
	AvailableAfterBytes    int64
}

type memorySampler struct {
	workspaceRoot string
	processID     int
	stop          chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
	mu            sync.Mutex
	stats         MemoryStats
	err           error
}

func startMemorySampler(workspaceRoot string) (*memorySampler, error) {
	available, err := availableMemoryBytes()
	if err != nil {
		return nil, err
	}
	sampler := &memorySampler{
		workspaceRoot: workspaceRoot,
		processID:     os.Getpid(),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		stats:         MemoryStats{BaselineAvailableBytes: available},
	}
	sampler.sample()
	go func() {
		defer close(sampler.done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampler.stop:
				sampler.sample()
				return
			case <-ticker.C:
				sampler.sample()
			}
		}
	}()
	return sampler, nil
}

func (s *memorySampler) Stop() (MemoryStats, error) {
	s.stopOnce.Do(func() {
		close(s.stop)
		<-s.done
	})
	after, afterErr := availableMemoryBytes()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.AvailableAfterBytes = after
	if s.err != nil {
		return s.stats, s.err
	}
	if afterErr != nil {
		return s.stats, afterErr
	}
	return s.stats, nil
}

func (s *memorySampler) sample() {
	groups, err := ownedProcessGroups(s.workspaceRoot)
	if err != nil {
		s.mu.Lock()
		if s.err == nil {
			s.err = err
		}
		s.mu.Unlock()
		return
	}
	rss, err := processRSSBytes(s.processID, groups)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		return
	}
	s.stats.Samples++
	if rss > s.stats.PeakFixtureRSSBytes {
		s.stats.PeakFixtureRSSBytes = rss
	}
}

func ownedProcessGroups(workspaceRoot string) (map[int]struct{}, error) {
	root := filepath.Join(workspaceRoot, ".runner-runtime-ownership")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return map[int]struct{}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runtime ownership directory: %w", err)
	}
	type marker struct {
		ProcessGroupID int `json:"process_group_id"`
	}
	groups := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read runtime ownership marker: %w", err)
		}
		var value marker
		if err := json.Unmarshal(encoded, &value); err != nil || value.ProcessGroupID <= 0 {
			return nil, fmt.Errorf("decode runtime ownership marker %s: %w", entry.Name(), err)
		}
		groups[value.ProcessGroupID] = struct{}{}
	}
	return groups, nil
}

func availableMemoryBytes() (int64, error) {
	switch runtime.GOOS {
	case "linux":
		contents, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(contents), "\n") {
			if !strings.HasPrefix(line, "MemAvailable:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				break
			}
			kib, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return kib * 1024, nil
		}
		return 0, fmt.Errorf("MemAvailable is absent from /proc/meminfo")
	case "darwin":
		output, err := exec.Command("vm_stat").Output()
		if err != nil {
			return 0, fmt.Errorf("read macOS vm_stat: %w", err)
		}
		var pageSize, availablePages int64
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, "page size of") {
				fields := strings.Fields(line)
				for index, field := range fields {
					if field == "of" && index+1 < len(fields) {
						pageSize, _ = strconv.ParseInt(fields[index+1], 10, 64)
						break
					}
				}
				continue
			}
			colon := strings.IndexByte(line, ':')
			if colon < 0 {
				continue
			}
			label := strings.TrimSpace(line[:colon])
			switch label {
			case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
				value := strings.Trim(strings.TrimSpace(line[colon+1:]), ".")
				pages, parseErr := strconv.ParseInt(value, 10, 64)
				if parseErr == nil {
					availablePages += pages
				}
			}
		}
		if pageSize <= 0 || availablePages <= 0 {
			return 0, fmt.Errorf("vm_stat did not report a usable available-page estimate")
		}
		return pageSize * availablePages, nil
	default:
		return 0, fmt.Errorf("unsupported host memory measurement OS %q", runtime.GOOS)
	}
}
