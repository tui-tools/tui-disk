// Package storage is the storage backend of tui-disk, and the only package in
// the repository that starts a process.
//
// Everything about reaching the machine — resolving the binaries, applying the
// privilege prefix, bounding each call, turning a failure into one readable
// line — belongs to the kit runner. What is left here is the translation
// between the output of the storage tools and the backend-neutral model in
// internal/disk, and the assembly of the argv that a confirm dialog will show
// before it runs.
//
// Three families of program are driven, and the manifest declares each as a
// backend of its own so its version is probed and reported:
//
//	util-linux    lsblk, findmnt, blkid and df: the devices, the mounts and
//	              the space. Required.
//	btrfs-progs   btrfs: usage, subvolumes, qgroups, scrub, balance and the
//	              per-device error counters. Optional; the view is hidden on
//	              a machine without it.
//	smartmontools smartctl: the drive health. Optional, and the one read that
//	              genuinely needs root.
//
// Two more are used for the writes: `install` copies a staged fstab into
// place, and `systemctl daemon-reload` regenerates the units fstab produces.
// `mount` and `umount` apply one entry.
//
// # Which reads escalate
//
// The family rule is that reading is unprivileged and only a change escalates.
// Storage is where that rule needs an exception written down, because several
// reads simply do not answer to a normal user:
//
//	blkid                     reads /dev directly: empty output without root
//	btrfs subvolume list      "ERROR: can't perform the search"
//	btrfs qgroup show         "ERROR: can't list qgroups"
//	btrfs balance status      "ERROR: … Operation not permitted"
//	smartctl -a               needs the raw device
//
// while lsblk, findmnt, df, `btrfs filesystem usage`, `btrfs scrub status` and
// `btrfs device stats` all answer to anyone. Rather than escalate everything
// or nothing, each program gets two runners — a plain one and an escalated one
// — and the reads that can need root go through readEscalating, which tries
// the plain call first and retries with `sudo -n` only when the answer looks
// like a permission failure. A machine where `sudo -n` prompts loses those
// sections and keeps the rest, with a note saying which.
//
// This is not a hypothetical: tui-network and tui-snapper both shipped with
// reads that silently returned nothing on an unprivileged run, and the screens
// that depended on them looked like a machine with no configuration rather
// than a tool that could not look.
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
)

// ErrNotAvailable reports that the storage backend cannot be used on this
// machine (lsblk missing, or no non-interactive privilege escalation).
var ErrNotAvailable = runner.ErrNotAvailable

// BackendUtilLinux, BackendBtrfs and BackendSmartmontools are the manifest's
// names for the three backends. The version probe is keyed on them.
const (
	BackendUtilLinux     = "util-linux"
	BackendBtrfs         = "btrfs-progs"
	BackendSmartmontools = "smartmontools"
)

// searchPaths are the locations a non-root PATH commonly omits. Every one of
// these tools has lived in an sbin directory on some distribution.
var searchPaths = map[string][]string{
	"lsblk":     {"/usr/bin/lsblk", "/bin/lsblk"},
	"findmnt":   {"/usr/bin/findmnt", "/bin/findmnt"},
	"blkid":     {"/usr/sbin/blkid", "/sbin/blkid", "/usr/bin/blkid"},
	"df":        {"/usr/bin/df", "/bin/df"},
	"btrfs":     {"/usr/sbin/btrfs", "/sbin/btrfs", "/usr/bin/btrfs"},
	"smartctl":  {"/usr/sbin/smartctl", "/sbin/smartctl", "/usr/bin/smartctl"},
	"mount":     {"/usr/bin/mount", "/bin/mount"},
	"umount":    {"/usr/bin/umount", "/bin/umount"},
	"install":   {"/usr/bin/install", "/bin/install"},
	"systemctl": {"/usr/bin/systemctl", "/bin/systemctl"},
	"ncdu":      {"/usr/bin/ncdu", "/bin/ncdu"},
}

// installHint is appended to the "not found" error for the one binary the
// tool cannot run without.
const installHint = "it ships in the util-linux package; " +
	"or use --demo to explore the UI"

// permissionMarkers are the phrases the storage tools use when a read failed
// for want of privileges. They are matched as text because none of these
// commands distinguishes the case in its exit status: `blkid` even exits 0
// with an empty body.
var permissionMarkers = []string{
	"operation not permitted",
	"permission denied",
	"must be run as root",
	"you must be root",
	"requires root",
}

