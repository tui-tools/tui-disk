package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/ui"
)

// Layout constants: the rows the table cannot use.
const (
	// tabLines is the one-line bar that names the five screens; helpLines and
	// statusLines are the two lines under the body.
	tabLines    = 1
	helpLines   = 1
	statusLines = 1
	// minTableHeight keeps at least one visible row on a very short terminal.
	minTableHeight = 1
)

// headerHeight is how many lines the header really occupies.
//
// It is measured rather than assumed. The header wraps its facts onto as many
// lines as the width requires, so on a narrow terminal it is four lines, not
// two — and a body sized against the constant overflows the terminal by
// exactly that difference, which makes Bubble Tea wrap the frame and draw
// every frame after it in the wrong place.
func (a *app) headerHeight() int {
	return lipgloss.Height(a.headerView(a.headerExtra()))
}

// headerExtra is the subtitle the current screen adds, kept in one place so
// the measured header is the header that is drawn.
func (a *app) headerExtra() string {
	if a.mode == modeDetail {
		return a.detailTitle()
	}
	return ""
}

// tableHeight is the number of rows that fit on screen.
func (a *app) tableHeight() int {
	// header + tab bar + the table's own header row + help bar + status line.
	return max(a.height-a.headerHeight()-tabLines-1-helpLines-statusLines,
		minTableHeight)
}

// detailHeight is the number of detail lines that fit on screen.
func (a *app) detailHeight() int {
	return max(a.height-a.headerHeight()-tabLines-helpLines-statusLines,
		minTableHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeFilter:
		return a.input.View(a.theme, a.width, a.height)
	case modePicker:
		return a.picker.View(a.theme, a.width, a.height)
	case modeForm:
		return a.form.view(a.theme, a.width, a.height)
	case modeHelp:
		return placeCenter(a.helpScreen(), a.width, a.height)
	case modeDetail:
		return a.detailView()
	}
	return a.listView()
}

