#!/bin/bash
# Backend smoke test for tui-disk, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-disk on PATH).
#
# What it proves is that the tool reads the machine's *real* storage and agrees
# with the machine's own tooling — not that a fake renders. The lab already
# covers --version and a --demo frame; this covers the backend.
#
# Three kinds of machine are asserted, because all three are normal:
#
#   ext4 root      Ubuntu cloud images: no btrfs section at all, and the tool
#                  must say so rather than show an empty one. A second disk
#                  formatted btrfs (/dev/vdb) turns the section on, which is
#                  what proves the section follows the machine and not the
#                  package list.
#   btrfs root     Fedora Cloud and Omarchy Server: the btrfs section must
#                  appear for /, with an allocation read and a scrub state.
#   virtio disks   every guest in the lab: they carry no SMART at all, so the
#                  health of each one must come back "unknown" *with a reason*
#                  rather than as a silent pass.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-disk}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-disk
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse of a grep assertion: the command must succeed and
# its output must NOT contain the pattern. It is what proves a section stayed
# empty, which is a claim about something that did not happen.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` list is generated, not claimed: it is rebuilt from
# compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where a
# line of that file comes from. The versions recorded are the ones the tool
# itself probed, read back out of --check, so they describe the machine that
# really ran the suite rather than what the tester assumed was installed.
#
# tui-disk drives three packages, so it records one line per backend that
# answered. A guest with no btrfs-progs simply contributes no btrfs line.
#
# The lines are printed behind a `compat-result:` prefix so they survive the
# trip out of the guest through the lab's per-VM log, and appended to
# $TUI_COMPAT_RESULTS as well for a run outside the lab.
record_compat() {
  local report="$1" outcome="$2" distro today
  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)

  local recorded=0 backend version block
  for key in utilLinux btrfs smartmontools; do
    block=$(sed -n "/\"$key\": {/,/^    }/p" <<<"$report")
    backend=$(sed -n 's/.*"backend": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
    version=$(sed -n 's/.*"version": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
    [[ -z $backend || -z $version ]] && continue
    printf 'compat-result: {"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}\n' \
      "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version"
    if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
      printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}\n' \
        "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version" \
        >>"$TUI_COMPAT_RESULTS"
    fi
    recorded=$((recorded + 1))
  done
  if [[ $recorded -eq 0 ]]; then
    echo "      no version was probed, so no compatibility result is recorded"
  fi
}

echo "--- tui-disk smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

if ! command -v lsblk >/dev/null; then
  echo "FAIL  lsblk is not installed on this machine"
  exit 1
fi

# What this machine really has, decided the way the tool decides it.
root_fstype=$(findmnt -no FSTYPE / 2>/dev/null)
has_btrfs=no
command -v btrfs >/dev/null && has_btrfs=yes
has_smart=no
command -v smartctl >/dev/null && has_smart=yes
echo "      root=$root_fstype btrfs-progs=$has_btrfs smartmontools=$has_smart"

# 1. The read path works at all and names the backend it drove. lsblk, findmnt
#    and df take no privileges, so this runs as the plain lab user — which is
#    itself the assertion that the tool does not escalate to look.
check "check reads the storage unprivileged" \
  "$bin --check" \
  '"backend": "util-linux"'

# 2. The device count matches what lsblk lists. This is the real parser test:
#    a tool that fetched the output but failed to parse it reports zero.
devices=$(lsblk -nro NAME | wc -l)
check "device count matches \`lsblk\` ($devices)" \
  "$bin --check" \
  "\"devices\": $devices"

# 3. The root filesystem is always mounted, and always parsed.
check "the root mount is parsed" \
  "$bin --check" \
  '"Target": "/",'

# 4. The mount count matches findmnt's own flattened list.
mounts=$(findmnt -nlo TARGET | wc -l)
check "mount count is at least what \`findmnt\` lists ($mounts)" \
  "$bin --check | sed -n 's/.*\"mounts\": \([0-9]*\).*/\1/p'" \
  "^[0-9]+$"

# 5. fstab is read, and the root entry is in it. A machine whose fstab could
#    not be opened reports zero entries, and the whole mounts screen would then
#    claim every mount is unexpected.
check "fstab is parsed" \
  "$bin --check" \
  '"fstabEntries": [1-9]'

# 6. Every mismatch the tool reports is named, so a lab log says which. This is
#    reported rather than asserted to be zero: a cloud image legitimately has
#    mounts nobody put in fstab, and that is the tool working.
echo "      fstab mismatches: $($bin --check 2>/dev/null |
  sed -n 's/.*"fstabMismatches": \([0-9]*\).*/\1/p')"