// permissionDenied reports whether a read failed because it was unprivileged.
func permissionDenied(output string, err error) bool {
	text := strings.ToLower(output)
	if err != nil {
		text += " " + strings.ToLower(err.Error())
	}
	for _, marker := range permissionMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Real drives the storage tools on the host. It satisfies disk.Backend.
type Real struct {
	// The plain runners: what an unprivileged read goes through.
	lsblk   *runner.Runner
	findmnt *runner.Runner
	df      *runner.Runner
	btrfs   *runner.Runner

	// The escalated runners. blkid and smartctl have no plain form worth
	// trying; btrfsRoot and blkid back the reads listed in the package
	// comment, and mount, umount, install and systemctl are the mutations.
	blkid     *runner.Runner
	btrfsRoot *runner.Runner
	smartctl  *runner.Runner
	mount     *runner.Runner
	umount    *runner.Runner
	install   *runner.Runner
	systemctl *runner.Runner

	// ncduPath is where ncdu was found, empty when it is absent. It is never
	// run: the space view names it as the next step for a user who wants to
	// know what is filling a filesystem, because recursing a tree is not
	// something a preview-and-confirm tool should be doing in v0.1.
	ncduPath string

	// caps gates the reads that only exist on a new enough util-linux,
	// btrfsCaps the ones that need a new enough btrfs-progs, and smartCaps
	// the JSON smartmontools only learned in 7.0. All three come from the
	// manifest, so no version number is written into this file.
	caps      compat.Caps
	btrfsCaps compat.Caps
	smartCaps compat.Caps

	// notes collects what could not be read on this machine, so the UI can
	// say so instead of showing an empty section.
	notes []string
}

// Available reports whether lsblk is installed on this host.
func Available() bool {
	return runner.Available("lsblk", searchPaths["lsblk"]...)
}

// HasBtrfs and HasSmartctl report whether the optional backends are present.
func HasBtrfs() bool {
	return runner.Available("btrfs", searchPaths["btrfs"]...)
}

func HasSmartctl() bool {
	return runner.Available("smartctl", searchPaths["smartctl"]...)
}

// NewReal locates the binaries and, when not running as root, validates the
// configured privilege prefix. sudoPrefix comes from the configuration
// ("sudo -n"); pass nil to run the commands directly.
func NewReal(sudoPrefix []string, caps, btrfsCaps, smartCaps compat.Caps) (*Real, error) {
	real := &Real{caps: caps, btrfsCaps: btrfsCaps, smartCaps: smartCaps}
	unprivileged := false

	// The plain read runners. Only lsblk is essential: without findmnt, df or
	// btrfs the tool still shows the devices and says what is missing.
	plain := []struct {
		bin    string
		target **runner.Runner
	}{
		{"lsblk", &real.lsblk},
		{"findmnt", &real.findmnt},
		{"df", &real.df},
		{"btrfs", &real.btrfs},
	}
	for _, spec := range plain {
		r, err := runner.New(runner.Options{
			Bin:             spec.bin,
			SearchPaths:     searchPaths[spec.bin],
			SudoPrefix:      sudoPrefix,
			InstallHint:     installHint,
			PrivilegedReads: &unprivileged,
		})
		if err != nil {
			if spec.bin == "lsblk" {
				return nil, err
			}
			real.notef("%s is not installed, so the sections that need it are empty",
				spec.bin)
			continue
		}
		*spec.target = r
	}

	// The escalating runners. A missing one is never fatal: the section it
	// serves says why it is empty.
	escalating := []struct {
		bin    string
		target **runner.Runner
	}{
		{"blkid", &real.blkid},
		{"btrfs", &real.btrfsRoot},
		{"smartctl", &real.smartctl},
		{"mount", &real.mount},
		{"umount", &real.umount},
		{"install", &real.install},
		{"systemctl", &real.systemctl},
	}
	for _, spec := range escalating {
		r, err := runner.New(runner.Options{
			Bin:         spec.bin,
			SearchPaths: searchPaths[spec.bin],
			SudoPrefix:  sudoPrefix,
		})
		if err != nil {
			continue
		}
		*spec.target = r
	}

	if path, err := lookNcdu(); err == nil {
		real.ncduPath = path
	}
	return real, nil
}

// lookNcdu finds ncdu without building a runner for it: the tool only ever
// names it, and a runner would imply it could be run.
func lookNcdu() (string, error) {
	for _, candidate := range searchPaths["ncdu"] {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

// notef records a one-line fact about something that could not be read.
func (r *Real) notef(format string, args ...any) {
	note := fmt.Sprintf(format, args...)
	for _, existing := range r.notes {
		if existing == note {
			return
		}
	}
	r.notes = append(r.notes, note)
}

// Name identifies the backend. It is the manifest's name for the one backend
// the tool cannot run without, which is what the header's version badge shows.
func (r *Real) Name() string { return BackendUtilLinux }

// Describe names the backend for the header.
func (r *Real) Describe() string { return r.lsblk.Describe() }

// Capabilities reports what this backend supports.
func (r *Real) Capabilities() disk.Capabilities {
	return disk.Capabilities{
		HasBtrfs:          r.btrfs != nil,
		HasSMART:          r.smartctl != nil,
		SupportsMount:     r.mount != nil && r.umount != nil,
		SupportsFstabEdit: r.install != nil,
		FstabPath:         FstabPath,
		OptionPresets:     OptionPresets,
		FSTypes:           FSTypes,
		NcduPath:          r.ncduPath,
	}
}

// runnerFor picks the runner that owns a command, by its argv[0]. Only the
// mutations are listed: a command this map does not know is one no screen can
// build, and Run refuses it by name.
func (r *Real) runnerFor(cmd disk.Command) *runner.Runner {
	if len(cmd.Argv) == 0 {
		return nil
	}
	switch cmd.Argv[0] {
	case "mount":
		return r.mount
	case "umount":
		return r.umount
	case "install":
		return r.install
	case "systemctl":
		return r.systemctl
	case "btrfs":
		return r.btrfsRoot
	case "smartctl":
		return r.smartctl
	default:
		return nil
	}
}

// Preview renders the exact command line Run will execute. Every command goes
// through the runner of its own binary, so the preview carries the privilege
// prefix that binary will really be called with.
func (r *Real) Preview(cmd disk.Command) string {
	if run := r.runnerFor(cmd); run != nil {
		return run.Preview(cmd)
	}
	return cmd.String()
}

// Run executes a previewed command.
func (r *Real) Run(ctx context.Context, cmd disk.Command) (string, error) {
	run := r.runnerFor(cmd)
	if run == nil {
		return "", fmt.Errorf("storage: %q is not available on this machine",
			firstArg(cmd))
	}
	return run.Run(ctx, cmd)
}

// firstArg names the binary a command wanted, for an error message.
func firstArg(cmd disk.Command) string {
	if len(cmd.Argv) == 0 {
		return "(empty command)"
	}
	return cmd.Argv[0]
}

// readEscalating runs a read that may need root: the plain call first, and the
// escalated one only when the answer looks like a permission failure. See the
// package comment for why this exists.
func readEscalating(ctx context.Context, plain, escalated *runner.Runner,
	argv ...string) (string, error) {
	if plain == nil && escalated == nil {
		return "", fmt.Errorf("storage: %s is not available on this machine", argv[0])
	}
	var out string
	var err error
	if plain != nil {
		out, err = plain.Read(ctx, argv...)
		if err == nil && !permissionDenied(out, nil) {
			return out, nil
		}
		if err != nil && !permissionDenied(out, err) {
			return out, err
		}
	}
	if escalated == nil {
		return out, err
	}
	return escalated.Read(ctx, argv...)
}

// Load reads the machine's storage picture.
//
// The read is layered, and every layer is allowed to fail on its own: a
// machine with no btrfs and no smartctl still shows its devices, its mounts
// and its space, and says in the notes what was skipped. Only a total failure
// to list the block devices is an error.
func (r *Real) Load(ctx context.Context) (disk.Model, error) {
	r.notes = nil
	model := disk.Model{Backend: r.Name(), FstabPath: FstabPath,
		NcduPath: r.ncduPath}

	devices, err := r.loadDevices(ctx)
	if err != nil {
		return disk.Model{}, err
	}
	model.Devices = devices

	model.Fstab = r.loadFstab()
	model.Mounts = CrossCheck(r.loadMounts(ctx), model.Fstab)
	model.Space = r.loadSpace(ctx)
	model.Btrfs = r.loadBtrfs(ctx, model.Mounts, model.Devices)
	model.SMART = r.loadSMART(ctx, model.Devices)
	attachHealth(model.Devices, model.SMART)

	model.Notes = r.notes
	return model, nil
}

// loadDevices reads the block device tree, asking for the mount point column
// under the name this util-linux knows. lsblk fails the whole call on an
// unknown column, so the spelling is a capability rather than a fallback.
func (r *Real) loadDevices(ctx context.Context) ([]disk.Device, error) {
	argv := BuildLsblk(r.caps.Has(FeatureMountpoints))
	out, err := r.lsblk.Read(ctx, argv...)
	if err != nil {
		return nil, err
	}
	return ParseLsblk(out)
}

// loadMounts reads the mount table.
func (r *Real) loadMounts(ctx context.Context) []disk.Mount {
	if r.findmnt == nil {
		return nil
	}
	out, err := r.findmnt.Read(ctx, BuildFindmnt()...)
	if err != nil {
		r.notef("the mount table could not be read: %s", runner.FirstLine(err.Error()))
		return nil
	}
	mounts, err := ParseFindmnt(out)
	if err != nil {
		r.notef("the mount table could not be parsed: %s", err)
		return nil
	}
	return mounts
}

// loadFstab reads /etc/fstab. It is world-readable on every distribution, so
// this is a plain file read rather than a command.
func (r *Real) loadFstab() []disk.FstabEntry {
	raw, err := os.ReadFile(FstabPath)
	if err != nil {
		r.notef("%s could not be read: %s", FstabPath, err)
		return nil
	}
	return ParseFstab(string(raw))
}

// loadSpace reads the df view.
func (r *Real) loadSpace(ctx context.Context) []disk.SpaceRow {
	if r.df == nil {
		return nil
	}
	out, err := r.df.Read(ctx, BuildDF()...)
	if err != nil {
		return nil
	}
	return ParseDF(out)
}

// LoadSpecs reads the UUID list the fstab picker offers, falling back to what
// lsblk already reported when blkid cannot be run. See BlkidFromDevices.
func (r *Real) LoadSpecs(ctx context.Context, devices []disk.Device) []disk.DeviceSpec {
	if r.blkid != nil {
		if out, err := r.blkid.Read(ctx, BuildBlkid()...); err == nil {
			if entries := ParseBlkid(out); len(entries) > 0 {
				return entries
			}
		}
	}
	return BlkidFromDevices(devices)
}

// loadBtrfs reads every mounted btrfs filesystem, once per mount point that is
// the top of one.
func (r *Real) loadBtrfs(ctx context.Context, mounts []disk.Mount,
	devices []disk.Device) []disk.Btrfs {
	if r.btrfs == nil && r.btrfsRoot == nil {
		return nil
	}
	var out []disk.Btrfs
	seen := map[string]bool{}
	for _, mount := range mounts {
		if mount.FSType != "btrfs" || !mount.Mounted || seen[mount.Source] {
			continue
		}
		// One filesystem is often mounted several times, once per subvolume.
		// Reading it once per source keeps the view one row per filesystem.
		seen[mount.Source] = true
		fs := r.readBtrfs(ctx, mount.Target)
		identifyBtrfs(&fs, devices)
		out = append(out, fs)
	}
	return out
}

// identifyBtrfs fills in the filesystem's UUID and label from the device tree.
//
// They come from lsblk rather than from `btrfs filesystem show`, because that
// command answers an unprivileged caller with "size 0 … MISSING" — a report
// that looks like a broken array and is nothing of the kind. lsblk already
// read both, unprivileged and correctly.
func identifyBtrfs(fs *disk.Btrfs, devices []disk.Device) {
	for _, device := range flatten(devices) {
		if device.FSType != "btrfs" {
			continue
		}
		if !containsString(fs.Devices, device.Path()) &&
			device.Mountpoint() != fs.Mountpoint {
			continue
		}
		fs.UUID, fs.Label = device.UUID, device.Label
		return
	}
}

// flatten walks the device tree depth-first.
func flatten(devices []disk.Device) []disk.Device {
	var out []disk.Device
	for _, device := range devices {
		out = append(out, device)
		out = append(out, flatten(device.Children)...)
	}
	return out
}

// containsString reports whether a list holds a value.
func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// readBtrfs runs the six reads that make up one filesystem's view. Each is
// allowed to fail on its own, because they do not all need the same
// privileges: usage, scrub status and device stats answer to anyone, while the
// subvolume list, the qgroups and the balance status do not.
func (r *Real) readBtrfs(ctx context.Context, mountpoint string) disk.Btrfs {
	fs := disk.Btrfs{Mountpoint: mountpoint}

	if out, err := r.readBtrfsPlain(ctx, "filesystem", "usage", mountpoint); err == nil {
		fs.Usage = ParseBtrfsUsage(out)
	}
	if out, err := r.readBtrfsRoot(ctx, "subvolume", "list", "-p", mountpoint); err == nil {
		fs.Subvolumes = ParseSubvolumes(out)
	} else {
		r.notef("the subvolumes of %s need root to list: %s",
			mountpoint, runner.FirstLine(err.Error()))
	}
	if out, err := r.readBtrfsRoot(ctx, "qgroup", "show", "-re", mountpoint); err == nil {
		fs.Qgroups = ParseQgroups(out)
		fs.QuotaOn = len(fs.Qgroups) > 0
	}
	if out, err := r.readBtrfsPlain(ctx, "scrub", "status", mountpoint); err == nil {
		fs.Scrub = ParseScrubStatus(out)
	} else {
		fs.Scrub = disk.Scrub{State: disk.TaskUnknown,
			Detail: runner.FirstLine(err.Error())}
	}
	if out, err := r.readBtrfsRoot(ctx, "balance", "status", mountpoint); err == nil {
		fs.Balance = ParseBalanceStatus(out)
	} else {
		fs.Balance = disk.Balance{State: disk.TaskUnknown,
			Detail: runner.FirstLine(err.Error())}
	}
	fs.DeviceStats = r.readDeviceStats(ctx, mountpoint)
	for _, stat := range fs.DeviceStats {
		fs.Devices = append(fs.Devices, stat.Device)
		fs.Errors += stat.Errors()
	}
	return fs
}

// readBtrfsPlain runs a btrfs read that does not need root.
func (r *Real) readBtrfsPlain(ctx context.Context, args ...string) (string, error) {
	if r.btrfs == nil {
		return "", fmt.Errorf("storage: btrfs is not available on this machine")
	}
	return r.btrfs.Read(ctx, append([]string{"btrfs"}, args...)...)
}

// readBtrfsRoot runs a btrfs read that may need root, escalating only if the
// plain call is refused.
func (r *Real) readBtrfsRoot(ctx context.Context, args ...string) (string, error) {
	out, err := readEscalating(ctx, r.btrfs, r.btrfsRoot,
		append([]string{"btrfs"}, args...)...)
	if err != nil {
		return "", err
	}
	if permissionDenied(out, nil) {
		return "", fmt.Errorf("%s", runner.FirstLine(out))
	}
	return out, nil
}

// readDeviceStats reads the per-device error counters, preferring JSON.
//
// `device stats` is one of the two btrfs commands that really does emit JSON,
// so the capability gate is worth having here; everything else in this file
// parses text because btrfs-progs refuses `--format json` for it outright,
// through 6.19 at least.
func (r *Real) readDeviceStats(ctx context.Context, mountpoint string) []disk.DeviceStat {
	if r.btrfsCaps.Has(FeatureBtrfsJSON) {
		out, err := r.readBtrfsPlain(ctx, "--format", "json", "device", "stats",
			mountpoint)
		if err == nil {
			if stats, parseErr := ParseDeviceStatsJSON(out); parseErr == nil {
				return stats
			}
		}
	}
	out, err := r.readBtrfsPlain(ctx, "device", "stats", mountpoint)
	if err != nil {
		return nil
	}
	return ParseDeviceStatsText(out)
}

// loadSMART reads the health of every whole disk.
//
// This is the one read that genuinely needs root everywhere: smartctl talks to
// the raw device. A machine where `sudo -n` prompts gets HealthUnknown with
// the reason, which is the honest answer and is what the lab asserts on the
// virtio disks that carry no SMART at all.
func (r *Real) loadSMART(ctx context.Context, devices []disk.Device) []disk.SMART {
	if r.smartctl == nil {
		return nil
	}
	// Below smartmontools 7.0 there is no machine-readable output at all.
	// Reporting every drive as unknown is the honest answer; scraping the
	// human table would be a parser nobody could trust.
	if !r.smartCaps.Has(FeatureSmartJSON) {
		since, _ := r.smartCaps.Since(FeatureSmartJSON)
		r.notef("smartctl %s has no --json (it arrived in %s), so no drive "+
			"health could be read", r.smartCaps.Version(), since)
		return nil
	}
	var out []disk.SMART
	for _, device := range devices {
		if !device.IsDisk() {
			continue
		}
		// A zram or loop device is a disk to the kernel and has no firmware to
		// ask; skipping them keeps the health view to the drives that have one.
		if isVirtualDisk(device) {
			continue
		}
		out = append(out, r.readSMART(ctx, device.Path()))
	}
	return out
}

// virtualPrefixes are the device names that are disks to the kernel but have
// no drive behind them.
var virtualPrefixes = []string{"zram", "loop", "ram", "md", "dm-"}

// isVirtualDisk reports whether a whole device has no firmware to ask.
func isVirtualDisk(device disk.Device) bool {
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(device.KName, prefix) {
			return true
		}
	}
	return false
}

// readSMART asks one drive, turning every failure into a reading rather than
// an error.
func (r *Real) readSMART(ctx context.Context, path string) disk.SMART {
	argv, err := BuildSmartRead(path)
	if err != nil {
		return UnavailableSMART(path, err.Error())
	}
	out, err := r.smartctl.Read(ctx, argv...)
	// smartctl exits non-zero for facts that are not failures — bit 1 means
	// the device does not speak SMART, and the higher bits are the drive's own
	// warnings — so the body is parsed whatever the status was. What is *not*
	// parsed is a body that is not JSON at all: that is `sudo` refusing to
	// escalate, and reporting it as a parse error would blame the wrong thing.
	// Found on a workstation whose `sudo -n` prompts: every drive came back
	// "invalid character 's'".
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		reason := "smartctl returned nothing"
		switch {
		case err != nil:
			reason = runner.FirstLine(err.Error())
		case strings.TrimSpace(out) != "":
			reason = runner.FirstLine(out)
		}
		r.notef("the health of %s could not be read: %s", path, reason)
		return UnavailableSMART(path, reason)
	}
	reading, parseErr := ParseSMART(path, out)
	if parseErr != nil {
		return UnavailableSMART(path, runner.FirstLine(parseErr.Error()))
	}
	return reading
}

// attachHealth copies each drive's reading onto its device node, so the device
// table can show a health column without looking anything up.
func attachHealth(devices []disk.Device, readings []disk.SMART) {
	byPath := map[string]disk.SMART{}
	for _, reading := range readings {
		byPath[reading.Device] = reading
	}
	for i := range devices {
		if reading, ok := byPath[devices[i].Path()]; ok {
			devices[i].Health = reading
		}
	}
}

// LoadMount re-reads one mount in full: the fstab line that covers it, and
// what `findmnt --verify` says about the file as a whole.
//
// The verify is run against the live /etc/fstab rather than a copy, because
// what the user is asking on this screen is "is my fstab sound", and the
// answer must be about the file the machine will actually boot from.
func (r *Real) LoadMount(ctx context.Context, target string) (disk.MountDetail, error) {
	if err := checkTarget(target); err != nil {
		return disk.MountDetail{}, err
	}
	detail := disk.MountDetail{Target: target}
	for _, entry := range ParseFstab(readFileOrEmpty(FstabPath)) {
		if !entry.Comment && entry.Target == target {
			detail.FstabLine = entry.Line
			break
		}
	}
	if r.findmnt == nil {
		detail.VerifyErr = "findmnt is not installed on this machine"
		return detail, nil
	}
	out, err := r.findmnt.Read(ctx, BuildFindmntVerify(FstabPath)...)
	detail.Verify = out
	if err != nil && strings.TrimSpace(out) == "" {
		detail.VerifyErr = runner.FirstLine(err.Error())
	}
	return detail, nil
}

// readFileOrEmpty reads a file, returning an empty string when it cannot.
func readFileOrEmpty(path string) string {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the fstab constant
	if err != nil {
		return ""
	}
	return string(raw)
}

// BuildMount, BuildUmount and BuildDaemonReload forward to the builders, which
// are where the validation lives.
func (r *Real) BuildMount(target string) (disk.Command, error) {
	return BuildMount(target)
}

func (r *Real) BuildUmount(target string) (disk.Command, error) {
	return BuildUmount(target)
}

func (r *Real) BuildDaemonReload() (disk.Command, error) {
	return BuildDaemonReload()
}

// BuildWriteFstab stages the new /etc/fstab in a temporary directory, has
// findmnt verify that staged file, and returns the diff plus the two commands
// that apply it.
//
// The order matters and is the point of the whole method. The file is written
// somewhere harmless, `findmnt --verify --tab-file` is run against *that* copy,
// and only a file that passed reaches the confirm dialog. A user cannot
// approve an fstab that would not parse, because one never gets that far — and
// on the machine's real fstab, a line that does not parse is a boot that drops
// to an emergency shell.
func (r *Real) BuildWriteFstab(ctx context.Context,
	spec disk.FstabSpec) (disk.WritePlan, error) {
	before := readFileOrEmpty(FstabPath)
	content, err := RenderFstab(before, spec)
	if err != nil {
		return disk.WritePlan{}, err
	}
	if before == content {
		return disk.WritePlan{}, fmt.Errorf("%s already says exactly this", FstabPath)
	}

	temp, err := stageFile(FstabPath, content)
	if err != nil {
		return disk.WritePlan{}, err
	}
	verify, err := r.verifyStaged(ctx, temp)
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
		Diff:     Diff(FstabPath, before, content),
		TempPath: temp,
		Verify:   verify,
		Commands: []disk.Command{installCmd, reloadCmd},
	}, nil
}

