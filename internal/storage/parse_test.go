package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// read loads a captured command output from testdata.
func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests above, and testdata is in the repository
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// find returns the device with a name, searching the whole tree.
func find(devices []disk.Device, name string) (disk.Device, bool) {
	for _, device := range devices {
		if device.Name == name {
			return device, true
		}
		if child, ok := find(device.Children, name); ok {
			return child, true
		}
	}
	return disk.Device{}, false
}

// TestParseLsblkFedora reads the tree captured from a Fedora 42 workstation
// with util-linux 2.40.
func TestParseLsblkFedora(t *testing.T) {
	devices, err := ParseLsblk(read(t, "lsblk-fedora42.json"))
	if err != nil {
		t.Fatalf("ParseLsblk: %v", err)
	}
	if len(devices) != 4 {
		t.Fatalf("top-level devices = %d, want 4", len(devices))
	}

	root, ok := find(devices, "nvme1n1p7")
	if !ok {
		t.Fatal("the root partition is missing from the tree")
	}
	for _, want := range []struct{ name, got, expected string }{
		{"kind", root.Kind, disk.KindPart},
		{"fstype", root.FSType, "btrfs"},
		{"size", root.Size, "328.3G"},
		{"fsused", root.FSUsed, "278.6G"},
		{"uuid", root.UUID, "c56f896c-d8dc-4253-9402-4e7af5267047"},
		{"mountpoint", root.Mountpoint(), "/"},
		{"path", root.Path(), "/dev/nvme1n1p7"},
		{"transport", root.Transport, "nvme"},
	} {
		if want.got != want.expected {
			t.Errorf("%s = %q, want %q", want.name, want.got, want.expected)
		}
	}
	if root.UsePercent() != 85 {
		t.Errorf("use%% = %d, want 85", root.UsePercent())
	}

	// A device-mapper node: the pretty name is not the kernel name, and the
	// device node has to be built from the kernel one.
	mapped, ok := find(devices, "home")
	if !ok {
		t.Fatal("the mapped home volume is missing from the tree")
	}
	if mapped.Path() != "/dev/dm-0" {
		t.Errorf("mapped path = %q, want /dev/dm-0", mapped.Path())
	}
	if mapped.Kind != "crypt" {
		t.Errorf("mapped kind = %q, want crypt", mapped.Kind)
	}

	// The USB stick is removable and spinning-flag false; both come back as
	// real JSON booleans on this util-linux.
	stick, ok := find(devices, "sda")
	if !ok {
		t.Fatal("the USB device is missing from the tree")
	}
	if !stick.Removable {
		t.Error("the USB device should be removable")
	}
	if stick.Transport != "usb" {
		t.Errorf("USB transport = %q, want usb", stick.Transport)
	}
}

// TestParseLsblkLegacyColumns reads the pre-2.37 shape: a single MOUNTPOINT
// rather than a list, and the flags as quoted "0"/"1" rather than booleans.
// A parser that decoded them as typed fields fails outright on this input,
// which is what half the distributions in the lab print.
func TestParseLsblkLegacyColumns(t *testing.T) {
	devices, err := ParseLsblk(read(t, "lsblk-utillinux234.json"))
	if err != nil {
		t.Fatalf("ParseLsblk: %v", err)
	}
	part, ok := find(devices, "sda1")
	if !ok {
		t.Fatal("sda1 is missing from the tree")
	}
	if part.Mountpoint() != "/data" {
		t.Errorf("mountpoint = %q, want /data", part.Mountpoint())
	}
	if !part.Rotational {
		t.Error(`rota "1" should parse as rotational`)
	}
	if part.Removable {
		t.Error(`rm "0" should not parse as removable`)
	}
	rom, ok := find(devices, "sr0")
	if !ok {
		t.Fatal("sr0 is missing from the tree")
	}
	if !rom.Removable {
		t.Error(`rm "1" should parse as removable`)
	}
}

