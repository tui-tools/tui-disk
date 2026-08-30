package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// section is one of the five screens the tool shows over a single read. They
// are screens rather than one long page because each answers a different
// question, and a machine with forty mounts and six drives has no layout where
// all of them fit at once.
type section int

const (
	// sectionDevices is the block device tree.
	sectionDevices section = iota
	// sectionMounts is the mount table crossed with fstab.
	sectionMounts
	// sectionBtrfs is one row per btrfs filesystem.
	sectionBtrfs
	// sectionSMART is one row per drive.
	sectionSMART
	// sectionSpace is the df view.
	sectionSpace
	// sectionCount is how many there are.
	sectionCount
)

// title names a section for the header and the tab bar.
func (s section) title() string {
	switch s {
	case sectionDevices:
		return "devices"
	case sectionMounts:
		return "mounts"
	case sectionBtrfs:
		return "btrfs"
	case sectionSMART:
		return "health"
	case sectionSpace:
		return "space"
	default:
		return ""
	}
}

// mode is the dialog the app currently shows. Only one is open at a time,
// which keeps the update loop flat.
type mode int

const (
	modeList mode = iota
	modeDetail
	modeConfirm
	modeFilter
	modePicker
	modeForm
	modeHelp
)

// pickerTarget says what an open picker's answer applies to.
type pickerTarget int

const (
	pickerNone pickerTarget = iota
	// pickerField is a choice field of the fstab form.
	pickerField
	// pickerDevice is the UUID picker that seeds a new fstab entry.
	pickerDevice
	// pickerBalance is the block group filter of a balance.
	pickerBalance
)

// app is the tui-disk Bubble Tea model.
type app struct {
	backend disk.Backend
	theme   theme.Theme
	caps    disk.Capabilities
	// probes is what the three version probes found, rendered in the header
	// and in the help screen.
	probes compatSet

	model disk.Model
	// cursor and offset are per section, indexed by the section constants, so
	// each screen keeps its own selection and its own scroll position.
	cursor [sectionCount]int
	offset [sectionCount]int

	// The filtered row slices, rebuilt whenever the model or the filter
	// changes. One slice per section, of that section's own row type.
	devices []disk.FlatDevice
	mounts  []disk.Mount
	btrfs   []disk.Btrfs
	smart   []disk.SMART
	space   []disk.SpaceRow

	section       section
	width, height int
	filter        string

	// detailOpen reports that the detail screen is the one a dialog returns
	// to, detailOffset is its scroll position, and mountDetail is the second
	// read the mounts detail makes.
	detailOpen   bool
	detailOffset int
	mountDetail  disk.MountDetail

	mode    mode
	confirm ui.Confirm
	input   ui.Input
	picker  ui.Picker
	form    fstabForm

	pickerFor pickerTarget
	// specs are the devices the UUID picker offers, read on demand.
	specs []disk.DeviceSpec
	// balanceTarget is the mount point a pending balance applies to.
	balanceTarget string

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last Load returned an error, so the empty
	// state does not claim the machine simply has no disks.
	loadFailed bool
	// busy blocks input while a command runs.
	busy bool
}

// loadedMsg carries the result of a Load.
type loadedMsg struct {
	model disk.Model
	err   error
}

// mountDetailMsg carries the result of a per-mount read.
type mountDetailMsg struct {
	detail disk.MountDetail
	err    error
}

// specsMsg carries the device list the UUID picker offers.
type specsMsg struct {
	specs []disk.DeviceSpec
}

// ranMsg carries the result of running a plan.
type ranMsg struct {
	// title is the plan's title, echoed in the status line.
	title  string
	output string
	err    error
}

// plan is what a confirm dialog is holding: one or more commands, run in
// order. Most actions are a single command; writing fstab is two, the install
// and the daemon-reload, and both are shown before either runs.
type plan struct {
	title    string
	commands []disk.Command
}

// newApp builds the model around a backend.
func newApp(backend disk.Backend, th theme.Theme, probes compatSet) *app {
	a := &app{
		backend: backend,
		theme:   th,
		caps:    backend.Capabilities(),
		probes:  probes,
		width:   80,
		height:  24,
		loading: true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first load.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads the storage state in the background.
func (a *app) load() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		model, err := backend.Load(ctx)
		return loadedMsg{model: model, err: err}
	}
}

