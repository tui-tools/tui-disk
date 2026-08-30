// Package disk defines the backend-agnostic model tui-disk renders and the
// interface every storage implementation satisfies. The UI knows only these
// types: it never builds an lsblk, findmnt, btrfs or smartctl argv itself.
// Mutations are Command values produced by the backend, shown in a preview
// dialog and only then executed.
package disk

import (
	"context"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
)

// Command is a single invocation the user is about to run. Argv excludes any
// privilege wrapper: the backend adds it when previewing and when executing.
//
// It is an alias rather than a type of its own, so a backend hands the very
// value the confirm dialog displayed straight to the kit runner, with no
// conversion in between. That identity is what makes the preview a promise.
type Command = runner.Command

// The device kinds lsblk reports in its TYPE column. They are strings rather
// than an enum because the list grows with the kernel and an unknown one must
// still be shown.
const (
	// KindDisk is a whole block device.
	KindDisk = "disk"
	// KindPart is a partition of one.
	KindPart = "part"
	// KindLoop is a loopback device backed by a file.
	KindLoop = "loop"
	// KindCrypt is a device-mapper crypt target.
	KindCrypt = "crypt"
	// KindLVM is a device-mapper logical volume.
	KindLVM = "lvm"
	// KindROM is an optical drive.
	KindROM = "rom"
)

// Device is one node of the block device tree, as lsblk reports it.
type Device struct {
	// Name is the device name lsblk prints ("nvme0n1p2", "home"), which for a
	// device-mapper name is not the kernel name.
	Name string
	// KName is the kernel name ("dm-0"), which is what /dev/<KName> resolves
	// to for every device including the mapped ones.
	KName string
	// Kind is the TYPE column (see the Kind* constants).
	Kind string
	// Size is the human size lsblk rendered ("476.9G").
	Size string
	// FSType is the filesystem or container signature ("btrfs", "crypto_LUKS").
	FSType string
	// FSUsed is the used space of a mounted filesystem, empty when unmounted.
	FSUsed string
	// FSUsePercent is lsblk's FSUSE% column ("85%"), empty when unmounted.
	FSUsePercent string
	// Mountpoints are every path this device is mounted at. lsblk reports a
	// list since util-linux 2.37 and a single value before it; both land here.
	Mountpoints []string
	Label       string
	UUID        string
	Model       string
	Serial      string
	// PartUUID is the GPT partition UUID, empty on a whole disk.
	PartUUID string
	// Rotational reports a spinning disk, as the kernel's ROTA flag.
	Rotational bool
	// Transport is the bus ("nvme", "sata", "usb"), empty when unknown.
	Transport string
	// Removable and ReadOnly are the RM and RO flags.
	Removable bool
	ReadOnly  bool

	// Children are the partitions, or the mapped devices on top of them.
	Children []Device

	// Health is the SMART verdict for the disk this device belongs to, filled
	// in for a whole disk only. See SMART.
	Health SMART
}

// Path is the absolute device node. The kernel name is used rather than the
// pretty name, because /dev/dm-0 always exists while /dev/home is a symlink
// only device-mapper's udev rules create.
func (d Device) Path() string {
	if d.KName == "" {
		return "/dev/" + d.Name
	}
	return "/dev/" + d.KName
}

// Mountpoint is the first path the device is mounted at, or an empty string.
func (d Device) Mountpoint() string {
	for _, m := range d.Mountpoints {
		if m != "" {
			return m
		}
	}
	return ""
}

// Mounted reports whether the device is mounted anywhere.
func (d Device) Mounted() bool { return d.Mountpoint() != "" }

// UsePercent parses the FSUSE% column into a number, returning -1 when there
// is none. It is what the usage bar is drawn from.
func (d Device) UsePercent() int {
	text := strings.TrimSuffix(strings.TrimSpace(d.FSUsePercent), "%")
	if text == "" {
		return -1
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return -1
	}
	return value
}

// IsDisk reports whether this node is a whole device rather than something
// carved out of one. Only a whole device is asked for its SMART health.
func (d Device) IsDisk() bool { return d.Kind == KindDisk }