// verifyStaged runs `findmnt --verify` against the staged file and refuses the
// plan when it reports an error. A warning is kept and shown in the dialog:
// findmnt warns about things that are true and harmless — a mount point that
// does not exist yet is one — and refusing those would make the editor useless
// for exactly the entry somebody is adding.
func (r *Real) verifyStaged(ctx context.Context, path string) (string, error) {
	if r.findmnt == nil {
		return "findmnt is not installed, so the file was not verified", nil
	}
	out, err := r.findmnt.Read(ctx, BuildFindmntVerify(path)...)
	if err == nil {
		return out, nil
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("the staged fstab could not be verified: %s",
			runner.FirstLine(err.Error()))
	}
	return "", fmt.Errorf("findmnt refused the new %s: %s", FstabPath,
		firstErrorLine(out))
}

// firstErrorLine picks the line of a findmnt --verify report that names the
// problem, so the message is the reason rather than the summary.
func firstErrorLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[E]") || strings.Contains(trimmed, "parse error") {
			return trimmed
		}
	}
	return runner.FirstLine(strings.TrimSpace(output))
}

// stageFile writes the pending file to a private temporary directory and
// returns its path. The directory is the user's own, so staging needs no
// privileges; only the install step does.
func stageFile(destination, content string) (string, error) {
	dir, err := os.MkdirTemp("", "tui-disk-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.Base(destination))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// The btrfs and SMART mutations forward to the builders, refusing early when
// the backend they need is not installed.
func (r *Real) BuildScrubStart(mountpoint string) (disk.Command, error) {
	if err := r.requireBtrfs(); err != nil {
		return disk.Command{}, err
	}
	return BuildScrubStart(mountpoint)
}

func (r *Real) BuildScrubCancel(mountpoint string) (disk.Command, error) {
	if err := r.requireBtrfs(); err != nil {
		return disk.Command{}, err
	}
	return BuildScrubCancel(mountpoint)
}

func (r *Real) BuildBalanceStart(mountpoint, filter string) (disk.Command, error) {
	if err := r.requireBtrfs(); err != nil {
		return disk.Command{}, err
	}
	return BuildBalanceStart(mountpoint, filter)
}

func (r *Real) BuildBalanceCancel(mountpoint string) (disk.Command, error) {
	if err := r.requireBtrfs(); err != nil {
		return disk.Command{}, err
	}
	return BuildBalanceCancel(mountpoint)
}

func (r *Real) BuildSelfTest(device, kind string) (disk.Command, error) {
	if r.smartctl == nil {
		return disk.Command{}, fmt.Errorf(
			"storage: smartmontools is not installed on this machine")
	}
	return BuildSelfTest(device, kind)
}

// requireBtrfs refuses a btrfs mutation on a machine that has no btrfs.
func (r *Real) requireBtrfs() error {
	if r.btrfsRoot == nil {
		return fmt.Errorf("storage: btrfs-progs is not installed on this machine")
	}
	return nil
}