// loadMountDetail re-reads one mount in the background.
func (a *app) loadMountDetail(target string) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		detail, err := backend.LoadMount(ctx, target)
		return mountDetailMsg{detail: detail, err: err}
	}
}

// loadSpecs reads the device list the UUID picker offers. It is a separate
// read because it can escalate, and a picker nobody opened should not be
// asking the machine for a password prompt it does not need.
func (a *app) loadSpecs() tea.Cmd {
	backend, devices := a.backend, a.model.Devices
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return specsMsg{specs: backend.LoadSpecs(ctx, devices)}
	}
}

// run executes a confirmed plan in the background, one command at a time,
// stopping at the first failure.
func (a *app) run(p plan) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		var outputs []string
		for _, cmd := range p.commands {
			out, err := backend.Run(ctx, cmd)
			if err != nil {
				return ranMsg{title: p.title, output: out, err: err}
			}
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				outputs = append(outputs, trimmed)
			}
		}
		return ranMsg{title: p.title, output: strings.Join(outputs, "; ")}
	}
}

// setStatus records a plain message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		a.loading = false
		if msg.err != nil {
			a.loadFailed = true
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.loadFailed = false
		a.model = msg.model
		a.applyFilter()
		// A note is the backend telling the user what it could not read. It
		// is worth the status line, once, rather than only the help screen.
		if a.status == "" && len(a.model.Notes) > 0 {
			a.setStatus(ui.StatusWarn, a.model.Notes[0])
		}
		return a, nil

	case mountDetailMsg:
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.mountDetail = msg.detail
		return a, nil

	case specsMsg:
		a.specs = msg.specs
		return a, a.openDevicePicker()

	case ranMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, a.load()
		}
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.title, firstLine(summary))
		a.loading = true
		return a, a.load()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	if a.mode == modeForm {
		return a, a.form.updateActive(msg)
	}
	return a, nil
}

// handleKey routes a key press to the open dialog, or to the current screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeFilter:
		return a.handleFilter(msg)
	case modePicker:
		return a.handlePicker(msg)
	case modeForm:
		return a.handleForm(msg)
	case modeHelp:
		a.mode = modeList
		return a, nil
	case modeDetail:
		return a.handleDetailKey(msg)
	default:
		return a.handleListKey(msg)
	}
}

// handleConfirm resolves the confirm dialog.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = a.returnMode()
	confirmed := a.confirm.Confirmed
	pending, ok := a.confirm.Payload.(plan)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", a.backend.Preview(pending.commands[0]))
	return a, a.run(pending)
}

// returnMode is the screen a dialog goes back to.
func (a *app) returnMode() mode {
	if a.detailOpen {
		return modeDetail
	}
	return modeList
}

// handleFilter resolves the filter prompt.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		// Filter as the user types.
		a.filter = a.input.Value()
		a.applyFilter()
		return a, cmd
	}
	if a.input.Accepted {
		a.filter = a.input.Value()
	} else {
		a.filter = ""
	}
	a.applyFilter()
	a.mode = modeList
	return a, nil
}

// handlePicker resolves the open picker.
func (a *app) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.picker.Update(msg)
	if !a.picker.Done {
		return a, nil
	}
	choice, accepted := a.picker.Selected(), a.picker.Accepted
	target := a.pickerFor
	a.picker, a.pickerFor = ui.Picker{}, pickerNone

	if !accepted {
		a.mode = modeList
		if target == pickerField {
			a.mode = modeForm
		}
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	switch target {
	case pickerField:
		a.form.setActiveValue(choice)
		a.mode = modeForm
		return a, nil
	case pickerDevice:
		return a, a.startFormFromSpec(choice)
	case pickerBalance:
		a.mode = modeList
		return a, a.confirmBalance(a.balanceTarget, choice)
	default:
		a.mode = modeList
		return a, nil
	}
}

