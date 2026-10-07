package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-disk/internal/storage"
)

// uncountedFake is the demo machine with the btrfs device error counters
// unread, which is what a kernel that refuses `btrfs device stats` to the
// caller leaves behind.
type uncountedFake struct{ *storage.Fake }

func (f uncountedFake) Load(ctx context.Context) (disk.Model, error) {
	model, err := f.Fake.Load(ctx)
	for i := range model.Btrfs {
		model.Btrfs[i].DeviceStats = nil
	}
	return model, err
}

func checkFields(t *testing.T, backend disk.Backend) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := runCheck(backend, compatSet{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatalf("--check is not JSON: %v", err)
	}
	return fields
}

// A zero btrfsErrors means clean only when the counters behind it were read,
// so --check says how many filesystems had none read.
func TestCheckCountsUnreadBtrfsCounters(t *testing.T) {
	read := checkFields(t, storage.NewFake())
	if read["btrfsFilesystems"].(float64) == 0 {
		t.Fatal("the demo machine should have a btrfs filesystem")
	}
	if got := read["btrfsUncounted"]; got != float64(0) {
		t.Errorf("demo btrfsUncounted = %v, want 0", got)
	}

	unread := checkFields(t, uncountedFake{storage.NewFake()})
	if got, want := unread["btrfsUncounted"], unread["btrfsFilesystems"]; got != want {
		t.Errorf("btrfsUncounted = %v, want every filesystem (%v)", got, want)
	}
	if got := unread["btrfsErrors"]; got != float64(0) {
		t.Errorf("btrfsErrors = %v, want 0: nothing was counted", got)
	}
}
