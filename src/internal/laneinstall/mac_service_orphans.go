package laneinstall

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/logcapture"
)

type macLsofFile struct {
	descriptor string
	name       string
}

type macLsofProcess struct {
	pid     int
	command string
	uid     string
	files   []macLsofFile
}

func (c *SimpleServiceController) stopMacOrphanSocketListeners(ctx context.Context, plan Plan) error {
	for _, selected := range []struct {
		socket string
		binary string
	}{
		{socket: plan.Namespace.LocalDSocket, binary: "runner-locald"},
		{socket: plan.Namespace.APISocket, binary: "runner-local"},
	} {
		listeners, err := c.macSocketListeners(ctx, selected.socket)
		if err != nil {
			return err
		}
		if len(listeners) == 0 {
			continue
		}
		if len(listeners) != 1 {
			return fmt.Errorf("selected Mac lane socket has multiple listeners; refusing process termination")
		}
		process := listeners[0]
		if err := c.validateMacSocketProcess(ctx, plan, process, selected.socket, selected.binary); err != nil {
			return err
		}
		child, err := c.macProcessCommand(ctx, process.pid)
		if err != nil {
			return err
		}
		if child.user != c.user || child.command != macLaneRunnerCommand(plan, selected.binary) || child.parentPID <= 1 {
			return errors.New("selected Mac lane listener command does not match its lane; refusing process termination")
		}
		wrapper, err := c.macProcessCommand(ctx, child.parentPID)
		if err != nil {
			return err
		}
		if wrapper.user != c.user || wrapper.command != macLaneCaptureCommand(plan, selected.binary) {
			return errors.New("selected Mac lane listener is not supervised by its expected log-capture wrapper; refusing process termination")
		}
		pid := strconv.Itoa(wrapper.pid)
		result, err := c.runner.Run(ctx, "/bin/kill", "-TERM", pid)
		if err != nil {
			return fmt.Errorf("gracefully stop verified selected Mac lane wrapper: %w", err)
		}
		if result.ExitCode != 0 {
			// The wrapper may have exited after process inspection. Treat that
			// as success only after proving its child and socket are gone.
			stillListening, inspectErr := c.macSocketListeners(ctx, selected.socket)
			wrapperState, wrapperErr := c.macProcessExists(ctx, wrapper.pid)
			childState, childErr := c.macProcessExists(ctx, process.pid)
			if inspectErr != nil || wrapperErr != nil || childErr != nil || len(stillListening) != 0 || wrapperState || childState {
				return fmt.Errorf("graceful stop of verified selected Mac lane wrapper failed (exit %d)", result.ExitCode)
			}
			continue
		}
		if err := c.waitForMacSocketListenerExit(ctx, selected.socket, wrapper.pid, process.pid); err != nil {
			return err
		}
	}
	return nil
}

type macPSProcess struct {
	pid       int
	parentPID int
	user      string
	command   string
}

func (c *SimpleServiceController) validateMacSocketProcess(ctx context.Context, plan Plan, process macLsofProcess, socket, binary string) error {
	if process.pid <= 1 || process.uid != c.uid || process.command != binary {
		return errors.New("selected Mac lane socket is held by an unverified process; refusing process termination")
	}
	details, err := c.macProcessDetails(ctx, process.pid)
	if err != nil {
		return err
	}
	if details.uid != c.uid || details.command != binary ||
		!macLsofHasFile(details.files, "cwd", plan.Namespace.ServiceRoot) ||
		!macLsofHasFile(process.files, "", socket) {
		return errors.New("selected Mac lane socket process identity does not match its lane; refusing process termination")
	}
	executable := filepath.Join(plan.Namespace.ServiceRoot, "current", "bin", binary)
	resolved := executable
	if c.resolvePath != nil {
		resolved, err = c.resolvePath(executable)
	} else {
		resolved, err = filepath.EvalSymlinks(executable)
	}
	if err != nil || !macLsofHasFile(details.files, "txt", resolved) {
		return errors.New("selected Mac lane listener executable does not match the current lane bundle; refusing process termination")
	}
	return nil
}

