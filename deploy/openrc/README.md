# lsget OpenRC Service Deployment

This directory contains OpenRC init scripts and installation files for deploying lsget as a system service on Alpine Linux, Gentoo, and other OpenRC-based distributions.

## Files

- **lsget** - OpenRC init script (`/etc/init.d/lsget`)
- **lsget.confd** - Default configuration file (`/etc/conf.d/lsget`)
- **install.sh** - Installation script
- **uninstall.sh** - Uninstallation script
- **lsget.logrotate** - Logrotate configuration

## Installation

### Prerequisites

1. Build or download the lsget binary:
   ```sh
   go build -o lsget .
   ```

2. Install the binary system-wide:
   ```sh
   cp lsget /usr/local/bin/
   chmod +x /usr/local/bin/lsget
   ```

### Quick Install

Run the installation script:

```sh
cd deploy/openrc
sh install.sh
```

The script will:
- Create a `lsget` system user and group
- Create required directories (`/var/lib/lsget`, `/var/log/lsget`)
- Install init script to `/etc/init.d/lsget`
- Install configuration to `/etc/conf.d/lsget`
- Install logrotate configuration (if logrotate is available)

### Manual Installation

If you prefer to install manually:

```sh
# Create user and group (Alpine/BusyBox)
adduser -S -D -H -h /var/lib/lsget -s /sbin/nologin -G lsget lsget

# Or for other systems
useradd --system --no-create-home --shell /sbin/nologin --user-group lsget

# Create directories
mkdir -p /var/lib/lsget /var/log/lsget /etc/conf.d
chown lsget:lsget /var/lib/lsget /var/log/lsget

# Copy files
cp lsget /etc/init.d/lsget
chmod 755 /etc/init.d/lsget
cp lsget.confd /etc/conf.d/lsget
chmod 644 /etc/conf.d/lsget

# Install logrotate (optional)
cp lsget.logrotate /etc/logrotate.d/lsget
chmod 644 /etc/logrotate.d/lsget
```

## Configuration

Edit the configuration file to customize settings:

```sh
vi /etc/conf.d/lsget
```

### Configuration Options

| Variable | Default | Description |
|----------|---------|-------------|
| `LSGET_ADDR` | `127.0.0.1:8037` | Listen address (IP:PORT) |
| `LSGET_DIR` | `/srv/ftp` | Root directory to serve |
| `LSGET_CATMAX` | `262144` | Max bytes for `cat` command (256 KiB) |
| `LSGET_LOGFILE` | `/var/log/lsget/lsget.log` | Log file for statistics |

### Important Notes

1. **Directory Permissions**: The `LSGET_DIR` must be readable by the `lsget` user:
   ```sh
   chmod -R o+rX /srv/ftp
   # or add lsget user to appropriate group
   addgroup lsget ftp
   ```

2. **SELinux**: If running on a system with SELinux (rare for OpenRC), adjust policies accordingly.

## Usage

### Single Instance

Start and enable the service:

```sh
# Enable on boot
rc-update add lsget default

# Start the service
rc-service lsget start

# Check status
rc-service lsget status

# View logs
tail -f /var/log/lsget/lsget.log
tail -f /var/log/lsget/lsget.out
tail -f /var/log/lsget/lsget.err
```

### Multiple Instances

OpenRC supports multiple instances through symbolic links:

1. Create a symlink for your instance:
   ```sh
   ln -s /etc/init.d/lsget /etc/init.d/lsget.docs
   ```

2. Create instance-specific configuration:
   ```sh
   cp /etc/conf.d/lsget /etc/conf.d/lsget.docs
   vi /etc/conf.d/lsget.docs
   ```

3. Update configuration (example for docs instance):
   ```sh
   LSGET_ADDR="127.0.0.1:8038"
   LSGET_DIR="/srv/docs"
   LSGET_LOGFILE="/var/log/lsget/lsget-docs.log"
   ```

4. Enable and start the instance:
   ```sh
   rc-update add lsget.docs default
   rc-service lsget.docs start
   rc-service lsget.docs status
   ```

### Service Management Commands