// TestParseFindmnt flattens the tree the command really returns.
func TestParseFindmnt(t *testing.T) {
	mounts, err := ParseFindmnt(read(t, "findmnt-fedora42.json"))
	if err != nil {
		t.Fatalf("ParseFindmnt: %v", err)
	}
	if len(mounts) < 20 {
		t.Fatalf("mounts = %d, want the whole flattened tree", len(mounts))
	}
	if mounts[0].Target != "/" {
		t.Errorf("first mount = %q, want /", mounts[0].Target)
	}
	if mounts[0].FSType != "btrfs" {
		t.Errorf("root fstype = %q, want btrfs", mounts[0].FSType)
	}
	// A child of the root mount must be in the flat list: the tree is what
	// findmnt returns, and a parser that read only the top level would report
	// exactly one mount on every machine.
	var found bool
	for _, mount := range mounts {
		if mount.Target == "/boot" {
			found = true
		}
	}
	if !found {
		t.Error("/boot is missing: the tree was not flattened")
	}
}

// TestParseFstab reads the file captured from the same machine, including its
// commented-out spare entry and its escaped fields.
func TestParseFstab(t *testing.T) {
	entries := ParseFstab(read(t, "fstab-fedora42.txt"))

	var real int
	byTarget := map[string]disk.FstabEntry{}
	for _, entry := range entries {
		if entry.Comment {
			continue
		}
		real++
		byTarget[entry.Target] = entry
	}
	if real != 5 {
		t.Fatalf("real entries = %d, want 5", real)
	}

	root := byTarget["/"]
	if root.FSType != "btrfs" {
		t.Errorf("root fstype = %q, want btrfs", root.FSType)
	}
	if root.Options != "subvol=fedora,compress=zstd:1" {
		t.Errorf("root options = %q", root.Options)
	}
	uuid, ok := root.UUID()
	if !ok || uuid != "c56f896c-d8dc-4253-9402-4e7af5267047" {
		t.Errorf("root UUID = %q, %v", uuid, ok)
	}
	// The commented-out /home line must not be an entry: the file also has a
	// live /home entry, and counting the comment would give the target two.
	if home := byTarget["/home"]; home.Spec != "/dev/mapper/home" {
		t.Errorf("the /home entry is %q, want the uncommented one", home.Spec)
	}
}

// TestFstabRoundTrip asserts that rendering a file back with nothing changed
// changes nothing. It is the property the diff in the confirm dialog rests on.
func TestFstabRoundTrip(t *testing.T) {
	before := read(t, "fstab-fedora42.txt")
	entries := ParseFstab(before)

	var target disk.FstabEntry
	for _, entry := range entries {
		if !entry.Comment && entry.Target == "/boot" {
			target = entry
		}
	}
	if target.Number == 0 {
		t.Fatal("the /boot entry was not found")
	}

	after, err := RenderFstab(before, SpecFromEntry(target))
	if err != nil {
		t.Fatalf("RenderFstab: %v", err)
	}
	// Only the one line may differ, and only in its column padding.
	diff := Diff(FstabPath, before, after)
	changed := 0
	for _, line := range splitLines(diff) {
		if len(line) > 0 && (line[0] == '+' || line[0] == '-') &&
			line != "--- "+FstabPath && line != "+++ "+FstabPath {
			changed++
		}
	}
	if changed > 2 {
		t.Errorf("re-rendering one unchanged entry touched %d lines:\n%s",
			changed, diff)
	}
	// And the entry must read back the same.
	for _, entry := range ParseFstab(after) {
		if entry.Comment || entry.Target != "/boot" {
			continue
		}
		if entry.Spec != target.Spec || entry.FSType != target.FSType ||
			entry.Options != target.Options {
			t.Errorf("round trip changed the entry: %+v vs %+v", entry, target)
		}
	}
}