// Spindle renders the media kind for a column: a spinning disk, or solid
// state. It is derived from ROTA rather than from the model name.
func (d Device) Spindle() string {
	if d.Rotational {
		return "hdd"
	}
	return "ssd"
}

// The mismatch verdicts a mount or an fstab entry can carry. They name what is
// wrong from the user's point of view, which is what the column shows.
const (
	// MatchOK is a mount that is in fstab, with the options fstab asks for.
	MatchOK = "ok"
	// MatchNotInFstab is mounted now and absent from fstab: it will not come
	// back after a reboot.
	MatchNotInFstab = "not in fstab"
	// MatchNotMounted is in fstab and not mounted now.
	MatchNotMounted = "not mounted"
	// MatchOptionsDiffer is mounted, in fstab, with options that do not agree.
	MatchOptionsDiffer = "options differ"
	// MatchTransient is a kernel or runtime filesystem nobody puts in fstab
	// (proc, sysfs, cgroup, tmpfs under /run). It is reported so the count of
	// real mismatches stays honest.
	MatchTransient = "transient"
)

// Mount is one entry of the mount table, cross-checked against fstab.
type Mount struct {
	Target  string
	Source  string
	FSType  string
	Options string
	// Size, Used, Avail and UsePercent are what findmnt reported for the
	// filesystem; every one is empty on a filesystem with no size.
	Size       string
	Used       string
	Avail      string
	UsePercent string

	// Mounted reports whether this row is mounted right now. A row built from
	// an fstab entry that is not mounted has it false.
	Mounted bool
	// InFstab reports whether an fstab entry covers this target.
	InFstab bool
	// FstabLine is the verbatim line from fstab, empty when there is none.
	FstabLine string
	// FstabOptions are the options fstab asks for, empty when there is none.
	FstabOptions string
	// Match is the verdict (see the Match* constants).
	Match string
}

// Mismatch reports whether this row is one the user should look at: mounted
// without an fstab entry, in fstab without being mounted, or mounted with
// options fstab does not ask for.
func (m Mount) Mismatch() bool {
	switch m.Match {
	case MatchNotInFstab, MatchNotMounted, MatchOptionsDiffer:
		return true
	default:
		return false
	}
}

// UsePercentValue parses the USE% column, returning -1 when there is none.
func (m Mount) UsePercentValue() int {
	text := strings.TrimSuffix(strings.TrimSpace(m.UsePercent), "%")
	if text == "" || text == "-" {
		return -1
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return -1
	}
	return value
}

// FstabEntry is one line of /etc/fstab.
type FstabEntry struct {
	// Spec is the first field: "UUID=…", "LABEL=…" or a device path.
	Spec   string
	Target string
	FSType string
	// Options is the fourth field, verbatim.
	Options string
	// Dump and Pass are the last two numeric fields, as written.
	Dump string
	Pass string
	// Line is the verbatim text, which is what the detail screen shows.
	Line string
	// Number is the 1-based line number in the file.
	Number int
	// Comment reports a line that is a comment or blank; such a line is kept
	// so the file can be rendered back with everything the user wrote.
	Comment bool
}

// UUID returns the filesystem UUID an entry refers to, and whether the spec is
// a UUID at all.
func (e FstabEntry) UUID() (string, bool) {
	if value, ok := strings.CutPrefix(e.Spec, "UUID="); ok {
		return strings.Trim(value, `"`), true
	}
	return "", false
}

// MountDetail is the second read a mount's detail screen makes: what fstab
// says about the target and what `findmnt --verify` thinks of the file.
type MountDetail struct {
	Target string
	// FstabLine is the verbatim fstab line, empty when the target has none.
	FstabLine string
	// Verify is the output of `findmnt --verify`, which checks the whole file.
	Verify string
	// VerifyErr is set when the verify command could not be run at all.
	VerifyErr string
}

// Subvolume is one btrfs subvolume, as `btrfs subvolume list -p` reports it.
type Subvolume struct {
	ID         int
	Generation int
	// ParentID is the `parent` field the -p flag adds, 0 at the top.
	ParentID int
	// TopLevel is the subvolume this one is nested under.
	TopLevel int
	// Path is the path relative to the filesystem root ("root", "home/.cache").
	Path string
}

