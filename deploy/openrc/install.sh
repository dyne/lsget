#!/bin/sh
set -e

if [ "$(id -u)" -ne 0 ]; then
  printf '%s\n' 'Run this installer as root.' >&2
  exit 1
fi

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
if [ ! -x /usr/local/bin/lsget ]; then
  printf '%s\n' 'Install the executable lsget to /usr/local/bin/lsget first.' >&2
  exit 1
fi

if ! id -u lsget >/dev/null 2>&1; then
  login_shell=/sbin/nologin
  if [ ! -x "$login_shell" ]; then
    login_shell=/bin/false
  fi
  if command -v useradd >/dev/null 2>&1; then
    if ! getent group lsget >/dev/null; then
      groupadd --system lsget
    fi
    useradd --system --no-create-home --home-dir /var/lib/lsget --shell "$login_shell" --gid lsget lsget
  else
    if ! getent group lsget >/dev/null; then
      addgroup -S lsget
    fi
    adduser -S -D -H -h /var/lib/lsget -s "$login_shell" -G lsget lsget
  fi
fi

service_group=$(id -gn lsget)
for directory in /var/lib/lsget /var/log/lsget; do
  if [ ! -d "$directory" ]; then
    install -d -m 0750 -o lsget -g "$service_group" "$directory"
  fi
done

install -m 0755 "$script_dir/lsget" /etc/init.d/lsget
if [ ! -e /etc/conf.d/lsget ] && [ ! -L /etc/conf.d/lsget ]; then
  mkdir -p /etc/conf.d
  install -m 0644 "$script_dir/lsget.confd" /etc/conf.d/lsget
fi

if command -v logrotate >/dev/null 2>&1 && [ ! -e /etc/logrotate.d/lsget ] && [ ! -L /etc/logrotate.d/lsget ]; then
  mkdir -p /etc/logrotate.d
  install -m 0644 "$script_dir/lsget.logrotate" /etc/logrotate.d/lsget
fi

printf '%s\n' 'Edit /etc/conf.d/lsget, then run rc-service lsget start.'
printf '%s\n' 'To enable it at boot, run rc-update add lsget default.'
