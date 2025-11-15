#!/bin/bash
# Uninstallation script for lsget systemd service
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if running as root
if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}Error: This script must be run as root${NC}"
  exit 1
fi

echo -e "${YELLOW}Uninstalling lsget systemd service...${NC}"

# Stop and disable main service
if systemctl is-active --quiet lsget.service; then
  echo "Stopping lsget.service..."
  systemctl stop lsget.service
fi

if systemctl is-enabled --quiet lsget.service 2>/dev/null; then
  echo "Disabling lsget.service..."
  systemctl disable lsget.service
fi

# Stop and disable any templated instances
for instance in /etc/systemd/system/multi-user.target.wants/lsget@*.service; do
  if [ -f "$instance" ]; then
    instance_name=$(basename "$instance")
    echo "Stopping $instance_name..."
    systemctl stop "$instance_name" 2>/dev/null || true
    systemctl disable "$instance_name" 2>/dev/null || true
  fi
done

# Remove systemd service files
echo "Removing systemd service files..."
rm -f /etc/systemd/system/lsget.service
rm -f /etc/systemd/system/lsget@.service

# Reload systemd
echo "Reloading systemd..."
systemctl daemon-reload
systemctl reset-failed

echo -e "${GREEN}Uninstallation complete!${NC}"
echo ""
echo -e "${YELLOW}Note: The following items were NOT removed:${NC}"
echo "  - Binary: /usr/local/bin/lsget"
echo "  - Configuration: /etc/default/lsget*"
echo "  - Data directory: /var/lib/lsget"
echo "  - Log directory: /var/log/lsget"
echo "  - User: lsget"
echo ""
echo "To remove these manually:"
echo "  sudo rm /usr/local/bin/lsget"
echo "  sudo rm /etc/default/lsget*"
echo "  sudo rm -rf /var/lib/lsget"
echo "  sudo rm -rf /var/log/lsget"
echo "  sudo userdel lsget"
