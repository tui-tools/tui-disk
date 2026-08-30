package storage

import (
	"testing"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// TestParseBtrfsUsageUnprivileged reads the output an ordinary user gets:
// btrfs refuses to read the chunk tree and says so on its first line, and the
// per-device breakdown is missing. That fact has to survive into the model,
// because otherwise the screen shows an empty section for no visible reason.
func TestParseBtrfsUsageUnprivileged(t *testing.T) {
	usage := ParseBtrfsUsage(read(t, "btrfs-usage-unprivileged.txt"))
	if usage.PerDevice {
		t.Error("the unprivileged read should not claim a per-device breakdown")
	}
	for _, want := range []struct{ name, got, expected string }{
		{"device size", usage.DeviceSize, "328.34GiB"},
		{"allocated", usage.Allocated, "296.02GiB"},
		{"unallocated", usage.Unallocated, "32.32GiB"},
		{"used", usage.Used, "278.28GiB"},
		{"free", usage.Free, "48.91GiB"},
		{"global reserve", usage.GlobalReserve, "305.00MiB"},
	} {
		if want.got != want.expected {
			t.Errorf("%s = %q, want %q", want.name, want.got, want.expected)
		}
	}
	if len(usage.Blocks) != 3 {
		t.Fatalf("profile blocks = %d, want 3", len(usage.Blocks))
	}
	data := usage.Blocks[0]
	if data.Type != "Data" || data.Profile != "single" ||
		data.Size != "292.01GiB" || data.Used != "275.42GiB" ||
		data.Percent != "94.32%" {
		t.Errorf("the data block parsed as %+v", data)
	}
}

// TestParseBtrfsUsageRoot reads the privileged form, which adds the per-device
// lines under each profile block and an Unallocated section.
func TestParseBtrfsUsageRoot(t *testing.T) {
	usage := ParseBtrfsUsage(read(t, "btrfs-usage-root.txt"))
	if !usage.PerDevice {
		t.Error("the privileged read has the per-device breakdown")
	}
	if usage.Used != "182.71GiB" {
		t.Errorf("used = %q, want 182.71GiB", usage.Used)
	}
	if usage.GlobalReserve != "512.00MiB" {
		t.Errorf("global reserve = %q, want 512.00MiB", usage.GlobalReserve)
	}
	// The per-device lines and the "Free (statfs, df)" line must not be
	// mistaken for profile blocks: the statfs one carries a comma in its key,
	// which is exactly what a profile block is recognised by.
	if len(usage.Blocks) != 3 {
		t.Fatalf("profile blocks = %d, want 3: %+v", len(usage.Blocks), usage.Blocks)
	}
}

// TestParseSubvolumes reads the -p form, whose fields are named rather than
// positional.
func TestParseSubvolumes(t *testing.T) {
	subvolumes := ParseSubvolumes(read(t, "btrfs-subvolume-list.txt"))
	if len(subvolumes) != 4 {
		t.Fatalf("subvolumes = %d, want 4", len(subvolumes))
	}
	first := subvolumes[0]
	if first.ID != 256 || first.Generation != 41822 || first.TopLevel != 5 ||
		first.Path != "@" {
		t.Errorf("the first subvolume parsed as %+v", first)
	}
	nested := subvolumes[3]
	if nested.ParentID != 257 || nested.Path != "@home/user/.cache/subvol" {
		t.Errorf("the nested subvolume parsed as %+v", nested)
	}
}

// TestParseQgroups skips the header and the rule under it.
func TestParseQgroups(t *testing.T) {
	qgroups := ParseQgroups(read(t, "btrfs-qgroup-show.txt"))
	if len(qgroups) != 3 {
		t.Fatalf("qgroups = %d, want 3", len(qgroups))
	}
	if qgroups[1].ID != "0/256" || qgroups[1].Referenced != "41.22GiB" ||
		qgroups[1].Exclusive != "2.31GiB" || qgroups[1].MaxReferenced != "100.00GiB" {
		t.Errorf("the second qgroup parsed as %+v", qgroups[1])
	}
}

// TestParseScrubStatus covers the three shapes the command prints.
func TestParseScrubStatus(t *testing.T) {
	never := ParseScrubStatus(read(t, "btrfs-scrub-status-never-run.txt"))
	if never.State != disk.TaskIdle {
		t.Errorf("a filesystem never scrubbed = %q, want idle", never.State)
	}
	if never.Detail == "" {
		t.Error("a filesystem never scrubbed should say so")
	}
	if never.ErrorCount != 0 {
		t.Errorf("errors = %d, want 0", never.ErrorCount)
	}

	done := ParseScrubStatus(read(t, "btrfs-scrub-status-finished.txt"))
	if done.State != disk.TaskFinished {
		t.Errorf("state = %q, want finished", done.State)
	}
	if done.Duration != "0:21:47" || done.Rate != "143.02MiB/s" {
		t.Errorf("the finished scrub parsed as %+v", done)
	}
	if done.ErrorCount != 0 {
		t.Errorf("errors = %d, want 0", done.ErrorCount)
	}

	bad := ParseScrubStatus(read(t, "btrfs-scrub-status-errors.txt"))
	if bad.State != disk.TaskRunning {
		t.Errorf("state = %q, want running", bad.State)
	}
	// "read=2 csum=1" is three errors, and the number is what the summary
	// column and --check report.
	if bad.ErrorCount != 3 {
		t.Errorf("errors = %d, want 3", bad.ErrorCount)
	}
}

// TestParseBalanceStatus covers both of the sentences the command prints.
func TestParseBalanceStatus(t *testing.T) {
	idle := ParseBalanceStatus(read(t, "btrfs-balance-none.txt"))
	if idle.State != disk.TaskIdle {
		t.Errorf("state = %q, want idle", idle.State)
	}
	running := ParseBalanceStatus(read(t, "btrfs-balance-running.txt"))
	if running.State != disk.TaskRunning {
		t.Errorf("state = %q, want running", running.State)
	}
	if running.Summary == "" {
		t.Error("a running balance should carry its progress")
	}
}

// TestParseDeviceStatsBothForms asserts that the JSON and the text parser
// agree, which is the property that makes the version gate safe: a machine
// that falls back to text gets the same model.
func TestParseDeviceStatsBothForms(t *testing.T) {
	fromJSON, err := ParseDeviceStatsJSON(read(t, "btrfs-device-stats.json"))
	if err != nil {
		t.Fatalf("ParseDeviceStatsJSON: %v", err)
	}
	fromText := ParseDeviceStatsText(read(t, "btrfs-device-stats.txt"))

	if len(fromJSON) != 1 || len(fromText) != 1 {
		t.Fatalf("stats = %d json, %d text, want 1 each",
			len(fromJSON), len(fromText))
	}
	if fromJSON[0].Device != fromText[0].Device {
		t.Errorf("device = %q json, %q text",
			fromJSON[0].Device, fromText[0].Device)
	}
	if fromJSON[0].Errors() != 0 || fromText[0].Errors() != 0 {
		t.Errorf("a clean filesystem reported errors: %+v / %+v",
			fromJSON[0], fromText[0])
	}
	// The text form carries no devid; everything else must match.
	fromJSON[0].DevID = 0
	if fromJSON[0] != fromText[0] {
		t.Errorf("the two forms disagree:\n json %+v\n text %+v",
			fromJSON[0], fromText[0])
	}
}

// TestDeviceStatErrorsAreFlagged asserts that a corruption counter is counted,
// because it is the number that means the data itself was wrong.
func TestDeviceStatErrorsAreFlagged(t *testing.T) {
	stats := ParseDeviceStatsText(`[/dev/sdb1].write_io_errs    0
[/dev/sdb1].read_io_errs     3
[/dev/sdb1].flush_io_errs    0
[/dev/sdb1].corruption_errs  1
[/dev/sdb1].generation_errs  0
`)
	if len(stats) != 1 {
		t.Fatalf("stats = %d, want 1", len(stats))
	}
	if stats[0].Errors() != 4 {
		t.Errorf("errors = %d, want 4", stats[0].Errors())
	}
}
