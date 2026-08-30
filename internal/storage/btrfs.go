package storage

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// The version-gated capabilities of the btrfs backend, named the way the
// manifest names them. A tool asks the compat set for these instead of
// comparing version numbers in the code.
const (
	// FeatureBtrfsJSON is `btrfs --format json`, which arrived in
	// btrfs-progs 5.15. It is a per-command thing rather than a global one:
	// even on 6.19 only `device stats` and `filesystem df` emit JSON, and
	// `filesystem usage`, `subvolume list`, `scrub status` and
	// `balance status` answer "output format json is unsupported for this
	// command". The text parsers below are therefore not a fallback for old
	// releases, they are the only way to read those four.
	FeatureBtrfsJSON = "json-output"
)

// ParseBtrfsUsage reads `btrfs filesystem usage <mountpoint>`.
//
// The text form is parsed rather than JSON because btrfs-progs has never
// emitted JSON for this command: `--format json` is refused outright, through
// 6.19 at least. The Overall block is `Label: value` lines; the profile blocks
// are one `Data,single: Size:…, Used:… (…)` line each.
func ParseBtrfsUsage(output string) disk.BtrfsUsage {
	usage := disk.BtrfsUsage{PerDevice: true}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Unprivileged btrfs cannot read the chunk tree and says so on its
		// first line. Recording it is what lets the UI explain a missing
		// per-device breakdown instead of leaving a blank section.
		if strings.Contains(trimmed, "cannot read detailed chunk info") {
			usage.PerDevice = false
			continue
		}
		if block, ok := parseUsageBlock(trimmed); ok {
			usage.Blocks = append(usage.Blocks, block)
			continue
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Device size":
			usage.DeviceSize = value
		case "Device allocated":
			usage.Allocated = value
		case "Device unallocated":
			usage.Unallocated = value
		case "Used":
			usage.Used = value
		case "Free (estimated)":
			// The line carries a parenthesised minimum after the number,
			// which is a second fact on one line; the first field is the one
			// the header shows.
			usage.Free = firstField(value)
		case "Global reserve":
			// "305.00MiB\t(used: 0.00B)" is two facts on one line; the
			// reserve itself is the first field.
			usage.GlobalReserve = firstField(value)
		}
	}
	return usage
}

// parseUsageBlock reads one profile line, which is the only line of the output
// whose key carries a comma: "Data,single: Size:292.01GiB, Used:275.42GiB (94.32%)".
func parseUsageBlock(line string) (disk.UsageBlock, bool) {
	head, rest, found := strings.Cut(line, ":")
	if !found {
		return disk.UsageBlock{}, false
	}
	kind, profile, hasProfile := strings.Cut(head, ",")
	if !hasProfile {
		return disk.UsageBlock{}, false
	}
	block := disk.UsageBlock{Type: strings.TrimSpace(kind),
		Profile: strings.TrimSpace(profile)}
	for _, field := range strings.Split(rest, ",") {
		field = strings.TrimSpace(field)
		name, value, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(name) {
		case "Size":
			block.Size = value
		case "Used":
			// "275.42GiB (94.32%)" is the number and the share on one field.
			number, percent, hasPercent := strings.Cut(value, " ")
			block.Used = number
			if hasPercent {
				block.Percent = strings.Trim(percent, "()")
			}
		}
	}
	return block, block.Size != ""
}

// ParseSubvolumes reads `btrfs subvolume list -p <mountpoint>`, whose lines
// read "ID 256 gen 1234 parent 5 top level 5 path fedora".
//
// The fields are read by name rather than by position, because the flags
// change which of them are present: -p adds `parent`, -c adds `cgen`, and the
// path is always last.
func ParseSubvolumes(output string) []disk.Subvolume {
	var out []disk.Subvolume
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "ID" {
			continue
		}
		sub := disk.Subvolume{}
		for i := 0; i < len(fields)-1; i++ {
			switch fields[i] {
			case "ID":
				sub.ID = atoiOr(fields[i+1], 0)
			case "gen":
				sub.Generation = atoiOr(fields[i+1], 0)
			case "parent":
				sub.ParentID = atoiOr(fields[i+1], 0)
			case "level":
				// "top level 5": the number follows the word "level".
				sub.TopLevel = atoiOr(fields[i+1], 0)
			case "path":
				sub.Path = strings.Join(fields[i+1:], " ")
			}
		}
		if sub.ID != 0 {
			out = append(out, sub)
		}
	}
	return out
}

// ParseQgroups reads `btrfs qgroup show -re <mountpoint>`, whose columns are
// qgroupid, rfer, excl, max_rfer and max_excl.
func ParseQgroups(output string) []disk.Qgroup {
	var out []disk.Qgroup
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		// The header is "qgroupid rfer excl …" and the rule under it is all
		// dashes; a data row's first field is "level/id".
		id := fields[0]
		if !strings.Contains(id, "/") {
			continue
		}
		if _, err := strconv.Atoi(strings.SplitN(id, "/", 2)[0]); err != nil {
			continue
		}
		qgroup := disk.Qgroup{ID: id, Referenced: fields[1], Exclusive: fields[2]}
		if len(fields) > 3 {
			qgroup.MaxReferenced = fields[3]
		}
		out = append(out, qgroup)
	}
	return out
}