// placeCenter centers a rendered box in the terminal.
func placeCenter(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// listView renders a list screen: header, tab bar, table, help bar, status.
func (a *app) listView() string {
	header := a.headerView("")
	tabs := a.tabBar()

	var body string
	switch {
	case a.loading && a.rowCount() == 0:
		body = ui.EmptyState(a.theme, "reading the disks…", a.width, a.tableHeight()+1)
	case a.rowCount() == 0 && a.filter != "":
		body = ui.EmptyState(a.theme, "nothing matches "+strconv.Quote(a.filter),
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme,
			"could not read the storage — see the message below",
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0:
		body = ui.EmptyState(a.theme, a.emptyMessage(), a.width, a.tableHeight()+1)
	default:
		body = a.table()
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(), a.width)
	return strings.Join([]string{header, tabs, body, help, status}, "\n")
}

// emptyMessage explains an empty screen in that screen's own terms.
func (a *app) emptyMessage() string {
	switch a.section {
	case sectionDevices:
		return "this machine reports no block devices"
	case sectionMounts:
		return "nothing is mounted and fstab is empty"
	case sectionBtrfs:
		return "no btrfs filesystem is mounted"
	case sectionSMART:
		return "no drive on this machine answered SMART"
	default:
		return "df reported no filesystems"
	}
}

// shortTitle is the screen's name abbreviated for a narrow terminal.
func (s section) shortTitle() string {
	switch s {
	case sectionDevices:
		return "dev"
	case sectionMounts:
		return "mnt"
	case sectionBtrfs:
		return "btrfs"
	case sectionSMART:
		return "hlth"
	default:
		return "spc"
	}
}

// tabBar names the five screens on exactly one line, marking the current one
// and dimming the ones this machine has no backend for.
//
// It must never wrap: the bar is one line in the height arithmetic, and a
// second one pushes the status line off the bottom of the terminal.
func (a *app) tabBar() string {
	labels := a.tabLabels(false)
	if plainWidth(labels) > a.width-2 {
		labels = a.tabLabels(true)
	}
	// Still too wide: keep the screen the user is on and drop the rest, which
	// is the one label that has to be readable.
	if plainWidth(labels) > a.width-2 {
		labels = []string{labels[int(a.section)]}
	}

	t := a.theme
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		s := a.section
		if len(labels) > 1 {
			s = section(i)
		}
		switch {
		case s == a.section:
			parts = append(parts, t.SelRow.Render(" "+label+" "))
		case !a.sectionAvailable(s):
			parts = append(parts, t.Muted.Render(" "+label+" "))
		default:
			parts = append(parts, t.KeyDesc.Render(" "+label+" "))
		}
	}
	return t.Footer.Width(a.width).Render(
		ui.Truncate(strings.Join(parts, " "), a.width-2))
}

// tabLabels builds the five labels, long or abbreviated.
func (a *app) tabLabels(short bool) []string {
	labels := make([]string, 0, sectionCount)
	for s := section(0); s < sectionCount; s++ {
		title := s.title()
		if short {
			title = s.shortTitle()
		}
		label := strconv.Itoa(int(s)+1) + " " + title
		if badge := a.sectionBadge(s); badge != "" {
			label += " " + badge
		}
		labels = append(labels, label)
	}
	return labels
}

// plainWidth is how wide the labels are once the padding and the separators
// between them are counted.
func plainWidth(labels []string) int {
	total := 0
	for _, label := range labels {
		total += lipgloss.Width(label) + 3 // one space either side, one between
	}
	return total
}

// sectionBadge is the number a screen carries in the tab bar: the count that
// says whether it is worth opening. Only the counts that mean "look here" are
// shown, so a healthy machine has a quiet bar.
func (a *app) sectionBadge(s section) string {
	switch s {
	case sectionMounts:
		if n := a.model.Mismatches(); n > 0 {
			return "(" + strconv.Itoa(n) + "!)"
		}
	case sectionBtrfs:
		if n := a.model.BtrfsErrors(); n > 0 {
			return "(" + strconv.Itoa(n) + "!)"
		}
	case sectionSMART:
		n := 0
		for _, reading := range a.model.SMART {
			if reading.Concerning() {
				n++
			}
		}
		if n > 0 {
			return "(" + strconv.Itoa(n) + "!)"
		}
	}
	return ""
}

// headerView renders the facts at the top of every screen.
func (a *app) headerView(subtitleExtra string) string {
	t := a.theme

	facts := []ui.Fact{
		{Label: "disks", Value: strconv.Itoa(len(a.model.Disks()))},
		{Label: "mounts", Value: strconv.Itoa(len(a.model.Mounts))},
	}
	if n := a.model.Mismatches(); n > 0 {
		style := t.Warn
		facts = append(facts, ui.Fact{Label: "fstab mismatches",
			Value: strconv.Itoa(n), Style: &style})
	}
	if n := a.model.BtrfsErrors(); n > 0 {
		style := t.Danger
		facts = append(facts, ui.Fact{Label: "btrfs errors",
			Value: strconv.Itoa(n), Style: &style})
	}
	if value, style := a.healthFact(); value != "" {
		facts = append(facts, ui.Fact{Label: "health", Value: value, Style: style})
	}
	// The version of the backend the tool cannot run without: quiet on a
	// tested version, coloured on one nobody has run against.
	if a.probes.UtilLinux.Backend != "" {
		facts = append(facts, ui.CompatFact(t, a.probes.UtilLinux))
	}

	subtitle := a.backend.Describe()
	if subtitleExtra != "" {
		subtitle += "  ·  " + subtitleExtra
	}
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	return ui.Header{Title: "tui-disk", Subtitle: subtitle, Facts: facts}.
		Render(t, a.width)
}

// healthFact summarises the drives in one word, coloured by the worst verdict.
func (a *app) healthFact() (string, *lipgloss.Style) {
	if len(a.model.SMART) == 0 {
		return "", nil
	}
	failed, concerning, unknown := 0, 0, 0
	for _, reading := range a.model.SMART {
		switch {
		case !reading.Available:
			unknown++
		case reading.Health == disk.HealthFailed:
			failed++
		case reading.Concerning():
			concerning++
		}
	}
	switch {
	case failed > 0:
		style := a.theme.Danger
		return strconv.Itoa(failed) + " failing", &style
	case concerning > 0:
		style := a.theme.Warn
		return strconv.Itoa(concerning) + " to watch", &style
	case unknown == len(a.model.SMART):
		style := a.theme.Muted
		return "unknown", &style
	default:
		style := a.theme.OK
		return "all passed", &style
	}
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	count := strconv.Itoa(a.rowCount())
	if a.filter != "" {
		return count + " of " + strconv.Itoa(a.totalRows()) + " rows  ·  ? for help"
	}
	return count + " " + a.section.title() + "  ·  enter for detail  ·  ? for help"
}

// totalRows is how many rows the current screen has before the filter.
func (a *app) totalRows() int {
	switch a.section {
	case sectionDevices:
		return len(a.model.Flatten())
	case sectionMounts:
		return len(a.model.Mounts)
	case sectionBtrfs:
		return len(a.model.Btrfs)
	case sectionSMART:
		return len(a.model.SMART)
	default:
		return len(a.model.Space)
	}
}

// table renders the current screen's rows.
func (a *app) table() string {
	switch a.section {
	case sectionDevices:
		return a.devicesTable()
	case sectionMounts:
		return a.mountsTable()
	case sectionBtrfs:
		return a.btrfsTable()
	case sectionSMART:
		return a.smartTable()
	default:
		return a.spaceTable()
	}
}

// render draws a table with the current screen's cursor and offset.
func (a *app) render(columns []ui.Column, rows [][]string,
	styles []*lipgloss.Style) string {
	return ui.Table{
		Columns:  columns,
		Rows:     rows,
		Styles:   styles,
		Selected: a.cursor[a.section],
		Offset:   a.offset[a.section],
		Height:   a.tableHeight(),
	}.Render(a.theme, a.width)
}

// barWidth is how many cells a usage bar takes. It is small on purpose: the
// number next to it is the fact, and the bar is there to be scanned.
const barWidth = 10

// usageBar renders a percentage as a bar plus the number. A percentage the
// backend does not know renders as nothing at all, which is honest: an empty
// cell is not a filesystem that is 0% full.
func usageBar(percent int) string {
	if percent < 0 {
		return ""
	}
	filled := percent * barWidth / 100
	filled = min(max(filled, 0), barWidth)
	return strings.Repeat("█", filled) + strings.Repeat("·", barWidth-filled) +
		" " + strconv.Itoa(percent) + "%"
}

// devicesTable renders the block device tree, dropping columns on narrow
// terminals.
func (a *app) devicesTable() string {
	columns := []ui.Column{
		{Title: "NAME", Width: 20, Flex: true},
		{Title: "SIZE", Width: 8},
		{Title: "FSTYPE", Width: 9},
		{Title: "USED", Width: barWidth + 5},
	}
	showMount := a.width >= 76
	showHealth := a.width >= 100
	if showMount {
		columns = append(columns, ui.Column{Title: "MOUNTPOINT", Width: 18, Flex: true})
	}
	if showHealth {
		columns = append(columns, ui.Column{Title: "HEALTH", Width: 18})
	}

	rows := make([][]string, 0, len(a.devices))
	styles := make([]*lipgloss.Style, 0, len(a.devices))
	for _, device := range a.devices {
		row := []string{
			treePrefix(device.Depth) + device.Name,
			device.Size,
			device.FSType,
			usageBar(device.UsePercent()),
		}
		if showMount {
			row = append(row, strings.Join(device.Mountpoints, " "))
		}
		if showHealth {
			row = append(row, deviceHealthCell(device))
		}
		rows = append(rows, row)
		styles = append(styles, a.deviceStyle(device))
	}
	return a.render(columns, rows, styles)
}

// treePrefix indents a device under its parent.
func treePrefix(depth int) string {
	if depth == 0 {
		return ""
	}
	return strings.Repeat("  ", depth-1) + "└─"
}

// deviceHealthCell renders the health column: the drive's verdict on a whole
// device, and the transport on anything carved out of one.
func deviceHealthCell(device disk.FlatDevice) string {
	if !device.IsDisk() {
		return ""
	}
	if device.Health.Device == "" {
		return device.Spindle() + " · " + orDash(device.Transport)
	}
	return device.Health.Summary()
}

// deviceStyle colours a row by what it is and how full it is.
func (a *app) deviceStyle(device disk.FlatDevice) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case device.IsDisk() && device.Health.Concerning():
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case device.UsePercent() >= 90:
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case device.UsePercent() >= 80:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case device.ReadOnly:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// mountsTable renders the mount table crossed with fstab.
func (a *app) mountsTable() string {
	columns := []ui.Column{
		{Title: "MOUNT POINT", Width: 20, Flex: true},
		{Title: "SOURCE", Width: 18, Flex: true},
		{Title: "TYPE", Width: 8},
		{Title: "FSTAB", Width: 14},
	}
	showUsed := a.width >= 88
	if showUsed {
		columns = append(columns, ui.Column{Title: "USED", Width: barWidth + 5})
	}

	rows := make([][]string, 0, len(a.mounts))
	styles := make([]*lipgloss.Style, 0, len(a.mounts))
	for _, mount := range a.mounts {
		row := []string{mount.Target, mount.Source, mount.FSType, mount.Match}
		if showUsed {
			row = append(row, usageBar(mount.UsePercentValue()))
		}
		rows = append(rows, row)
		styles = append(styles, a.mountStyle(mount))
	}
	return a.render(columns, rows, styles)
}

// mountStyle colours a mount by its verdict, so the rows that disagree with
// fstab are the ones the eye lands on.
func (a *app) mountStyle(mount disk.Mount) *lipgloss.Style {
	var style lipgloss.Style
	switch mount.Match {
	case disk.MatchNotMounted:
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case disk.MatchNotInFstab, disk.MatchOptionsDiffer:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case disk.MatchTransient:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// btrfsTable renders one row per btrfs filesystem.
func (a *app) btrfsTable() string {
	columns := []ui.Column{
		{Title: "MOUNT POINT", Width: 18, Flex: true},
		{Title: "ALLOCATED", Width: 12},
		{Title: "USED", Width: 12},
		{Title: "SUBVOLS", Width: 8},
		{Title: "SCRUB", Width: 10},
	}
	showBalance := a.width >= 88
	if showBalance {
		columns = append(columns,
			ui.Column{Title: "BALANCE", Width: 10},
			ui.Column{Title: "ERRORS", Width: 7})
	}

	rows := make([][]string, 0, len(a.btrfs))
	styles := make([]*lipgloss.Style, 0, len(a.btrfs))
	for _, fs := range a.btrfs {
		row := []string{
			fs.Mountpoint,
			orDash(fs.Usage.Allocated),
			orDash(fs.Usage.Used),
			strconv.Itoa(len(fs.Subvolumes)),
			fs.Scrub.State,
		}
		if showBalance {
			row = append(row, fs.Balance.State, strconv.Itoa(fs.Errors))
		}
		rows = append(rows, row)
		style := a.theme.Row
		if fs.Errors > 0 || fs.Scrub.ErrorCount > 0 {
			style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
		}
		styles = append(styles, &style)
	}
	return a.render(columns, rows, styles)
}

// smartTable renders one row per drive.
func (a *app) smartTable() string {
	columns := []ui.Column{
		{Title: "DEVICE", Width: 14, Flex: true},
		{Title: "HEALTH", Width: 18},
		{Title: "TEMP", Width: 6},
		{Title: "HOURS", Width: 8},
	}
	showModel := a.width >= 84
	if showModel {
		columns = append(columns, ui.Column{Title: "MODEL", Width: 24, Flex: true})
	}

	rows := make([][]string, 0, len(a.smart))
	styles := make([]*lipgloss.Style, 0, len(a.smart))
	for _, reading := range a.smart {
		row := []string{
			reading.Device,
			reading.Summary(),
			temperatureCell(reading.Temperature),
			numberCell(reading.PowerOnHours),
		}
		if showModel {
			row = append(row, reading.Model)
		}
		rows = append(rows, row)
		styles = append(styles, a.smartStyle(reading))
	}
	return a.render(columns, rows, styles)
}

// smartStyle colours a drive by its verdict.
func (a *app) smartStyle(reading disk.SMART) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case reading.Health == disk.HealthFailed:
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case reading.Concerning():
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case !reading.Available:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	default:
		style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
	}
	return &style
}

// spaceTable renders the df view.
func (a *app) spaceTable() string {
	columns := []ui.Column{
		{Title: "MOUNT POINT", Width: 20, Flex: true},
		{Title: "SIZE", Width: 8},
		{Title: "USED", Width: 8},
		{Title: "AVAIL", Width: 8},
		{Title: "FULL", Width: barWidth + 5},
	}
	showSource := a.width >= 92
	if showSource {
		columns = append(columns, ui.Column{Title: "SOURCE", Width: 20, Flex: true})
	}

	rows := make([][]string, 0, len(a.space))
	styles := make([]*lipgloss.Style, 0, len(a.space))
	for _, row := range a.space {
		cells := []string{row.Target, row.Size, row.Used, row.Avail,
			usageBar(row.UsePercentValue())}
		if showSource {
			cells = append(cells, row.Source)
		}
		rows = append(rows, cells)
		style := a.theme.Row
		switch {
		case row.UsePercentValue() >= 90:
			style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
		case row.UsePercentValue() >= 80:
			style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
		}
		styles = append(styles, &style)
	}
	return a.render(columns, rows, styles)
}

// temperatureCell renders a temperature, or nothing when the drive has none.
func temperatureCell(celsius int) string {
	if celsius < 0 {
		return ""
	}
	return strconv.Itoa(celsius) + "°C"
}

// numberCell renders a counter, or nothing when it is absent.
func numberCell(value int) string {
	if value < 0 {
		return ""
	}
	return strconv.Itoa(value)
}

// detailView renders one row in full.
func (a *app) detailView() string {
	lines := a.detailLines()
	header := a.headerView(a.detailTitle())
	tabs := a.tabBar()

	height := a.detailHeight()
	offset := min(a.detailOffset, max(len(lines)-height, 0))
	a.detailOffset = offset
	end := min(offset+height, len(lines))

	body := make([]string, 0, height)
	for _, line := range lines[offset:end] {
		body = append(body, a.theme.Row.Width(a.width).Render(
			ui.Truncate(line, a.width-2)))
	}
	for i := len(body); i < height; i++ {
		body = append(body, a.theme.Row.Width(a.width).Render(""))
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	position := strconv.Itoa(offset+1) + "–" + strconv.Itoa(end) +
		" of " + strconv.Itoa(len(lines)) + " lines  ·  esc to go back"
	status := ui.StatusLine(a.theme, a.statusKind, a.status, position, a.width)
	return strings.Join([]string{header, tabs,
		strings.Join(body, "\n"), help, status}, "\n")
}

// detailTitle names the row the detail screen is showing.
func (a *app) detailTitle() string {
	index := a.cursor[a.section]
	switch a.section {
	case sectionDevices:
		if index < len(a.devices) {
			return a.devices[index].Name
		}
	case sectionMounts:
		if index < len(a.mounts) {
			return a.mounts[index].Target
		}
	case sectionBtrfs:
		if index < len(a.btrfs) {
			return a.btrfs[index].Mountpoint
		}
	case sectionSMART:
		if index < len(a.smart) {
			return a.smart[index].Device
		}
	case sectionSpace:
		if index < len(a.space) {
			return a.space[index].Target
		}
	}
	return ""
}

// detailLines builds the detail screen's text, section by section. It returns
// plain strings so the screen can be scrolled and width-truncated in one place.
func (a *app) detailLines() []string {
	index := a.cursor[a.section]
	switch a.section {
	case sectionDevices:
		if index < len(a.devices) {
			return a.deviceDetail(a.devices[index])
		}
	case sectionMounts:
		if index < len(a.mounts) {
			return a.mountDetailLines(a.mounts[index])
		}
	case sectionBtrfs:
		if index < len(a.btrfs) {
			return btrfsDetail(a.btrfs[index])
		}
	case sectionSMART:
		if index < len(a.smart) {
			return smartDetail(a.smart[index])
		}
	case sectionSpace:
		if index < len(a.space) {
			return a.spaceDetail(a.space[index])
		}
	}
	return []string{"(nothing selected)"}
}

// deviceDetail renders one block device in full.
func (a *app) deviceDetail(device disk.FlatDevice) []string {
	lines := []string{
		device.Kind + " " + device.Name,
		"",
		"  path           " + device.Path(),
		"  size           " + orDash(device.Size),
		"  filesystem     " + orDash(device.FSType),
		"  label          " + orDash(device.Label),
		"  uuid           " + orDash(device.UUID),
		"  mounted at     " + orDash(strings.Join(device.Mountpoints, ", ")),
	}
	if device.PartUUID != "" {
		lines = append(lines, "  partuuid       "+device.PartUUID)
	}
	if device.IsDisk() {
		lines = append(lines,
			"  model          "+orDash(device.Model),
			"  serial         "+orDash(device.Serial),
			"  transport      "+orDash(device.Transport),
			"  media          "+device.Spindle())
	}
	if device.Removable {
		lines = append(lines, "  removable      yes")
	}
	if device.ReadOnly {
		lines = append(lines, "  read-only      yes")
	}
	if device.FSUsePercent != "" {
		lines = append(lines, "", "Usage",
			"  "+usageBar(device.UsePercent())+"   "+orDash(device.FSUsed)+" used")
	}

	if device.IsDisk() && device.Health.Device != "" {
		lines = append(lines, "")
		lines = append(lines, smartDetail(device.Health)...)
	}
	if len(device.Children) > 0 {
		lines = append(lines, "", "Contains")
		for _, child := range device.Children {
			lines = append(lines, "  "+child.Name+"  "+orDash(child.Size)+"  "+
				orDash(child.FSType)+"  "+orDash(strings.Join(child.Mountpoints, " ")))
		}
	}
	return lines
}

// mountDetailLines renders one mount in full: what is mounted, what fstab
// says, where the two disagree, and what findmnt makes of the file.
func (a *app) mountDetailLines(mount disk.Mount) []string {
	lines := []string{
		"Mount " + mount.Target,
		"",
		"  source         " + orDash(mount.Source),
		"  type           " + orDash(mount.FSType),
		"  mounted        " + yesNo(mount.Mounted),
		"  in fstab       " + yesNo(mount.InFstab),
		"  verdict        " + mount.Match,
	}
	if mount.Size != "" {
		lines = append(lines,
			"  size           "+mount.Size,
			"  used           "+orDash(mount.Used)+"  "+usageBar(mount.UsePercentValue()),
			"  available      "+orDash(mount.Avail))
	}

	lines = append(lines, "", "Options in effect")
	lines = append(lines, "  "+orDash(mount.Options))

	lines = append(lines, "", "fstab")
	switch {
	case a.mountDetail.FstabLine != "":
		lines = append(lines, "  "+a.mountDetail.FstabLine)
	case mount.FstabLine != "":
		lines = append(lines, "  "+mount.FstabLine)
	default:
		lines = append(lines,
			"  (no entry — this mount will not come back after a reboot; "+
				"press e to write one)")
	}
	if missing := storage.MissingOptions(mount.FstabOptions, mount.Options); len(missing) > 0 {
		lines = append(lines, "",
			"  fstab asks for options the kernel is not reporting:",
			"    "+strings.Join(missing, ", "))
	}

	lines = append(lines, "", "findmnt --verify")
	switch {
	case a.mountDetail.VerifyErr != "":
		lines = append(lines, "  ("+a.mountDetail.VerifyErr+")")
	case strings.TrimSpace(a.mountDetail.Verify) == "":
		lines = append(lines, "  (reading…)")
	default:
		for _, line := range strings.Split(strings.TrimSpace(a.mountDetail.Verify), "\n") {
			lines = append(lines, "  "+line)
		}
	}
	return lines
}

// btrfsDetail renders one btrfs filesystem in full.
func btrfsDetail(fs disk.Btrfs) []string {
	lines := []string{
		"btrfs at " + fs.Mountpoint,
		"",
		"  devices        " + orDash(strings.Join(fs.Devices, ", ")),
		"",
		"Allocation",
		"  device size    " + orDash(fs.Usage.DeviceSize),
		"  allocated      " + orDash(fs.Usage.Allocated),
		"  unallocated    " + orDash(fs.Usage.Unallocated),
		"  used           " + orDash(fs.Usage.Used),
		"  free (est.)    " + orDash(fs.Usage.Free),
		"  global reserve " + orDash(fs.Usage.GlobalReserve),
	}
	if !fs.Usage.PerDevice {
		lines = append(lines,
			"  (the per-device breakdown needs root, so btrfs did not print it)")
	}
	for _, block := range fs.Usage.Blocks {
		lines = append(lines, "  "+block.Type+","+block.Profile+"  size "+
			block.Size+"  used "+block.Used+"  "+block.Percent)
	}

	lines = append(lines, "", "Subvolumes")
	if len(fs.Subvolumes) == 0 {
		lines = append(lines, "  (none listed — the read needs root)")
	}
	for _, sub := range fs.Subvolumes {
		lines = append(lines, "  id "+strconv.Itoa(sub.ID)+"  gen "+
			strconv.Itoa(sub.Generation)+"  top "+strconv.Itoa(sub.TopLevel)+
			"  "+sub.Path)
	}

	if fs.QuotaOn {
		lines = append(lines, "", "Quota groups")
		for _, qgroup := range fs.Qgroups {
			line := "  " + qgroup.ID + "  referenced " + qgroup.Referenced +
				"  exclusive " + qgroup.Exclusive
			if qgroup.MaxReferenced != "" {
				line += "  limit " + qgroup.MaxReferenced
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "", "Scrub",
		"  state          "+fs.Scrub.State)
	if fs.Scrub.Started != "" {
		lines = append(lines, "  started        "+fs.Scrub.Started)
	}
	if fs.Scrub.Duration != "" {
		lines = append(lines, "  duration       "+fs.Scrub.Duration)
	}
	if fs.Scrub.Rate != "" {
		lines = append(lines, "  rate           "+fs.Scrub.Rate)
	}
	if fs.Scrub.Total != "" {
		lines = append(lines, "  total          "+fs.Scrub.Total)
	}
	if fs.Scrub.Errors != "" {
		lines = append(lines, "  errors         "+fs.Scrub.Errors)
	}
	if fs.Scrub.Detail != "" {
		lines = append(lines, "  note           "+fs.Scrub.Detail)
	}

	lines = append(lines, "", "Balance",
		"  state          "+fs.Balance.State)
	if fs.Balance.Summary != "" {
		lines = append(lines, "  "+fs.Balance.Summary)
	}
	if fs.Balance.Detail != "" && fs.Balance.Detail != fs.Balance.Summary {
		lines = append(lines, "  note           "+fs.Balance.Detail)
	}

	lines = append(lines, "", "Device error counters")
	if len(fs.DeviceStats) == 0 {
		lines = append(lines, "  (none read)")
	}
	for _, stat := range fs.DeviceStats {
		marker := ""
		if stat.Errors() > 0 {
			marker = "   ← errors recorded"
		}
		lines = append(lines, "  "+stat.Device+
			"  write "+strconv.Itoa(stat.Write)+
			"  read "+strconv.Itoa(stat.Read)+
			"  flush "+strconv.Itoa(stat.Flush)+
			"  corruption "+strconv.Itoa(stat.Corruption)+
			"  generation "+strconv.Itoa(stat.Generation)+marker)
	}
	return lines
}

// smartDetail renders one drive's health in full.
func smartDetail(reading disk.SMART) []string {
	lines := []string{
		"SMART " + reading.Device,
		"",
		"  health         " + reading.Health,
	}
	if !reading.Available {
		lines = append(lines, "  reason         "+orUnknown(reading.Detail))
		return lines
	}
	lines = append(lines,
		"  model          "+orDash(reading.Model),
		"  serial         "+orDash(reading.Serial),
		"  interface      "+orDash(reading.Kind),
		"  temperature    "+orDash(temperatureCell(reading.Temperature)),
		"  power-on hours "+orDash(numberCell(reading.PowerOnHours)))

	switch reading.Kind {
	case "nvme":
		lines = append(lines,
			"  endurance used "+orDash(percentCell(reading.PercentageUsed)),
			"  media errors   "+orDash(numberCell(reading.MediaErrors)))
	default:
		lines = append(lines,
			"  reallocated    "+orDash(numberCell(reading.ReallocatedSectors)),
			"  pending        "+orDash(numberCell(reading.PendingSectors)))
	}
	if reading.Concerning() {
		lines = append(lines, "",
			"  This drive is worth watching. A reallocated or pending sector, "+
				"an NVMe media error or an endurance estimate at 100% all mean "+
				"the drive is already working around damage.")
	}
	if reading.Detail != "" {
		lines = append(lines, "  note           "+reading.Detail)
	}

	lines = append(lines, "", "Self-test log")
	if len(reading.SelfTests) == 0 {
		lines = append(lines, "  (no self-test has been run)")
	}
	for _, test := range reading.SelfTests {
		line := "  " + test.Type + "  " + test.Status
		if test.Hours > 0 {
			line += "  at " + strconv.Itoa(test.Hours) + "h"
		}
		if test.LBA != "" {
			line += "  first failing LBA " + test.LBA
		}
		lines = append(lines, line)
	}
	return lines
}

// spaceDetail renders one filesystem's space, and points at ncdu for the
// question this tool does not answer.
func (a *app) spaceDetail(row disk.SpaceRow) []string {
	lines := []string{
		"Space at " + row.Target,
		"",
		"  source         " + orDash(row.Source),
		"  type           " + orDash(row.FSType),
		"  size           " + orDash(row.Size),
		"  used           " + orDash(row.Used),
		"  available      " + orDash(row.Avail),
		"  " + usageBar(row.UsePercentValue()),
		"",
	}
	// v0.1 does not walk a tree. Naming the tool that does is more useful
	// than a recursive scan a preview-and-confirm UI has no place running.
	if a.model.NcduPath != "" {
		lines = append(lines,
			"What is filling it is a question this screen does not answer.",
			"ncdu is installed at "+a.model.NcduPath+":",
			"",
			"  ncdu -x "+row.Target)
		return lines
	}
	lines = append(lines,
		"What is filling it is a question this screen does not answer.",
		"`ncdu -x "+row.Target+"` does; it is not installed here.")
	return lines
}

// percentCell renders a percentage, or nothing when it is absent.
func percentCell(value int) string {
	if value < 0 {
		return ""
	}
	return strconv.Itoa(value) + "%"
}

// orDash renders an empty value as a visible placeholder, so a blank line is
// never mistaken for a missing read.
func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// yesNo renders a boolean the way a detail line reads.
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// dialogDiffLines is the most diff the confirm dialog will show. The kit's
// dialog does not scroll, so a diff longer than the terminal would push its
// own title and the command preview off the screen — and the command preview
// is the one thing that must never be missed.
const dialogDiffLines = 12

// diffForDialog trims a diff to what fits above the command preview, saying
// how much was left out.
func (a *app) diffForDialog(diff string) string {
	budget := max(min(a.height-14, dialogDiffLines), 4)
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	if len(lines) <= budget {
		return diff
	}
	kept := append([]string{}, lines[:budget]...)
	return strings.Join(kept, "\n") + "\n… " +
		strconv.Itoa(len(lines)-budget) + " more diff lines"
}

// shortHelpKeys is the single-line hint bar, built from the current screen so
// a key that would be refused is never advertised.
func (a *app) shortHelpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{{Key: "enter", Desc: "detail"}}
	switch a.section {
	case sectionMounts:
		if a.caps.SupportsMount {
			hints = append(hints,
				ui.KeyHint{Key: "m", Desc: "mount"},
				ui.KeyHint{Key: "u", Desc: "umount"})
		}
		if a.caps.SupportsFstabEdit {
			hints = append(hints,
				ui.KeyHint{Key: "e", Desc: "edit fstab"},
				ui.KeyHint{Key: "a", Desc: "add"})
		}
	case sectionDevices:
		if a.caps.SupportsFstabEdit {
			hints = append(hints, ui.KeyHint{Key: "a", Desc: "add to fstab"})
		}
	case sectionBtrfs:
		hints = append(hints,
			ui.KeyHint{Key: "c", Desc: "scrub"},
			ui.KeyHint{Key: "b", Desc: "balance"})
	case sectionSMART:
		hints = append(hints,
			ui.KeyHint{Key: "s", Desc: "short test"},
			ui.KeyHint{Key: "S", Desc: "long test"})
	}
	return append(hints,
		ui.KeyHint{Key: "tab", Desc: "screen"},
		ui.KeyHint{Key: "/", Desc: "filter"},
		ui.KeyHint{Key: "R", Desc: "reload"},
		ui.KeyHint{Key: "?", Desc: "help"},
		ui.KeyHint{Key: "q", Desc: "quit"})
}

// helpScreen renders the key list, trimmed to what fits the terminal.
//
// The kit's help panel does not scroll, and a list longer than the screen
// pushes its own title off the top. The trimming is done by rendering and
// measuring rather than by counting hints, because a description wraps onto as
// many lines as the width gives it: on a narrow terminal twenty hints are
// forty lines, and no arithmetic over the hint count would know that.
//
// The keys come first and the backend versions last, which is the order
// somebody reading the panel wants them in anyway.
func (a *app) helpScreen() string {
	hints := a.helpKeys()
	full := len(hints)
	for {
		panel := ui.HelpScreen(a.theme, "tui-disk — keys", hints, a.width)
		if lipgloss.Height(panel) <= a.height || len(hints) <= 1 {
			return panel
		}
		hints = hints[:len(hints)-1]
		if dropped := full - len(hints); dropped > 0 {
			hints[len(hints)-1] = ui.KeyHint{Key: "…",
				Desc: strconv.Itoa(dropped+1) + " more, on a taller terminal"}
		}
	}
}

// helpKeys is the full key list shown on the help screen, with the backend
// versions and the caveats that apply to them underneath.
func (a *app) helpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{
		{Key: "1…5", Desc: "devices, mounts, btrfs, health, space"},
		{Key: "tab", Desc: "next screen (shift+tab for the previous one)"},
		{Key: "↑/k, ↓/j", Desc: "move the selection, or scroll the detail screen"},
		{Key: "g / G", Desc: "first / last row"},
		{Key: "pgup/pgdn", Desc: "scroll a page"},
		{Key: "enter", Desc: "open the selected row in full"},
		{Key: "esc", Desc: "leave the detail screen"},
		{Key: "/", Desc: "filter this screen (esc clears)"},
		{Key: "m / u", Desc: "mount / unmount the selected fstab entry"},
		{Key: "e", Desc: "edit the fstab entry for the selected row"},
		{Key: "a", Desc: "add an fstab entry, picking the device by UUID"},
		{Key: "D", Desc: "reload the systemd units generated from fstab"},
		{Key: "c / C", Desc: "start / cancel a btrfs scrub"},
		{Key: "b / B", Desc: "start / cancel a btrfs balance"},
		{Key: "s / S", Desc: "start a short / extended drive self-test"},
		{Key: "R", Desc: "re-read the storage"},
		{Key: "?", Desc: "this help"},
		{Key: "q", Desc: "quit"},
		{Key: "", Desc: ""},
		{Key: "note", Desc: "every change is previewed and confirmed first"},
		{Key: "note", Desc: "an fstab edit is verified with findmnt before you are asked"},
	}
	for _, result := range a.probes.Results() {
		if result.Backend == "" {
			continue
		}
		version := result.Version
		if version == "" {
			version = "not installed"
		}
		hints = append(hints, ui.KeyHint{Key: result.Backend, Desc: version})
	}
	for _, note := range a.probes.Notes() {
		hints = append(hints, ui.KeyHint{Key: note.Range, Desc: note.Impact})
	}
	for _, note := range a.model.Notes {
		hints = append(hints, ui.KeyHint{Key: "note", Desc: note})
	}
	return hints
}
