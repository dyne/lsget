#!/bin/sh
# Installation script for lsget OpenRC service
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

printf "${GREEN}Installing lsget OpenRC service...${NC}\n"

# Check if lsget binary exists
if [ ! -f "/usr/local/bin/lsget" ]; then
  printf "${YELLOW}Warning: /usr/local/bin/lsget not found${NC}\n"
  printf "Please install the lsget binary first:\n"
  printf "  cp lsget /usr/local/bin/\n"
  printf "  chmod +x /usr/local/bin/lsget\n"
  exit 1
fi

# Create lsget user and group if they don't exist
if ! id lsget >/dev/null 2>&1; then
  printf "Creating lsget user and group...\n"
  adduser -S -D -H -h /var/lib/lsget -s /sbin/nologin -G lsget lsget 2>/dev/null || \
  adduser --system --no-create-home --home /var/lib/lsget --shell /sbin/nologin --ingroup lsget lsget 2>/dev/null || \
  useradd --system --no-create-home --home-dir /var/lib/lsget --shell /sbin/nologin --user-group lsget
else
  printf "User 'lsget' already exists\n"
fi

# Create required directories
printf "Creating directories...\n"
mkdir -p /var/lib/lsget
mkdir -p /var/log/lsget
mkdir -p /etc/conf.d

# Set ownership
chown lsget:lsget /var/lib/lsget
chown lsget:lsget /var/log/lsget

# Install init script
printf "Installing init script...\n"
cp lsget /etc/init.d/lsget
chmod 755 /etc/init.d/lsget

# Install default configuration if it doesn't exist
if [ ! -f "/etc/conf.d/lsget" ]; then
  printf "Installing default configuration...\n"
  cp lsget.confd /etc/conf.d/lsget
  chmod 644 /etc/conf.d/lsget
  printf "${YELLOW}Note: Please edit /etc/conf.d/lsget to configure your settings${NC}\n"
else
  printf "${YELLOW}Configuration file /etc/conf.d/lsget already exists, skipping...${NC}\n"
  printf "To update, manually copy: cp lsget.confd /etc/conf.d/lsget\n"
fi

# Install logrotate configuration if logrotate is available
if command -v logrotate >/dev/null 2>&1; then
  printf "Installing logrotate configuration...\n"
  if [ -f "lsget.logrotate" ]; then
    cp lsget.logrotate /etc/logrotate.d/lsget
    chmod 644 /etc/logrotate.d/lsget
  fi
fi

printf "${GREEN}Installation complete!${NC}\n"
printf "\n"
printf "Next steps:\n"
printf "  1. Edit configuration: vi /etc/conf.d/lsget\n"
printf "  2. Enable service: rc-update add lsget default\n"
printf "  3. Start service: rc-service lsget start\n"
printf "  4. Check status: rc-service lsget status\n"
printf "\n"
printf "For multiple instances:\n"
printf "  1. Create symlink: ln -s /etc/init.d/lsget /etc/init.d/lsget.docs\n"
printf "  2. Create config: cp /etc/conf.d/lsget /etc/conf.d/lsget.docs\n"
printf "  3. Edit config: vi /etc/conf.d/lsget.docs\n"
printf "  4. Enable instance: rc-update add lsget.docs default\n"
printf "  5. Start instance: rc-service lsget.docs start\n"