// ParseScrubStatus reads `btrfs scrub status <mountpoint>`.
func ParseScrubStatus(output string) disk.Scrub {
	scrub := disk.Scrub{State: disk.TaskIdle}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "no stats available" {
			// The filesystem has never been scrubbed. That is idle, and it is
			// worth saying so rather than leaving the section blank.
			scrub.Detail = "this filesystem has never been scrubbed"
			continue
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Status":
			scrub.State = normalizeTaskState(value)
		case "Scrub started":
			scrub.Started = value
		case "Duration":
			scrub.Duration = value
		case "Rate":
			scrub.Rate = value
		case "Total to scrub":
			scrub.Total = value
		case "Error summary":
			scrub.Errors = value
			scrub.ErrorCount = countScrubErrors(value)
		}
	}
	return scrub
}

// normalizeTaskState maps what btrfs prints onto the Task* constants.
func normalizeTaskState(value string) string {
	switch strings.ToLower(firstField(value)) {
	case "running":
		return disk.TaskRunning
	case "finished":
		return disk.TaskFinished
	case "aborted", "cancelled", "interrupted":
		return disk.TaskAborted
	case "":
		return disk.TaskIdle
	default:
		return value
	}
}

// countScrubErrors turns the error summary into a number. "no errors found" is
// zero; anything else carries counters like "read=2 csum=1", which are summed.
func countScrubErrors(summary string) int {
	if strings.Contains(summary, "no errors found") {
		return 0
	}
	total := 0
	for _, field := range strings.Fields(summary) {
		_, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		total += atoiOr(value, 0)
	}
	return total
}

// ParseBalanceStatus reads `btrfs balance status <mountpoint>`. The command
// prints one sentence when nothing is running and a progress block when
// something is.
func ParseBalanceStatus(output string) disk.Balance {
	trimmed := strings.TrimSpace(output)
	switch {
	case trimmed == "":
		return disk.Balance{State: disk.TaskIdle}
	case strings.Contains(trimmed, "No balance found"):
		return disk.Balance{State: disk.TaskIdle, Summary: firstLine(trimmed)}
	case strings.Contains(trimmed, "is running"):
		return disk.Balance{State: disk.TaskRunning,
			Summary: strings.Join(strings.Split(trimmed, "\n"), "  ")}
	case strings.Contains(trimmed, "paused"):
		return disk.Balance{State: disk.TaskAborted, Summary: firstLine(trimmed)}
	default:
		return disk.Balance{State: disk.TaskUnknown, Summary: firstLine(trimmed),
			Detail: firstLine(trimmed)}
	}
}

// deviceStatsJSON mirrors `btrfs --format json device stats`, which is one of
// the two commands btrfs-progs really does emit JSON for.
type deviceStatsJSON struct {
	DeviceStats []struct {
		Device     string          `json:"device"`
		DevID      json.RawMessage `json:"devid"`
		Write      json.RawMessage `json:"write_io_errs"`
		Read       json.RawMessage `json:"read_io_errs"`
		Flush      json.RawMessage `json:"flush_io_errs"`
		Corruption json.RawMessage `json:"corruption_errs"`
		Generation json.RawMessage `json:"generation_errs"`
	} `json:"device-stats"`
}

// ParseDeviceStatsJSON reads the JSON form of `btrfs device stats`.
func ParseDeviceStatsJSON(output string) ([]disk.DeviceStat, error) {
	var parsed deviceStatsJSON
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return nil, err
	}
	out := make([]disk.DeviceStat, 0, len(parsed.DeviceStats))
	for _, entry := range parsed.DeviceStats {
		out = append(out, disk.DeviceStat{
			Device:     entry.Device,
			DevID:      number(entry.DevID),
			Write:      number(entry.Write),
			Read:       number(entry.Read),
			Flush:      number(entry.Flush),
			Corruption: number(entry.Corruption),
			Generation: number(entry.Generation),
		})
	}
	return out, nil
}

// ParseDeviceStatsText reads the text form, whose lines are
// "[/dev/sda1].write_io_errs    0". It is what an older btrfs-progs, or one
// asked without --format, prints.
func ParseDeviceStatsText(output string) []disk.DeviceStat {
	byDevice := map[string]*disk.DeviceStat{}
	var order []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "[") {
			continue
		}
		device, counter, found := strings.Cut(strings.TrimPrefix(fields[0], "["), "].")
		// A counter with no device in front of it names nothing: keeping it
		// would put a nameless row of zeros on the device table, where every
		// row is supposed to be a disk the error counters belong to.
		if !found || device == "" {
			continue
		}
		stat, ok := byDevice[device]
		if !ok {
			stat = &disk.DeviceStat{Device: device}
			byDevice[device] = stat
			order = append(order, device)
		}
		value := atoiOr(fields[1], 0)
		switch counter {
		case "write_io_errs":
			stat.Write = value
		case "read_io_errs":
			stat.Read = value
		case "flush_io_errs":
			stat.Flush = value
		case "corruption_errs":
			stat.Corruption = value
		case "generation_errs":
			stat.Generation = value
		}
	}
	out := make([]disk.DeviceStat, 0, len(order))
	for _, device := range order {
		out = append(out, *byDevice[device])
	}
	return out
}

// number reads a JSON field that may be a number or a quoted number, which is
// how btrfs-progs has rendered its counters across releases.
func number(raw json.RawMessage) int {
	return atoiOr(strings.Trim(string(raw), `"`), 0)
}

// atoiOr parses an integer, falling back rather than failing: a counter nobody
// can read is better reported as zero next to the raw output than as an error
// that hides the four counters that did parse.
func atoiOr(text string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return fallback
	}
	return value
}

// firstField is the first whitespace-separated token of a value, or the empty
// string. Several `btrfs filesystem usage` lines carry two facts separated by
// a tab, and the first is the one the header shows.
func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// firstLine keeps a summary to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