// handleForm routes keys to the fstab editor.
func (a *app) handleForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.mode = a.returnMode()
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	case "tab", "down":
		a.form.next()
		return a, nil
	case "shift+tab", "up":
		a.form.prev()
		return a, nil
	case "left":
		if a.form.activeIsChoice() {
			a.form.cycle(-1)
			return a, nil
		}
	case "right":
		if a.form.activeIsChoice() {
			a.form.cycle(1)
			return a, nil
		}
	case "enter":
		if a.form.activeIsChoice() {
			// A choice field opens a picker: better than cycling a long list.
			a.picker = ui.NewPicker(a.form.activeLabel(),
				a.form.activeOptions(), a.form.activeValue())
			a.pickerFor = pickerField
			a.mode = modePicker
			return a, nil
		}
		return a, a.submitForm()
	}
	return a, a.form.updateActive(msg)
}

// submitForm renders the new fstab, has the backend verify the staged file,
// diffs it against what is on disk and opens the confirm dialog with the
// verify report, the diff and the commands that apply it.
func (a *app) submitForm() tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write, err := a.backend.BuildWriteFstab(ctx, a.form.spec())
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   "Write " + write.Path,
		Body:    a.writeBody(write),
		Command: a.previewAll(write.Commands),
		Danger:  true,
		Payload: plan{title: "Write " + write.Path, commands: write.Commands},
	}
	return nil
}

// writeBody is the explanation above the command preview: what findmnt said
// about the staged file, then the diff.
func (a *app) writeBody(write disk.WritePlan) string {
	verify := strings.TrimSpace(write.Verify)
	if verify == "" {
		verify = "the staged file was not verified"
	}
	return "findmnt --verify on the staged file: " + firstLine(verify) + "\n\n" +
		a.diffForDialog(write.Diff)
}

// previewAll renders every command of a plan, one per line, each with the
// prompt the dialog puts in front of the first one.
func (a *app) previewAll(commands []disk.Command) string {
	previews := make([]string, 0, len(commands))
	for _, cmd := range commands {
		previews = append(previews, a.backend.Preview(cmd))
	}
	return strings.Join(previews, "\n$ ")
}

// handleListKey handles a list screen.
func (a *app) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor[a.section], a.offset[a.section] = 0, 0
	case "G", "end":
		a.cursor[a.section] = max(a.rowCount()-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.tableHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.tableHeight())
	case "tab", "l", "right":
		a.switchSection(1)
	case "shift+tab", "h", "left":
		a.switchSection(-1)
	case "1", "2", "3", "4", "5":
		a.gotoSection(section(msg.String()[0] - '1'))
	case "/":
		a.input = ui.NewInput("Filter "+a.section.title(),
			"name, mount point, type…", a.filter)
		a.input.Help = "Matches any column of this screen. Empty clears the filter."
		a.mode = modeFilter
	case "enter":
		return a, a.openDetail()
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
	return a, nil
}

// handleDetailKey handles the detail screen. The action keys are the same ones
// the list offers, applied to the row on screen.
func (a *app) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.closeDetail()
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "j", "down":
		a.detailOffset++
		return a, nil
	case "k", "up":
		a.detailOffset = max(a.detailOffset-1, 0)
		return a, nil
	case "g", "home":
		a.detailOffset = 0
		return a, nil
	case "pgdown", "ctrl+f":
		a.detailOffset += a.detailHeight()
		return a, nil
	case "pgup", "ctrl+b":
		a.detailOffset = max(a.detailOffset-a.detailHeight(), 0)
		return a, nil
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
}

// closeDetail returns to the list.
func (a *app) closeDetail() {
	a.detailOpen = false
	a.detailOffset = 0
	a.mountDetail = disk.MountDetail{}
	a.mode = modeList
}

