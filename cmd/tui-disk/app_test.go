package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/theme"
)

// newDemoApp builds the app over the demo machine, loaded, at a given size.
func newDemoApp(t *testing.T, width, height int) *app {
	t.Helper()
	a := newApp(storage.NewFake(), theme.New(), compatSet{})
	a.Update(tea.WindowSizeMsg{Width: width, Height: height})

	model, err := storage.NewFake().Load(context.Background())
	if err != nil {
		t.Fatalf("loading the demo machine: %v", err)
	}
	a.Update(loadedMsg{model: model})
	return a
}

// press sends a key to the app, the way a terminal would.
func press(a *app, key string) {
	switch key {
	case "enter":
		a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	case "tab":
		a.Update(tea.KeyMsg{Type: tea.KeyTab})
	default:
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
}

// TestWidthIsRespected renders every screen at a range of terminal widths and
// asserts that no rendered line is wider than the terminal.
//
// It is the test the whole family needs and the one whose absence is felt
// immediately: a line one cell too wide does not merely look wrong, it makes
// Bubble Tea wrap the frame, which desynchronises its line accounting and
// draws every frame after it in the wrong place.
func TestWidthIsRespected(t *testing.T) {
	for _, width := range []int{40, 60, 72, 80, 100, 120, 160, 200} {
		for _, height := range []int{12, 24, 40} {
			a := newDemoApp(t, width, height)
			for s := section(0); s < sectionCount; s++ {
				a.gotoSection(s)
				assertFits(t, a.View(), width, height, "%s at %dx%d",
					s.title(), width, height)

				// The detail screen of the first row, which is where the
				// longest lines live.
				press(a, "enter")
				assertFits(t, a.View(), width, height, "%s detail at %dx%d",
					s.title(), width, height)
				press(a, "esc")
			}
			// And the dialogs, which are centred boxes rather than full-width
			// frames and have their own way of overflowing.
			a.gotoSection(sectionMounts)
			press(a, "?")
			assertFits(t, a.View(), width, height, "help at %dx%d", width, height)
			press(a, "esc")
		}
	}
}

// assertFits checks that a rendered frame stays inside the terminal.
func assertFits(t *testing.T, frame string, width, height int,
	format string, args ...any) {
	t.Helper()
	label := fmt.Sprintf(format, args...)
	for i, line := range strings.Split(frame, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("%s: line %d is %d cells wide, terminal is %d:\n%q",
				label, i+1, got, width, line)
			return
		}
	}
	if lines := strings.Count(frame, "\n") + 1; lines > height {
		t.Errorf("%s: frame is %d lines tall, terminal is %d", label, lines, height)
	}
}

// selectMount moves the cursor onto a mount point by name, so a test says
// which row it is acting on rather than counting them.
func selectMount(t *testing.T, a *app, target string) {
	t.Helper()
	for i, mount := range a.mounts {
		if mount.Target == target {
			a.cursor[sectionMounts] = i
			return
		}
	}
	t.Fatalf("%s is not on the mounts screen", target)
}

// TestSectionsAreReachable asserts that the number keys and tab both land on
// every screen the demo machine has.
func TestSectionsAreReachable(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	for i, want := range []section{sectionDevices, sectionMounts, sectionBtrfs,
		sectionSMART, sectionSpace} {
		press(a, string(rune('1'+i)))
		if a.section != want {
			t.Errorf("key %d landed on %q, want %q", i+1, a.section.title(),
				want.title())
		}
	}
	a.gotoSection(sectionDevices)
	for range int(sectionCount) {
		press(a, "tab")
	}
	if a.section != sectionDevices {
		t.Errorf("tab through every screen ended on %q", a.section.title())
	}
}

// TestUnmountIsPreviewed drives the demo the way a user would — to the mounts
// screen, onto the USB stick nobody put in fstab — and asserts the confirm
// dialog carries the exact command line.
func TestUnmountIsPreviewed(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionMounts)
	selectMount(t, a, "/media/backup")

	press(a, "u")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm dialog; status: %s", a.mode, a.status)
	}
	if !strings.HasSuffix(a.confirm.Command, "umount /media/backup") {
		t.Errorf("preview = %q", a.confirm.Command)
	}
	if !a.confirm.Danger {
		t.Error("unmounting should paint the dialog in the danger colour")
	}
	// The payload the dialog holds must be the same command it displayed.
	pending, ok := a.confirm.Payload.(plan)
	if !ok || len(pending.commands) != 1 {
		t.Fatalf("payload = %#v", a.confirm.Payload)
	}
	if a.backend.Preview(pending.commands[0]) != a.confirm.Command {
		t.Error("the dialog shows one command and holds another")
	}
}

// TestMountRefusals covers the two answers the mount key gives instead of a
// command.
//
// Mounting is `mount <target>`, which makes the kernel read the device and the
// options out of fstab — so a target with no entry has nothing to mount from,
// and one that is already mounted has nothing to do. Both refusals say which.
func TestMountRefusals(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionMounts)

	selectMount(t, a, "/media/backup")
	press(a, "m")
	if a.mode == modeConfirm {
		t.Fatal("a mount was offered for a filesystem that is already mounted")
	}
	if !strings.Contains(a.status, "already mounted") {
		t.Errorf("status = %q, want the reason", a.status)
	}

	// A row that is neither mounted nor in fstab cannot be built from either
	// source. It does not occur on the demo machine — a row with no entry is
	// there because it is mounted — so it is injected rather than staged.
	a.mounts = append(a.mounts, disk.Mount{Target: "/mnt/orphan",
		FSType: "ext4", Match: disk.MatchNotInFstab})
	a.cursor[sectionMounts] = len(a.mounts) - 1
	a.status = ""
	press(a, "m")
	if a.mode == modeConfirm {
		t.Fatal("a mount with no fstab entry was offered")
	}
	if !strings.Contains(a.status, "no fstab entry") {
		t.Errorf("status = %q, want the reason", a.status)
	}
}

