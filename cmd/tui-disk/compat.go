package main

import (
	"context"

	tuidisk "github.com/tui-tools/tui-disk"
	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
)

// compatSet is what the three version probes found.
//
// tui-disk drives three independent packages, and each has its own answer to
// "is this version one anybody has tested". util-linux is the one the tool
// cannot run without, so it is the version the header badge shows; the other
// two are reported in the help screen and in --check, and their absence is a
// fact rather than a failure.
type compatSet struct {
	UtilLinux compat.Result `json:"utilLinux"`
	Btrfs     compat.Result `json:"btrfs"`
	Smart     compat.Result `json:"smartmontools"`
}

// Results returns the three probes in a fixed order, for the screens that
// render them as a list.
func (c compatSet) Results() []compat.Result {
	return []compat.Result{c.UtilLinux, c.Btrfs, c.Smart}
}

// Notes returns every manifest note that applies to the versions found, so the
// help screen can show all three backends' caveats in one place.
func (c compatSet) Notes() []compat.Note {
	var notes []compat.Note
	for _, result := range c.Results() {
		notes = append(notes, result.Notes...)
	}
	return notes
}

// probeAll reads the versions of the three packages this tool drives.
//
// The facts each is judged against — the minimum version, the versions the lab
// has actually run against, the caveats that apply to a range, and which reads
// exist on which release — come from the repository's own tool.json, embedded
// in the binary, so there is no second copy of them in the code.
//
// It never fails: a manifest that cannot be parsed and a missing binary both
// produce the zero Result, whose capability set answers yes to everything —
// which is the right default, because a backend that cannot do what was asked
// refuses in its own words, and that is a better message than a view hidden
// over an unreadable version string.
func probeAll(ctx context.Context, demo bool) compatSet {
	// --demo drives an in-memory machine; probing the host's real tools would
	// report versions that have nothing to do with what is on screen.
	if demo {
		return compatSet{}
	}
	m, err := manifest.Load(tuidisk.ManifestJSON)
	if err != nil {
		return compatSet{}
	}
	return compatSet{
		UtilLinux: probeOne(ctx, m, storage.BackendUtilLinux),
		Btrfs:     probeOne(ctx, m, storage.BackendBtrfs),
		Smart:     probeOne(ctx, m, storage.BackendSmartmontools),
	}
}

// probeOne probes a single declared backend by name.
func probeOne(ctx context.Context, m manifest.Manifest, name string) compat.Result {
	backend, ok := m.Backend(name)
	if !ok {
		return compat.Result{}
	}
	return compat.Probe(ctx, backend)
}
