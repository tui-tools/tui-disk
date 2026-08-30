package storage

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// lsblkDevice mirrors one node of `lsblk -J`.
//
// Two fields carry the same fact under different names across util-linux
// releases: MOUNTPOINTS is a list and arrived in 2.37, MOUNTPOINT is the
// single value every older release prints. Both are decoded, and ParseLsblk
// folds them into one list — the manifest gates which column is *asked* for,
// this decodes whichever came back.
type lsblkDevice struct {
	Name        string          `json:"name"`
	KName       string          `json:"kname"`
	Type        string          `json:"type"`
	Size        json.RawMessage `json:"size"`
	FSType      *string         `json:"fstype"`
	FSUsed      json.RawMessage `json:"fsused"`
	FSUsePct    *string         `json:"fsuse%"`
	Mountpoints []*string       `json:"mountpoints"`
	Mountpoint  *string         `json:"mountpoint"`
	Label       *string         `json:"label"`
	UUID        *string         `json:"uuid"`
	Model       *string         `json:"model"`
	Serial      *string         `json:"serial"`
	PartUUID    *string         `json:"partuuid"`
	Rota        json.RawMessage `json:"rota"`
	Tran        *string         `json:"tran"`
	RM          json.RawMessage `json:"rm"`
	RO          json.RawMessage `json:"ro"`
	Children    []lsblkDevice   `json:"children"`
}

// lsblkOutput is the top-level object.
type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

// ParseLsblk turns `lsblk -J` output into the device tree.
//
// Every scalar is decoded through a helper rather than a typed field, because
// lsblk has changed the JSON type of its columns between releases: sizes and
// the boolean flags are strings on util-linux 2.3x and real numbers and
// booleans from 2.38 on, and a tool that decoded `Size string` simply failed
// on half the distributions in the lab.
func ParseLsblk(output string) ([]disk.Device, error) {
	var parsed lsblkOutput
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return nil, fmt.Errorf("storage: cannot parse lsblk JSON: %w", err)
	}
	return convertDevices(parsed.Blockdevices), nil
}

// convertDevices maps the decoded nodes onto the model, recursively.
func convertDevices(nodes []lsblkDevice) []disk.Device {
	var out []disk.Device
	for _, node := range nodes {
		device := disk.Device{
			Name:         node.Name,
			KName:        node.KName,
			Kind:         node.Type,
			Size:         scalar(node.Size),
			FSType:       value(node.FSType),
			FSUsed:       scalar(node.FSUsed),
			FSUsePercent: value(node.FSUsePct),
			Mountpoints:  mountpoints(node),
			Label:        value(node.Label),
			UUID:         value(node.UUID),
			Model:        strings.TrimSpace(value(node.Model)),
			Serial:       value(node.Serial),
			PartUUID:     value(node.PartUUID),
			Rotational:   flag(node.Rota),
			Transport:    value(node.Tran),
			Removable:    flag(node.RM),
			ReadOnly:     flag(node.RO),
		}
		if device.KName == "" {
			device.KName = device.Name
		}
		device.Children = convertDevices(node.Children)
		out = append(out, device)
	}
	return out
}

// mountpoints folds the two spellings of the column into one list, dropping
// the nulls lsblk uses for an unmounted device.
func mountpoints(node lsblkDevice) []string {
	var out []string
	for _, m := range node.Mountpoints {
		if m != nil && *m != "" {
			out = append(out, *m)
		}
	}
	if len(out) == 0 && node.Mountpoint != nil && *node.Mountpoint != "" {
		out = append(out, *node.Mountpoint)
	}
	return out
}

// value dereferences an optional string column.
func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// scalar renders a column that may be a JSON string or a JSON number, which is
// what lsblk did to SIZE and FSUSED when it grew the `--bytes` handling.
func scalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return strings.Trim(string(raw), `"`)
}

// flag reads a column that may be a JSON boolean or the strings lsblk used
// before it emitted real booleans ("0", "1", "true").
func flag(raw json.RawMessage) bool {
	switch strings.Trim(string(raw), `"`) {
	case "true", "1":
		return true
	default:
		return false
	}
}

// findmntNode mirrors one node of `findmnt -J`, which is a tree: a mount's
// children are the mounts underneath it.
type findmntNode struct {
	Target   string          `json:"target"`
	Source   string          `json:"source"`
	FSType   string          `json:"fstype"`
	Options  string          `json:"options"`
	Size     json.RawMessage `json:"size"`
	Used     json.RawMessage `json:"used"`
	Avail    json.RawMessage `json:"avail"`
	UsePct   *string         `json:"use%"`
	Children []findmntNode   `json:"children"`
}

// findmntOutput is the top-level object.
type findmntOutput struct {
	Filesystems []findmntNode `json:"filesystems"`
}

