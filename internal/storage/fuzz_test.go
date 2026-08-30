package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// This package is where output tui-disk did not write becomes what the screens
// show and what /etc/fstab is rewritten from: lsblk and findmnt JSON whose
// column types have changed between util-linux releases, four btrfs text
// formats that have no JSON at all, smartctl reports from firmwares nobody
// here has seen, and the machine's own fstab. Every parser therefore carries a
// fuzz target. `go test` replays every seed below on each commit, and
// `go test -run=^$ -fuzz=FuzzParseLsblk ./internal/storage/` explores past them
// locally. See tui-kit/templates/FUZZING.md for the family rule.
//
// The seeds are the captured fixtures the table tests already use, plus the
// shapes a real capture never has: nothing, a lone separator, a truncated
// line, a JSON document of the wrong shape.

// seed adds every named testdata file to the corpus.
func seed(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(string(raw))
	}
	f.Add("")
	f.Add("\n\n\n")
	f.Add(":")
	f.Add("=")
	f.Add("[")
	f.Add("{}")
	f.Add("null")
}

// --- lsblk, findmnt, df, blkid ------------------------------------------

func FuzzParseLsblk(f *testing.F) {
	seed(f, "lsblk-fedora42.json", "lsblk-utillinux234.json")
	f.Fuzz(func(t *testing.T, output string) {
		devices, err := ParseLsblk(output)
		if err != nil {
			// A tree that did not decode is not a partial tree: nothing here
			// may reach a screen or a command.
			if len(devices) != 0 {
				t.Fatalf("returned %d devices alongside an error: %v", len(devices), err)
			}
			return
		}
		checkDevices(t, devices)

		// The picker's fallback for a machine where blkid cannot run is built
		// from this tree, and every row of it is offered as an fstab spec.
		for _, spec := range BlkidFromDevices(devices) {
			if spec.UUID == "" || spec.FSType == "" {
				t.Fatalf("offered a device with no UUID or no type: %+v", spec)
			}
		}
	})
}

// checkDevices asserts what every reader of the device tree may assume, at
// every depth: a path that is really under /dev, and a mountpoint list with no
// holes in it.
func checkDevices(t *testing.T, devices []disk.Device) {
	t.Helper()
	for _, device := range devices {
		if !strings.HasPrefix(device.Path(), "/dev/") {
			t.Fatalf("device %q has path %q", device.Name, device.Path())
		}
		for _, mountpoint := range device.Mountpoints {
			if mountpoint == "" {
				t.Fatalf("device %q kept a null mountpoint", device.Name)
			}
		}
		if got := device.Mounted(); got != (device.Mountpoint() != "") {
			t.Fatalf("device %q disagrees with itself about being mounted", device.Name)
		}
		checkDevices(t, device.Children)
	}
}

func FuzzParseFindmnt(f *testing.F) {
	seed(f, "findmnt-fedora42.json")
	f.Fuzz(func(t *testing.T, output string) {
		mounts, err := ParseFindmnt(output)
		if err != nil {
			if len(mounts) != 0 {
				t.Fatalf("returned %d mounts alongside an error: %v", len(mounts), err)
			}
			return
		}
		for _, mount := range mounts {
			// findmnt only lists what is mounted, so a row from it says so.
			if !mount.Mounted {
				t.Fatalf("findmnt row %q is not marked mounted", mount.Target)
			}
		}
	})
}

