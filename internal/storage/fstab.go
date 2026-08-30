package storage

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// FstabPath is the file tui-disk reads and, with a confirmation, writes. It is
// the only path this tool will ever install to.
const FstabPath = "/etc/fstab"

// FstabMode is the mode a written fstab gets. It is what every distribution
// ships the file with, and `install -m` sets it in the same call that copies
// the file, so there is no window where /etc/fstab is on disk with the wrong
// permissions.
const FstabMode = "644"

// ParseFstab reads the file into entries, keeping the comment and blank lines
// as entries of their own.
//
// Keeping them matters: the file is rendered back whole when an entry is
// added, and a rewrite that dropped the "created by anaconda" header and the
// commented-out spare line would show a diff nobody wants to read and lose
// text the administrator put there on purpose.
func ParseFstab(raw string) []disk.FstabEntry {
	var out []disk.FstabEntry
	for i, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		entry := disk.FstabEntry{Line: line, Number: i + 1}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			entry.Comment = true
			out = append(out, entry)
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			// A line with fewer than three fields is not an fstab entry at
			// all. It is kept verbatim rather than dropped, so a malformed
			// file survives a round trip and the user sees it in the diff.
			entry.Comment = true
			out = append(out, entry)
			continue
		}
		entry.Spec, entry.Target, entry.FSType = fields[0], unescape(fields[1]), fields[2]
		if len(fields) > 3 {
			entry.Options = fields[3]
		} else {
			entry.Options = "defaults"
		}
		if len(fields) > 4 {
			entry.Dump = fields[4]
		}
		if len(fields) > 5 {
			entry.Pass = fields[5]
		}
		out = append(out, entry)
	}
	return out
}