// TestRenderFstabRefusesDuplicate asserts that adding a second entry for a
// target the file already covers is refused. Two answers for one mount point
// is a machine that mounts whichever the kernel read last.
func TestRenderFstabRefusesDuplicate(t *testing.T) {
	before := read(t, "fstab-fedora42.txt")
	_, err := RenderFstab(before, disk.FstabSpec{
		Spec: "UUID=0f2b1c44-9999-4a2b-9c3d-000000000009", Target: "/boot",
		FSType: "ext4", Options: "defaults",
	})
	if err == nil {
		t.Fatal("a duplicate target was accepted")
	}
}

// TestValidateFstabSpec covers the arguments the form can produce, because the
// spec is the one value that ends up in a file the machine boots from.
func TestValidateFstabSpec(t *testing.T) {
	good := disk.FstabSpec{Spec: "UUID=1A2B-3C4D", Target: "/data",
		FSType: "ext4", Options: "defaults,noatime", Dump: "0", Pass: "2"}
	if err := ValidateFstabSpec(good); err != nil {
		t.Fatalf("a valid spec was refused: %v", err)
	}

	for name, spec := range map[string]disk.FstabSpec{
		"relative target": {Spec: "UUID=x", Target: "data", FSType: "ext4",
			Options: "defaults"},
		"traversal": {Spec: "UUID=1A2B", Target: "/data/../etc", FSType: "ext4",
			Options: "defaults"},
		"spaces in the options": {Spec: "UUID=1A2B", Target: "/data",
			FSType: "ext4", Options: "defaults 0 0 /etc/shadow"},
		"a device spec that is a command": {Spec: "$(reboot)", Target: "/data",
			FSType: "ext4", Options: "defaults"},
		"an fstype with a slash": {Spec: "UUID=1A2B", Target: "/data",
			FSType: "../../bin/sh", Options: "defaults"},
		"a two digit pass": {Spec: "UUID=1A2B", Target: "/data", FSType: "ext4",
			Options: "defaults", Pass: "22"},
	} {
		if err := ValidateFstabSpec(spec); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestFstabEscaping asserts that a mount point with a space in it survives the
// round trip through the octal escapes fstab uses.
func TestFstabEscaping(t *testing.T) {
	rendered := RenderFstabLine(disk.FstabSpec{
		Spec: "UUID=1A2B-3C4D", Target: "/media/my backup", FSType: "vfat",
		Options: "defaults", Dump: "0", Pass: "0"})
	entries := ParseFstab(rendered)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Target != "/media/my backup" {
		t.Errorf("target = %q, want %q", entries[0].Target, "/media/my backup")
	}
}

// TestCrossCheck is the mounts screen's whole reason to exist: what is mounted
// now against what fstab says will be mounted next boot.
func TestCrossCheck(t *testing.T) {
	entries := ParseFstab(`UUID=aaaa /      ext4 defaults      0 1
UUID=bbbb /data  ext4 defaults,noatime 0 2
UUID=cccc /spare ext4 defaults      0 2
`)
	mounts := []disk.Mount{
		{Target: "/", Source: "/dev/sda1", FSType: "ext4",
			Options: "rw,relatime", Mounted: true},
		{Target: "/data", Source: "/dev/sdb1", FSType: "ext4",
			Options: "rw,relatime", Mounted: true},
		{Target: "/media/usb", Source: "/dev/sdc1", FSType: "vfat",
			Options: "rw,relatime", Mounted: true},
		{Target: "/proc", Source: "proc", FSType: "proc",
			Options: "rw", Mounted: true},
		{Target: "/tmp", Source: "tmpfs", FSType: "tmpfs",
			Options: "rw", Mounted: true},
		{Target: "/tmp/.mount_appXY", Source: "app.squashfs",
			FSType: "fuse.app", Options: "ro", Mounted: true},
	}

	crossed := CrossCheck(mounts, entries)
	verdicts := map[string]string{}
	for _, mount := range crossed {
		verdicts[mount.Target] = mount.Match
	}

	for target, want := range map[string]string{
		"/":                 disk.MatchOK,
		"/data":             disk.MatchOptionsDiffer,
		"/media/usb":        disk.MatchNotInFstab,
		"/spare":            disk.MatchNotMounted,
		"/proc":             disk.MatchTransient,
		"/tmp":              disk.MatchTransient,
		"/tmp/.mount_appXY": disk.MatchTransient,
	} {
		if verdicts[target] != want {
			t.Errorf("%s = %q, want %q", target, verdicts[target], want)
		}
	}

	// The fstab entry nobody mounted must be a row of its own: it is invisible
	// in findmnt by definition, and it is the boot that failed.
	var spare disk.Mount
	for _, mount := range crossed {
		if mount.Target == "/spare" {
			spare = mount
		}
	}
	if spare.Mounted || !spare.InFstab {
		t.Errorf("/spare = %+v, want an unmounted fstab row", spare)
	}
}

// TestMissingOptionsIgnoresExpansions asserts that the options a kernel never
// echoes back do not read as a mismatch. Getting this wrong flags the ESP on
// every machine, and a column that is always red is a column nobody reads.
func TestMissingOptionsIgnoresExpansions(t *testing.T) {
	cases := []struct {
		name, fstab, live string
		want              int
	}{
		{"umask expands to fmask and dmask", "umask=0077,shortname=winnt",
			"rw,relatime,fmask=0077,dmask=0077,shortname=winnt", 0},
		{"defaults expands to several flags", "defaults", "rw,relatime", 0},
		{"the systemd generator options are not the kernel's",
			"defaults,nofail,x-systemd.automount", "rw,relatime", 0},
		{"a value the kernel normalises still matches by name",
			"compress=zstd", "rw,compress=zstd:3", 0},
		{"an option that is genuinely not in effect is reported",
			"defaults,noatime", "rw,relatime", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MissingOptions(tc.fstab, tc.live)
			if len(got) != tc.want {
				t.Errorf("missing = %v, want %d of them", got, tc.want)
			}
		})
	}
}

// TestParseDF reads the columns of the captured df output.
func TestParseDF(t *testing.T) {
	rows := ParseDF(read(t, "df-fedora42.txt"))
	if len(rows) < 5 {
		t.Fatalf("rows = %d, want the whole table", len(rows))
	}
	if rows[0].Target != "/" || rows[0].FSType != "btrfs" {
		t.Errorf("first row = %+v", rows[0])
	}
	if rows[0].UsePercentValue() != 86 {
		t.Errorf("use%% = %d, want 86", rows[0].UsePercentValue())
	}
}

// TestParseBlkid reads the export form, which is the only output of blkid
// worth parsing.
func TestParseBlkid(t *testing.T) {
	specs := ParseBlkid(read(t, "blkid-export.txt"))
	if len(specs) != 3 {
		t.Fatalf("specs = %d, want 3", len(specs))
	}
	if specs[1].Device != "/dev/nvme0n1p2" || specs[1].FSType != "btrfs" {
		t.Errorf("second spec = %+v", specs[1])
	}
	if specs[1].Label != "fedora" {
		t.Errorf("label = %q, want fedora", specs[1].Label)
	}
	// UUID_SUB must not be mistaken for UUID: on a multi-device btrfs it is a
	// different value, and writing it into fstab produces a machine that does
	// not boot.
	if specs[1].UUID != "0f2b1c44-1111-4a2b-9c3d-000000000001" {
		t.Errorf("uuid = %q", specs[1].UUID)
	}
}

// TestBlkidFromDevices asserts the unprivileged fallback: a picker built from
// what lsblk already reported, for a machine where blkid cannot be run.
func TestBlkidFromDevices(t *testing.T) {
	devices, err := ParseLsblk(read(t, "lsblk-fedora42.json"))
	if err != nil {
		t.Fatalf("ParseLsblk: %v", err)
	}
	specs := BlkidFromDevices(devices)
	if len(specs) == 0 {
		t.Fatal("the fallback picker is empty")
	}
	for _, spec := range specs {
		if spec.UUID == "" || spec.FSType == "" {
			t.Errorf("a device with no filesystem got into the picker: %+v", spec)
		}
	}
}
