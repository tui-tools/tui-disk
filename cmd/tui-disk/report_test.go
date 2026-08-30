package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
)

// reportConfig is the configuration a report is rendered against: the defaults,
// with escalation as it ships.
func reportConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that it names the packages the fake stands in for, and
// that no disk was read to produce any of it.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	if err := runReport(reportConfig(), options{demo: true, report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: util-linux, btrfs-progs, smartmontools\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "optional backends:") {
		t.Errorf("a demo report must not claim to have probed the machine:\n%s", got)
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportLive is the privacy guard on the real path: the block a user
// pastes into a public issue must name no host, no account and no home path,
// on a machine that may or may not have any of the three packages.
func TestRunReportLive(t *testing.T) {
	t.Setenv("HOME", "/home/somebody")
	t.Setenv("USER", "somebody")
	t.Setenv("HOSTNAME", "a-machine-nobody-should-read-about")

	var out strings.Builder
	if err := runReport(reportConfig(), options{report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, forbidden := range []string{
		"somebody", "a-machine-nobody-should-read-about", "/home/",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the report leaked %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "mode: live\n") {
		t.Errorf("a live report must say so on the mode line:\n%s", got)
	}
	if !strings.Contains(got, "optional backends: btrfs-progs") {
		t.Errorf("a live report must describe the optional packages:\n%s", got)
	}
}

// TestDescribeProbe renders one package the way the block carries it, which is
// what tells "btrfs-progs 6.19 is here" from "btrfs is not installed".
func TestDescribeProbe(t *testing.T) {
	tests := []struct {
		name   string
		result compat.Result
		want   string
	}{
		{
			name:   "a probed version",
			result: compat.Result{Backend: "btrfs-progs", Version: "6.19.1"},
			want:   "btrfs-progs 6.19.1",
		},
		{
			name:   "a package that is not installed says why",
			result: compat.Result{Detail: "btrfs not found"},
			want:   "btrfs-progs (version unknown: btrfs not found)",
		},
		{
			name:   "neither a version nor a reason",
			result: compat.Result{},
			want:   "btrfs-progs (version unknown)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeProbe("btrfs-progs", tc.result); got != tc.want {
				t.Errorf("describeProbe = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDescribeOptional keeps both optional packages on the line, in order, even
// when nothing could be probed at all.
func TestDescribeOptional(t *testing.T) {
	got := describeOptional(compatSet{
		Btrfs: compat.Result{Version: "6.19.1"},
		Smart: compat.Result{Detail: "smartctl not found"},
	})
	want := "btrfs-progs 6.19.1, smartmontools (version unknown: smartctl not found)"
	if got != want {
		t.Errorf("describeOptional = %q, want %q", got, want)
	}

	if got := describeOptional(compatSet{}); !strings.Contains(got, "btrfs-progs") ||
		!strings.Contains(got, "smartmontools") {
		t.Errorf("describeOptional(zero) = %q, want both packages named", got)
	}
}