// unescape resolves the octal escapes fstab uses for the characters that
// cannot appear in a whitespace-separated field: a mount point with a space in
// it is written "\040".
func unescape(field string) string {
	if !strings.Contains(field, `\`) {
		return field
	}
	var b strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] != '\\' || i+3 >= len(field) {
			b.WriteByte(field[i])
			continue
		}
		var value int
		// Three octal digits reach 0777, but an fstab escape stands for one
		// byte, so anything above 0377 is not an escape at all.
		if _, err := fmt.Sscanf(field[i+1:i+4], "%o", &value); err != nil || value <= 0 || value > 0xff {
			b.WriteByte(field[i])
			continue
		}
		b.WriteByte(byte(value))
		i += 3
	}
	return b.String()
}

// escape is the inverse: the characters fstab splits on are written back as
// octal escapes, so a mount point with a space in it round-trips.
func escape(field string) string {
	replacer := strings.NewReplacer(
		` `, `\040`, "\t", `\011`, "\n", `\012`, `\`, `\134`)
	return replacer.Replace(field)
}

// targetRe is the set of paths the editor will write. It is deliberately
// narrow: an absolute path, no "..", and only the characters a mount point
// normally carries. A target that needs more than this is a job for an editor,
// not for a guided form.
var targetRe = regexp.MustCompile(`^/[A-Za-z0-9._@+/-]*$`)

// specRe accepts the specs the form writes: a UUID, a PARTUUID, a LABEL, or a
// device path under /dev.
var specRe = regexp.MustCompile(
	`^(UUID=[A-Za-z0-9-]+|PARTUUID=[A-Za-z0-9-]+|LABEL=[A-Za-z0-9._-]+|/dev/[A-Za-z0-9._/-]+)$`)

// fsTypeRe accepts a filesystem type name.
var fsTypeRe = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)

// optionsRe accepts the option field: a comma-separated list of the characters
// mount options are made of. A space would split the field into two, which is
// the one way a bad option string can corrupt the file.
var optionsRe = regexp.MustCompile(`^[A-Za-z0-9=:,._@%+/-]+$`)

// passRe accepts the two numeric trailing fields.
var passRe = regexp.MustCompile(`^[0-9]$`)

// ValidateFstabSpec checks a spec before anything is rendered from it. Every
// field the form can produce is checked here, once, so the renderer and the
// commands below can assume a sane value.
func ValidateFstabSpec(spec disk.FstabSpec) error {
	if !specRe.MatchString(spec.Spec) {
		return fmt.Errorf(
			"storage: %q is not a device spec; use UUID=…, LABEL=… or /dev/…",
			spec.Spec)
	}
	if !targetRe.MatchString(spec.Target) || strings.Contains(spec.Target, "..") {
		return fmt.Errorf("storage: %q is not a mount point", spec.Target)
	}
	if !fsTypeRe.MatchString(spec.FSType) {
		return fmt.Errorf("storage: %q is not a filesystem type", spec.FSType)
	}
	if !optionsRe.MatchString(spec.Options) {
		return fmt.Errorf(
			"storage: %q is not a mount option list; options are comma separated "+
				"and carry no spaces", spec.Options)
	}
	if spec.Dump != "" && !passRe.MatchString(spec.Dump) {
		return fmt.Errorf("storage: the dump field is a single digit, not %q", spec.Dump)
	}
	if spec.Pass != "" && !passRe.MatchString(spec.Pass) {
		return fmt.Errorf("storage: the pass field is a single digit, not %q", spec.Pass)
	}
	return nil
}

// RenderFstabLine formats one entry, padded into the columns the file is
// conventionally written in.
func RenderFstabLine(spec disk.FstabSpec) string {
	dump, pass := spec.Dump, spec.Pass
	if dump == "" {
		dump = "0"
	}
	if pass == "" {
		pass = "0"
	}
	options := spec.Options
	if strings.TrimSpace(options) == "" {
		options = "defaults"
	}
	return fmt.Sprintf("%-42s %-24s %-8s %-24s %s %s",
		spec.Spec, escape(spec.Target), spec.FSType, options, dump, pass)
}

// RenderFstab rewrites the whole file with one entry added or replaced.
//
// The rest of the file is copied through byte for byte. Only the line being
// edited changes, which is what keeps the diff in the confirm dialog down to
// the change the user actually made.
func RenderFstab(existing string, spec disk.FstabSpec) (string, error) {
	if err := ValidateFstabSpec(spec); err != nil {
		return "", err
	}
	line := RenderFstabLine(spec)
	lines := strings.Split(strings.TrimSuffix(existing, "\n"), "\n")
	// An empty file splits into one empty element, which must not become a
	// blank first line.
	if existing == "" {
		lines = nil
	}

	switch {
	case spec.Replace > 0 && spec.Replace <= len(lines):
		lines[spec.Replace-1] = line
	case spec.Replace > 0:
		return "", fmt.Errorf("storage: %s has no line %d to replace",
			FstabPath, spec.Replace)
	default:
		// A new entry that names a target the file already carries would give
		// the machine two answers for one mount point, and the kernel takes
		// the last. Refusing is better than silently shadowing.
		for _, entry := range ParseFstab(existing) {
			if !entry.Comment && entry.Target == spec.Target {
				return "", fmt.Errorf(
					"storage: %s already has an entry for %s on line %d; edit "+
						"that one instead", FstabPath, spec.Target, entry.Number)
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// SpecFromEntry seeds the guided form from an entry that already exists, so
// editing starts from what is on disk instead of from an empty form.
func SpecFromEntry(entry disk.FstabEntry) disk.FstabSpec {
	return disk.FstabSpec{
		Spec: entry.Spec, Target: entry.Target, FSType: entry.FSType,
		Options: entry.Options, Dump: entry.Dump, Pass: entry.Pass,
		Replace: entry.Number,
	}
}

// SpecFromDevice seeds the form for a device that has no fstab entry: its
// UUID, its filesystem type, and the options preset that suits it.
func SpecFromDevice(device disk.Device) disk.FstabSpec {
	spec := disk.FstabSpec{FSType: device.FSType, Options: DefaultOptions,
		Dump: "0", Pass: "0"}
	if device.UUID != "" {
		spec.Spec = "UUID=" + device.UUID
	} else {
		spec.Spec = device.Path()
	}
	if device.FSType == "btrfs" {
		spec.Options = BtrfsOptions
	}
	spec.Target = device.Mountpoint()
	return spec
}

// The option presets the form offers. They are the four answers that cover
// nearly every entry an administrator writes by hand, and they are offered as
// a starting point: the field stays free text.
const (
	// DefaultOptions is what every distribution's installer writes.
	DefaultOptions = "defaults"
	// NoatimeOptions drops the access-time writes, which is the one change
	// that helps every filesystem on flash.
	NoatimeOptions = "defaults,noatime"
	// BtrfsOptions is the btrfs pairing worth having: transparent compression
	// and no access times.
	BtrfsOptions = "compress=zstd:1,noatime"
	// NofailOptions keeps a boot going when the device is not there, which is
	// what an external disk needs.
	NofailOptions = "defaults,nofail"
	// AutomountOptions mounts on first access instead of at boot, through the
	// systemd automount unit the generator produces from this option.
	AutomountOptions = "defaults,nofail,x-systemd.automount,x-systemd.idle-timeout=600"
)

// OptionPresets are offered by the form's picker, in this order.
var OptionPresets = []string{
	DefaultOptions, NoatimeOptions, BtrfsOptions, NofailOptions,
	AutomountOptions,
}

// FSTypes are the filesystem types the form offers. `auto` is first because it
// is the answer that is never wrong.
var FSTypes = []string{
	"auto", "ext4", "btrfs", "xfs", "vfat", "ntfs3", "f2fs", "swap", "none",
}

// diffContext is how many unchanged lines are shown around a change. Two is
// enough to place a line in its part of the file without turning a twenty line
// fstab into a wall of text in the confirm dialog.
const diffContext = 2

// Diff renders a unified diff between two versions of a file.
//
// It is a real line diff — a longest-common-subsequence walk — rather than
// "everything out, everything in", because the confirm dialog for a file edit
// has one job: show the line that changed. A diff that repeats the whole file
// buries exactly that.
//
// The files here are a few dozen lines, so the quadratic table costs nothing.
func Diff(path, before, after string) string {
	if before == after {
		return ""
	}
	oldLines, newLines := splitLines(before), splitLines(after)
	ops := diffOps(oldLines, newLines)
	hunks := hunksOf(ops)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", labelFor(path, before))
	fmt.Fprintf(&b, "+++ %s\n", path)
	for _, hunk := range hunks {
		oldCount, newCount := 0, 0
		for _, op := range hunk.ops {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
			hunk.oldStart+1, oldCount, hunk.newStart+1, newCount)
		for _, op := range hunk.ops {
			fmt.Fprintf(&b, "%c%s\n", op.kind, op.text)
		}
	}
	return b.String()
}

// op is one line of a diff: kept (' '), removed ('-') or added ('+').
type op struct {
	kind byte
	text string
	// oldIndex and newIndex are the line's position in each file, used to
	// number the hunk headers.
	oldIndex, newIndex int
}

// diffOps walks the longest common subsequence of the two line lists and
// returns the operations that turn the first into the second.
func diffOps(oldLines, newLines []string) []op {
	// lcs[i][j] is the length of the longest common subsequence of
	// oldLines[i:] and newLines[j:].
	lcs := make([][]int, len(oldLines)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
		}
	}

	var ops []op
	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, op{' ', oldLines[i], i, j})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', oldLines[i], i, j})
			i++
		default:
			ops = append(ops, op{'+', newLines[j], i, j})
			j++
		}
	}
	for ; i < len(oldLines); i++ {
		ops = append(ops, op{'-', oldLines[i], i, j})
	}
	for ; j < len(newLines); j++ {
		ops = append(ops, op{'+', newLines[j], i, j})
	}
	return ops
}

// hunk is a run of changes with its surrounding context.
type hunk struct {
	oldStart, newStart int
	ops                []op
}

// hunksOf groups the operations into hunks, keeping diffContext unchanged
// lines around each change and merging changes that are close enough to share
// their context.
func hunksOf(ops []op) []hunk {
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for j := max(i-diffContext, 0); j <= min(i+diffContext, len(ops)-1); j++ {
			keep[j] = true
		}
	}

	var hunks []hunk
	var current *hunk
	for i, o := range ops {
		if !keep[i] {
			current = nil
			continue
		}
		if current == nil {
			hunks = append(hunks, hunk{oldStart: o.oldIndex, newStart: o.newIndex})
			current = &hunks[len(hunks)-1]
		}
		current.ops = append(current.ops, o)
	}
	return hunks
}

// labelFor names the left side of the diff: the file, or /dev/null when it
// does not exist yet.
func labelFor(path, before string) string {
	if before == "" {
		return "/dev/null"
	}
	return path
}

// splitLines splits a file into lines, dropping the empty element a trailing
// newline produces.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
