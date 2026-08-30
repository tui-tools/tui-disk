package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-disk/internal/disk"
)

// checkTimeout bounds the read. Loading the model shells out to lsblk,
// findmnt, df, btrfs and smartctl, and a machine with a drive that is failing
// to answer must not hang a non-interactive check forever.
const checkTimeout = 60 * time.Second

// checkReport is what --check prints: the model the backend parsed, plus the
// counts and verdicts a test can assert on without walking the whole
// structure.
//
// It is a report of the read path only. --check never builds and never runs a
// mutation: the whole point is that it is safe to run anywhere, including in
// CI against a production-shaped machine.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Backend string `json:"backend"`
	// Describe is the backend's own one-line summary, which is where the demo
	// backend says it is a demo.
	Describe string `json:"describe"`

	// Devices, Disks and Mounts are the totals across the model.
	Devices int `json:"devices"`
	Disks   int `json:"disks"`
	Mounts  int `json:"mounts"`
	// FstabEntries counts the real entries, comments excluded.
	FstabEntries int `json:"fstabEntries"`
	// FstabMismatches is the number of mount rows that disagree with fstab,
	// and MismatchTargets names them, so a test can assert on which.
	FstabMismatches int      `json:"fstabMismatches"`
	MismatchTargets []string `json:"mismatchTargets,omitempty"`

	// BtrfsFilesystems and BtrfsErrors summarise the btrfs view. A machine
	// with no btrfs reports zero for both, which is not a failure.
	BtrfsFilesystems int `json:"btrfsFilesystems"`
	BtrfsErrors      int `json:"btrfsErrors"`

	// SMARTHealth is one entry per drive: the device node and its verdict.
	SMARTHealth []healthLine `json:"smartHealth"`

	// Compat is what the three version probes found. It is reported rather
	// than asserted: an untested version is a fact about the machine, not a
	// failure of the read path.
	Compat compatSet `json:"compat"`
	// Notes are the one-line facts about what could not be read.
	Notes []string `json:"notes,omitempty"`
	// Model is the parsed state in full.
	Model disk.Model `json:"model"`
}

// healthLine is one drive's verdict, flattened so a smoke test can grep it.
type healthLine struct {
	Device string `json:"device"`
	Health string `json:"health"`
	// Detail explains an unknown verdict, which on a virtio disk is the
	// expected answer rather than an error.
	Detail string `json:"detail,omitempty"`
	// Concerning marks a drive worth looking at.
	Concerning bool `json:"concerning"`
}

// runCheck exercises the backend's real read path and prints the parsed model
// as JSON. It returns an error when the backend cannot be read, which main
// turns into a non-zero exit — so a caller can treat the exit code alone as
// the verdict.
//
// A machine with no btrfs, no smartctl, or no drive that carries SMART is not
// a failure: the sections come back empty and the notes say why. That is the
// read path working, and it is what the smoke test asserts on a virtio guest.
func runCheck(backend disk.Backend, probes compatSet, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	model, err := backend.Load(ctx)
	if err != nil {
		return fmt.Errorf("%s backend read failed: %w", backend.Name(), err)
	}

	report := checkReport{
		Tool:             toolName,
		Version:          version,
		Backend:          backend.Name(),
		Describe:         backend.Describe(),
		Devices:          len(model.Flatten()),
		Disks:            len(model.Disks()),
		Mounts:           len(model.Mounts),
		FstabMismatches:  model.Mismatches(),
		BtrfsFilesystems: len(model.Btrfs),
		BtrfsErrors:      model.BtrfsErrors(),
		Compat:           probes,
		Notes:            model.Notes,
		Model:            model,
	}
	for _, entry := range model.Fstab {
		if !entry.Comment {
			report.FstabEntries++
		}
	}
	for _, mount := range model.Mounts {
		if mount.Mismatch() {
			report.MismatchTargets = append(report.MismatchTargets, mount.Target)
		}
	}
	for _, reading := range model.SMART {
		report.SMARTHealth = append(report.SMARTHealth, healthLine{
			Device: reading.Device, Health: reading.Health,
			Detail: reading.Detail, Concerning: reading.Concerning(),
		})
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