// FuzzCrossCheck is the mounts screen itself: findmnt on one side, the
// machine's fstab on the other, and a verdict per row. Both inputs are files
// this tool did not write, so the pair is fuzzed together.
func FuzzCrossCheck(f *testing.F) {
	findmnt, err := os.ReadFile(filepath.Join("testdata", "findmnt-fedora42.json"))
	if err != nil {
		f.Fatalf("read fixture: %v", err)
	}
	fstab, err := os.ReadFile(filepath.Join("testdata", "fstab-fedora42.txt"))
	if err != nil {
		f.Fatalf("read fixture: %v", err)
	}
	f.Add(string(findmnt), string(fstab))
	f.Add(string(findmnt), "")
	f.Add("", string(fstab))
	f.Add("{}", "UUID=x / btrfs defaults 0 0\n")

	verdicts := map[string]bool{
		disk.MatchOK: true, disk.MatchNotInFstab: true,
		disk.MatchNotMounted: true, disk.MatchOptionsDiffer: true,
		disk.MatchTransient: true,
	}
	f.Fuzz(func(t *testing.T, output, raw string) {
		mounts, err := ParseFindmnt(output)
		if err != nil {
			return
		}
		entries := ParseFstab(raw)
		rows := CrossCheck(mounts, entries)
		if len(rows) < len(mounts) {
			t.Fatalf("cross-check dropped rows: %d mounts became %d",
				len(mounts), len(rows))
		}
		for _, row := range rows {
			// Every row carries a verdict the screen knows how to colour, and
			// a row that claims an fstab entry has to name the line it came
			// from — that line is what the detail screen shows and what the
			// editor replaces.
			if !verdicts[row.Match] {
				t.Fatalf("row %q got the verdict %q", row.Target, row.Match)
			}
			if row.InFstab && strings.TrimSpace(row.FstabLine) == "" {
				t.Fatalf("row %q claims an fstab entry with no line", row.Target)
			}
			if !row.Mounted && !row.InFstab {
				t.Fatalf("row %q is neither mounted nor in fstab", row.Target)
			}
			// The options comparison feeds the "options differ" verdict, and
			// it never invents an option the file did not ask for.
			for _, option := range MissingOptions(row.FstabOptions, row.Options) {
				if !strings.Contains(row.FstabOptions, option) {
					t.Fatalf("row %q misses %q, which fstab never asked for",
						row.Target, option)
				}
			}
		}
	})
}

func FuzzParseDF(f *testing.F) {
	seed(f, "df-fedora42.txt")
	f.Fuzz(func(t *testing.T, output string) {
		for _, row := range ParseDF(output) {
			// A row is only built from a line with all seven columns, so every
			// column the screen prints is really there.
			switch {
			case row.Source == "" || row.FSType == "" || row.Target == "":
				t.Fatalf("row with a blank column: %+v", row)
			case row.Size == "" || row.Used == "" || row.Avail == "":
				t.Fatalf("row with a blank size: %+v", row)
			case row.UsePercent == "":
				t.Fatalf("row with no use percentage: %+v", row)
			}
			// Only the target may carry spaces: it is the field df prints last
			// and the only one a path can widen.
			for _, field := range []string{row.Source, row.FSType, row.Size,
				row.Used, row.Avail, row.UsePercent} {
				if strings.ContainsAny(field, " \t") {
					t.Fatalf("column %q carries whitespace: %+v", field, row)
				}
			}
		}
	})
}

func FuzzParseBlkid(f *testing.F) {
	seed(f, "blkid-export.txt")
	f.Fuzz(func(t *testing.T, output string) {
		for _, spec := range ParseBlkid(output) {
			// A device is only flushed when it has a DEVNAME, and that name is
			// what the picker writes into fstab as a spec.
			if spec.Device == "" {
				t.Fatalf("kept a device with no name: %+v", spec)
			}
			// Every field is a value the export form really carried: the
			// picker writes them into fstab, so none of them is assembled or
			// invented here.
			for _, field := range []string{spec.Device, spec.UUID, spec.FSType,
				spec.Label, spec.PartUUID} {
				if field == "" {
					continue
				}
				if strings.Contains(field, "\n") {
					t.Fatalf("field %q spans lines: %+v", field, spec)
				}
				if !strings.Contains(output, field) {
					t.Fatalf("field %q is not in the output: %+v", field, spec)
				}
			}
		}
	})
}

// --- fstab ---------------------------------------------------------------

func FuzzParseFstab(f *testing.F) {
	seed(f, "fstab-fedora42.txt")
	f.Add("UUID=x /data ext4 defaults 0 0\n")
	f.Add("/dev/sda1 /mnt/with\\040space ext4 defaults\n")
	f.Fuzz(func(t *testing.T, raw string) {
		entries := ParseFstab(raw)

		// The file is rewritten whole, so the parse has to be lossless: every
		// line is kept, in order, verbatim. A rewrite that dropped a comment
		// would throw away text the administrator put there on purpose.
		var lines []string
		for i, entry := range entries {
			if entry.Number != i+1 {
				t.Fatalf("entry %d is numbered %d", i+1, entry.Number)
			}
			lines = append(lines, entry.Line)
		}
		if rejoined := strings.Join(lines, "\n"); rejoined != strings.TrimSuffix(raw, "\n") {
			t.Fatalf("round trip changed the file:\n%q\n%q", raw, rejoined)
		}

		for _, entry := range entries {
			if entry.Comment {
				continue
			}
			// A real entry is what the cross-check joins on and what the
			// editor seeds a form from: three fields at least, and options
			// that default rather than come back blank.
			switch {
			case entry.Spec == "" || entry.Target == "" || entry.FSType == "":
				t.Fatalf("entry on line %d has a blank field: %+v",
					entry.Number, entry)
			case entry.Options == "":
				t.Fatalf("entry on line %d has no options at all: %+v",
					entry.Number, entry)
			}
			if spec := SpecFromEntry(entry); spec.Replace != entry.Number {
				t.Fatalf("the form for line %d would replace line %d",
					entry.Number, spec.Replace)
			}
		}
	})
}

