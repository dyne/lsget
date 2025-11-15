#!/bin/bash
# Installation script for lsget systemd service
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

echo -e "${GREEN}Installing lsget systemd service...${NC}"

# Check if lsget binary exists
if [ ! -f "/usr/local/bin/lsget" ]; then
  echo -e "${YELLOW}Warning: /usr/local/bin/lsget not found${NC}"
  echo "Please install the lsget binary first:"
  echo "  sudo cp lsget /usr/local/bin/"
  echo "  sudo chmod +x /usr/local/bin/lsget"
  exit 1
fi

# Create lsget user and group if they don't exist
if ! id -u lsget >/dev/null 2>&1; then
  echo "Creating lsget user and group..."
  useradd --system --no-create-home --shell /usr/sbin/nologin lsget
else
  echo "User 'lsget' already exists"
fi

# Create required directories
echo "Creating directories..."
mkdir -p /var/lib/lsget
mkdir -p /var/log/lsget
mkdir -p /etc/default

# Set ownership
chown lsget:lsget /var/lib/lsget
chown lsget:lsget /var/log/lsget

# Determine config directory (Debian style vs RHEL style)
if [ -d "/etc/default" ]; then
  CONFIG_DIR="/etc/default"
elif [ -d "/etc/sysconfig" ]; then
  CONFIG_DIR="/etc/sysconfig"
else
  # Create /etc/default if neither exists
  mkdir -p /etc/default
  CONFIG_DIR="/etc/default"
fi

# Install default configuration if it doesn't exist
if [ ! -f "${CONFIG_DIR}/lsget" ]; then
  echo "Installing default configuration to ${CONFIG_DIR}/lsget..."
  cp lsget.default ${CONFIG_DIR}/lsget
  chmod 644 ${CONFIG_DIR}/lsget
  echo -e "${YELLOW}Note: Please edit ${CONFIG_DIR}/lsget to configure your settings${NC}"
else
  echo -e "${YELLOW}Configuration file ${CONFIG_DIR}/lsget already exists, skipping...${NC}"
  echo "To update, manually copy: cp lsget.default ${CONFIG_DIR}/lsget"
fi

# Install systemd service files
echo "Installing systemd service files..."
cp lsget.service /etc/systemd/system/
cp lsget@.service /etc/systemd/system/

# Set correct permissions
chmod 644 /etc/systemd/system/lsget.service
chmod 644 /etc/systemd/system/lsget@.service
chmod 644 /etc/default/lsget

# Reload systemd
echo "Reloading systemd..."
systemctl daemon-reload

echo -e "${GREEN}Installation complete!${NC}"
echo ""
echo "Next steps:"
echo "  1. Edit configuration: sudo nano ${CONFIG_DIR}/lsget"
echo "  2. Enable service: sudo systemctl enable lsget.service"
echo "  3. Start service: sudo systemctl start lsget.service"
echo "  4. Check status: sudo systemctl status lsget.service"
echo ""
echo "For multiple instances:"
echo "  1. Create config: sudo cp ${CONFIG_DIR}/lsget ${CONFIG_DIR}/lsget-instance1"
echo "  2. Edit config: sudo nano ${CONFIG_DIR}/lsget-instance1"
echo "  3. Enable instance: sudo systemctl enable lsget@instance1.service"
echo "  4. Start instance: sudo systemctl start lsget@instance1.service"