// handleActionKey handles the keys that build a change. Each one is refused,
// with a reason, on a screen or a machine where it does not apply.
func (a *app) handleActionKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "m":
		return a.confirmMount(true)
	case "u":
		return a.confirmMount(false)
	case "e":
		return a.openFstabForm()
	case "a":
		return a.addFstabEntry()
	case "D":
		return a.buildAndConfirm("Reload the systemd units from fstab",
			a.backend.BuildDaemonReload)
	case "c":
		return a.confirmScrub(true)
	case "C":
		return a.confirmScrub(false)
	case "b":
		return a.openBalancePicker()
	case "B":
		return a.confirmBalanceCancel()
	case "s":
		return a.confirmSelfTest(disk.SelfTestShort)
	case "S":
		return a.confirmSelfTest(disk.SelfTestLong)
	}
	return nil
}

// currentMount is the mount row the mount actions apply to.
func (a *app) currentMount() (disk.Mount, bool) {
	if a.section != sectionMounts {
		a.setStatus(ui.StatusWarn,
			"mounting is on the mounts screen — press 2")
		return disk.Mount{}, false
	}
	index := a.cursor[sectionMounts]
	if index < 0 || index >= len(a.mounts) {
		a.setStatus(ui.StatusWarn, "no mount selected")
		return disk.Mount{}, false
	}
	return a.mounts[index], true
}

// confirmMount asks before mounting or unmounting the selected entry.
func (a *app) confirmMount(mounting bool) tea.Cmd {
	if !a.caps.SupportsMount {
		a.setStatus(ui.StatusWarn, "mount and umount are not available here")
		return nil
	}
	mount, ok := a.currentMount()
	if !ok {
		return nil
	}
	if mounting {
		if mount.Mounted {
			a.setStatusf(ui.StatusWarn, "%s is already mounted", mount.Target)
			return nil
		}
		if !mount.InFstab {
			// Mounting by target is the only form this tool offers, and it
			// needs an fstab entry to read the device and the options from.
			a.setStatusf(ui.StatusWarn,
				"%s has no fstab entry to mount from; press e to write one",
				mount.Target)
			return nil
		}
		return a.buildAndConfirm("Mount "+mount.Target, func() (disk.Command, error) {
			return a.backend.BuildMount(mount.Target)
		})
	}
	if !mount.Mounted {
		a.setStatusf(ui.StatusWarn, "%s is not mounted", mount.Target)
		return nil
	}
	cmd, err := a.backend.BuildUmount(mount.Target)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	body := cmd.Description + ".\nAnything with a file open under it keeps the " +
		"mount busy, and anything writing there stops."
	a.openConfirm("Unmount "+mount.Target, body, cmd)
	return nil
}

// openFstabForm opens the guided editor for the selected row's fstab entry.
func (a *app) openFstabForm() tea.Cmd {
	if !a.caps.SupportsFstabEdit {
		a.setStatus(ui.StatusWarn, "this machine has no writable fstab")
		return nil
	}
	switch a.section {
	case sectionMounts:
		mount, ok := a.currentMount()
		if !ok {
			return nil
		}
		if entry, found := a.model.FstabFor(mount.Target); found {
			a.form = newFstabForm(storage.SpecFromEntry(entry), a.caps)
			a.mode = modeForm
			return nil
		}
		// Mounted with no entry: seed a new one from the live mount, which is
		// exactly the fix the mismatch column is pointing at.
		a.form = newFstabForm(a.specFromMount(mount), a.caps)
		a.mode = modeForm
		return nil
	case sectionDevices, sectionSpace:
		return a.addFstabEntry()
	default:
		a.setStatus(ui.StatusWarn,
			"the fstab editor is on the devices or mounts screen")
		return nil
	}
}

// specFromMount seeds a new fstab entry from a filesystem that is mounted but
// absent from the file, preferring the UUID of the device behind it.
func (a *app) specFromMount(mount disk.Mount) disk.FstabSpec {
	spec := disk.FstabSpec{Spec: mount.Source, Target: mount.Target,
		FSType: mount.FSType, Options: storage.DefaultOptions,
		Dump: "0", Pass: "0"}
	for _, device := range a.model.Flatten() {
		if device.Mountpoint() == mount.Target && device.UUID != "" {
			spec.Spec = "UUID=" + device.UUID
			break
		}
	}
	return spec
}

