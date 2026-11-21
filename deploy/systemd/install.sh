#!/bin/bash
set -e

if [ "$EUID" -ne 0 ]; then
  printf '%s\n' 'Run this installer as root.' >&2
  exit 1
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [ ! -x /usr/local/bin/lsget ]; then
  printf '%s\n' 'Install the executable lsget to /usr/local/bin/lsget first.' >&2
  exit 1
fi

if ! id -u lsget >/dev/null 2>&1; then
  if ! getent group lsget >/dev/null; then
    groupadd --system lsget
  fi
  useradd --system --no-create-home --home-dir /var/lib/lsget --shell /usr/sbin/nologin --gid lsget lsget
fi

service_group=$(id -gn lsget)
for directory in /var/lib/lsget /var/log/lsget; do
  if [ ! -d "$directory" ]; then
    install -d -m 0750 -o lsget -g "$service_group" "$directory"
  fi
done

if [[ -e /etc/sysconfig/lsget || -L /etc/sysconfig/lsget ]]; then
  config=/etc/sysconfig/lsget
elif [[ -e /etc/default/lsget || -L /etc/default/lsget ]]; then
  config=/etc/default/lsget
elif [[ -d /etc/sysconfig ]]; then
  config=/etc/sysconfig/lsget
else
  config=/etc/default/lsget
fi
if [[ ! -e $config && ! -L $config ]]; then
  mkdir -p "${config%/*}"
  install -m 0644 "$script_dir/lsget.default" "$config"
fi

install -m 0644 "$script_dir/lsget.service" "$script_dir/lsget@.service" /etc/systemd/system/
if command -v logrotate >/dev/null 2>&1 && [ ! -e /etc/logrotate.d/lsget ] && [ ! -L /etc/logrotate.d/lsget ]; then
  mkdir -p /etc/logrotate.d
  install -m 0644 "$script_dir/lsget.logrotate" /etc/logrotate.d/lsget
fi

systemctl daemon-reload

printf 'Edit %s, then run systemctl start lsget.service.\n' "$config"
printf '%s\n' 'To enable it at boot, run systemctl enable lsget.service.'
