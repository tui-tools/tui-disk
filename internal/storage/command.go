package storage

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// The version-gated capability of the util-linux backend, named the way the
// manifest names it.
const (
	// FeatureMountpoints is lsblk's MOUNTPOINTS column, which arrived in
	// util-linux 2.37. Before it the column is MOUNTPOINT, one value per
	// device, and a device mounted twice reports only the first — which is why
	// this is a feature and not just a spelling.
	FeatureMountpoints = "mountpoints"
)

// LsblkColumns is what the device tree is read with, in the order the table
// shows them. It is a single constant because the argv is asserted in a test:
// the columns are the tool's contract with lsblk, and a column silently
// dropped is a column silently missing from the screen.
var LsblkColumns = []string{
	"NAME", "KNAME", "TYPE", "SIZE", "FSTYPE", "FSUSED", "FSUSE%",
	"MOUNTPOINTS", "LABEL", "UUID", "MODEL", "SERIAL", "ROTA", "TRAN",
	"RM", "RO", "PARTUUID",
}

// LsblkColumnsLegacy is the same list with the pre-2.37 spelling of the
// mount point column. lsblk fails the whole call on an unknown column name, so
// asking for MOUNTPOINTS on an old util-linux would leave the screen empty
// rather than degrade.
var LsblkColumnsLegacy = replaceColumn(LsblkColumns, "MOUNTPOINTS", "MOUNTPOINT")

// replaceColumn returns the column list with one name swapped.
func replaceColumn(columns []string, from, to string) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		if column == from {
			out[i] = to
			continue
		}
		out[i] = column
	}
	return out
}

// FindmntColumns is what the mount table is read with.
var FindmntColumns = []string{
	"TARGET", "SOURCE", "FSTYPE", "OPTIONS", "SIZE", "USED", "AVAIL", "USE%",
}

// DFColumns is what the space view is read with. `--output` is used rather
// than the positional default so the columns cannot move under the parser.
const DFColumns = "source,fstype,size,used,avail,pcent,target"

// BuildLsblk is the device tree read. It is a builder like the mutations, so
// the argv the test asserts is the argv the backend runs.
func BuildLsblk(mountpointsColumn bool) []string {
	columns := LsblkColumns
	if !mountpointsColumn {
		columns = LsblkColumnsLegacy
	}
	return []string{"lsblk", "-J", "-o", strings.Join(columns, ",")}
}

// BuildFindmnt is the mount table read.
func BuildFindmnt() []string {
	return []string{"findmnt", "-J", "-o", strings.Join(FindmntColumns, ",")}
}

// BuildDF is the space read.
func BuildDF() []string {
	return []string{"df", "-h", "--output=" + DFColumns}
}

// BuildBlkid is the UUID read the fstab picker offers. The export form is one
// `KEY=value` line per field, which is the only output of blkid worth parsing.
func BuildBlkid() []string { return []string{"blkid", "-o", "export"} }

// BuildFindmntVerify checks a candidate fstab. It is run against the *staged*
// file, before the confirm dialog opens, so a file that findmnt refuses never
// reaches the point where the user could install it.
func BuildFindmntVerify(tabFile string) []string {
	return []string{"findmnt", "--verify", "--tab-file", tabFile}
}

// mountTargetRe is the set of paths the mount and umount commands accept. It
// is the same shape the fstab editor validates, because the target is the one
// argument that comes from the machine and ends up in an argv.
var mountTargetRe = regexp.MustCompile(`^/[A-Za-z0-9._@+/-]*$`)

// checkTarget rejects a mount point that is not a plausible path.
func checkTarget(target string) error {
	if !mountTargetRe.MatchString(target) || strings.Contains(target, "..") {
		return fmt.Errorf("storage: %q is not a mount point", target)
	}
	return nil
}

// devicePathRe is the set of device nodes the SMART commands accept.
var devicePathRe = regexp.MustCompile(`^/dev/[A-Za-z0-9._-]+$`)

// checkDevice rejects anything that is not a device node under /dev.
func checkDevice(device string) error {
	if !devicePathRe.MatchString(device) {
		return fmt.Errorf("storage: %q is not a device node", device)
	}
	return nil
}

// BuildMount mounts an fstab entry by its mount point.
//
// The target alone is passed on purpose: `mount /data` makes the kernel read
// the options out of fstab, so what gets mounted is exactly what the file
// says. Passing the device and the options again would let the tool mount
// something fstab does not describe, which is the one thing the mounts screen
// exists to warn about.
func BuildMount(target string) (disk.Command, error) {
	if err := checkTarget(target); err != nil {
		return disk.Command{}, err
	}
	return disk.Command{
		Argv:        []string{"mount", target},
		Description: "Mount " + target + " as " + FstabPath + " describes it",
	}, nil
}

// BuildUmount unmounts a target. It is destructive: a process with a file open
// under it keeps the mount busy, and anything that was writing there stops.
func BuildUmount(target string) (disk.Command, error) {
	if err := checkTarget(target); err != nil {
		return disk.Command{}, err
	}
	return disk.Command{
		Argv:        []string{"umount", target},
		Description: "Unmount " + target,
		Destructive: true,
	}, nil
}

