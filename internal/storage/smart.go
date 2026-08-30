package storage

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// FeatureSmartJSON is `smartctl --json`, which arrived in smartmontools 7.0.
// Below it there is no machine-readable output at all, and tui-disk reports
// the health as unknown rather than parsing a table meant for human eyes.
const FeatureSmartJSON = "json-output"

// The ATA attribute identifiers that mean a drive is losing sectors. They are
// matched by number rather than by name because the name is vendor text and
// differs between firmwares, while the identifier is fixed by the standard.
const (
	// attrReallocated is 5, Reallocated_Sector_Ct: sectors the drive has
	// already replaced from its spare pool.
	attrReallocated = 5
	// attrPending is 197, Current_Pending_Sector: sectors the drive cannot
	// read and has not yet remapped. It is the one that predicts trouble.
	attrPending = 197
)

// smartctlJSON is the subset of `smartctl --json=c -a` this tool reads.
// Everything else the report carries is left alone rather than mirrored: what
// the health column needs is the self-assessment and the four counters below.
type smartctlJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	SmartStatus  *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	ATAAttributes *struct {
		Table []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Raw  struct {
				Value  int64  `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMeHealth *struct {
		Temperature    *int `json:"temperature"`
		PowerOnHours   *int `json:"power_on_hours"`
		PercentageUsed *int `json:"percentage_used"`
		MediaErrors    *int `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	ATASelfTestLog *struct {
		Standard struct {
			Table []struct {
				Type struct {
					String string `json:"string"`
				} `json:"type"`
				Status struct {
					String string `json:"string"`
					Passed *bool  `json:"passed"`
				} `json:"status"`
				LifetimeHours int `json:"lifetime_hours"`
				LBA           *struct {
					String string `json:"string"`
				} `json:"lba"`
			} `json:"table"`
		} `json:"standard"`
	} `json:"ata_smart_self_test_log"`
	NVMeSelfTestLog *struct {
		Table []struct {
			SelfTestCode struct {
				String string `json:"string"`
			} `json:"self_test_code"`
			SelfTestResult struct {
				String string `json:"string"`
				Value  int    `json:"value"`
			} `json:"self_test_result"`
			PowerOnHours int `json:"power_on_hours"`
		} `json:"table"`
	} `json:"nvme_self_test_log"`
}

// ParseSMART reads one `smartctl --json=c -a <device>` report.
//
// smartctl exits non-zero for facts that are not failures — bit 0 means the
// command line was wrong, but bit 1 means the device is open but does not
// speak SMART, and bits 3 and above are the drive's own warnings. The report
// is therefore parsed whatever the exit status was, and only an unparsable
// body is an error. A device with no SMART at all (every virtio disk in the
// lab is one) comes back Available=false with the reason smartctl gave.
func ParseSMART(device, output string) (disk.SMART, error) {
	result := disk.SMART{
		Device: device, Health: disk.HealthUnknown,
		Temperature: -1, PowerOnHours: -1,
		ReallocatedSectors: -1, PendingSectors: -1,
		PercentageUsed: -1, MediaErrors: -1,
	}
	var parsed smartctlJSON
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return result, fmt.Errorf("storage: cannot parse smartctl JSON for %s: %w",
			device, err)
	}

	result.Kind = smartKind(parsed)
	result.Model = strings.TrimSpace(parsed.ModelName)
	result.Serial = strings.TrimSpace(parsed.SerialNumber)

	if parsed.SmartStatus == nil {
		result.Detail = smartMessage(parsed)
		if result.Detail == "" {
			result.Detail = "this device reports no SMART data"
		}
		return result, nil
	}
	result.Available = true
	result.Health = disk.HealthFailed
	if parsed.SmartStatus.Passed {
		result.Health = disk.HealthPassed
	}

	if parsed.Temperature != nil {
		result.Temperature = parsed.Temperature.Current
	}
	if parsed.PowerOnTime != nil {
		result.PowerOnHours = parsed.PowerOnTime.Hours
	}
	readATAAttributes(&result, parsed)
	readNVMeHealth(&result, parsed)
	result.SelfTests = readSelfTests(parsed)
	if message := smartMessage(parsed); message != "" {
		result.Detail = message
	}
	return result, nil
}