```sh
# Start service
rc-service lsget start

# Stop service
rc-service lsget stop

# Restart service
rc-service lsget restart

# Reload configuration
rc-service lsget reload

# Check status
rc-service lsget status

# Enable on boot
rc-update add lsget default

# Disable on boot
rc-update del lsget default

# List all services
rc-status

# List services in default runlevel
rc-update show default
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

For HTTPS with Let's Encrypt on Alpine:

```sh
apk add certbot certbot-nginx
certbot --nginx -d files.example.com
```

## Alpine Linux Specific

### Installing Dependencies

```sh
# Install Go (if building from source)
apk add go

# Install nginx (for reverse proxy)
apk add nginx

# Install logrotate (for log management)
apk add logrotate

# Enable services
rc-update add nginx default
rc-update add crond default  # Required for logrotate
```

### Firewall (awall)

If using Alpine Wall (awall), create `/etc/awall/optional/lsget.json`:

```json
{
  "description": "lsget file server",
  "filter": [
    {
      "in": "eth0",
      "out": "_fw",
      "service": { "proto": "tcp", "port": 8037 },
      "action": "accept",
      "conn-limit": { "count": 100, "interval": 60 }
    }
  ]
}
```

Enable the policy:
```sh
awall enable lsget
awall activate
```

## Gentoo Specific

### Installing Dependencies

```sh
# Install Go
emerge dev-lang/go

# Install nginx
emerge www-servers/nginx

# Enable services
rc-update add lsget default
rc-update add nginx default
```

### Firewall (iptables)

```sh
# Allow local connections only
iptables -A INPUT -i lo -p tcp --dport 8037 -j ACCEPT
iptables -A INPUT -p tcp --dport 8037 -j DROP

# Save rules
/etc/init.d/iptables save
```

## Runlevels

OpenRC uses different runlevels:

- **default** - Normal system operation (most common)
- **boot** - System boot
- **sysinit** - System initialization
- **shutdown** - System shutdown

Add lsget to desired runlevel:
```sh
rc-update add lsget default
```

## Logging

Logs are written to:
- `/var/log/lsget/lsget.log` - Access log (statistics)
- `/var/log/lsget/lsget.out` - Standard output
- `/var/log/lsget/lsget.err` - Standard error

View logs in real-time:
```sh
tail -f /var/log/lsget/lsget.log
```

## Troubleshooting

### Service won't start

Check status and logs:
```sh
rc-service lsget status
tail -n 50 /var/log/lsget/lsget.err
```

Common issues:
1. **Binary not found**: Ensure `/usr/local/bin/lsget` exists and is executable
2. **Permission denied**: Check that `lsget` user can read `LSGET_DIR`
3. **Port in use**: Check if another service is using the port
   ```sh
   netstat -tulpn | grep 8037
   ```
4. **Invalid configuration**: Verify `/etc/conf.d/lsget` syntax

### Permission errors

Test if lsget user can access the directory:
```sh
su -s /bin/sh lsget -c "ls /srv/ftp"
```

### Check which services are running

```sh
rc-status
```

### Verbose startup

Run the service in foreground for debugging:
```sh
/etc/init.d/lsget start --verbose
```

## Uninstallation

Run the uninstallation script:

```sh
cd deploy/openrc
sh uninstall.sh
```

This will:
- Stop and remove all lsget services
- Remove init scripts and symlinks
- Remove logrotate configuration

Note: Configuration files, data, and the binary are preserved. Remove manually if needed.

## Dependencies

The init script has the following dependencies:
- **need net** - Requires networking
- **use dns** - Optional DNS resolution
- **use logger** - Optional system logger
- **after firewall** - Start after firewall if present

These are automatically managed by OpenRC.

## Comparison with systemd

| Feature | OpenRC | systemd |
|---------|--------|---------|
| Config location | `/etc/conf.d/` | `/etc/default/` or `/etc/sysconfig/` |
| Init script | `/etc/init.d/` | `/etc/systemd/system/` |
| Multiple instances | Symlinks | Template units (@) |
| Enable service | `rc-update add` | `systemctl enable` |
| Start service | `rc-service start` | `systemctl start` |
| View logs | `/var/log/` files | `journalctl` |

## Support

- GitHub: https://github.com/dyne/lsget
- Issues: https://github.com/dyne/lsget/issues
- Website: https://dyne.org

## Alpine Linux Resources

- OpenRC documentation: https://wiki.gentoo.org/wiki/OpenRC
- Alpine Linux wiki: https://wiki.alpinelinux.org/
- Service management: https://wiki.alpinelinux.org/wiki/Alpine_Linux_Init_System
