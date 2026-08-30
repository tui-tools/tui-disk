package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
)

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only
// tui-disk knows: the version the same probes --check uses read off util-linux,
// and what they found of the two optional packages, because half the bugs
// filed against this tool are a view that is empty for want of btrfs-progs or
// smartmontools rather than a parser that got something wrong.
//
// It never reads the storage. --check is the flag that does that, and it shells
// out a dozen times; a report has to be cheap and has to work for a user who
// cannot escalate, because the missing privilege may be the bug. For the same
// reason a machine without lsblk still gets a report, with the selection error
// as one of its lines: "there is nothing here to drive" is a bug report, not a
// refusal.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same probes --check and the header use. There is one version probe
	// in this tool and this is it.
	probes := probeAll(context.Background(), opts.demo)

	var backendName, selectError string
	if backend, err := pickBackend(cfg, opts, probes); err != nil {
		selectError = err.Error()
	} else {
		backendName = backend.Name()
	}

	info := report.Info{
		Tool:           toolName,
		Version:        version,
		Backend:        backendName,
		BackendVersion: probes.UtilLinux.Version,
		BackendDetail:  probes.UtilLinux.Detail,
		Demo:           opts.demo,
		Sudo:           cfg.String(config.KeySudo, ""),
		Theme:          palette.Name,
	}
	if opts.demo {
		// The fake imitates a machine that has all three packages, so a demo
		// report says which ones the session was really exercising rather than
		// leaving "demo" to stand for anything at all.
		info.Backend = "demo"
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: strings.Join(imitatedBackends, ", "),
		})
	} else {
		info.Extra = append(info.Extra, report.Field{
			Key: "optional backends", Value: describeOptional(probes),
		})
	}
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: selectError,
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// describeOptional renders the two packages the tool can run without as one
// line. A report that named only util-linux leaves the reader guessing whether
// the btrfs screen was empty because the filesystem has nothing to show or
// because btrfs-progs is not installed, and that difference is most of the
// "the view is blank" reports.
func describeOptional(probes compatSet) string {
	return describeProbe(storage.BackendBtrfs, probes.Btrfs) + ", " +
		describeProbe(storage.BackendSmartmontools, probes.Smart)
}

// describeProbe names one probed package and what the probe made of it. The
// name is passed in rather than read off the result, because a manifest that
// could not be loaded leaves a zero Result with no name on it, and the package
// it stands for is still worth naming.
func describeProbe(name string, result compat.Result) string {
	switch {
	case result.Version != "":
		return name + " " + result.Version
	case result.Detail != "":
		return name + " (version unknown: " + result.Detail + ")"
	}
	return name + " (version unknown)"
}

// imitatedBackends are the three packages the fake stands in for, in the order
// the compatibility screens use.
var imitatedBackends = []string{storage.BackendUtilLinux, storage.BackendBtrfs,
	storage.BackendSmartmontools}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)