// Qgroup is one btrfs quota group, reported only when quotas are enabled.
type Qgroup struct {
	// ID is the qgroup identifier ("0/256").
	ID string
	// Referenced and Exclusive are the two sizes, as btrfs rendered them.
	Referenced string
	Exclusive  string
	// MaxReferenced is the limit, empty or "none" when there is none.
	MaxReferenced string
	// Path names the subvolume the qgroup tracks, when btrfs printed one.
	Path string
}

// The scrub and balance states a btrfs filesystem can be in.
const (
	// TaskIdle is no scrub or balance running, and none recorded.
	TaskIdle = "idle"
	// TaskRunning is one in progress.
	TaskRunning = "running"
	// TaskFinished is one that completed.
	TaskFinished = "finished"
	// TaskAborted is one that was cancelled or failed.
	TaskAborted = "aborted"
	// TaskUnknown is a status that could not be read, usually for want of
	// privileges.
	TaskUnknown = "unknown"
)

// Scrub is the state of a filesystem's scrub.
type Scrub struct {
	// State is one of the Task* constants.
	State string
	// Started, Duration and Rate are what `btrfs scrub status` printed.
	Started  string
	Duration string
	Rate     string
	// Total is the amount to scrub, as rendered.
	Total string
	// Errors is the error summary line ("no errors found").
	Errors string
	// ErrorCount is the number of errors, 0 when the summary says none.
	ErrorCount int
	// Detail explains an unknown state.
	Detail string
}

// Balance is the state of a filesystem's balance.
type Balance struct {
	// State is one of the Task* constants.
	State string
	// Summary is the line btrfs printed, which carries the progress when one
	// is running.
	Summary string
	// Detail explains an unknown state.
	Detail string
}

// DeviceStat is the error counter set btrfs keeps per device.
type DeviceStat struct {
	Device string
	DevID  int
	Write  int
	Read   int
	Flush  int
	// Corruption and Generation are the two counters that mean the data itself
	// was wrong rather than the transport.
	Corruption int
	Generation int
}

// Errors is the total number of errors recorded for the device.
func (s DeviceStat) Errors() int {
	return s.Write + s.Read + s.Flush + s.Corruption + s.Generation
}

// BtrfsUsage is the space accounting of `btrfs filesystem usage`.
type BtrfsUsage struct {
	// DeviceSize, Allocated, Unallocated, Used and Free are the Overall block,
	// as rendered.
	DeviceSize  string
	Allocated   string
	Unallocated string
	Used        string
	Free        string
	// GlobalReserve is the reserve line, which is worth showing because a full
	// one is how a btrfs filesystem gets stuck.
	GlobalReserve string
	// Blocks are the per-profile lines ("Data,single", "Metadata,DUP").
	Blocks []UsageBlock
	// PerDevice reports whether btrfs printed the per-device breakdown, which
	// it refuses to do unprivileged.
	PerDevice bool
}

// UsageBlock is one "Data,single: Size:…, Used:…" line.
type UsageBlock struct {
	// Type is "Data", "Metadata" or "System".
	Type string
	// Profile is the replication profile ("single", "DUP", "RAID1").
	Profile string
	Size    string
	Used    string
	// Percent is the used share as btrfs printed it ("94.32%").
	Percent string
}

// Btrfs is one btrfs filesystem, keyed on the mount point it was read through.
type Btrfs struct {
	// Mountpoint is the path the reads were made against.
	Mountpoint string
	UUID       string
	Label      string
	// Devices are the device paths that make up the filesystem.
	Devices []string

	Usage       BtrfsUsage
	Subvolumes  []Subvolume
	Qgroups     []Qgroup
	QuotaOn     bool
	Scrub       Scrub
	Balance     Balance
	DeviceStats []DeviceStat
	// Errors is the sum of every device's error counters, which is what the
	// summary column and --check report.
	Errors int
}

// The SMART health verdicts.
const (
	// HealthPassed is the drive's own self-assessment saying it is fine.
	HealthPassed = "PASSED"
	// HealthFailed is the drive saying it is not.
	HealthFailed = "FAILED"
	// HealthUnknown is a drive that could not be asked: no smartctl, no
	// privileges, or a device that carries no SMART at all (a virtio disk).
	HealthUnknown = "unknown"
)

