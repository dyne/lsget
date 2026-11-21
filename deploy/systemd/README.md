# lsget systemd Service Deployment

This directory contains systemd service files and installation scripts for deploying lsget as a system service.

## Files

- **lsget.service** - Main systemd service unit file for single instance
- **lsget@.service** - Template systemd service unit file for multiple instances
- **lsget.default** - Default configuration file for `/etc/default` or `/etc/sysconfig`
- **lsget.logrotate** - Logrotate configuration (installs to `/etc/logrotate.d/lsget`)
- **install.sh** - Installation script
- **uninstall.sh** - Uninstallation script

## Installation

### Prerequisites

1. Build or download the lsget binary:
   ```bash
   go build -o lsget ./cmd/lsget
   ```

2. Install the binary system-wide:
   ```bash
   sudo cp lsget /usr/local/bin/
   sudo chmod +x /usr/local/bin/lsget
   ```

### Quick Install

From the checkout root, run the installer. From another directory, use its absolute path.

```bash
sudo bash deploy/systemd/install.sh
```

The script will:
- Create a `lsget` system user and group
- Create required directories (`/var/lib/lsget`, `/var/log/lsget`)
- Install configuration at the path printed at the end of the run
- Install systemd service files and the logrotate recipe if logrotate is available
- Reload systemd

Rerun the installer to update the units. It keeps existing environment files, logrotate configuration and state directories. Put systemd overrides in a drop-in created with `systemctl edit` so they survive unit replacement. Restart the affected services after updating their configuration or binary.

### Manual Installation

For a fresh manual installation, run these commands from `deploy/systemd`.

```bash
# Create user and group
sudo useradd --system --user-group --no-create-home --home-dir /var/lib/lsget --shell /usr/sbin/nologin lsget

# Create directories
sudo install -d -m 0750 -o lsget -g lsget /var/lib/lsget /var/log/lsget
sudo mkdir -p /etc/default

# Copy files
sudo install -m 0644 lsget.default /etc/default/lsget
sudo install -m 0644 lsget.service lsget@.service /etc/systemd/system/

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

If both files exist, settings in `/etc/sysconfig/lsget` take precedence over settings in `/etc/default/lsget`. A fresh install uses `/etc/sysconfig` where that directory exists and `/etc/default` elsewhere.

### Configuration Options

| Variable | Default | Description |
|----------|---------|-------------|
| `LSGET_ADDR` | `127.0.0.1:8037` | Listen address (IP:PORT) |
| `LSGET_DIR` | `/srv/ftp` | Root directory to serve |
| `LSGET_CATMAX` | `4096` | Max bytes for `cat` and completion |
| `LSGET_PID` | Unset | Optional application PID file |
| `LSGET_LOGFILE` | `/var/log/lsget/access.log` | Access log, or an empty value to disable it |

systemd tracks each service process. If an existing configuration sets `LSGET_PIDFILE`, the service uses that path when `LSGET_PID` is absent. Use a unique path under `/var/lib/lsget` for each application PID file you configure. If an abrupt exit leaves that file behind, verify the previous process has exited before removing the stale file.

### Important Notes

1. The `lsget` user needs read access to the served files and traversal permission on their directories. The content tree can be mounted read-only. Give `lsget` write access where it stores state or logs, under `/var/lib/lsget` and `/var/log/lsget`.
   ```bash
   sudo -u lsget ls /srv/ftp
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

1. Create an instance-specific configuration. On systems using `/etc/sysconfig`, place it at `/etc/sysconfig/lsget-docs`.
   ```bash
   sudo nano /etc/default/lsget-docs
   ```

2. Update configuration (example for docs instance):
   ```bash
   LSGET_ADDR="127.0.0.1:8038"
   LSGET_DIR="/srv/docs"
   ```

   This instance writes `/var/log/lsget/access-docs.log` by default.

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

# Restart after changing the configuration
sudo systemctl restart lsget.service

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
- `ProtectSystem=strict` - Read-only filesystem view with writable state and log paths
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
sudo systemctl show lsget.service --property=EnvironmentFiles
```

## Uninstallation

Run the uninstallation script:

```bash
sudo bash deploy/systemd/uninstall.sh
```

This will:
- Stop lsget services and disable enabled instances
- Remove systemd service files
- Remove the bundled logrotate configuration if it is unchanged
- Reload systemd

Configuration files, customized logrotate configuration, content, state, logs, the binary and the service account are preserved.

## Log Rotation

The [bundled logrotate recipe](lsget.logrotate) rotates file access logs. lsget reopens the access log on each request. If upgrading an older rotation setup, compare its `postrotate` hook with the bundled recipe.

## Support

- GitHub: https://github.com/dyne/lsget
- Issues: https://github.com/dyne/lsget/issues
- Website: https://dyne.org