// FuzzRenderFstab is the write path: whatever is already in the file, adding an
// entry either fails or produces a file that still parses, still ends in a
// newline, and really carries the entry that was added.
func FuzzRenderFstab(f *testing.F) {
	seed(f, "fstab-fedora42.txt")
	f.Add("UUID=x /data ext4 defaults 0 0\n")
	f.Fuzz(func(t *testing.T, existing string) {
		spec := disk.FstabSpec{Spec: "UUID=0000-1111", Target: "/mnt/fuzz",
			FSType: "ext4", Options: "defaults,noatime", Dump: "0", Pass: "2"}
		rendered, err := RenderFstab(existing, spec)
		if err != nil {
			// A refusal writes nothing at all: the caller installs whatever
			// comes back, so a partial file here would be a partial /etc/fstab.
			if rendered != "" {
				t.Fatalf("returned %d bytes alongside an error: %v",
					len(rendered), err)
			}
			return
		}
		if !strings.HasSuffix(rendered, "\n") {
			t.Fatalf("rendered a file with no trailing newline")
		}
		var found bool
		for _, entry := range ParseFstab(rendered) {
			if !entry.Comment && entry.Target == spec.Target {
				found = true
				if entry.Spec != spec.Spec || entry.FSType != spec.FSType {
					t.Fatalf("the entry came back changed: %+v", entry)
				}
			}
		}
		if !found {
			t.Fatalf("the entry is not in the file that was rendered")
		}
		// Everything else is copied through byte for byte.
		for _, line := range strings.Split(strings.TrimSuffix(existing, "\n"), "\n") {
			if existing != "" && !strings.Contains(rendered, line) {
				t.Fatalf("the rewrite lost the line %q", line)
			}
		}
	})
}

// --- btrfs ---------------------------------------------------------------

func FuzzParseBtrfsUsage(f *testing.F) {
	seed(f, "btrfs-usage-root.txt", "btrfs-usage-unprivileged.txt")
	f.Fuzz(func(t *testing.T, output string) {
		usage := ParseBtrfsUsage(output)
		for _, block := range usage.Blocks {
			// A profile block is only kept when it has a size, and its two
			// halves are the words the screen puts in the row's label.
			switch {
			case block.Size == "":
				t.Fatalf("kept a block with no size: %+v", block)
			case block.Type != strings.TrimSpace(block.Type):
				t.Fatalf("block type kept its padding: %q", block.Type)
			case block.Profile != strings.TrimSpace(block.Profile):
				t.Fatalf("block profile kept its padding: %q", block.Profile)
			}
		}
		// The overall figures are single numbers, never the two-fact lines
		// btrfs prints them on.
		for _, value := range []string{usage.Free, usage.GlobalReserve} {
			if strings.ContainsAny(value, " \t") {
				t.Fatalf("an overall figure carries a second fact: %q", value)
			}
		}
	})
}

func FuzzParseSubvolumes(f *testing.F) {
	seed(f, "btrfs-subvolume-list.txt")
	f.Fuzz(func(t *testing.T, output string) {
		for _, sub := range ParseSubvolumes(output) {
			// The id is what a delete is built around, so a row without one is
			// never kept.
			if sub.ID == 0 {
				t.Fatalf("kept a subvolume with no id: %+v", sub)
			}
			if sub.Path != strings.TrimSpace(sub.Path) {
				t.Fatalf("subvolume path kept its padding: %q", sub.Path)
			}
		}
	})
}

func FuzzParseQgroups(f *testing.F) {
	seed(f, "btrfs-qgroup-show.txt")
	f.Fuzz(func(t *testing.T, output string) {
		for _, qgroup := range ParseQgroups(output) {
			// A qgroup id is "level/id", which is what separates a data row
			// from the header and the rule under it.
			if !strings.Contains(qgroup.ID, "/") {
				t.Fatalf("kept %q as a qgroup id", qgroup.ID)
			}
			if qgroup.Referenced == "" || qgroup.Exclusive == "" {
				t.Fatalf("qgroup %q has a blank column: %+v", qgroup.ID, qgroup)
			}
		}
	})
}