// SelfTest is one row of the drive's self-test log.
type SelfTest struct {
	// Type is the test that ran ("Short offline", "Extended offline").
	Type string
	// Status is the result string smartctl reported.
	Status string
	// Passed reports whether the row is a clean result.
	Passed bool
	// Hours is the power-on hour the test ran at.
	Hours int
	// LBA is the first failing block, when the test failed.
	LBA string
}

// SMART is what smartctl found about one drive.
type SMART struct {
	// Device is the node that was asked ("/dev/nvme0n1").
	Device string
	// Available reports whether an answer was obtained at all. When it is
	// false, Health is HealthUnknown and Detail says why.
	Available bool
	// Kind is "ata", "nvme" or "scsi": which attribute set applies.
	Kind string
	// Health is one of the Health* constants.
	Health string
	// Model and Serial identify the drive as the firmware reports it.
	Model  string
	Serial string
	// Temperature is in degrees Celsius, -1 when the drive reports none.
	Temperature int
	// PowerOnHours is the drive's own hour counter, -1 when absent.
	PowerOnHours int

	// ReallocatedSectors and PendingSectors are the two ATA attributes worth
	// waking somebody up for. Both are -1 on a drive that has no such
	// attribute, which is every NVMe.
	ReallocatedSectors int
	PendingSectors     int

	// PercentageUsed is the NVMe endurance estimate, -1 on an ATA drive.
	PercentageUsed int
	// MediaErrors is the NVMe media-and-data-integrity error count, -1 on ATA.
	MediaErrors int

	SelfTests []SelfTest
	// Detail explains an unavailable or unusual reading, in one sentence.
	Detail string
}

// Concerning reports whether this drive is one the user should look at: a
// failed self-assessment, a reallocated or pending sector, an NVMe media error,
// or an endurance estimate past its life.
func (s SMART) Concerning() bool {
	switch {
	case !s.Available:
		return false
	case s.Health == HealthFailed:
		return true
	case s.ReallocatedSectors > 0 || s.PendingSectors > 0:
		return true
	case s.MediaErrors > 0:
		return true
	case s.PercentageUsed >= 100:
		return true
	default:
		return false
	}
}

// Summary renders the health for a table cell: the verdict, plus the first
// number that explains a concerning one.
func (s SMART) Summary() string {
	if !s.Available {
		return HealthUnknown
	}
	switch {
	case s.Health == HealthFailed:
		return HealthFailed
	case s.ReallocatedSectors > 0:
		return "PASSED " + strconv.Itoa(s.ReallocatedSectors) + " realloc"
	case s.PendingSectors > 0:
		return "PASSED " + strconv.Itoa(s.PendingSectors) + " pending"
	case s.MediaErrors > 0:
		return "PASSED " + strconv.Itoa(s.MediaErrors) + " media err"
	case s.PercentageUsed >= 0:
		return s.Health + " " + strconv.Itoa(s.PercentageUsed) + "% used"
	default:
		return s.Health
	}
}

// The self-test kinds a user can start.
const (
	SelfTestShort = "short"
	SelfTestLong  = "long"
)

// SpaceRow is one line of the space view: a mounted filesystem and what `df`
// says about it.
type SpaceRow struct {
	Target string
	Source string
	FSType string
	Size   string
	Used   string
	Avail  string
	// UsePercent is df's PCENT column ("86%").
	UsePercent string
}

// UsePercentValue parses the PCENT column, returning -1 when there is none.
func (r SpaceRow) UsePercentValue() int {
	text := strings.TrimSuffix(strings.TrimSpace(r.UsePercent), "%")
	if text == "" || text == "-" {
		return -1
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return -1
	}
	return value
}

// Model is the whole picture tui-disk renders.
type Model struct {
	// Backend names the implementation that produced this model.
	Backend string
	// Devices is the block device tree, whole disks at the top.
	Devices []Device
	// Mounts is the mount table crossed with fstab: every mounted filesystem,
	// plus every fstab entry that is not mounted.
	Mounts []Mount
	// Fstab is the parsed file, comment lines included.
	Fstab []FstabEntry
	// FstabPath is the file those entries came from.
	FstabPath string
	// Btrfs is one entry per btrfs filesystem found in the mount table.
	Btrfs []Btrfs
	// SMART is one entry per whole disk, in the order the disks appear.
	SMART []SMART
	// Space is the `df` view, one row per mounted filesystem worth showing.
	Space []SpaceRow
	// NcduPath is where ncdu was found, empty when it is not installed. The
	// space view points at it rather than recursing itself.
	NcduPath string
	// Notes are one-line facts about what could not be read, which the UI
	// shows instead of a silently empty section.
	Notes []string
}

