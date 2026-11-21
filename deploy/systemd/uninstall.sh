#!/bin/bash
set -e

if [ "$EUID" -ne 0 ]; then
  printf '%s\n' 'Run this uninstaller as root.' >&2
  exit 1
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
running_units=$(systemctl list-units --plain --no-legend --no-pager 'lsget.service' 'lsget@*.service')
while read -r unit _; do
  case "$unit" in
    lsget.service|lsget@?*.service) systemctl stop "$unit" ;;
  esac
done <<< "$running_units"

installed_units=()
for unit in lsget.service lsget@.service; do
  if [ -f "/etc/systemd/system/$unit" ]; then
    installed_units+=("$unit")
  fi
done
if ((${#installed_units[@]})); then
  # Disabling the template also removes links for instances that never started.
  systemctl --no-reload disable "${installed_units[@]}"
  systemctl --runtime --no-reload disable "${installed_units[@]}"
fi

rm -f /etc/systemd/system/lsget.service /etc/systemd/system/lsget@.service
if [ ! -L /etc/logrotate.d/lsget ] && cmp -s "$script_dir/lsget.logrotate" /etc/logrotate.d/lsget; then
  rm /etc/logrotate.d/lsget
fi
systemctl daemon-reload

printf '%s\n' 'Removed the lsget systemd units. Configuration, data, logs, binary and user have been kept.'
