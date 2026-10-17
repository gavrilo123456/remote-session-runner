//go:build linux

package lanefixtures

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// RunLinuxControlsInBoundedScope starts the exact F command under a transient
// system.slice scope when it is not already in that scope. The test runs as
// the invoking user, while sudo is used only to ask systemd to create the
// bounded scope. No persistent unit or host configuration is written.
func RunLinuxControlsInBoundedScope(m Manifest, executable string, args []string) (bool, error) {
	if m.Platform != "linux" || m.TestSystemdUnit == "" {
		return false, ErrManifest
	}
	current, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false, fmt.Errorf("read current cgroup before F dispatch: %w", err)
	}
	expected := "/system.slice/" + m.TestSystemdUnit
	for _, line := range strings.Split(string(current), "\n") {
		if strings.HasPrefix(line, "0::") && strings.TrimPrefix(line, "0::") == expected {
			return false, nil
		}
	}
	if os.Getenv("LANES_FIXTURE_SCOPE_STARTED") == "1" {
		return false, fmt.Errorf("transient scope launcher returned without placing F command in %s", expected)
	}
	invoker, err := user.Current()
	if err != nil {
		return false, fmt.Errorf("resolve fixture operator identity: %w", err)
	}
	runuser, err := exec.LookPath("runuser")
	if err != nil {
		return false, fmt.Errorf("runuser is required to keep F execution under the fixture operator identity: %w", err)
	}
	env, err := exec.LookPath("env")
	if err != nil {
		return false, fmt.Errorf("env is required for scoped F dispatch: %w", err)
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return false, fmt.Errorf("systemd-run is required for scoped F dispatch: %w", err)
	}
	goBinary := os.Getenv("LANES_TEST_GO")
	if !filepath.IsAbs(goBinary) || goBinary == "" {
		return false, fmt.Errorf("LANES_TEST_GO must be an absolute pinned Go toolchain path")
	}
	command, err := buildLinuxControlsScopeCommand(m, invoker.Username, invoker.HomeDir, goBinary, executable, args, runuser, env)
	if err != nil {
		return false, err
	}
	var cmd *exec.Cmd
	if os.Geteuid() != 0 {
		sudo, err := exec.LookPath("sudo")
		if err != nil {
			return false, fmt.Errorf("sudo is required to create the transient system.slice scope: %w", err)
		}
		cmd = exec.Command(sudo, append([]string{"-n", "--", systemdRun}, command...)...)
	} else {
		cmd = exec.Command(systemdRun, command...)
	}
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + invoker.HomeDir,
		"LANG=C",
	}
	output, runErr := cmd.CombinedOutput()
	if len(output) != 0 {
		_, _ = os.Stdout.Write(output)
	}
	if runErr != nil {
		return true, fmt.Errorf("run bounded F fixture scope: %w", runErr)
	}
	return true, nil
}

func buildLinuxControlsScopeCommand(m Manifest, username, home, goBinary, executable string, args []string, runuser, env string) ([]string, error) {
	if m.Platform != "linux" || m.TestSystemdUnit != "remote-session-runner-fixture-"+m.FixtureID+".scope" || username == "" || home == "" || !filepath.IsAbs(goBinary) || !filepath.IsAbs(executable) || runuser == "" || env == "" {
		return nil, ErrManifest
	}
	command := []string{
		"--system", "--scope", "--wait", "--pipe", "--quiet",
		"--unit=" + m.TestSystemdUnit,
		"--slice=system.slice",
		"--property=CPUQuota=25%",
		"--property=MemoryMax=2G",
		"--property=TasksMax=64",
	}
	for _, mount := range m.Mounts {
		command = append(command,
			"--property=IOWriteBandwidthMax="+mount.DevicePath+" 16M",
			"--property=IOWriteIOPSMax="+mount.DevicePath+" 1000",
		)
	}
	command = append(command, "--", runuser, "--user", username, "--",
		env, "-i",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME="+home,
		"TMPDIR="+filepath.Join(m.FixtureRoot, "data", "go-build"),
		"GOMAXPROCS=2",
		"GOCACHE="+filepath.Join(m.FixtureRoot, "data", "go-build"),
		"GOMODCACHE="+filepath.Join(m.FixtureRoot, "data", "go-modules"),
		"LANES_FIXTURE_SCOPE_STARTED=1",
		"LANES_TEST_GO="+goBinary,
		"GOTOOLCHAIN=local",
		executable,
	)
	command = append(command, args...)
	return command, nil
}