// Disks returns the whole devices of the tree, which are the ones SMART
// applies to.
func (m Model) Disks() []Device {
	var out []Device
	for _, d := range m.Devices {
		if d.IsDisk() {
			out = append(out, d)
		}
	}
	return out
}

// Flatten walks the device tree depth-first, returning each node with its
// depth, which is how the tree is drawn as a table.
func (m Model) Flatten() []FlatDevice {
	var out []FlatDevice
	var walk func(devices []Device, depth int)
	walk = func(devices []Device, depth int) {
		for _, d := range devices {
			out = append(out, FlatDevice{Device: d, Depth: depth})
			walk(d.Children, depth+1)
		}
	}
	walk(m.Devices, 0)
	return out
}

// FlatDevice is a device with its depth in the tree.
type FlatDevice struct {
	Device
	Depth int
}

// Mount returns the mount row for a target.
func (m Model) Mount(target string) (Mount, bool) {
	for _, mount := range m.Mounts {
		if mount.Target == target {
			return mount, true
		}
	}
	return Mount{}, false
}

// FstabFor returns the fstab entry covering a target.
func (m Model) FstabFor(target string) (FstabEntry, bool) {
	for _, entry := range m.Fstab {
		if !entry.Comment && entry.Target == target {
			return entry, true
		}
	}
	return FstabEntry{}, false
}

// BtrfsAt returns the btrfs filesystem read through a mount point.
func (m Model) BtrfsAt(mountpoint string) (Btrfs, bool) {
	for _, fs := range m.Btrfs {
		if fs.Mountpoint == mountpoint {
			return fs, true
		}
	}
	return Btrfs{}, false
}

// SMARTFor returns the SMART reading of a device path.
func (m Model) SMARTFor(path string) (SMART, bool) {
	for _, s := range m.SMART {
		if s.Device == path {
			return s, true
		}
	}
	return SMART{}, false
}

// Mismatches counts the mount rows that disagree with fstab.
func (m Model) Mismatches() int {
	count := 0
	for _, mount := range m.Mounts {
		if mount.Mismatch() {
			count++
		}
	}
	return count
}

// BtrfsErrors sums the device error counters of every btrfs filesystem.
func (m Model) BtrfsErrors() int {
	total := 0
	for _, fs := range m.Btrfs {
		total += fs.Errors
	}
	return total
}

// DeviceSpec is one device the fstab editor's picker offers.
//
// A UUID is the only spec that survives a disk being renumbered, so it is the
// one the form writes; the rest of the fields are here so the user can tell
// one device from another in the list.
type DeviceSpec struct {
	Device   string
	UUID     string
	FSType   string
	Label    string
	PartUUID string
}

// PickerLabel renders the device for the picker.
func (s DeviceSpec) PickerLabel() string {
	parts := []string{s.Device}
	if s.FSType != "" {
		parts = append(parts, s.FSType)
	}
	if s.Label != "" {
		parts = append(parts, "["+s.Label+"]")
	}
	if s.UUID != "" {
		parts = append(parts, s.UUID)
	}
	return strings.Join(parts, "  ")
}

// FstabSpec describes the fstab entry the guided form wants written. The
// backend renders it into a whole file and into the commands that install it.
type FstabSpec struct {
	// Spec is the first field ("UUID=…"), which the picker fills in.
	Spec   string
	Target string
	FSType string
	// Options is the fourth field.
	Options string
	Dump    string
	Pass    string
	// Replace is the 1-based line number being edited; 0 appends a new entry.
	Replace int
}