// ParseFindmnt flattens `findmnt -J` into the mount list. The tree structure
// is dropped on purpose: what the user is looking for is "is this mounted and
// does fstab agree", which is a property of one row.
func ParseFindmnt(output string) ([]disk.Mount, error) {
	var parsed findmntOutput
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return nil, fmt.Errorf("storage: cannot parse findmnt JSON: %w", err)
	}
	var out []disk.Mount
	var walk func(nodes []findmntNode)
	walk = func(nodes []findmntNode) {
		for _, node := range nodes {
			out = append(out, disk.Mount{
				Target:     node.Target,
				Source:     node.Source,
				FSType:     node.FSType,
				Options:    node.Options,
				Size:       scalar(node.Size),
				Used:       scalar(node.Used),
				Avail:      scalar(node.Avail),
				UsePercent: value(node.UsePct),
				Mounted:    true,
			})
			walk(node.Children)
		}
	}
	walk(parsed.Filesystems)
	return out, nil
}

// transientTypes are the filesystems nobody writes into fstab: the kernel
// mounts them, systemd mounts them, and reporting them as "not in fstab" would
// bury the two rows that actually matter under forty that never will be.
var transientTypes = map[string]bool{
	"autofs": true, "bpf": true, "cgroup": true, "cgroup2": true,
	"configfs": true, "debugfs": true, "devpts": true, "devtmpfs": true,
	"efivarfs": true, "fusectl": true, "fuse.gvfsd-fuse": true,
	"fuse.portal": true, "hugetlbfs": true, "mqueue": true, "nsfs": true,
	"overlay": true, "proc": true, "pstore": true, "ramfs": true,
	"rpc_pipefs": true, "nfsd": true, "binfmt_misc": true,
	"securityfs": true, "selinuxfs": true, "squashfs": true, "sysfs": true,
	"tracefs": true,
}

// transientPrefixes are the paths a runtime mount lives under. A tmpfs at
// /tmp is a real choice a machine makes and belongs in fstab; a tmpfs at
// /run/user/1000 is logind's business.
var transientPrefixes = []string{
	"/proc", "/sys", "/dev", "/run", "/var/lib/docker", "/var/lib/containers",
	"/var/lib/kubelet", "/snap",
}

// transient reports whether a mount is one no fstab would carry.
//
// Three rules, and each one is here because a real machine tripped it. A
// tmpfs that is absent from fstab was mounted by something else — systemd's
// tmp.mount, logind, a container runtime — and flagging /tmp and /dev/shm on
// every Fedora and Arch machine would drown the rows that matter. A mount
// point with a hidden path component is a program's private business: an
// AppImage mounts itself at /tmp/.mount_XXXXXX. And the kernel and runtime
// filesystems are listed by type and by prefix above.
func transient(mount disk.Mount) bool {
	if transientTypes[mount.FSType] {
		return true
	}
	if mount.FSType == "tmpfs" {
		return true
	}
	for _, component := range strings.Split(mount.Target, "/") {
		if strings.HasPrefix(component, ".") {
			return true
		}
	}
	for _, prefix := range transientPrefixes {
		if mount.Target == prefix || strings.HasPrefix(mount.Target, prefix+"/") {
			return true
		}
	}
	return false
}

// CrossCheck folds fstab into the mount list: every mounted filesystem gets
// the fstab line that covers it, every fstab entry that is not mounted is
// added as a row of its own, and each row gets a verdict.
//
// This is the whole point of the mounts screen. `findmnt` says what is mounted
// now and fstab says what will be mounted next boot, and the interesting rows
// are the ones where those two disagree — a filesystem mounted by hand that
// will vanish on reboot, and an fstab entry that failed to mount and nobody
// noticed.
func CrossCheck(mounts []disk.Mount, entries []disk.FstabEntry) []disk.Mount {
	byTarget := map[string]disk.FstabEntry{}
	for _, entry := range entries {
		if entry.Comment || entry.Target == "" || entry.Target == "none" {
			continue
		}
		byTarget[entry.Target] = entry
	}

	seen := map[string]bool{}
	out := make([]disk.Mount, 0, len(mounts))
	for _, mount := range mounts {
		seen[mount.Target] = true
		entry, ok := byTarget[mount.Target]
		switch {
		case ok:
			mount.InFstab = true
			mount.FstabLine = entry.Line
			mount.FstabOptions = entry.Options
			mount.Match = disk.MatchOK
			if missing := missingOptions(entry.Options, mount.Options); len(missing) > 0 {
				mount.Match = disk.MatchOptionsDiffer
			}
		case transient(mount):
			mount.Match = disk.MatchTransient
		default:
			mount.Match = disk.MatchNotInFstab
		}
		out = append(out, mount)
	}

	// The fstab entries nobody mounted. They are the rows a boot would have
	// failed on, and they are invisible in `findmnt` by definition.
	for _, entry := range entries {
		if entry.Comment || entry.Target == "" || entry.Target == "none" {
			continue
		}
		if seen[entry.Target] {
			continue
		}
		out = append(out, disk.Mount{
			Target:       entry.Target,
			Source:       entry.Spec,
			FSType:       entry.FSType,
			Options:      "",
			Mounted:      false,
			InFstab:      true,
			FstabLine:    entry.Line,
			FstabOptions: entry.Options,
			Match:        disk.MatchNotMounted,
		})
	}
	return out
}

