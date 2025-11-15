#!/bin/sh
# Uninstallation script for lsget OpenRC service
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if running as root
if [ "$(id -u)" -ne 0 ]; then
  printf "${RED}Error: This script must be run as root${NC}\n"
  exit 1
fi

printf "${YELLOW}Uninstalling lsget OpenRC service...${NC}\n"

# Stop main service if running
if rc-service lsget status >/dev/null 2>&1; then
  printf "Stopping lsget service...\n"
  rc-service lsget stop 2>/dev/null || true
fi

# Remove from default runlevel
if rc-update show default 2>/dev/null | grep -q lsget; then
  printf "Removing lsget from default runlevel...\n"
  rc-update del lsget default 2>/dev/null || true
fi

# Find and stop any symlinked instances
for service in /etc/init.d/lsget.*; do
  if [ -L "$service" ] || [ -f "$service" ]; then
    service_name=$(basename "$service")
    if rc-service "$service_name" status >/dev/null 2>&1; then
      printf "Stopping $service_name...\n"
      rc-service "$service_name" stop 2>/dev/null || true
    fi
    
    # Remove from runlevels
    for runlevel in default boot sysinit shutdown; do
      if rc-update show "$runlevel" 2>/dev/null | grep -q "$service_name"; then
        printf "Removing $service_name from $runlevel runlevel...\n"
        rc-update del "$service_name" "$runlevel" 2>/dev/null || true
      fi
    done
    
    # Remove the symlinked service
    if [ "$service" != "/etc/init.d/lsget" ]; then
      printf "Removing $service...\n"
      rm -f "$service"
    fi
  fi
done

# Remove init script
if [ -f "/etc/init.d/lsget" ]; then
  printf "Removing init script...\n"
  rm -f /etc/init.d/lsget
fi

# Remove logrotate configuration
if [ -f "/etc/logrotate.d/lsget" ]; then
  printf "Removing logrotate configuration...\n"
  rm -f /etc/logrotate.d/lsget
fi

printf "${GREEN}Uninstallation complete!${NC}\n"
printf "\n"
printf "${YELLOW}Note: The following items were NOT removed:${NC}\n"
printf "  - Binary: /usr/local/bin/lsget\n"
printf "  - Configuration: /etc/conf.d/lsget*\n"
printf "  - Data directory: /var/lib/lsget\n"
printf "  - Log directory: /var/log/lsget\n"
printf "  - User: lsget\n"
printf "\n"
printf "To remove these manually:\n"
printf "  rm /usr/local/bin/lsget\n"
printf "  rm /etc/conf.d/lsget*\n"
printf "  rm -rf /var/lib/lsget\n"
printf "  rm -rf /var/log/lsget\n"
printf "  deluser lsget 2>/dev/null || userdel lsget\n"
