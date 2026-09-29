package runtime

import "testing"

func TestLinuxProcessStatAllowsKernelTaskWithoutProcessGroup(t *testing.T) {
	contents := []byte("10 (kworker/0:1) S 2 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 42")
	state, processGroupID, startIdentity, err := parseLinuxProcessStat(10, contents)
	if err != nil {
		t.Fatalf("parse kernel task stat: %v", err)
	}
	if state != 'S' || processGroupID != 0 || startIdentity != "42" {
		t.Fatalf("parsed kernel task stat = state %q group %d start %q", state, processGroupID, startIdentity)
	}
}

func TestLinuxProcessStatRejectsMalformedIdentityFields(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
	}{
		{name: "negative group", contents: "10 (kworker) S 2 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 42"},
		{name: "non-numeric group", contents: "10 (kworker) S 2 x 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 42"},
		{name: "missing start identity", contents: "10 (kworker) S 2 0 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := parseLinuxProcessStat(10, []byte(test.contents)); err == nil {
				t.Fatal("malformed process stat was accepted")
			}
		})
	}
}
