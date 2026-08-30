package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// fieldKind tells a cycled choice from a free-text field.
type fieldKind int

const (
	fieldChoice fieldKind = iota
	fieldText
)

// formField is one row of the fstab editor.
type formField struct {
	// key identifies the field when building the FstabSpec.
	key   string
	label string
	kind  fieldKind
	// options and choice hold the state of a choice field.
	options []string
	choice  int
	// input holds the state of a text field.
	input textinput.Model
	// help is a one-line hint shown under the form.
	help string
}

// value returns the current value of the field.
func (f formField) value() string {
	if f.kind == fieldChoice {
		if f.choice < 0 || f.choice >= len(f.options) {
			return ""
		}
		return f.options[f.choice]
	}
	return strings.TrimSpace(f.input.Value())
}

// fstabForm is the guided editor for one /etc/fstab entry.
//
// It is guided rather than free: the line it writes is generated from these
// fields, so what the confirm dialog diffs is a line this tool can read back.
// The whole file stays visible on the mounts detail screen for anything the
// form does not cover.
//
// The options field is a choice with the presets in it *and* free text: the
// presets are the four answers that cover nearly every entry, and an entry
// that needs a fifth is still one keystroke away.
type fstabForm struct {
	fields []formField
	active int
	// replace is the line number being edited, carried through from the seed
	// so the renderer replaces that line instead of appending a duplicate.
	replace int
}

// newFstabForm builds the form, seeded from an entry or a device.
func newFstabForm(spec disk.FstabSpec, caps disk.Capabilities) fstabForm {
	text := func(placeholder, value string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.SetValue(value)
		ti.CharLimit = 200
		ti.Prompt = ""
		return ti
	}

	fsType := formField{key: "fstype", label: "Type", kind: fieldChoice,
		options: caps.FSTypes,
		help:    "auto lets the kernel work it out, which is rarely wrong."}
	for i, option := range caps.FSTypes {
		if option == spec.FSType {
			fsType.choice = i
		}
	}

	// The options field offers the presets and whatever the entry already
	// says, so editing an existing line never loses its options.
	optionValues := append([]string{}, caps.OptionPresets...)
	if spec.Options != "" && !contains(optionValues, spec.Options) {
		optionValues = append([]string{spec.Options}, optionValues...)
	}
	options := formField{key: "options", label: "Options", kind: fieldChoice,
		options: optionValues,
		help: "nofail keeps a boot going when the device is missing; " +
			"x-systemd.automount mounts on first access."}
	for i, option := range optionValues {
		if option == spec.Options {
			options.choice = i
		}
	}

	fields := []formField{
		{key: "spec", label: "Device", kind: fieldText,
			input: text("UUID=…", spec.Spec),
			help:  "UUID= is the only spec that survives a disk being renumbered."},
		{key: "target", label: "Mount point", kind: fieldText,
			input: text("/data", spec.Target),
			help:  "An absolute path. It is created by nothing here: mkdir it first."},
		fsType,
		options,
		{key: "dump", label: "Dump", kind: fieldChoice,
			options: []string{"0", "1"},
			choice:  digitChoice(spec.Dump),
			help:    "The dump(8) flag. 0 on every modern machine."},
		{key: "pass", label: "Pass", kind: fieldChoice,
			options: []string{"0", "1", "2"},
			choice:  digitChoice(spec.Pass),
			help:    "fsck order: 1 for the root filesystem, 2 for the rest, 0 to skip."},
	}

	f := fstabForm{fields: fields, replace: spec.Replace}
	f.focusActive()
	return f
}

// contains reports whether a list already holds a value.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// digitChoice maps a written dump or pass field onto its option index.
func digitChoice(value string) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 0 {
		return 0
	}
	return number
}

// focusActive moves the text cursor to the active field.
func (f *fstabForm) focusActive() {
	for i := range f.fields {
		if f.fields[i].kind != fieldText {
			continue
		}
		if i == f.active {
			f.fields[i].input.Focus()
			continue
		}
		f.fields[i].input.Blur()
	}
}

// next moves to the following field.
func (f *fstabForm) next() {
	f.active = (f.active + 1) % len(f.fields)
	f.focusActive()
}

// prev moves to the previous field.
func (f *fstabForm) prev() {
	f.active = (f.active - 1 + len(f.fields)) % len(f.fields)
	f.focusActive()
}

// activeIsChoice reports whether the active field is a cycled choice.
func (f fstabForm) activeIsChoice() bool {
	return f.fields[f.active].kind == fieldChoice
}