// BuildDaemonReload regenerates the mount and automount units systemd derives
// from fstab. Without it a `nofail` or an `x-systemd.automount` that was just
// written does nothing until the next boot, and the file and the running
// system disagree in a way nothing on screen would show.
func BuildDaemonReload() (disk.Command, error) {
	return disk.Command{
		Argv:        []string{"systemctl", "daemon-reload"},
		Description: "Reload the systemd units generated from " + FstabPath,
	}, nil
}

// BuildInstallFstab copies a staged file over /etc/fstab. `install` is used
// rather than `cp` because it sets the mode in the same call, so there is no
// window where the file is on disk with the wrong permissions.
func BuildInstallFstab(tempPath string) (disk.Command, error) {
	if strings.ContainsAny(tempPath, " \t") || tempPath == "" {
		return disk.Command{}, fmt.Errorf(
			"storage: %q is not a staging path", tempPath)
	}
	return disk.Command{
		Argv:        []string{"install", "-m", FstabMode, tempPath, FstabPath},
		Description: "Install " + tempPath + " as " + FstabPath,
		Destructive: true,
	}, nil
}

// BuildScrubStart starts a scrub. It is started in the background rather than
// with -B, because a scrub of a full disk runs for hours and a TUI that waited
// for it would be a TUI that hung: the status is read back on the next refresh
// and shown in the btrfs view.
func BuildScrubStart(mountpoint string) (disk.Command, error) {
	if err := checkTarget(mountpoint); err != nil {
		return disk.Command{}, err
	}
	return disk.Command{
		Argv: []string{"btrfs", "scrub", "start", mountpoint},
		Description: "Start a scrub of " + mountpoint +
			" in the background; it reads every block and can take hours",
	}, nil
}

// BuildScrubCancel stops a running scrub. The progress is saved, so a later
// `scrub resume` picks it up; cancelling loses nothing but the time.
func BuildScrubCancel(mountpoint string) (disk.Command, error) {
	if err := checkTarget(mountpoint); err != nil {
		return disk.Command{}, err
	}
	return disk.Command{
		Argv:        []string{"btrfs", "scrub", "cancel", mountpoint},
		Description: "Cancel the scrub running on " + mountpoint,
	}, nil
}

// balanceFilterRe accepts the filter forms the dialog offers: a usage filter
// with a percentage, on data, metadata or system block groups.
var balanceFilterRe = regexp.MustCompile(`^[dms]usage=[0-9]{1,3}$`)

// BuildBalanceStart starts a balance, optionally filtered.
//
// A balance rewrites block groups, which means reading and writing every byte
// it touches: on a full filesystem an unfiltered one runs for hours and holds
// the disk the whole time. The description says so, and the UI marks it
// destructive so the dialog is painted in the danger colour — not because it
// loses data, but because it is not something to start by accident on a
// machine somebody is using.
func BuildBalanceStart(mountpoint, filter string) (disk.Command, error) {
	if err := checkTarget(mountpoint); err != nil {
		return disk.Command{}, err
	}
	argv := []string{"btrfs", "balance", "start"}
	description := "Start a full balance of " + mountpoint +
		"; it rewrites every block group and can take hours"
	if filter != "" && filter != disk.BalanceFull {
		if !balanceFilterRe.MatchString(filter) {
			return disk.Command{}, fmt.Errorf(
				"storage: %q is not a balance filter", filter)
		}
		// "dusage=10" becomes the flag "-dusage=10", which is btrfs's own
		// spelling of a block group filter.
		argv = append(argv, "-"+filter)
		description = "Balance the block groups of " + mountpoint +
			" matching " + filter
	}
	argv = append(argv, "--bg", mountpoint)
	return disk.Command{Argv: argv, Description: description, Destructive: true}, nil
}

// BuildBalanceCancel stops a running balance at the next block group.
func BuildBalanceCancel(mountpoint string) (disk.Command, error) {
	if err := checkTarget(mountpoint); err != nil {
		return disk.Command{}, err
	}
	return disk.Command{
		Argv:        []string{"btrfs", "balance", "cancel", mountpoint},
		Description: "Cancel the balance running on " + mountpoint,
	}, nil
}

// BuildSelfTest starts a drive's short or long self-test. The drive runs it
// itself, in the background, and the result lands in its self-test log — which
// is what the SMART detail screen shows.
func BuildSelfTest(device, kind string) (disk.Command, error) {
	if err := checkDevice(device); err != nil {
		return disk.Command{}, err
	}
	if kind != disk.SelfTestShort && kind != disk.SelfTestLong {
		return disk.Command{}, fmt.Errorf(
			"storage: %q is not a self-test; use %s or %s",
			kind, disk.SelfTestShort, disk.SelfTestLong)
	}
	description := "Start a short self-test on " + device + "; it takes a minute or two"
	if kind == disk.SelfTestLong {
		description = "Start an extended self-test on " + device +
			"; it reads the whole surface and can take hours"
	}
	return disk.Command{
		Argv:        []string{"smartctl", "--test=" + kind, device},
		Description: description,
	}, nil
}

// BuildSmartRead is the SMART read of one device. `--json=c` asks for compact
// JSON, which is the form every smartmontools from 7.0 on emits.
func BuildSmartRead(device string) ([]string, error) {
	if err := checkDevice(device); err != nil {
		return nil, err
	}
	return []string{"smartctl", "--json=c", "-a", device}, nil
}
