package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// TestArgvEquality is the family's central assertion, in table form: the argv
// a builder produces is the argv the user is shown and the argv that runs. A
// change to any of these lines is a change to what tui-disk does to a machine,
// and it has to be made here, deliberately.
func TestArgvEquality(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		got  func() (disk.Command, error)
	}{
		{"mount", []string{"mount", "/data"},
			func() (disk.Command, error) { return BuildMount("/data") }},
		{"umount", []string{"umount", "/data"},
			func() (disk.Command, error) { return BuildUmount("/data") }},
		{"daemon-reload", []string{"systemctl", "daemon-reload"},
			BuildDaemonReload},
		{"install fstab",
			[]string{"install", "-m", "644", "/tmp/tui-disk-1/fstab", "/etc/fstab"},
			func() (disk.Command, error) {
				return BuildInstallFstab("/tmp/tui-disk-1/fstab")
			}},
		{"scrub start", []string{"btrfs", "scrub", "start", "/"},
			func() (disk.Command, error) { return BuildScrubStart("/") }},
		{"scrub cancel", []string{"btrfs", "scrub", "cancel", "/"},
			func() (disk.Command, error) { return BuildScrubCancel("/") }},
		{"balance, filtered",
			[]string{"btrfs", "balance", "start", "-dusage=10", "--bg", "/"},
			func() (disk.Command, error) {
				return BuildBalanceStart("/", disk.BalanceUsage10)
			}},
		{"balance, full",
			[]string{"btrfs", "balance", "start", "--bg", "/"},
			func() (disk.Command, error) {
				return BuildBalanceStart("/", disk.BalanceFull)
			}},
		{"balance cancel", []string{"btrfs", "balance", "cancel", "/"},
			func() (disk.Command, error) { return BuildBalanceCancel("/") }},
		{"short self-test", []string{"smartctl", "--test=short", "/dev/sda"},
			func() (disk.Command, error) {
				return BuildSelfTest("/dev/sda", disk.SelfTestShort)
			}},
		{"long self-test", []string{"smartctl", "--test=long", "/dev/nvme0n1"},
			func() (disk.Command, error) {
				return BuildSelfTest("/dev/nvme0n1", disk.SelfTestLong)
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.got()
			if err != nil {
				t.Fatalf("builder returned %v", err)
			}
			if strings.Join(cmd.Argv, " ") != strings.Join(tc.argv, " ") {
				t.Errorf("argv = %v, want %v", cmd.Argv, tc.argv)
			}
			if cmd.Description == "" {
				t.Error("a command with no description cannot be previewed")
			}
		})
	}
}

// TestReadArgv covers the read commands, which are builders for the same
// reason: the columns are the tool's contract with lsblk, and a column
// silently dropped is a column silently missing from the screen.
func TestReadArgv(t *testing.T) {
	modern := strings.Join(BuildLsblk(true), " ")
	if !strings.Contains(modern, "MOUNTPOINTS") {
		t.Errorf("the modern read = %q, want the MOUNTPOINTS column", modern)
	}
	legacy := strings.Join(BuildLsblk(false), " ")
	if strings.Contains(legacy, "MOUNTPOINTS") {
		t.Errorf("the legacy read = %q, want the singular column", legacy)
	}
	if !strings.Contains(legacy, "MOUNTPOINT,") {
		t.Errorf("the legacy read = %q, want MOUNTPOINT", legacy)
	}
	// Every other column must be identical between the two.
	if strings.Count(modern, ",") != strings.Count(legacy, ",") {
		t.Error("the two column lists differ by more than the mount point")
	}

	if got := strings.Join(BuildFindmnt(), " "); !strings.HasPrefix(got, "findmnt -J ") {
		t.Errorf("findmnt read = %q", got)
	}
	if got := strings.Join(BuildDF(), " "); got != "df -h --output="+DFColumns {
		t.Errorf("df read = %q", got)
	}
	if got := strings.Join(BuildBlkid(), " "); got != "blkid -o export" {
		t.Errorf("blkid read = %q", got)
	}
	if got := strings.Join(BuildFindmntVerify("/tmp/x/fstab"), " "); got !=
		"findmnt --verify --tab-file /tmp/x/fstab" {
		t.Errorf("verify read = %q", got)
	}
}

