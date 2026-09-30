package runnerd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRunRecoverLostRequiresExplicitTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"recover-lost", "--config", "/fixture/linux.yaml"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("recover-lost exit=%d stderr=%q, want usage error", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--config, --session-id, and --command-id are required") {
		t.Fatalf("recover-lost usage=%q", stderr.String())
	}
}

func TestRequireRunnerdServiceStoppedAcceptsRemovedCgroup(t *testing.T) {
	values := map[string]string{
		"ActiveState":  "inactive",
		"MainPID":      "0",
		"ControlGroup": "",
	}
	readCalled := false
	err := requireRunnerdServiceStoppedWith(func(property string) (string, error) {
		value, ok := values[property]
		if !ok {
			return "", fmt.Errorf("unexpected property %q", property)
		}
		return value, nil
	}, func(string) ([]byte, error) {
		readCalled = true
		return nil, fmt.Errorf("cgroup must not be read when systemd removed it")
	})
	if err != nil {
		t.Fatalf("require stopped service with removed cgroup: %v", err)
	}
	if readCalled {
		t.Fatal("read cgroup after systemd reported no control group")
	}
}

func TestRequireRunnerdServiceStoppedRejectsFailedUnitWithoutCgroup(t *testing.T) {
	values := map[string]string{
		"ActiveState":  "failed",
		"MainPID":      "0",
		"ControlGroup": "",
	}
	err := requireRunnerdServiceStoppedWith(func(property string) (string, error) {
		return values[property], nil
	}, func(string) ([]byte, error) {
		t.Fatal("read cgroup after systemd reported no control group")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "control group is unavailable after failed state") {
		t.Fatalf("require failed unit without cgroup error=%v", err)
	}
}

func TestRequireRunnerdServiceStoppedRejectsCgroupMembers(t *testing.T) {
	values := map[string]string{
		"ActiveState":  "inactive",
		"MainPID":      "0",
		"ControlGroup": "/system.slice/runnerd.service",
	}
	err := requireRunnerdServiceStoppedWith(func(property string) (string, error) {
		return values[property], nil
	}, func(path string) ([]byte, error) {
		if path != "/sys/fs/cgroup/system.slice/runnerd.service/cgroup.procs" {
			t.Fatalf("cgroup path=%q", path)
		}
		return []byte("123\n"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "control group still has processes") {
		t.Fatalf("require stopped service with cgroup member error=%v", err)
	}
}