func FuzzParseScrubStatus(f *testing.F) {
	seed(f, "btrfs-scrub-status-finished.txt", "btrfs-scrub-status-errors.txt",
		"btrfs-scrub-status-never-run.txt")
	f.Fuzz(func(t *testing.T, output string) {
		scrub := ParseScrubStatus(output)
		// The state drives the colour of the row, and "nothing said" is idle
		// rather than blank.
		if scrub.State == "" {
			t.Fatalf("scrub state came back blank: %+v", scrub)
		}
		if strings.Contains(scrub.Errors, "no errors found") && scrub.ErrorCount != 0 {
			t.Fatalf("counted %d errors in a clean summary %q",
				scrub.ErrorCount, scrub.Errors)
		}
	})
}

func FuzzParseBalanceStatus(f *testing.F) {
	seed(f, "btrfs-balance-none.txt", "btrfs-balance-running.txt")
	f.Fuzz(func(t *testing.T, output string) {
		balance := ParseBalanceStatus(output)
		if balance.State == "" {
			t.Fatalf("balance state came back blank: %+v", balance)
		}
		// The summary is one line: it is printed in a single-line field, and a
		// second line there would be text the user never sees.
		if strings.Contains(balance.Summary, "\n") {
			t.Fatalf("balance summary spans lines: %q", balance.Summary)
		}
	})
}

func FuzzParseDeviceStatsJSON(f *testing.F) {
	seed(f, "btrfs-device-stats.json")
	f.Fuzz(func(t *testing.T, output string) {
		stats, err := ParseDeviceStatsJSON(output)
		if err != nil && len(stats) != 0 {
			t.Fatalf("returned %d stats alongside an error: %v", len(stats), err)
		}
	})
}

func FuzzParseDeviceStatsText(f *testing.F) {
	seed(f, "btrfs-device-stats.txt")
	f.Add("[/dev/sda1].write_io_errs 0\n[/dev/sda1].read_io_errs 3\n")
	f.Fuzz(func(t *testing.T, output string) {
		stats := ParseDeviceStatsText(output)
		seen := map[string]bool{}
		for _, stat := range stats {
			// One row per device, named: the row is a device the error
			// counters are attributed to, and two rows for one device would
			// show the same disk twice with different numbers.
			if stat.Device == "" {
				t.Fatalf("kept a stat row with no device: %+v", stat)
			}
			if seen[stat.Device] {
				t.Fatalf("device %q got two rows", stat.Device)
			}
			seen[stat.Device] = true
		}
	})
}

// --- SMART ---------------------------------------------------------------

func FuzzParseSMART(f *testing.F) {
	seed(f, "smartctl-ata.json", "smartctl-nvme.json", "smartctl-failing.json",
		"smartctl-no-smart.json")
	f.Fuzz(func(t *testing.T, output string) {
		result, err := ParseSMART("/dev/sda", output)
		// The reading is a value even when the report did not parse, because
		// "we do not know" is a fact about the machine and belongs in the
		// column. What it must never be is a health verdict nobody read.
		if result.Device != "/dev/sda" {
			t.Fatalf("the reading is for %q", result.Device)
		}
		if err != nil {
			if result.Health != disk.HealthUnknown || result.Available {
				t.Fatalf("an unparsable report gave a verdict: %+v", result)
			}
			return
		}
		switch {
		case !result.Available && result.Health != disk.HealthUnknown:
			t.Fatalf("a device with no SMART got the verdict %q", result.Health)
		case result.Health != disk.HealthPassed &&
			result.Health != disk.HealthFailed &&
			result.Health != disk.HealthUnknown:
			t.Fatalf("unknown health verdict %q", result.Health)
		}
		// A device that could not be asked reports nothing at all: every
		// counter stays on the "not reported" sentinel and no self-test is
		// invented, so the detail screen has nothing to print as fact.
		if !result.Available {
			for _, counter := range []int{result.Temperature, result.PowerOnHours,
				result.ReallocatedSectors, result.PendingSectors,
				result.PercentageUsed, result.MediaErrors} {
				if counter != -1 {
					t.Fatalf("a device with no SMART reported %d: %+v",
						counter, result)
				}
			}
			if len(result.SelfTests) != 0 {
				t.Fatalf("a device with no SMART has %d self-tests",
					len(result.SelfTests))
			}
		}
	})
}