// TestBuildersRefuseBadArguments covers the arguments that come from the
// machine and end up in an argv. None of these can reach a shell — the runner
// never builds one — but a path that escaped its directory would still be a
// command doing something the preview did not say.
func TestBuildersRefuseBadArguments(t *testing.T) {
	for name, build := range map[string]func() (disk.Command, error){
		"mount a relative path":   func() (disk.Command, error) { return BuildMount("data") },
		"mount through a parent":  func() (disk.Command, error) { return BuildMount("/data/../etc") },
		"umount an empty target":  func() (disk.Command, error) { return BuildUmount("") },
		"scrub something odd":     func() (disk.Command, error) { return BuildScrubStart("; reboot") },
		"a self-test off /dev":    func() (disk.Command, error) { return BuildSelfTest("/etc/passwd", "short") },
		"an unknown self-test":    func() (disk.Command, error) { return BuildSelfTest("/dev/sda", "destroy") },
		"a bogus balance filter":  func() (disk.Command, error) { return BuildBalanceStart("/", "xusage=abc") },
		"install from a bad path": func() (disk.Command, error) { return BuildInstallFstab("/tmp/a b") },
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestDestructiveMarking asserts which commands paint the dialog red. It is a
// judgement about what can cost the user something, and it belongs in a test
// so it cannot drift silently.
func TestDestructiveMarking(t *testing.T) {
	destructive := map[string]bool{}
	for name, build := range map[string]func() (disk.Command, error){
		"mount":         func() (disk.Command, error) { return BuildMount("/data") },
		"umount":        func() (disk.Command, error) { return BuildUmount("/data") },
		"install":       func() (disk.Command, error) { return BuildInstallFstab("/tmp/x/fstab") },
		"daemon-reload": BuildDaemonReload,
		"scrub":         func() (disk.Command, error) { return BuildScrubStart("/") },
		"balance":       func() (disk.Command, error) { return BuildBalanceStart("/", "") },
		"self-test":     func() (disk.Command, error) { return BuildSelfTest("/dev/sda", "long") },
	} {
		cmd, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		destructive[name] = cmd.Destructive
	}

	for name, want := range map[string]bool{
		// Unmounting stops whatever was writing there, overwriting fstab is
		// what a machine boots from, and a balance holds the disk for hours.
		"umount": true, "install": true, "balance": true,
		// Mounting, reloading the generator, a scrub and a self-test all
		// leave the data alone.
		"mount": false, "daemon-reload": false, "scrub": false,
		"self-test": false,
	} {
		if destructive[name] != want {
			t.Errorf("%s destructive = %v, want %v", name, destructive[name], want)
		}
	}
}

// TestFakeBuildsRealCommands asserts that --demo builds the same argv a real
// machine would, which is what makes a preview shown in the demo worth
// reading.
func TestFakeBuildsRealCommands(t *testing.T) {
	fake := NewFake()
	model, err := fake.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(model.Devices) != 3 {
		t.Fatalf("demo disks = %d, want 3", len(model.Devices))
	}
	// The demo machine exists to show the two situations the tool surfaces:
	// exactly one fstab mismatch, and exactly one drive worth watching.
	if got := model.Mismatches(); got != 1 {
		t.Errorf("demo mismatches = %d, want 1", got)
	}
	concerning := 0
	for _, reading := range model.SMART {
		if reading.Concerning() {
			concerning++
		}
	}
	if concerning != 1 {
		t.Errorf("demo drives worth watching = %d, want 1", concerning)
	}
	if len(model.Btrfs) != 1 || len(model.Btrfs[0].Subvolumes) != 3 {
		t.Errorf("the demo btrfs filesystem is %+v", model.Btrfs)
	}

	cmd, err := fake.BuildUmount("/media/backup")
	if err != nil {
		t.Fatalf("BuildUmount: %v", err)
	}
	if preview := fake.Preview(cmd); !strings.HasSuffix(preview,
		"umount /media/backup") {
		t.Errorf("preview = %q", preview)
	}
}

// TestFakeWriteFstabDiffsOneLine asserts that adding an entry to the demo file
// produces a diff of the one line that changed. A diff that repeats the whole
// file is a dialog nobody reads, and the diff is the only thing standing
// between a user and a machine that will not boot.
func TestFakeWriteFstabDiffsOneLine(t *testing.T) {
	fake := NewFake()
	plan, err := fake.BuildWriteFstab(context.Background(), disk.FstabSpec{
		Spec:   "UUID=0f2b1c44-3333-4a2b-9c3d-000000000003",
		Target: "/media/backup", FSType: "vfat",
		Options: NofailOptions, Dump: "0", Pass: "0",
	})
	if err != nil {
		t.Fatalf("BuildWriteFstab: %v", err)
	}
	added := 0
	for _, line := range strings.Split(plan.Diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Errorf("adding an entry removed a line: %q", line)
		}
	}
	if added != 1 {
		t.Errorf("added lines = %d, want 1:\n%s", added, plan.Diff)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("commands = %d, want install then daemon-reload",
			len(plan.Commands))
	}
	if plan.Commands[0].Argv[0] != "install" ||
		strings.Join(plan.Commands[1].Argv, " ") != "systemctl daemon-reload" {
		t.Errorf("the plan runs %v then %v",
			plan.Commands[0].Argv, plan.Commands[1].Argv)
	}
	// The staged file is what install copies, so it has to exist and to hold
	// exactly what the user approved.
	if plan.TempPath == "" || !strings.Contains(plan.Commands[0].String(), plan.TempPath) {
		t.Errorf("the install command does not name the staged file: %v",
			plan.Commands[0].Argv)
	}
}