// addFstabEntry reads the device list and opens the UUID picker.
func (a *app) addFstabEntry() tea.Cmd {
	if !a.caps.SupportsFstabEdit {
		a.setStatus(ui.StatusWarn, "this machine has no writable fstab")
		return nil
	}
	if len(a.specs) > 0 {
		return a.openDevicePicker()
	}
	a.setStatus(ui.StatusInfo, "reading the devices…")
	return a.loadSpecs()
}

// openDevicePicker offers the devices that carry a filesystem.
func (a *app) openDevicePicker() tea.Cmd {
	if len(a.specs) == 0 {
		a.setStatus(ui.StatusWarn,
			"no device with a filesystem was found to add")
		return nil
	}
	options := make([]string, 0, len(a.specs))
	for _, spec := range a.specs {
		options = append(options, spec.PickerLabel())
	}
	a.picker = ui.NewPicker("Which device?", options, "")
	a.pickerFor = pickerDevice
	a.mode = modePicker
	return nil
}

// startFormFromSpec opens the form seeded from the device the user picked.
func (a *app) startFormFromSpec(label string) tea.Cmd {
	for _, spec := range a.specs {
		if spec.PickerLabel() != label {
			continue
		}
		seed := disk.FstabSpec{FSType: spec.FSType,
			Options: storage.DefaultOptions, Dump: "0", Pass: "0"}
		if spec.UUID != "" {
			seed.Spec = "UUID=" + spec.UUID
		} else {
			seed.Spec = spec.Device
		}
		if spec.FSType == "btrfs" {
			seed.Options = storage.BtrfsOptions
		}
		// An entry the file already has is an edit, not a duplicate: seeding
		// from it is what lets a user change the options of a mount they
		// picked out of the device list.
		for _, entry := range a.model.Fstab {
			if !entry.Comment && entry.Spec == seed.Spec {
				seed = storage.SpecFromEntry(entry)
				break
			}
		}
		a.form = newFstabForm(seed, a.caps)
		a.mode = modeForm
		return nil
	}
	a.mode = modeList
	return nil
}

// currentBtrfs is the filesystem the btrfs actions apply to.
func (a *app) currentBtrfs() (disk.Btrfs, bool) {
	if !a.caps.HasBtrfs {
		a.setStatus(ui.StatusWarn, "btrfs-progs is not installed on this machine")
		return disk.Btrfs{}, false
	}
	if a.section != sectionBtrfs {
		a.setStatus(ui.StatusWarn, "scrub and balance are on the btrfs screen — press 3")
		return disk.Btrfs{}, false
	}
	index := a.cursor[sectionBtrfs]
	if index < 0 || index >= len(a.btrfs) {
		a.setStatus(ui.StatusWarn, "no btrfs filesystem selected")
		return disk.Btrfs{}, false
	}
	return a.btrfs[index], true
}

// confirmScrub asks before starting or cancelling a scrub.
func (a *app) confirmScrub(start bool) tea.Cmd {
	fs, ok := a.currentBtrfs()
	if !ok {
		return nil
	}
	if start {
		if fs.Scrub.State == disk.TaskRunning {
			a.setStatusf(ui.StatusWarn,
				"a scrub is already running on %s; press C to cancel it",
				fs.Mountpoint)
			return nil
		}
		return a.buildAndConfirm("Scrub "+fs.Mountpoint, func() (disk.Command, error) {
			return a.backend.BuildScrubStart(fs.Mountpoint)
		})
	}
	if fs.Scrub.State != disk.TaskRunning {
		a.setStatusf(ui.StatusWarn, "no scrub is running on %s", fs.Mountpoint)
		return nil
	}
	return a.buildAndConfirm("Cancel the scrub of "+fs.Mountpoint,
		func() (disk.Command, error) {
			return a.backend.BuildScrubCancel(fs.Mountpoint)
		})
}

// openBalancePicker asks which block groups a balance should touch.
func (a *app) openBalancePicker() tea.Cmd {
	fs, ok := a.currentBtrfs()
	if !ok {
		return nil
	}
	if fs.Balance.State == disk.TaskRunning {
		a.setStatusf(ui.StatusWarn,
			"a balance is already running on %s; press B to cancel it",
			fs.Mountpoint)
		return nil
	}
	a.balanceTarget = fs.Mountpoint
	a.picker = ui.NewPicker("Balance which block groups of "+fs.Mountpoint+"?",
		disk.BalanceFilters, disk.BalanceUsage10)
	a.pickerFor = pickerBalance
	a.mode = modePicker
	return nil
}

