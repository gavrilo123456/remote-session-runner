package runtime

// macProcessGroupMember is the narrow process-table view used only while
// proving a Mac lost-runtime boundary. It deliberately excludes command-line
// arguments and environment data: neither is needed to establish ownership.
type macProcessGroupMember struct {
	PID            int
	ParentPID      int
	ProcessGroupID int
	UID            int
	Status         uint32
}

// macProcessStatusZombie is Darwin's SZOMB value from <sys/proc.h>. A zombie
// has no runnable user-space execution and cannot consume Runner capacity.
const macProcessStatusZombie uint32 = 5

func (member macProcessGroupMember) runnable() bool {
	return member.Status != macProcessStatusZombie
}