// Capabilities tells the UI what a backend supports, so the key map and the
// forms are built from the backend rather than hardcoded.
type Capabilities struct {
	// HasBtrfs and HasSMART report whether the optional backends are present.
	HasBtrfs bool
	HasSMART bool
	// SupportsMount reports whether mount and umount can be offered.
	SupportsMount bool
	// SupportsFstabEdit reports whether the guided fstab editor is offered.
	SupportsFstabEdit bool
	// FstabPath is the file the editor writes.
	FstabPath string
	// OptionPresets are the option strings the form offers, in the order they
	// are cycled.
	OptionPresets []string
	// FSTypes are the filesystem types the form offers.
	FSTypes []string
	// NcduPath is where ncdu was found, empty when it is absent.
	NcduPath string
}

// WritePlan is a file change the user is about to make: what the file will
// look like, how that differs from what is there now, the validator's verdict
// on it, and the exact commands that apply it.
type WritePlan struct {
	// Path is the destination file.
	Path string
	// Content is the text that will be installed.
	Content string
	// Diff is the unified diff against the current file.
	Diff string
	// TempPath is the staging file the install command copies from.
	TempPath string
	// Verify is what `findmnt --verify` said about the staged file. It is run
	// before the dialog opens, so a file that would not parse never reaches a
	// confirm.
	Verify string
	// Commands are run in order, and are what the confirm dialog shows.
	Commands []Command
}

// The btrfs balance filters the form offers. They are the ones worth a key:
// a usage filter reclaims the mostly-empty block groups, which is the balance
// nearly everybody actually wants.
const (
	// BalanceUsage10 rewrites the data block groups under 10% full.
	BalanceUsage10 = "dusage=10"
	// BalanceUsage50 is the more aggressive version of the same.
	BalanceUsage50 = "dusage=50"
	// BalanceMetadata20 rewrites the metadata block groups under 20% full.
	BalanceMetadata20 = "musage=20"
	// BalanceFull is a full balance, which rewrites everything.
	BalanceFull = "full"
)

// BalanceFilters are the choices the balance dialog offers.
var BalanceFilters = []string{
	BalanceUsage10, BalanceUsage50, BalanceMetadata20, BalanceFull,
}

// Backend is the boundary between the UI and the machine. Load reads state;
// the Build* methods turn user intent into previewable Commands; Run executes
// a Command the user confirmed. Nothing else may mutate the system.
type Backend interface {
	// Name is the backend identifier ("util-linux", "demo").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Capabilities reports what this backend supports.
	Capabilities() Capabilities

	// Preview renders the exact command line Run will execute, privilege
	// wrapper included. This is the text shown in the confirm dialog.
	Preview(cmd Command) string

	// Load reads the whole storage picture.
	Load(ctx context.Context) (Model, error)
	// LoadMount re-reads one mount in full: its fstab line and what
	// `findmnt --verify` says about the file it came from.
	LoadMount(ctx context.Context, target string) (MountDetail, error)
	// LoadSpecs returns the devices the fstab editor's picker offers, read
	// with blkid where that is possible.
	LoadSpecs(ctx context.Context, devices []Device) []DeviceSpec
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd Command) (string, error)

	// BuildMount and BuildUmount mount or unmount a target by its path, which
	// is the form that makes the kernel read fstab for the options.
	BuildMount(target string) (Command, error)
	BuildUmount(target string) (Command, error)
	// BuildDaemonReload regenerates the systemd units fstab produces. It runs
	// after every fstab edit, and is offered on its own key too.
	BuildDaemonReload() (Command, error)
	// BuildWriteFstab renders the whole fstab with one entry added or
	// replaced, validates the staged file, and returns the diff plus the
	// commands that install it. Nothing is installed until those run.
	BuildWriteFstab(ctx context.Context, spec FstabSpec) (WritePlan, error)

	// BuildScrubStart, BuildScrubCancel, BuildBalanceStart and
	// BuildBalanceCancel drive the two long-running btrfs jobs. Both are
	// started asynchronously, so the tool can show their status instead of
	// blocking on them.
	BuildScrubStart(mountpoint string) (Command, error)
	BuildScrubCancel(mountpoint string) (Command, error)
	BuildBalanceStart(mountpoint, filter string) (Command, error)
	BuildBalanceCancel(mountpoint string) (Command, error)

	// BuildSelfTest starts a drive's short or long self-test.
	BuildSelfTest(device, kind string) (Command, error)
}