// confirmBalance asks before starting a balance with the chosen filter.
func (a *app) confirmBalance(mountpoint, filter string) tea.Cmd {
	cmd, err := a.backend.BuildBalanceStart(mountpoint, filter)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	body := cmd.Description + ".\nIt runs in the background and holds the disk " +
		"the whole time; the btrfs screen shows its progress."
	a.openConfirm("Balance "+mountpoint, body, cmd)
	return nil
}

// confirmBalanceCancel asks before stopping a running balance.
func (a *app) confirmBalanceCancel() tea.Cmd {
	fs, ok := a.currentBtrfs()
	if !ok {
		return nil
	}
	if fs.Balance.State != disk.TaskRunning {
		a.setStatusf(ui.StatusWarn, "no balance is running on %s", fs.Mountpoint)
		return nil
	}
	return a.buildAndConfirm("Cancel the balance of "+fs.Mountpoint,
		func() (disk.Command, error) {
			return a.backend.BuildBalanceCancel(fs.Mountpoint)
		})
}

// confirmSelfTest asks before starting a drive self-test.
func (a *app) confirmSelfTest(kind string) tea.Cmd {
	if !a.caps.HasSMART {
		a.setStatus(ui.StatusWarn, "smartmontools is not installed on this machine")
		return nil
	}
	if a.section != sectionSMART {
		a.setStatus(ui.StatusWarn, "self-tests are on the health screen — press 4")
		return nil
	}
	index := a.cursor[sectionSMART]
	if index < 0 || index >= len(a.smart) {
		a.setStatus(ui.StatusWarn, "no drive selected")
		return nil
	}
	reading := a.smart[index]
	if !reading.Available {
		a.setStatusf(ui.StatusWarn, "%s reports no SMART: %s",
			reading.Device, orUnknown(reading.Detail))
		return nil
	}
	return a.buildAndConfirm("Self-test "+reading.Device,
		func() (disk.Command, error) {
			return a.backend.BuildSelfTest(reading.Device, kind)
		})
}

// openDetail opens the detail screen for the highlighted row.
func (a *app) openDetail() tea.Cmd {
	if a.rowCount() == 0 {
		a.setStatus(ui.StatusWarn, "nothing to open")
		return nil
	}
	a.detailOpen, a.detailOffset = true, 0
	a.mode = modeDetail
	if a.section == sectionMounts {
		mount := a.mounts[a.cursor[sectionMounts]]
		a.mountDetail = disk.MountDetail{Target: mount.Target}
		return a.loadMountDetail(mount.Target)
	}
	a.mountDetail = disk.MountDetail{}
	return nil
}

// buildAndConfirm runs a command builder and opens the confirm dialog, or
// reports the builder's error in the status line.
func (a *app) buildAndConfirm(title string,
	build func() (disk.Command, error)) tea.Cmd {
	cmd, err := build()
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.openConfirm(title, cmd.Description+".", cmd)
	return nil
}

// openConfirm shows one command and what it does.
func (a *app) openConfirm(title, body string, cmd disk.Command) {
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    body,
		Command: a.backend.Preview(cmd),
		Danger:  cmd.Destructive,
		Payload: plan{title: title, commands: []disk.Command{cmd}},
	}
}

// switchSection moves to the next or previous screen, skipping the ones this
// machine has nothing to show on.
func (a *app) switchSection(delta int) {
	next := a.section
	for range int(sectionCount) {
		next = section((int(next) + delta + int(sectionCount)) % int(sectionCount))
		if a.sectionAvailable(next) {
			a.gotoSection(next)
			return
		}
	}
}