func (c *SimpleServiceController) macSocketListeners(ctx context.Context, socket string) ([]macLsofProcess, error) {
	result, err := c.runner.Run(ctx, "/usr/sbin/lsof", "-nP", "-Fpcufn", "+c", "0", "-U")
	if err != nil {
		return nil, fmt.Errorf("inspect selected Mac lane Unix socket owners: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("inspect selected Mac lane Unix socket owners failed (exit %d)", result.ExitCode)
	}
	processes, err := parseMacLsofProcesses(result.Output)
	if err != nil {
		return nil, err
	}
	listeners := make([]macLsofProcess, 0, 1)
	for _, process := range processes {
		if macLsofHasFile(process.files, "", socket) {
			listeners = append(listeners, process)
		}
	}
	return listeners, nil
}

func (c *SimpleServiceController) macProcessDetails(ctx context.Context, pid int) (macLsofProcess, error) {
	result, err := c.runner.Run(ctx, "/usr/sbin/lsof", "-nP", "-Fpcufn", "-a", "-p", strconv.Itoa(pid), "-d", "txt,cwd")
	if err != nil {
		return macLsofProcess{}, fmt.Errorf("inspect selected Mac lane process identity: %w", err)
	}
	if result.ExitCode != 0 {
		return macLsofProcess{}, fmt.Errorf("inspect selected Mac lane process identity failed (exit %d)", result.ExitCode)
	}
	processes, err := parseMacLsofProcesses(result.Output)
	if err != nil {
		return macLsofProcess{}, err
	}
	for _, process := range processes {
		if process.pid == pid {
			return process, nil
		}
	}
	return macLsofProcess{}, errors.New("selected Mac lane process disappeared during identity verification")
}

func (c *SimpleServiceController) macProcessCommand(ctx context.Context, pid int) (macPSProcess, error) {
	result, err := c.runner.Run(ctx, "/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,user=,command=")
	if err != nil {
		return macPSProcess{}, fmt.Errorf("inspect selected Mac lane process command: %w", err)
	}
	if result.ExitCode != 0 {
		return macPSProcess{}, errors.New("selected Mac lane process command could not be verified")
	}
	lines := strings.Split(strings.TrimSpace(result.Output), "\n")
	if len(lines) != 1 {
		return macPSProcess{}, errors.New("selected Mac lane process command output is ambiguous")
	}
	line := strings.TrimSpace(lines[0])
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return macPSProcess{}, errors.New("selected Mac lane process command output is incomplete")
	}
	processID, pidErr := strconv.Atoi(fields[0])
	parentPID, parentErr := strconv.Atoi(fields[1])
	if pidErr != nil || parentErr != nil || processID != pid {
		return macPSProcess{}, errors.New("selected Mac lane process command identity is invalid")
	}
	commandStart := 0
	for index := 0; index < 3; index++ {
		for commandStart < len(line) && line[commandStart] == ' ' {
			commandStart++
		}
		for commandStart < len(line) && line[commandStart] != ' ' {
			commandStart++
		}
	}
	for commandStart < len(line) && line[commandStart] == ' ' {
		commandStart++
	}
	if commandStart >= len(line) {
		return macPSProcess{}, errors.New("selected Mac lane process command is absent")
	}
	return macPSProcess{pid: processID, parentPID: parentPID, user: fields[2], command: line[commandStart:]}, nil
}

func macLaneRunnerCommand(plan Plan, binary string) string {
	return filepath.Join(plan.Namespace.ServiceRoot, "current", "bin", binary) + " --config " + plan.Namespace.ConfigFile
}

func macLaneCaptureCommand(plan Plan, binary string) string {
	limits := logcapture.SimpleLaneLimits()
	logName := "local.log"
	if binary == "runner-locald" {
		logName = "locald.log"
	}
	arguments := []string{
		filepath.Join(plan.Namespace.ServiceRoot, "current", "bin", "runner-log-capture"),
		"--log-path", filepath.Join(plan.Namespace.Logs, logName),
		"--active-file-bytes", strconv.FormatUint(limits.ActiveFileBytes, 10),
		"--rotated-file-bytes", strconv.FormatUint(limits.RotatedFileBytes, 10),
		"--max-rotated-files", strconv.Itoa(limits.MaxRotatedFiles),
		"--temp-file-bytes", strconv.FormatUint(limits.TempFileBytes, 10),
		"--", macLaneRunnerCommand(plan, binary),
	}
	return strings.Join(arguments, " ")
}

func (c *SimpleServiceController) macProcessExists(ctx context.Context, pid int) (bool, error) {
	result, err := c.runner.Run(ctx, "/bin/kill", "-0", strconv.Itoa(pid))
	if err != nil {
		return false, fmt.Errorf("check selected Mac lane process state: %w", err)
	}
	return result.ExitCode == 0, nil
}

func (c *SimpleServiceController) waitForMacSocketListenerExit(ctx context.Context, socket string, wrapperPID, childPID int) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		result, err := c.runner.Run(ctx, "/bin/kill", "-0", strconv.Itoa(wrapperPID))
		if err != nil {
			return fmt.Errorf("verify selected Mac lane listener termination: %w", err)
		}
		if result.ExitCode != 0 {
			listeners, err := c.macSocketListeners(ctx, socket)
			if err != nil {
				return err
			}
			if len(listeners) != 0 {
				return errors.New("selected Mac lane socket remained occupied after its process exited")
			}
			childExists, err := c.macProcessExists(ctx, childPID)
			if err != nil {
				return err
			}
			if childExists {
				return errors.New("selected Mac lane runner remained after its wrapper exited")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("selected Mac lane listener did not stop before the lane deadline: %w", ctx.Err())
		case <-timer.C:
			return errors.New("selected Mac lane listener did not stop after SIGTERM")
		case <-ticker.C:
		}
	}
}

func parseMacLsofProcesses(output string) ([]macLsofProcess, error) {
	processes := make([]macLsofProcess, 0)
	var current *macLsofProcess
	fileDescriptor := ""
	flush := func() {
		if current != nil {
			processes = append(processes, *current)
			current = nil
			fileDescriptor = ""
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		field, value := line[0], line[1:]
		if field == 'p' {
			flush()
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 0 {
				return nil, errors.New("lsof returned an invalid Mac process identity")
			}
			current = &macLsofProcess{pid: pid}
			continue
		}
		if current == nil {
			return nil, errors.New("lsof returned process details without a process identity")
		}
		switch field {
		case 'c':
			current.command = value
		case 'u':
			current.uid = value
		case 'f':
			fileDescriptor = value
		case 'n':
			current.files = append(current.files, macLsofFile{descriptor: fileDescriptor, name: value})
		}
	}
	flush()
	return processes, nil
}

func macLsofHasFile(files []macLsofFile, descriptor, name string) bool {
	for _, file := range files {
		if file.name == name && (descriptor == "" || file.descriptor == descriptor) {
			return true
		}
	}
	return false
}