// ignoredOptions are the ones fstab asks for that never appear in the kernel's
// own option list, so their absence is not a mismatch. `defaults` expands to
// several flags; the `x-systemd.*` and `nofail` options are read by the
// generator, not by the kernel; `auto` and `_netdev` likewise; and `umask` on
// a vfat mount is expanded into `fmask` and `dmask`, so the kernel never
// reports the name that was written.
//
// Getting this list wrong is what makes the mismatch column useless: a screen
// that flags every ESP on every machine is a screen people stop reading.
var ignoredOptions = map[string]bool{
	"defaults": true, "auto": true, "noauto": true, "nofail": true,
	"_netdev": true, "user": true, "users": true, "nouser": true,
	"owner": true, "group": true, "comment": true, "rw": true,
	"umask": true,
}

// missingOptions returns the fstab options the kernel is not reporting on the
// live mount. Comparison is by name for a `key=value` option, because the
// kernel normalises several of them ("compress=zstd" comes back as
// "compress=zstd:3").
func missingOptions(fstabOptions, live string) []string {
	if strings.TrimSpace(fstabOptions) == "" {
		return nil
	}
	have := map[string]bool{}
	for _, option := range strings.Split(live, ",") {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		have[option] = true
		if name, _, found := strings.Cut(option, "="); found {
			have[name] = true
		}
	}

	var missing []string
	for _, option := range strings.Split(fstabOptions, ",") {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		name := option
		if cut, _, found := strings.Cut(option, "="); found {
			name = cut
		}
		if ignoredOptions[name] || strings.HasPrefix(name, "x-systemd") {
			continue
		}
		if have[option] || have[name] {
			continue
		}
		missing = append(missing, option)
	}
	return missing
}

// MissingOptions is the exported form of the comparison, used by the detail
// screen to name which options are not in effect.
func MissingOptions(fstabOptions, live string) []string {
	return missingOptions(fstabOptions, live)
}

// ParseDF reads the columns of `df -h --output=source,fstype,size,used,avail,pcent,target`.
//
// df is used rather than the sizes findmnt already reports because it is the
// number every user recognises, and because it is the one command that answers
// for a filesystem the tool has no other read for.
func ParseDF(output string) []disk.SpaceRow {
	var out []disk.SpaceRow
	for i, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		// The first line is the header, whatever the locale rendered it as.
		if i == 0 && strings.EqualFold(fields[0], "Filesystem") {
			continue
		}
		// A target with spaces in it is the last field onwards.
		target := strings.Join(fields[6:], " ")
		out = append(out, disk.SpaceRow{
			Source: fields[0], FSType: fields[1], Size: fields[2],
			Used: fields[3], Avail: fields[4], UsePercent: fields[5],
			Target: target,
		})
	}
	return out
}

// ParseBlkid reads the `blkid -o export` form, which is one `KEY=value` line
// per field and a blank line between devices. The export form is parsed rather
// than the default one because the default quotes values and interleaves them
// on a single line, which is a parser nobody should have to write twice.
func ParseBlkid(output string) []disk.DeviceSpec {
	var out []disk.DeviceSpec
	current := disk.DeviceSpec{}
	flush := func() {
		if current.Device != "" {
			out = append(out, current)
		}
		current = disk.DeviceSpec{}
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "DEVNAME":
			// A new DEVNAME starts a device even without a blank line before
			// it, which is what `blkid -o export <device>` prints.
			flush()
			current.Device = value
		case "UUID":
			current.UUID = value
		case "TYPE":
			current.FSType = value
		case "LABEL":
			current.Label = value
		case "PARTUUID":
			current.PartUUID = value
		}
	}
	flush()
	return out
}

// BlkidFromDevices synthesises the picker list from the lsblk tree.
//
// It is the fallback for a machine where `blkid` cannot be run: the command
// reads /dev directly and needs root, and a user who cannot escalate would
// otherwise get an empty picker on a screen whose whole job is to offer them a
// UUID. lsblk answers the same question unprivileged for anything with a
// filesystem on it.
func BlkidFromDevices(devices []disk.Device) []disk.DeviceSpec {
	var out []disk.DeviceSpec
	var walk func(list []disk.Device)
	walk = func(list []disk.Device) {
		for _, d := range list {
			if d.UUID != "" && d.FSType != "" {
				out = append(out, disk.DeviceSpec{
					Device: d.Path(), UUID: d.UUID, FSType: d.FSType,
					Label: d.Label, PartUUID: d.PartUUID,
				})
			}
			walk(d.Children)
		}
	}
	walk(devices)
	return out
}