// gotoSection selects a screen by number, refusing one this machine has
// nothing for and saying why.
func (a *app) gotoSection(target section) {
	if target < 0 || target >= sectionCount {
		return
	}
	if !a.sectionAvailable(target) {
		a.setStatusf(ui.StatusWarn, "%s", a.sectionUnavailableReason(target))
		return
	}
	a.section = target
	a.closeDetail()
	a.clampCursor()
}

// sectionAvailable reports whether a screen has a backend behind it. The btrfs
// and health screens are hidden on a machine without btrfs-progs or
// smartmontools, rather than shown empty.
func (a *app) sectionAvailable(target section) bool {
	switch target {
	case sectionBtrfs:
		return a.caps.HasBtrfs
	case sectionSMART:
		return a.caps.HasSMART
	default:
		return true
	}
}

// sectionUnavailableReason names the package a screen needs.
func (a *app) sectionUnavailableReason(target section) string {
	switch target {
	case sectionBtrfs:
		return "btrfs-progs is not installed, so there is no btrfs screen"
	case sectionSMART:
		return "smartmontools is not installed, so there is no health screen"
	default:
		return ""
	}
}

// rowCount is how many rows the current screen has after the filter.
func (a *app) rowCount() int {
	switch a.section {
	case sectionDevices:
		return len(a.devices)
	case sectionMounts:
		return len(a.mounts)
	case sectionBtrfs:
		return len(a.btrfs)
	case sectionSMART:
		return len(a.smart)
	case sectionSpace:
		return len(a.space)
	default:
		return 0
	}
}

// applyFilter recomputes every screen's visible rows from the current filter.
func (a *app) applyFilter() {
	needle := strings.ToLower(a.filter)
	keep := func(haystack string) bool {
		return needle == "" || strings.Contains(strings.ToLower(haystack), needle)
	}

	a.devices = nil
	for _, device := range a.model.Flatten() {
		if keep(deviceHaystack(device)) {
			a.devices = append(a.devices, device)
		}
	}
	a.mounts = nil
	for _, mount := range a.model.Mounts {
		if keep(mountHaystack(mount)) {
			a.mounts = append(a.mounts, mount)
		}
	}
	a.btrfs = nil
	for _, fs := range a.model.Btrfs {
		if keep(fs.Mountpoint + " " + fs.Label + " " + strings.Join(fs.Devices, " ")) {
			a.btrfs = append(a.btrfs, fs)
		}
	}
	a.smart = nil
	for _, reading := range a.model.SMART {
		if keep(reading.Device + " " + reading.Model + " " + reading.Health) {
			a.smart = append(a.smart, reading)
		}
	}
	a.space = nil
	for _, row := range a.model.Space {
		if keep(row.Target + " " + row.Source + " " + row.FSType) {
			a.space = append(a.space, row)
		}
	}
	a.clampCursor()
}

// deviceHaystack is the text the filter matches a device against.
func deviceHaystack(device disk.FlatDevice) string {
	return strings.Join([]string{
		device.Name, device.KName, device.Kind, device.FSType, device.Label,
		device.UUID, device.Model, device.Serial, device.Transport,
		strings.Join(device.Mountpoints, " "),
	}, " ")
}

// mountHaystack is the text the filter matches a mount against.
func mountHaystack(mount disk.Mount) string {
	return strings.Join([]string{
		mount.Target, mount.Source, mount.FSType, mount.Options, mount.Match,
	}, " ")
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor[a.section] += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset of the current screen
// within range.
func (a *app) clampCursor() {
	count := a.rowCount()
	if count == 0 {
		a.cursor[a.section], a.offset[a.section] = 0, 0
		return
	}
	a.cursor[a.section] = min(max(a.cursor[a.section], 0), count-1)

	height := a.tableHeight()
	if a.cursor[a.section] < a.offset[a.section] {
		a.offset[a.section] = a.cursor[a.section]
	}
	if a.cursor[a.section] >= a.offset[a.section]+height {
		a.offset[a.section] = a.cursor[a.section] - height + 1
	}
	a.offset[a.section] = max(min(a.offset[a.section], max(count-height, 0)), 0)
}

// firstLine keeps status messages to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// orUnknown renders an empty reason as something readable.
func orUnknown(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "no reason was given"
	}
	return reason
}
