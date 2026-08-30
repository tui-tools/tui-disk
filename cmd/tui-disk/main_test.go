package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tuidisk "github.com/tui-tools/tui-disk"
	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/manifest"
)

// devNull is a writable file the flag package can print usage into without the
// test output filling up.
func devNull(t *testing.T) *os.File {
	t.Helper()
	file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// TestParseFlags covers the command line, including the one flag whose empty
// value means something: `--sudo ""` disables escalation, which must not read
// as "not given".
func TestParseFlags(t *testing.T) {
	out := devNull(t)

	opts, err := parseFlags([]string{"--demo", "--check"}, out)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || !opts.check {
		t.Errorf("opts = %+v", opts)
	}

	opts, err = parseFlags([]string{"--sudo", ""}, out)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.sudoSet || opts.sudo != "" {
		t.Errorf("an explicitly empty --sudo was lost: %+v", opts)
	}

	if _, err := parseFlags([]string{"--nonsense"}, out); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

// TestApplyOverrides asserts that the command line is the last layer, and that
// disabling escalation really disables it.
func TestApplyOverrides(t *testing.T) {
	cfg := config.Config{Values: map[string]string{config.KeySudo: "sudo -n"}}
	applyOverrides(&cfg, options{sudoSet: true, sudo: ""})
	if prefix := cfg.SudoPrefix(); prefix != nil {
		t.Errorf("sudo prefix = %v, want none", prefix)
	}

	cfg = config.Config{Values: map[string]string{config.KeySudo: "sudo -n"}}
	applyOverrides(&cfg, options{themePath: "/tmp/colors.toml"})
	if cfg.Theme() != "/tmp/colors.toml" {
		t.Errorf("theme = %q", cfg.Theme())
	}
	if strings.Join(cfg.SudoPrefix(), " ") != "sudo -n" {
		t.Errorf("a --sudo that was not passed changed the prefix")
	}
}

// TestPickBackendDemo asserts that --demo never builds the host backend, which
// is what makes the flag safe on a machine with no lsblk at all.
func TestPickBackendDemo(t *testing.T) {
	backend, err := pickBackend(config.Config{}, options{demo: true}, compatSet{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	if backend.Name() != "demo" {
		t.Errorf("backend = %q, want demo", backend.Name())
	}
}

// TestManifestDeclaresEveryBackend asserts that the embedded tool.json carries
// the three backends the code probes, under the names the code uses.
//
// It is the check that keeps the compatibility block honest: the minimum
// version, the tested list and the version-gated features are declared once,
// in the manifest, and the binary reads that same file. A backend the code
// probes and the manifest does not declare simply reports "unknown" forever.
func TestManifestDeclaresEveryBackend(t *testing.T) {
	m, err := manifest.Load(tuidisk.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded manifest does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Errorf("manifest name = %q, want %q", m.Name, toolName)
	}
	if m.Category != "storage" {
		t.Errorf("category = %q, want storage", m.Category)
	}

	for _, name := range []string{storage.BackendUtilLinux, storage.BackendBtrfs,
		storage.BackendSmartmontools} {
		backend, ok := m.Backend(name)
		if !ok {
			t.Fatalf("the manifest declares no %q backend", name)
		}
		if len(backend.VersionCommand) == 0 {
			t.Errorf("%s has no version command", name)
		}
		if backend.Minimum == "" {
			t.Errorf("%s declares no minimum version", name)
		}
	}

	// The features the code gates reads on must be the features the manifest
	// declares, by name. A typo here is a view that silently never appears.
	utilLinux, _ := m.Backend(storage.BackendUtilLinux)
	if !hasFeature(utilLinux.Features, storage.FeatureMountpoints) {
		t.Errorf("util-linux does not declare %q", storage.FeatureMountpoints)
	}
	btrfs, _ := m.Backend(storage.BackendBtrfs)
	if !hasFeature(btrfs.Features, storage.FeatureBtrfsJSON) {
		t.Errorf("btrfs-progs does not declare %q", storage.FeatureBtrfsJSON)
	}
	smart, _ := m.Backend(storage.BackendSmartmontools)
	if !hasFeature(smart.Features, storage.FeatureSmartJSON) {
		t.Errorf("smartmontools does not declare %q", storage.FeatureSmartJSON)
	}
}

// hasFeature reports whether a declared feature list carries a name.
func hasFeature(features []compat.Feature, name string) bool {
	for _, feature := range features {
		if feature.Name == name {
			return true
		}
	}
	return false
}

// TestProbeAllIsEmptyInDemo asserts that --demo probes nothing: the host's real
// versions have nothing to do with the sample machine on screen, and showing
// them in the header would be a lie about what is being driven.
func TestProbeAllIsEmptyInDemo(t *testing.T) {
	probes := probeAll(context.Background(), true)
	for _, result := range probes.Results() {
		if result.Backend != "" || result.Version != "" {
			t.Errorf("--demo probed a backend: %+v", result)
		}
	}
	if len(probes.Notes()) != 0 {
		t.Errorf("--demo carried %d manifest notes", len(probes.Notes()))
	}
}

// TestExamplesConfigMatchesTheKeys asserts that the shipped example lists the
// keys the tool actually reads, so a user copying it gets a file that works.
func TestExamplesConfigMatchesTheKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "config.toml"))
	if err != nil {
		t.Fatalf("reading the example config: %v", err)
	}
	for key := range defaults() {
		if !strings.Contains(string(raw), key+" =") {
			t.Errorf("examples/config.toml does not document %q", key)
		}
	}
}
