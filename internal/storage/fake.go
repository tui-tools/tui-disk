package storage

import (
	"context"
	"fmt"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// Fake is the in-memory machine behind --demo. It satisfies disk.Backend and
// builds every command for real: the same builders the host backend uses, the
// same validation, the same previews. Only Run is inert.
//
// The machine it describes is the one worth practising on: an NVMe root on
// btrfs with three subvolumes, a spinning data disk whose SMART already
// carries two reallocated sectors, and a USB stick mounted by hand that no
// fstab entry covers. Two of those three are the situations the tool exists to
// surface, so `--demo` shows them rather than a machine where everything is
// fine.
type Fake struct {
	// fstab is the demo file, rewritten in memory by BuildWriteFstab so the
	// diff and the validation behave exactly as they do on a real host.
	fstab string
}

// NewFake builds the demo backend.
func NewFake() *Fake { return &Fake{fstab: demoFstab} }

// Name identifies the backend.
func (f *Fake) Name() string { return "demo" }

// Describe says, in the header, that nothing here is real.
func (f *Fake) Describe() string { return "demo — a sample machine, nothing is touched" }

// Capabilities reports what the demo machine supports, which is everything:
// the point of --demo is that every key works.
func (f *Fake) Capabilities() disk.Capabilities {
	return disk.Capabilities{
		HasBtrfs:          true,
		HasSMART:          true,
		SupportsMount:     true,
		SupportsFstabEdit: true,
		FstabPath:         FstabPath,
		OptionPresets:     OptionPresets,
		FSTypes:           FSTypes,
		NcduPath:          "/usr/bin/ncdu",
	}
}

// Preview renders the command line the demo would have run, with the same
// escalation prefix a real machine would carry, so what the dialog shows is
// what the tool would really do.
func (f *Fake) Preview(cmd disk.Command) string { return "sudo -n " + cmd.String() }

// Run pretends to execute. It never touches the machine.
func (f *Fake) Run(_ context.Context, cmd disk.Command) (string, error) {
	if len(cmd.Argv) == 0 {
		return "", fmt.Errorf("demo: empty command")
	}
	return "demo: would have run `" + cmd.String() + "`", nil
}

// Load returns the sample machine.
func (f *Fake) Load(_ context.Context) (disk.Model, error) {
	devices := demoDevices()
	readings := demoSMART()
	attachHealth(devices, readings)

	entries := ParseFstab(f.fstab)
	model := disk.Model{
		Backend:   f.Name(),
		Devices:   devices,
		Fstab:     entries,
		FstabPath: FstabPath,
		Mounts:    CrossCheck(demoMounts(), entries),
		Btrfs:     []disk.Btrfs{demoBtrfs()},
		SMART:     readings,
		Space:     demoSpace(),
		NcduPath:  "/usr/bin/ncdu",
	}
	return model, nil
}

// LoadMount returns the demo fstab line and a clean verify report.
func (f *Fake) LoadMount(_ context.Context, target string) (disk.MountDetail, error) {
	detail := disk.MountDetail{Target: target,
		Verify: "Success, no errors or warnings detected"}
	for _, entry := range ParseFstab(f.fstab) {
		if !entry.Comment && entry.Target == target {
			detail.FstabLine = entry.Line
			break
		}
	}
	return detail, nil
}

// LoadSpecs offers the demo machine's devices to the fstab picker.
func (f *Fake) LoadSpecs(_ context.Context, devices []disk.Device) []disk.DeviceSpec {
	return BlkidFromDevices(devices)
}

// The command builders are the real ones: a preview shown in --demo is the
// command line the tool would build against a real machine.
func (f *Fake) BuildMount(target string) (disk.Command, error)  { return BuildMount(target) }
func (f *Fake) BuildUmount(target string) (disk.Command, error) { return BuildUmount(target) }
func (f *Fake) BuildDaemonReload() (disk.Command, error)        { return BuildDaemonReload() }

func (f *Fake) BuildScrubStart(mountpoint string) (disk.Command, error) {
	return BuildScrubStart(mountpoint)
}

func (f *Fake) BuildScrubCancel(mountpoint string) (disk.Command, error) {
	return BuildScrubCancel(mountpoint)
}

func (f *Fake) BuildBalanceStart(mountpoint, filter string) (disk.Command, error) {
	return BuildBalanceStart(mountpoint, filter)
}

func (f *Fake) BuildBalanceCancel(mountpoint string) (disk.Command, error) {
	return BuildBalanceCancel(mountpoint)
}

func (f *Fake) BuildSelfTest(device, kind string) (disk.Command, error) {
	return BuildSelfTest(device, kind)
}

// BuildWriteFstab renders the demo file, diffs it and returns the same two
// commands a real host would run. The staged file is a real temporary file, so
// the preview names a path that exists.
func (f *Fake) BuildWriteFstab(_ context.Context,
	spec disk.FstabSpec) (disk.WritePlan, error) {
	content, err := RenderFstab(f.fstab, spec)
	if err != nil {
		return disk.WritePlan{}, err
	}
	if content == f.fstab {
		return disk.WritePlan{}, fmt.Errorf("%s already says exactly this", FstabPath)
	}
	temp, err := stageFile(FstabPath, content)
	if err != nil {
		return disk.WritePlan{}, err
	}
	installCmd, err := BuildInstallFstab(temp)
	if err != nil {
		return disk.WritePlan{}, err
	}
	reloadCmd, err := BuildDaemonReload()
	if err != nil {
		return disk.WritePlan{}, err
	}
	return disk.WritePlan{
		Path:     FstabPath,
		Content:  content,
		Diff:     Diff(FstabPath, f.fstab, content),
		TempPath: temp,
		Verify:   "Success, no errors or warnings detected",
		Commands: []disk.Command{installCmd, reloadCmd},
	}, nil
}

// The demo machine's identifiers. They are documentation-range values: a UUID
// nobody's disk carries and a serial nobody's drive was sold with.
const (
	demoRootUUID = "0f2b1c44-1111-4a2b-9c3d-000000000001"
	demoEFIUUID  = "1A2B-3C4D"
	demoDataUUID = "0f2b1c44-2222-4a2b-9c3d-000000000002"
	demoUSBUUID  = "0f2b1c44-3333-4a2b-9c3d-000000000003"
)

// demoFstab is the sample file. The USB stick is deliberately absent from it:
// it is mounted in demoMounts, so the mounts screen opens on exactly one real
// mismatch.
const demoFstab = `#
# /etc/fstab
# Written by the installer. See fstab(5).
#
# After editing this file, run 'systemctl daemon-reload'.
#
UUID=0f2b1c44-1111-4a2b-9c3d-000000000001 /            btrfs  subvol=@,compress=zstd:1,noatime 0 0
UUID=0f2b1c44-1111-4a2b-9c3d-000000000001 /home        btrfs  subvol=@home,compress=zstd:1,noatime 0 0
UUID=1A2B-3C4D                            /boot/efi    vfat   umask=0077,shortname=winnt 0 2
UUID=0f2b1c44-2222-4a2b-9c3d-000000000002 /data        ext4   defaults,noatime 0 2
`

// demoDevices is the sample block device tree.
func demoDevices() []disk.Device {
	return []disk.Device{
		{
			Name: "nvme0n1", KName: "nvme0n1", Kind: disk.KindDisk,
			Size: "476.9G", Model: "SAMSUNG MZVL2512HCJQ", Serial: "S64ANE0T000001",
			Transport: "nvme",
			Children: []disk.Device{
				{
					Name: "nvme0n1p1", KName: "nvme0n1p1", Kind: disk.KindPart,
					Size: "1G", FSType: "vfat", FSUsed: "31.4M", FSUsePercent: "3%",
					Mountpoints: []string{"/boot/efi"}, UUID: demoEFIUUID,
					Transport: "nvme", PartUUID: "aaaa1111-0001-4000-8000-000000000001",
				},
				{
					Name: "nvme0n1p2", KName: "nvme0n1p2", Kind: disk.KindPart,
					Size: "475.4G", FSType: "btrfs", FSUsed: "182.7G",
					FSUsePercent: "38%", Label: "fedora",
					Mountpoints: []string{"/", "/home"}, UUID: demoRootUUID,
					Transport: "nvme", PartUUID: "aaaa1111-0001-4000-8000-000000000002",
				},
			},
		},
		{
			Name: "sda", KName: "sda", Kind: disk.KindDisk,
			Size: "1.8T", Model: "WDC WD20EFAX-68B", Serial: "WD-000000000002",
			Transport: "sata", Rotational: true,
			Children: []disk.Device{
				{
					Name: "sda1", KName: "sda1", Kind: disk.KindPart,
					Size: "1.8T", FSType: "ext4", FSUsed: "1.4T",
					FSUsePercent: "79%", Label: "data",
					Mountpoints: []string{"/data"}, UUID: demoDataUUID,
					Transport: "sata", Rotational: true,
					PartUUID: "bbbb2222-0002-4000-8000-000000000001",
				},
			},
		},
		{
			Name: "sdb", KName: "sdb", Kind: disk.KindDisk,
			Size: "28.9G", Model: "SanDisk Ultra", Serial: "4C530000000003",
			Transport: "usb", Removable: true,
			Children: []disk.Device{
				{
					Name: "sdb1", KName: "sdb1", Kind: disk.KindPart,
					Size: "28.9G", FSType: "vfat", FSUsed: "12.1G",
					FSUsePercent: "42%", Label: "BACKUP",
					Mountpoints: []string{"/media/backup"}, UUID: demoUSBUUID,
					Transport: "usb", Removable: true,
					PartUUID: "cccc3333-0003-4000-8000-000000000001",
				},
			},
		},
	}
}

// demoMounts is the sample mount table, before fstab is folded into it.
func demoMounts() []disk.Mount {
	return []disk.Mount{
		{Target: "/", Source: "/dev/nvme0n1p2[/@]", FSType: "btrfs",
			Options: "rw,noatime,compress=zstd:1,ssd,space_cache=v2,subvol=/@",
			Size:    "475.4G", Used: "182.7G", Avail: "290.1G", UsePercent: "38%",
			Mounted: true},
		{Target: "/home", Source: "/dev/nvme0n1p2[/@home]", FSType: "btrfs",
			Options: "rw,noatime,compress=zstd:1,ssd,space_cache=v2,subvol=/@home",
			Size:    "475.4G", Used: "182.7G", Avail: "290.1G", UsePercent: "38%",
			Mounted: true},
		{Target: "/boot/efi", Source: "/dev/nvme0n1p1", FSType: "vfat",
			Options: "rw,relatime,fmask=0077,dmask=0077,shortname=winnt",
			Size:    "1G", Used: "31.4M", Avail: "992M", UsePercent: "3%",
			Mounted: true},
		{Target: "/data", Source: "/dev/sda1", FSType: "ext4",
			Options: "rw,noatime", Size: "1.8T", Used: "1.4T", Avail: "331G",
			UsePercent: "79%", Mounted: true},
		// The one mismatch: mounted by hand, absent from fstab, so it will not
		// be there after a reboot.
		{Target: "/media/backup", Source: "/dev/sdb1", FSType: "vfat",
			Options: "rw,nosuid,nodev,relatime,uid=1000,gid=1000",
			Size:    "28.9G", Used: "12.1G", Avail: "16.8G", UsePercent: "42%",
			Mounted: true},
		{Target: "/proc", Source: "proc", FSType: "proc",
			Options: "rw,nosuid,nodev,noexec,relatime", Mounted: true},
		{Target: "/run", Source: "tmpfs", FSType: "tmpfs",
			Options: "rw,nosuid,nodev,size=1638400k", Size: "1.6G", Used: "2.1M",
			Avail: "1.6G", UsePercent: "1%", Mounted: true},
	}
}

// demoBtrfs is the sample btrfs filesystem: three subvolumes, a finished
// scrub, no balance and clean device counters.
func demoBtrfs() disk.Btrfs {
	return disk.Btrfs{
		Mountpoint: "/", UUID: demoRootUUID, Label: "fedora",
		Devices: []string{"/dev/nvme0n1p2"},
		Usage: disk.BtrfsUsage{
			DeviceSize: "475.40GiB", Allocated: "198.02GiB",
			Unallocated: "277.38GiB", Used: "182.71GiB", Free: "290.10GiB",
			GlobalReserve: "512.00MiB", PerDevice: true,
			Blocks: []disk.UsageBlock{
				{Type: "Data", Profile: "single", Size: "194.01GiB",
					Used: "180.22GiB", Percent: "92.89%"},
				{Type: "Metadata", Profile: "DUP", Size: "2.00GiB",
					Used: "1.24GiB", Percent: "62.11%"},
				{Type: "System", Profile: "DUP", Size: "8.00MiB",
					Used: "32.00KiB", Percent: "0.39%"},
			},
		},
		Subvolumes: []disk.Subvolume{
			{ID: 256, Generation: 41822, ParentID: 5, TopLevel: 5, Path: "@"},
			{ID: 257, Generation: 41822, ParentID: 5, TopLevel: 5, Path: "@home"},
			{ID: 312, Generation: 41390, ParentID: 5, TopLevel: 5, Path: "@snapshots"},
		},
		Scrub: disk.Scrub{
			State: disk.TaskFinished, Started: "Sun Aug 24 03:00:01 2026",
			Duration: "0:21:47", Rate: "143.02MiB/s", Total: "182.71GiB",
			Errors: "no errors found",
		},
		Balance: disk.Balance{State: disk.TaskIdle,
			Summary: "No balance found on '/'"},
		DeviceStats: []disk.DeviceStat{{Device: "/dev/nvme0n1p2", DevID: 1}},
	}
}

// demoSMART is the sample health: a healthy NVMe, a spinning disk that has
// already replaced two sectors, and a USB stick whose bridge passes no SMART
// through at all — which is the third answer a real machine gives and the one
// a tool is most likely to get wrong.
func demoSMART() []disk.SMART {
	return []disk.SMART{
		{
			Device: "/dev/nvme0n1", Available: true, Kind: "nvme",
			Health: disk.HealthPassed, Model: "SAMSUNG MZVL2512HCJQ",
			Serial: "S64ANE0T000001", Temperature: 41, PowerOnHours: 4210,
			ReallocatedSectors: -1, PendingSectors: -1,
			PercentageUsed: 3, MediaErrors: 0,
			SelfTests: []disk.SelfTest{
				{Type: "Short", Status: "Completed without error", Passed: true,
					Hours: 4180},
			},
		},
		{
			Device: "/dev/sda", Available: true, Kind: "ata",
			Health: disk.HealthPassed, Model: "WDC WD20EFAX-68B",
			Serial: "WD-000000000002", Temperature: 38, PowerOnHours: 26914,
			ReallocatedSectors: 2, PendingSectors: 0,
			PercentageUsed: -1, MediaErrors: -1,
			SelfTests: []disk.SelfTest{
				{Type: "Short offline", Status: "Completed without error",
					Passed: true, Hours: 26890},
				{Type: "Extended offline", Status: "Completed without error",
					Passed: true, Hours: 26102},
			},
		},
		UnavailableSMART("/dev/sdb",
			"Unavailable - device lacks SMART capability"),
	}
}

// demoSpace is the sample df view.
func demoSpace() []disk.SpaceRow {
	return []disk.SpaceRow{
		{Source: "/dev/nvme0n1p2", FSType: "btrfs", Size: "476G", Used: "183G",
			Avail: "291G", UsePercent: "39%", Target: "/"},
		{Source: "/dev/nvme0n1p2", FSType: "btrfs", Size: "476G", Used: "183G",
			Avail: "291G", UsePercent: "39%", Target: "/home"},
		{Source: "/dev/nvme0n1p1", FSType: "vfat", Size: "1.0G", Used: "32M",
			Avail: "992M", UsePercent: "4%", Target: "/boot/efi"},
		{Source: "/dev/sda1", FSType: "ext4", Size: "1.8T", Used: "1.4T",
			Avail: "331G", UsePercent: "82%", Target: "/data"},
		{Source: "/dev/sdb1", FSType: "vfat", Size: "29G", Used: "13G",
			Avail: "17G", UsePercent: "43%", Target: "/media/backup"},
	}
}

// DemoFstab exposes the sample file to the tests, which assert that the
// editor's diff against it is the one line the form changed.
func DemoFstab() string { return demoFstab }
