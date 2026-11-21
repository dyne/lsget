#!/bin/sh
set -e

if [ "$(id -u)" -ne 0 ]; then
  printf '%s\n' 'Run this uninstaller as root.' >&2
  exit 1
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

remove_service() {
  service_path=$1
  service_name=${service_path##*/}
  rc-service "$service_name" stop
  for runlevel in /etc/runlevels/*; do
    if [ -e "$runlevel/$service_name" ] || [ -L "$runlevel/$service_name" ]; then
      rc-update del "$service_name" "${runlevel##*/}"
    fi
  done
  rm -f "$service_path"
}

for instance in /etc/init.d/lsget.*; do
  if [ -L "$instance" ] && [ "$(readlink -f "$instance")" = /etc/init.d/lsget ]; then
    remove_service "$instance"
  fi
done
if [ -e /etc/init.d/lsget ]; then
  remove_service /etc/init.d/lsget
fi

if [ ! -L /etc/logrotate.d/lsget ] && cmp -s "$script_dir/lsget.logrotate" /etc/logrotate.d/lsget; then
  rm /etc/logrotate.d/lsget
fi

printf '%s\n' 'Removed the lsget OpenRC service. Configuration, data, logs, binary and user have been kept.'
