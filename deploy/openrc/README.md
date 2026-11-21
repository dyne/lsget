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

From the checkout root, run the installer as root. From another directory, use its absolute path.

```sh
sh deploy/openrc/install.sh
```

The script will:
- Create a `lsget` system user and group
- Create required directories (`/var/lib/lsget`, `/var/log/lsget`)
- Install init script to `/etc/init.d/lsget`
- Install configuration to `/etc/conf.d/lsget`
- Install logrotate configuration (if logrotate is available)

Rerun the installer to update the service script. It keeps existing configuration, named instance links and state directories. Restart the affected services after updating their configuration or binary.

### Manual Installation

For a fresh manual installation, run these commands as root from `deploy/openrc`.

```sh
# Create user and group (Alpine/BusyBox)
addgroup -S lsget
adduser -S -D -H -h /var/lib/lsget -s /sbin/nologin -G lsget lsget

# Or for other systems
useradd --system --no-create-home --shell /sbin/nologin --user-group lsget

# Create directories
install -d -m 0750 -o lsget -g lsget /var/lib/lsget /var/log/lsget
mkdir -p /etc/conf.d

# Copy files
install -m 0755 lsget /etc/init.d/lsget
install -m 0644 lsget.confd /etc/conf.d/lsget

# Install logrotate (optional)
mkdir -p /etc/logrotate.d
install -m 0644 lsget.logrotate /etc/logrotate.d/lsget
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
| `LSGET_CATMAX` | `4096` | Max bytes for `cat` and completion |
| `LSGET_PID` | Unset | Optional application PID file |
| `LSGET_LOGFILE` | `/var/log/lsget/lsget.log` | Access log, or an empty value to disable it |

OpenRC tracks its supervisor in `/run/lsget`. If you set `LSGET_PID`, use a separate path under `/var/lib/lsget` for each service. If an abrupt exit leaves an application PID file behind, verify the previous process has exited before removing the stale file.

### Important Notes

1. The `lsget` user needs read access to the served files and traversal permission on their directories. The content tree can be mounted read-only. Give `lsget` write access where it stores state or logs, under `/var/lib/lsget` and `/var/log/lsget`.
   ```sh
   su -s /bin/sh -c 'ls /srv/ftp' lsget
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
   ln -s lsget /etc/init.d/lsget.docs
   ```

2. Create instance-specific configuration:
   ```sh
   vi /etc/conf.d/lsget.docs
   ```

3. Update configuration (example for docs instance):
   ```sh
   LSGET_ADDR="127.0.0.1:8038"
   LSGET_DIR="/srv/docs"
   ```

   OpenRC reads the main configuration first, then applies the instance file. If the main configuration explicitly sets `LSGET_LOGFILE` or `LSGET_PID`, give each instance a separate path in its own file. Otherwise the `docs` instance writes `/var/log/lsget/lsget.docs.log` by default.

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

# Restart after changing the configuration
rc-service lsget restart

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

Named instances use their own service name as the filename prefix. The [bundled logrotate recipe](lsget.logrotate) rotates access logs and copies and truncates open stdout and stderr logs. If upgrading an older rotation setup, compare its `postrotate` hook with the bundled recipe.

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
su -s /bin/sh -c 'ls /srv/ftp' lsget
```

### Check which services are running

```sh
rc-status
```

### Verbose startup

Show detailed OpenRC startup messages:
```sh
rc-service --debug lsget start
```

## Uninstallation

Run the uninstallation script:

```sh
sh deploy/openrc/uninstall.sh
```

This will:
- Stop lsget and its linked instances and remove their runlevel entries
- Remove the lsget init script and its instance symlinks
- Remove the bundled logrotate configuration if it is unchanged

Configuration files, customized logrotate configuration, content, state, logs, the binary and the service account are preserved.

## Dependencies

The init script has the following dependencies:
- `localmount` makes local filesystems available before lsget starts.
- `net` is used when the host provides that service.

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