// smartKind names which attribute set applies, from the transport smartctl
// reported.
func smartKind(parsed smartctlJSON) string {
	switch {
	case parsed.NVMeHealth != nil:
		return "nvme"
	case parsed.ATAAttributes != nil:
		return "ata"
	case parsed.Device.Protocol != "":
		return strings.ToLower(parsed.Device.Protocol)
	default:
		return ""
	}
}

// smartMessage joins the messages smartctl attached to the report, which is
// where "Unavailable - device lacks SMART capability" lands.
func smartMessage(parsed smartctlJSON) string {
	var parts []string
	for _, message := range parsed.Smartctl.Messages {
		text := strings.TrimSpace(message.String)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "; ")
}

// readATAAttributes pulls the two sector counters out of the attribute table.
func readATAAttributes(result *disk.SMART, parsed smartctlJSON) {
	if parsed.ATAAttributes == nil {
		return
	}
	for _, attribute := range parsed.ATAAttributes.Table {
		switch attribute.ID {
		case attrReallocated:
			result.ReallocatedSectors = int(attribute.Raw.Value)
		case attrPending:
			result.PendingSectors = int(attribute.Raw.Value)
		}
	}
}

// readNVMeHealth pulls the NVMe counters, which are the ones that mean the
// same thing as the ATA sector attributes on a drive that has no sectors.
func readNVMeHealth(result *disk.SMART, parsed smartctlJSON) {
	if parsed.NVMeHealth == nil {
		return
	}
	log := parsed.NVMeHealth
	if log.PercentageUsed != nil {
		result.PercentageUsed = *log.PercentageUsed
	}
	if log.MediaErrors != nil {
		result.MediaErrors = *log.MediaErrors
	}
	// The NVMe log carries its own copies of both, which some firmwares fill
	// in when the generic fields are absent.
	if result.Temperature < 0 && log.Temperature != nil {
		result.Temperature = *log.Temperature
	}
	if result.PowerOnHours < 0 && log.PowerOnHours != nil {
		result.PowerOnHours = *log.PowerOnHours
	}
}

// readSelfTests folds both self-test logs into one list, newest first, which
// is the order both logs are already in.
func readSelfTests(parsed smartctlJSON) []disk.SelfTest {
	var out []disk.SelfTest
	if parsed.ATASelfTestLog != nil {
		for _, row := range parsed.ATASelfTestLog.Standard.Table {
			test := disk.SelfTest{
				Type: row.Type.String, Status: row.Status.String,
				Hours: row.LifetimeHours,
			}
			if row.Status.Passed != nil {
				test.Passed = *row.Status.Passed
			}
			if row.LBA != nil {
				test.LBA = row.LBA.String
			}
			out = append(out, test)
		}
	}
	if parsed.NVMeSelfTestLog != nil {
		for _, row := range parsed.NVMeSelfTestLog.Table {
			out = append(out, disk.SelfTest{
				Type:   row.SelfTestCode.String,
				Status: row.SelfTestResult.String,
				// The NVMe log encodes success as result value 0.
				Passed: row.SelfTestResult.Value == 0,
				Hours:  row.PowerOnHours,
			})
		}
	}
	return out
}

// UnavailableSMART is the reading for a device that could not be asked at all:
// smartctl missing, or a read that failed before any JSON came back. It is a
// value rather than an error because "we do not know" is a fact about the
// machine worth showing in the column, and the reason belongs next to it.
func UnavailableSMART(device, reason string) disk.SMART {
	return disk.SMART{
		Device: device, Health: disk.HealthUnknown, Detail: reason,
		Temperature: -1, PowerOnHours: -1,
		ReallocatedSectors: -1, PendingSectors: -1,
		PercentageUsed: -1, MediaErrors: -1,
	}
}
