// Command tui-disk is a terminal UI for the machine's storage: the block
// devices and what is on them, the mount table crossed with fstab, the btrfs
// filesystems, and the SMART health of every drive. It previews the exact
// command line of every change before running it.
//
// util-linux is the backend it cannot run without; btrfs-progs and
// smartmontools are optional and their views appear only when they are there.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-disk/internal/disk"
	"github.com/tui-tools/tui-disk/internal/storage"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-disk/config.toml and ~/.config/tui-disk/config.toml.
const toolName = "tui-disk"

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys tui-disk understands. Only these
// are read from the environment (TUI_DISK_SUDO, …).
func defaults() map[string]string {
	return map[string]string{
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
	}
}

// options holds the parsed command line.
type options struct {
	demo        bool
	check       bool
	themePath   string
	sudo        string
	showVersion bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&opts.demo, "demo", false,
		"run against a sample machine, without touching the real disks")
	fs.BoolVar(&opts.check, "check", false,
		"read the storage and print the parsed model as JSON, then exit "+
			"(no UI, no changes); exit 1 if the backend cannot be read")
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-disk — a terminal UI for the machine's storage\n\n"+
			"Usage:\n  tui-disk [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_DISK_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sudo" {
			opts.sudoSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		// flag already printed the reason and the usage.
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	// The backend versions are probed once, before the backend is built,
	// because the backend needs the capability sets: which column lsblk
	// understands and which btrfs commands emit JSON are version questions,
	// and the answers come from the manifest.
	probes := probeAll(context.Background(), opts.demo)

	backend, err := pickBackend(cfg, opts, probes)
	if err != nil {
		return err
	}

	// --check is the non-interactive path: it reads the backend and prints,
	// and never starts a terminal program.
	if opts.check {
		return runCheck(backend, probes, os.Stdout)
	}

	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	program := tea.NewProgram(newApp(backend, theme.New(), probes),
		tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
}

// pickBackend returns the demo backend or the real one.
func pickBackend(cfg config.Config, opts options,
	probes compatSet) (disk.Backend, error) {
	if opts.demo {
		return storage.NewFake(), nil
	}
	return storage.NewReal(cfg.SudoPrefix(), probes.UtilLinux.Caps(),
		probes.Btrfs.Caps(), probes.Smart.Caps())
}
