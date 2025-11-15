# lsget systemd Service Deployment

This directory contains systemd service files and installation scripts for deploying lsget as a system service.

## Files

- **lsget.service** - Main systemd service unit file for single instance
- **lsget@.service** - Template systemd service unit file for multiple instances
- **lsget.default** - Default configuration file (installs to `/etc/default/lsget`)
- **lsget.logrotate** - Logrotate configuration (installs to `/etc/logrotate.d/lsget`)
- **install.sh** - Installation script
- **uninstall.sh** - Uninstallation script

## Installation

### Prerequisites

1. Build or download the lsget binary:
   ```bash
   go build -o lsget .
   ```

2. Install the binary system-wide:
   ```bash
   sudo cp lsget /usr/local/bin/
   sudo chmod +x /usr/local/bin/lsget
   ```

### Quick Install

Run the installation script:

```bash
cd deploy/systemd
sudo bash install.sh
```

The script will:
- Create a `lsget` system user and group
- Create required directories (`/var/lib/lsget`, `/var/log/lsget`)
- Install configuration to `/etc/default/lsget`
- Install systemd service files
- Reload systemd

### Manual Installation

If you prefer to install manually:

```bash
# Create user and group
sudo useradd --system --no-create-home --shell /usr/sbin/nologin lsget

# Create directories
sudo mkdir -p /var/lib/lsget /var/log/lsget /etc/default
sudo chown lsget:lsget /var/lib/lsget /var/log/lsget

# Copy files
sudo cp lsget.default /etc/default/lsget
sudo cp lsget.service /etc/systemd/system/
sudo cp lsget@.service /etc/systemd/system/

# Set permissions
sudo chmod 644 /etc/systemd/system/lsget*.service
sudo chmod 644 /etc/default/lsget

# Reload systemd
sudo systemctl daemon-reload
```

## Configuration

Edit the configuration file to customize settings:

```bash
# Debian/Ubuntu style
sudo nano /etc/default/lsget

# Or RHEL/Fedora style
sudo nano /etc/sysconfig/lsget
```

**Note**: The service automatically looks for configuration in both `/etc/default/lsget` (Debian/Ubuntu style) and `/etc/sysconfig/lsget` (RHEL/Fedora style). The install script creates the file in the appropriate location for your system.

### Configuration Options

| Variable | Default | Description |
|----------|---------|-------------|
| `LSGET_ADDR` | `127.0.0.1:8037` | Listen address (IP:PORT) |
| `LSGET_DIR` | `/srv/ftp` | Root directory to serve |
| `LSGET_CATMAX` | `262144` | Max bytes for `cat` command (256 KiB) |
| `LSGET_PIDFILE` | `/var/lib/lsget/lsget.pid` | PID file location |
| `LSGET_LOGFILE` | `/var/log/lsget/access.log` | Log file for statistics |

### Important Notes

1. **Directory Permissions**: The `LSGET_DIR` must be readable by the `lsget` user:
   ```bash
   sudo chmod -R o+rX /srv/ftp
   # or add lsget user to appropriate group
   sudo usermod -a -G ftp lsget
   ```

2. **SELinux**: If running on a system with SELinux, you may need to adjust policies:
   ```bash
   sudo semanage fcontext -a -t httpd_sys_content_t "/srv/ftp(/.*)?"
   sudo restorecon -R /srv/ftp
   ```

## Usage

### Single Instance

Start and enable the service:

```bash
# Enable on boot
sudo systemctl enable lsget.service

# Start the service
sudo systemctl start lsget.service

# Check status
sudo systemctl status lsget.service

# View logs
sudo journalctl -u lsget.service -f
```

### Multiple Instances

For running multiple instances with different configurations:

1. Create instance-specific configuration:
   ```bash
   sudo cp /etc/default/lsget /etc/default/lsget-docs
   sudo nano /etc/default/lsget-docs
   ```

2. Update configuration (example for docs instance):
   ```bash
   LSGET_ADDR="127.0.0.1:8038"
   LSGET_DIR="/srv/docs"
   LSGET_PIDFILE="/var/lib/lsget/lsget-docs.pid"
   LSGET_LOGFILE="/var/log/lsget/access-docs.log"
   ```

3. Enable and start the instance:
   ```bash
   sudo systemctl enable lsget@docs.service
   sudo systemctl start lsget@docs.service
   sudo systemctl status lsget@docs.service
   ```

### Service Management Commands

```bash
# Start service
sudo systemctl start lsget.service

# Stop service
sudo systemctl stop lsget.service

# Restart service
sudo systemctl restart lsget.service

# Reload configuration (if supported)
sudo systemctl reload lsget.service

# Enable on boot
sudo systemctl enable lsget.service

# Disable on boot
sudo systemctl disable lsget.service

# Check status
sudo systemctl status lsget.service

# View logs (real-time)
sudo journalctl -u lsget.service -f

# View logs (last 100 lines)
sudo journalctl -u lsget.service -n 100

# View logs since boot
sudo journalctl -u lsget.service -b
```

## Nginx Reverse Proxy

To expose lsget to the internet, use a reverse proxy like nginx:

```nginx
server {
    listen 80;
    server_name files.example.com;

    location / {
        proxy_pass http://127.0.0.1:8037;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

For HTTPS with Let's Encrypt:

```bash
sudo certbot --nginx -d files.example.com
```

## Security Hardening

The service file includes several security hardening measures:

- Runs as unprivileged `lsget` user
- `NoNewPrivileges=true` - Prevents privilege escalation
- `PrivateTmp=true` - Private /tmp directory
- `ProtectSystem=strict` - Read-only /usr, /boot, /efi
- `ProtectHome=true` - Makes /home inaccessible
- `RestrictAddressFamilies` - Limits network protocols
- `LimitNOFILE=65536` - File descriptor limits

### Additional Recommendations

1. Use firewall to restrict access:
   ```bash
   sudo ufw allow from 127.0.0.1 to any port 8037
   ```

2. Create `.lsgetignore` files to hide sensitive data
3. Regularly rotate logs:
   ```bash
   sudo logrotate /etc/logrotate.d/lsget
   ```

## Troubleshooting

### Service won't start

Check logs:
```bash
sudo journalctl -u lsget.service -n 50 --no-pager
```

Common issues:
1. Binary not found: Ensure `/usr/local/bin/lsget` exists and is executable
2. Permission denied: Check that `lsget` user can read `LSGET_DIR`
3. Port in use: Check if another service is using the port
4. Invalid configuration: Verify `/etc/default/lsget` syntax

### Permission errors

Ensure the lsget user has read access:
```bash
sudo -u lsget ls /srv/ftp
```

### Check configuration

Verify the configuration is being loaded:
```bash
sudo systemctl show lsget.service | grep EnvironmentFile
```

## Uninstallation

Run the uninstallation script:

```bash
cd deploy/systemd
sudo bash uninstall.sh
```

This will:
- Stop and disable all lsget services
- Remove systemd service files
- Reload systemd

Note: Configuration files, data, and the binary are preserved. Remove manually if needed.

## Log Rotation

Create `/etc/logrotate.d/lsget`:

```
/var/log/lsget/*.log {
    daily
    missingok
    rotate 14
    compress
    delaycompress
    notifempty
    create 0640 lsget lsget
    sharedscripts
    postrotate
        systemctl reload lsget.service > /dev/null 2>&1 || true
    endscript
}
```

## Support

- GitHub: https://github.com/dyne/lsget
- Issues: https://github.com/dyne/lsget/issues
- Website: https://dyne.org