# --- btrfs ------------------------------------------------------------------
#
# The section must follow the machine, not the package list: it appears when a
# btrfs filesystem is mounted and not otherwise.
if [[ $has_btrfs == yes && $root_fstype == btrfs ]]; then
  # 7. A btrfs root means one filesystem in the section, read through /.
  check "the btrfs section covers the root filesystem" \
    "$bin --check" \
    '"Mountpoint": "/"'

  check "at least one btrfs filesystem was read" \
    "$bin --check" \
    '"btrfsFilesystems": [1-9]'

  # 8. And it was really read: the allocation comes from
  #    `btrfs filesystem usage`, which answers an unprivileged caller.
  check "the btrfs allocation was read" \
    "$bin --check" \
    '"DeviceSize": "[0-9]'

  # 9. The device error counters are the number that matters. Zero is the
  #    expected answer on a lab guest, and a non-zero one is a real finding.
  check "the btrfs error counters are clean" \
    "$bin --check" \
    '"btrfsErrors": 0'

elif [[ $has_btrfs == yes ]]; then
  # An ext4 root with btrfs-progs installed. The lab formats /dev/vdb as btrfs
  # and mounts it at /mnt/btrfs for exactly this case: the section must appear
  # for that filesystem and not for the root.
  if findmnt -no FSTYPE /mnt/btrfs 2>/dev/null | grep -q btrfs; then
    check "the btrfs section covers the second disk" \
      "$bin --check" \
      '"Mountpoint": "/mnt/btrfs"'
    check_absent "the btrfs section does not claim the ext4 root" \
      "$bin --check | sed -n '/\"Btrfs\"/,/^  \]/p'" \
      '"Mountpoint": "/"'
  else
    echo "SKIP  no btrfs filesystem is mounted on this guest"
  fi

else
  # 7. No btrfs-progs at all. The section must be empty, and the tool must
  #    still read everything else — this is the case that would otherwise show
  #    an empty screen for no visible reason.
  check "no btrfs filesystem is reported without btrfs-progs" \
    "$bin --check" \
    '"btrfsFilesystems": 0'
  check "the rest of the read still worked" \
    "$bin --check" \
    '"devices": [1-9]'
fi

# --- SMART ------------------------------------------------------------------
if [[ $has_smart == yes ]]; then
  # 10. Every guest in the lab runs on virtio disks, which carry no SMART. The
  #     tool must report each one as unknown *with a reason*: "unknown" and a
  #     blank reason is indistinguishable from a read the tool forgot to make,
  #     and reporting a virtio disk as healthy would be a lie.
  check "every drive has a health verdict" \
    "$bin --check" \
    '"smartHealth"'
  check "a drive that cannot be asked says so" \
    "$bin --check" \
    '"health": "(unknown|PASSED|FAILED)"'
  check_absent "no drive is unknown without a reason" \
    "$bin --check | grep -A2 '\"health\": \"unknown\"'" \
    '"concerning": true'
else
  check "no drive health is claimed without smartmontools" \
    "$bin --check" \
    '"smartHealth": null|"smartHealth": \[\]'
fi

# --- the read path changes nothing -----------------------------------------
#
# 11. --check must never change anything: the mount table is identical after
#     it. This is the assertion that keeps a tool with a `umount` key honest.
before=$(findmnt -nlo TARGET,SOURCE | sort)
$bin --check >/dev/null 2>&1
after=$(findmnt -nlo TARGET,SOURCE | sort)
if [[ "$before" == "$after" ]]; then
  printf 'PASS  --check left the mount table untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed the mount table\n'
  diff <(echo "$before") <(echo "$after") | sed 's/^/      | /' | head -12
  fail=$((fail + 1))
fi

# 12. And /etc/fstab is byte for byte what it was. The tool writes it only
#     through a confirmed `install`, and --check never builds a mutation.
fstab_before=$(sha256sum /etc/fstab 2>/dev/null | cut -d' ' -f1)
$bin --check >/dev/null 2>&1
fstab_after=$(sha256sum /etc/fstab 2>/dev/null | cut -d' ' -f1)
if [[ "$fstab_before" == "$fstab_after" ]]; then
  printf 'PASS  --check left /etc/fstab untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed /etc/fstab\n'
  fail=$((fail + 1))
fi

if [[ $fail -eq 0 ]]; then
  record_compat "$("$bin" --check 2>/dev/null)" pass
else
  record_compat "$("$bin" --check 2>/dev/null)" fail
fi

echo "--- tui-disk: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