// TestFstabEditProducesADiffAndTwoCommands walks the editor end to end: open
// it on the mismatched mount, submit, and read what the confirm dialog holds.
func TestFstabEditProducesADiffAndTwoCommands(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionMounts)
	selectMount(t, a, "/media/backup")

	press(a, "e")
	if a.mode != modeForm {
		t.Fatalf("mode = %v, want the form; status: %s", a.mode, a.status)
	}
	press(a, "enter")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm dialog; status: %s", a.mode, a.status)
	}
	if !strings.Contains(a.confirm.Body, "findmnt --verify") {
		t.Errorf("the dialog does not say the file was verified:\n%s", a.confirm.Body)
	}
	if !strings.Contains(a.confirm.Body, "+UUID=") {
		t.Errorf("the dialog carries no diff of the new line:\n%s", a.confirm.Body)
	}
	if !strings.Contains(a.confirm.Command, "install -m 644") ||
		!strings.Contains(a.confirm.Command, "systemctl daemon-reload") {
		t.Errorf("the dialog shows %q, want both commands", a.confirm.Command)
	}
}

// TestBtrfsBalanceAsksWhichBlockGroups asserts that the balance key opens the
// filter picker rather than starting a full balance on a keystroke.
func TestBtrfsBalanceAsksWhichBlockGroups(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionBtrfs)
	press(a, "b")
	if a.mode != modePicker {
		t.Fatalf("mode = %v, want the filter picker; status: %s", a.mode, a.status)
	}
	press(a, "enter")
	if a.mode != modeConfirm {
		t.Fatalf("mode = %v, want the confirm dialog", a.mode)
	}
	if !strings.Contains(a.confirm.Command, "btrfs balance start -dusage=10") {
		t.Errorf("preview = %q", a.confirm.Command)
	}
}

// TestSelfTestIsRefusedOnADriveWithoutSMART asserts the third answer a real
// machine gives: the USB stick, whose bridge passes no SMART through.
func TestSelfTestIsRefusedOnADriveWithoutSMART(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionSMART)
	for i, reading := range a.smart {
		if reading.Device == "/dev/sdb" {
			a.cursor[sectionSMART] = i
		}
	}
	press(a, "s")
	if a.mode == modeConfirm {
		t.Fatal("a self-test was offered on a drive that reports no SMART")
	}
	if !strings.Contains(a.status, "no SMART") {
		t.Errorf("status = %q, want the reason", a.status)
	}
}

// TestActionKeysAreRefusedOffTheirScreen asserts that a key does nothing
// surprising on a screen it does not belong to, and says where it lives.
func TestActionKeysAreRefusedOffTheirScreen(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionSpace)
	for _, key := range []string{"m", "u", "c", "b", "s"} {
		a.status = ""
		press(a, key)
		if a.mode == modeConfirm {
			t.Fatalf("%q opened a confirm dialog on the space screen", key)
		}
		if a.status == "" {
			t.Errorf("%q was swallowed silently", key)
		}
	}
}

// TestFilterNarrowsOneScreen asserts that the filter applies to the screen the
// user is on and matches any of its columns.
func TestFilterNarrowsOneScreen(t *testing.T) {
	a := newDemoApp(t, 120, 40)
	a.gotoSection(sectionDevices)
	before := a.rowCount()

	a.filter = "nvme"
	a.applyFilter()
	if a.rowCount() >= before || a.rowCount() == 0 {
		t.Errorf("filtering to nvme left %d of %d rows", a.rowCount(), before)
	}
	for _, device := range a.devices {
		if !strings.Contains(strings.ToLower(deviceHaystack(device)), "nvme") {
			t.Errorf("%q does not match the filter", device.Name)
		}
	}
}

// TestCheckReport asserts the JSON --check prints, which is the contract the
// smoke test asserts against on a real machine.
func TestCheckReport(t *testing.T) {
	var buf bytes.Buffer
	if err := runCheck(storage.NewFake(), compatSet{}, &buf); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("the report is not valid JSON: %v", err)
	}
	if report.Tool != toolName || report.Backend != "demo" {
		t.Errorf("report = %+v", report)
	}
	if report.Disks != 3 || report.BtrfsFilesystems != 1 {
		t.Errorf("disks = %d, btrfs = %d", report.Disks, report.BtrfsFilesystems)
	}
	if report.FstabMismatches != 1 ||
		len(report.MismatchTargets) != 1 ||
		report.MismatchTargets[0] != "/media/backup" {
		t.Errorf("mismatches = %d %v", report.FstabMismatches,
			report.MismatchTargets)
	}
	if len(report.SMARTHealth) != 3 {
		t.Fatalf("health lines = %d, want one per drive", len(report.SMARTHealth))
	}
	// The health lines are what a smoke test greps: a device node, a verdict,
	// and a reason when the verdict is unknown.
	for _, line := range report.SMARTHealth {
		if line.Device == "" || line.Health == "" {
			t.Errorf("health line = %+v", line)
		}
		if line.Health == disk.HealthUnknown && line.Detail == "" {
			t.Errorf("%s is unknown with no reason", line.Device)
		}
	}
}