// activeLabel, activeOptions and activeValue expose the active field to the
// picker dialog.
func (f fstabForm) activeLabel() string     { return f.fields[f.active].label }
func (f fstabForm) activeOptions() []string { return f.fields[f.active].options }
func (f fstabForm) activeValue() string     { return f.fields[f.active].value() }

// setActiveValue applies a value chosen in the picker.
func (f *fstabForm) setActiveValue(value string) {
	field := &f.fields[f.active]
	for i, o := range field.options {
		if o == value {
			field.choice = i
			return
		}
	}
}

// cycle moves a choice field one step.
func (f *fstabForm) cycle(delta int) {
	field := &f.fields[f.active]
	if len(field.options) == 0 {
		return
	}
	field.choice = (field.choice + delta + len(field.options)) % len(field.options)
}

// updateActive forwards a message to the active text field.
func (f *fstabForm) updateActive(msg tea.Msg) tea.Cmd {
	if f.fields[f.active].kind != fieldText {
		return nil
	}
	var cmd tea.Cmd
	f.fields[f.active].input, cmd = f.fields[f.active].input.Update(msg)
	return cmd
}

// get returns the value of a field by key.
func (f fstabForm) get(key string) string {
	for _, field := range f.fields {
		if field.key == key {
			return field.value()
		}
	}
	return ""
}

// spec turns the form into an FstabSpec. Validation lives in the backend,
// which is the same code path the renderer uses, so the form cannot approve
// something the renderer would refuse.
func (f fstabForm) spec() disk.FstabSpec {
	return disk.FstabSpec{
		Spec:    f.get("spec"),
		Target:  f.get("target"),
		FSType:  f.get("fstype"),
		Options: f.get("options"),
		Dump:    f.get("dump"),
		Pass:    f.get("pass"),
		Replace: f.replace,
	}
}

// title names what the form is about to do, which is the one thing a user
// needs to know before reading the fields.
func (f fstabForm) title() string {
	if f.replace > 0 {
		return "Edit fstab line " + strconv.Itoa(f.replace)
	}
	return "Add an fstab entry"
}

// view renders the form as a dialog.
func (f fstabForm) view(t theme.Theme, width, height int) string {
	labelWidth := 0
	for _, field := range f.fields {
		if w := len(field.label); w > labelWidth {
			labelWidth = w
		}
	}

	inner := min(max(width-8, 30), 78)
	valueWidth := max(inner-labelWidth-6, 10)

	lines := []string{t.Title.Render(f.title()), ""}
	for i, field := range f.fields {
		label := t.Muted.Render(ui.Pad(field.label, labelWidth))
		var value string
		switch {
		case field.kind == fieldChoice:
			value = renderChoice(t, field, i == f.active, valueWidth)
		case i == f.active:
			field.input.Width = valueWidth - 2
			value = field.input.View()
		default:
			value = renderIdleText(t, field, valueWidth)
		}
		marker := "  "
		if i == f.active {
			marker = t.Accent.Render("> ")
		}
		lines = append(lines, marker+label+"  "+value)
	}

	if help := f.fields[f.active].help; help != "" {
		lines = append(lines, "", t.Muted.Render(help))
	}
	lines = append(lines, "",
		t.Muted.Render("findmnt verifies the file before you are asked to confirm."))
	lines = append(lines, "",
		t.Key.Render("tab")+t.KeyDesc.Render(" next    ")+
			t.Key.Render("←/→")+t.KeyDesc.Render(" change    ")+
			t.Key.Render("enter")+t.KeyDesc.Render(" pick/review    ")+
			t.Key.Render("esc")+t.KeyDesc.Render(" cancel"))

	box := t.Dialog.Width(inner).Render(strings.Join(lines, "\n"))
	return placeCenter(box, width, height)
}

// renderChoice draws a choice field with its cycling arrows.
func renderChoice(t theme.Theme, field formField, active bool, width int) string {
	value := ui.Truncate(field.value(), width-4)
	if active {
		return t.Accent.Render("‹ ") + t.Base.Render(value) + t.Accent.Render(" ›")
	}
	return t.Base.Render("  " + value)
}

// renderIdleText draws a text field that does not have focus.
func renderIdleText(t theme.Theme, field formField, width int) string {
	value := field.value()
	if value == "" {
		return t.Muted.Render(ui.Truncate(field.input.Placeholder, width))
	}
	return t.Base.Render(ui.Truncate(value, width))
}
