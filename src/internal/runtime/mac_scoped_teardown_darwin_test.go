//go:build darwin && cgo

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMacScopedTeardownNativeRootObservationRefusesCurrentLiveProcess(t *testing.T) {
	absent, err := inspectMacScopedRootAbsent(os.Getpid())
	if err != nil || absent {
		t.Fatalf("native read treated the live test process as absent: absent=%t err=%v", absent, err)
	}
}

func TestMacScopedTeardownNativeLiveAndExitedOwnedRoot(t *testing.T) {
	for _, phase := range []string{"live-refused", "zombie-refused", "exited-proven"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newMacScopedFixture(t, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/bin/cat")
			command.Dir = fixture.records[0].Workspace
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = input.Close()
				if !waited {
					_ = command.Wait()
				}
			}()
			start, err := inspectMacProcessStartIdentity(command.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			fixture.rewrite(t, 0, func(record *RuntimeOwnershipRecord) {
				record.PID = command.Process.Pid
				record.ProcessGroupID = command.Process.Pid
				record.ProcessStartIdentity = start
				record.Command = "/bin/cat"
			})
			before := macScopedPeerState(t, fixture)
			markerBefore, err := os.ReadFile(fixture.path(t, 0))
			if err != nil {
				t.Fatal(err)
			}
			if phase != "live-refused" {
				// EOF, not a signal, ends only this task-owned fixture process.
				_ = input.Close()
				if phase == "exited-proven" {
					if err := command.Wait(); err != nil {
						t.Fatal(err)
					}
					waited = true
				} else {
					deadline := time.Now().Add(5 * time.Second)
					for {
						runnable, err := inspectMacPersistentProcessRunnable(ctx, command.Process.Pid)
						if err != nil {
							t.Fatal(err)
						}
						if !runnable {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("task-owned child did not exit after EOF")
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
			}
			snapshot := fixture.read(t)
			checkErr := snapshot.CheckExited(context.Background())
			checkAfter, _ := os.ReadFile(fixture.path(t, 0))
			if string(checkAfter) != string(markerBefore) || !reflect.DeepEqual(before, macScopedPeerState(t, fixture)) {
				t.Fatal("native read-only applicability changed marker or peer state")
			}
			if (phase == "exited-proven") != (checkErr == nil) {
				t.Fatalf("wrong native read-only applicability for %s: %v", phase, checkErr)
			}
			proof, proofErr := snapshot.ConfirmExited(context.Background())
			if phase == "exited-proven" {
				if proofErr != nil || proof.LostRecoveryCleanupConfirmedAt == "" {
					t.Fatalf("native exit proof failed: %+v err=%v", proof, proofErr)
				}
			} else {
				if proofErr == nil {
					t.Fatal("native live root passed absence proof")
				}
				if phase == "live-refused" {
					absent, err := inspectMacScopedRootAbsent(command.Process.Pid)
					if err != nil || absent {
						t.Fatalf("refused proof stopped the live owned root: absent=%t err=%v", absent, err)
					}
				}
				markerAfter, _ := os.ReadFile(fixture.path(t, 0))
				if string(markerBefore) != string(markerAfter) {
					t.Fatal("live refusal changed marker")
				}
			}
			if !reflect.DeepEqual(before, macScopedPeerState(t, fixture)) {
				t.Fatal("native target proof changed peer")
			}
		})
	}
}

func TestMacScopedTeardownDetectsCaseAliasedNestedWorkspace(t *testing.T) {
	fixture := newMacScopedFixture(t, 2)
	nested := filepath.Join(fixture.records[1].Workspace, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(fixture.records[1].Workspace), strings.ToUpper(filepath.Base(fixture.records[1].Workspace)), "nested")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("fixture filesystem does not expose case aliases")
	}
	fixture.rewrite(t, 0, func(record *RuntimeOwnershipRecord) { record.Workspace = alias })
	if _, err := ReadMacScopedTeardownOwnership(context.Background(), fixture.root, fixture.records[0].SessionID, fixture.expected); err == nil {
		t.Fatal("case-aliased nested workspace passed disjointness proof")
	}
}
