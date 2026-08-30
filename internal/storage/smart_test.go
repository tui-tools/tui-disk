package storage

import (
	"testing"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// TestParseSMARTATA reads a spinning disk that has already replaced two
// sectors: healthy by its own self-assessment, and worth watching anyway.
// That gap is the whole reason the health column shows a number next to the
// verdict rather than the verdict alone.
func TestParseSMARTATA(t *testing.T) {
	reading, err := ParseSMART("/dev/sda", read(t, "smartctl-ata.json"))
	if err != nil {
		t.Fatalf("ParseSMART: %v", err)
	}
	if !reading.Available {
		t.Fatal("the reading should be available")
	}
	if reading.Health != disk.HealthPassed {
		t.Errorf("health = %q, want PASSED", reading.Health)
	}
	if reading.Kind != "ata" {
		t.Errorf("kind = %q, want ata", reading.Kind)
	}
	if reading.ReallocatedSectors != 2 {
		t.Errorf("reallocated = %d, want 2", reading.ReallocatedSectors)
	}
	if reading.PendingSectors != 0 {
		t.Errorf("pending = %d, want 0", reading.PendingSectors)
	}
	if reading.Temperature != 38 || reading.PowerOnHours != 26914 {
		t.Errorf("temperature %d, hours %d", reading.Temperature, reading.PowerOnHours)
	}
	// An ATA drive has no NVMe counters, and -1 is how "this drive has no such
	// attribute" is told from "the attribute is zero".
	if reading.PercentageUsed != -1 || reading.MediaErrors != -1 {
		t.Errorf("an ATA drive reported NVMe counters: %+v", reading)
	}
	if !reading.Concerning() {
		t.Error("two reallocated sectors should be concerning")
	}
	if len(reading.SelfTests) != 2 {
		t.Fatalf("self-tests = %d, want 2", len(reading.SelfTests))
	}
	if !reading.SelfTests[0].Passed {
		t.Error("a completed self-test should be passed")
	}
}

// TestParseSMARTNVMe reads a healthy NVMe, whose counters are a different set
// with the same meaning.
func TestParseSMARTNVMe(t *testing.T) {
	reading, err := ParseSMART("/dev/nvme0n1", read(t, "smartctl-nvme.json"))
	if err != nil {
		t.Fatalf("ParseSMART: %v", err)
	}
	if reading.Kind != "nvme" {
		t.Errorf("kind = %q, want nvme", reading.Kind)
	}
	if reading.PercentageUsed != 3 || reading.MediaErrors != 0 {
		t.Errorf("endurance %d, media errors %d",
			reading.PercentageUsed, reading.MediaErrors)
	}
	if reading.ReallocatedSectors != -1 || reading.PendingSectors != -1 {
		t.Errorf("an NVMe reported ATA sector counters: %+v", reading)
	}
	if reading.Temperature != 41 || reading.PowerOnHours != 4210 {
		t.Errorf("temperature %d, hours %d", reading.Temperature, reading.PowerOnHours)
	}
	if reading.Concerning() {
		t.Error("a drive at 3% endurance with no media errors is fine")
	}
	if len(reading.SelfTests) != 1 {
		t.Fatalf("self-tests = %d, want 1", len(reading.SelfTests))
	}
	if !reading.SelfTests[0].Passed {
		t.Error("an NVMe self-test result of 0 is a pass")
	}
}

// TestParseSMARTNoCapability is the answer a virtual disk and a USB bridge
// both give: smartctl opened the device, exited non-zero, and said there is no
// SMART here. That is a fact about the machine, not a failure, and it must not
// come back as an error.
func TestParseSMARTNoCapability(t *testing.T) {
	reading, err := ParseSMART("/dev/vda", read(t, "smartctl-no-smart.json"))
	if err != nil {
		t.Fatalf("a device without SMART must not be an error: %v", err)
	}
	if reading.Available {
		t.Error("a device without SMART has no reading")
	}
	if reading.Health != disk.HealthUnknown {
		t.Errorf("health = %q, want unknown", reading.Health)
	}
	if reading.Detail == "" {
		t.Error("an unknown verdict must carry the reason smartctl gave")
	}
	if reading.Concerning() {
		t.Error("a drive that cannot be asked is not concerning")
	}
	if reading.Summary() != disk.HealthUnknown {
		t.Errorf("summary = %q, want unknown", reading.Summary())
	}
}

// TestParseSMARTFailing reads a drive that says it is failing, which is the
// one reading that must never be softened.
func TestParseSMARTFailing(t *testing.T) {
	reading, err := ParseSMART("/dev/sdc", read(t, "smartctl-failing.json"))
	if err != nil {
		t.Fatalf("ParseSMART: %v", err)
	}
	if reading.Health != disk.HealthFailed {
		t.Errorf("health = %q, want FAILED", reading.Health)
	}
	if !reading.Concerning() {
		t.Error("a failed self-assessment is concerning")
	}
	if reading.Summary() != disk.HealthFailed {
		t.Errorf("summary = %q, want FAILED", reading.Summary())
	}
	if len(reading.SelfTests) != 1 || reading.SelfTests[0].Passed {
		t.Errorf("the failed self-test parsed as %+v", reading.SelfTests)
	}
	if reading.SelfTests[0].LBA != "1902348" {
		t.Errorf("first failing LBA = %q", reading.SelfTests[0].LBA)
	}
}

// TestUnavailableSMART asserts the value used when the read never produced
// JSON at all — no smartctl, or sudo refusing to escalate.
func TestUnavailableSMART(t *testing.T) {
	reading := UnavailableSMART("/dev/sda", "sudo needs a password")
	if reading.Available || reading.Health != disk.HealthUnknown {
		t.Errorf("reading = %+v", reading)
	}
	if reading.ReallocatedSectors != -1 || reading.PercentageUsed != -1 {
		t.Error("an unavailable reading must not report counters as zero")
	}
}
