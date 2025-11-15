# lsget Service Deployment

This directory contains deployment configurations for running lsget as a system service on various Linux distributions.

## Available Init Systems

- **[systemd/](systemd/)** - For Debian, Ubuntu, Fedora, RHEL, CentOS, Arch Linux, and most modern distributions
- **[openrc/](openrc/)** - For Alpine Linux, Gentoo, and other OpenRC-based distributions

## Quick Start

### systemd (Debian/Ubuntu/Fedora/RHEL/Arch)

```bash
# Build and install binary
go build -o lsget .
sudo cp lsget /usr/local/bin/
sudo chmod +x /usr/local/bin/lsget

# Install service
cd deploy/systemd
sudo bash install.sh

# Configure
sudo nano /etc/default/lsget

# Start service
sudo systemctl enable lsget.service
sudo systemctl start lsget.service
```

### OpenRC (Alpine/Gentoo)

```sh
# Build and install binary
go build -o lsget .
cp lsget /usr/local/bin/
chmod +x /usr/local/bin/lsget

# Install service
cd deploy/openrc
sh install.sh

# Configure
vi /etc/conf.d/lsget

# Start service
rc-update add lsget default
rc-service lsget start
```

## Distribution Compatibility

| Distribution | Init System | Deployment Folder | Package Manager |
|-------------|-------------|-------------------|-----------------|
| Alpine Linux | OpenRC | `openrc/` | apk |
| Arch Linux | systemd | `systemd/` | pacman |
| CentOS 7+ | systemd | `systemd/` | yum/dnf |
| Debian 8+ | systemd | `systemd/` | apt |
| Fedora | systemd | `systemd/` | dnf |
| Gentoo | OpenRC | `openrc/` | emerge |
| openSUSE | systemd | `systemd/` | zypper |
| RHEL 7+ | systemd | `systemd/` | yum/dnf |
| Ubuntu 16.04+ | systemd | `systemd/` | apt |

## Configuration

Both init systems use similar configuration variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `LSGET_ADDR` | `127.0.0.1:8037` | Listen address (IP:PORT) |
| `LSGET_DIR` | `/srv/ftp` | Root directory to serve |
| `LSGET_CATMAX` | `262144` | Max bytes for cat (256 KiB) |
| `LSGET_PIDFILE` | Auto-generated | PID file location |
| `LSGET_LOGFILE` | `/var/log/lsget/...` | Access log file |

### systemd Configuration
Edit: `/etc/default/lsget`

### OpenRC Configuration
Edit: `/etc/conf.d/lsget`

## Multiple Instances

Both init systems support running multiple instances with different configurations:

### systemd Instances
```bash
# Create instance config
sudo cp /etc/default/lsget /etc/default/lsget-docs
sudo nano /etc/default/lsget-docs

# Enable and start
sudo systemctl enable lsget@docs.service
sudo systemctl start lsget@docs.service
```

### OpenRC Instances
```sh
# Create symlink
ln -s /etc/init.d/lsget /etc/init.d/lsget.docs

# Create instance config
cp /etc/conf.d/lsget /etc/conf.d/lsget.docs
vi /etc/conf.d/lsget.docs

# Enable and start
rc-update add lsget.docs default
rc-service lsget.docs start
```

## Security

Both deployment configurations include security hardening:

### Common Security Features
- Runs as unprivileged `lsget` user
- Isolated working directory
- Resource limits
- Read-only system directories
- Private temporary directories

### systemd-specific
- `NoNewPrivileges=true`
- `ProtectSystem=strict`
- `ProtectHome=true`
- `RestrictAddressFamilies`
- Advanced sandboxing features

### OpenRC-specific
- Process supervision
- Dependency management
- Signal handling

## Reverse Proxy Setup

For production use, run lsget behind nginx:

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

Enable HTTPS:
```bash
# Debian/Ubuntu
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d files.example.com

# Alpine
apk add certbot certbot-nginx
certbot --nginx -d files.example.com
```

## Log Management

Both configurations include logrotate support:

- Rotates logs daily
- Keeps 14 days of logs
- Compresses old logs
- Reloads service after rotation

## Service Management Comparison

| Action | systemd | OpenRC |
|--------|---------|--------|
| Start | `systemctl start lsget` | `rc-service lsget start` |
| Stop | `systemctl stop lsget` | `rc-service lsget stop` |
| Restart | `systemctl restart lsget` | `rc-service lsget restart` |
| Reload | `systemctl reload lsget` | `rc-service lsget reload` |
| Status | `systemctl status lsget` | `rc-service lsget status` |
| Enable | `systemctl enable lsget` | `rc-update add lsget default` |
| Disable | `systemctl disable lsget` | `rc-update del lsget default` |
| Logs | `journalctl -u lsget -f` | `tail -f /var/log/lsget/*.log` |

## Directory Structure

```
deploy/
├── README.md                 # This file
├── systemd/                  # systemd deployment
│   ├── README.md            # systemd-specific documentation
│   ├── install.sh           # Installation script
│   ├── uninstall.sh         # Uninstallation script
│   ├── lsget.default        # Default configuration
│   ├── lsget.service        # Service unit file
│   ├── lsget@.service       # Template unit file
│   └── lsget.logrotate      # Logrotate configuration
└── openrc/                   # OpenRC deployment
    ├── README.md            # OpenRC-specific documentation
    ├── install.sh           # Installation script
    ├── uninstall.sh         # Uninstallation script
    ├── lsget                # Init script
    ├── lsget.confd          # Configuration file
    └── lsget.logrotate      # Logrotate configuration
```

## Troubleshooting

### Service Won't Start

**Check logs:**
```bash
# systemd
sudo journalctl -u lsget.service -n 50

# OpenRC
tail -n 50 /var/log/lsget/lsget.err
```

**Common issues:**
1. Binary not found at `/usr/local/bin/lsget`
2. Directory not readable by `lsget` user
3. Port already in use
4. Invalid configuration syntax

### Permission Issues

**Check directory permissions:**
```bash
# Grant read access
sudo chmod -R o+rX /srv/ftp

# Or add lsget to group
sudo usermod -a -G ftp lsget  # systemd
addgroup lsget ftp            # OpenRC
```

**Test access:**
```bash
sudo -u lsget ls /srv/ftp     # systemd
su -s /bin/sh lsget -c "ls /srv/ftp"  # OpenRC
```

### Port Already in Use

**Find process using port:**
```bash
sudo netstat -tulpn | grep 8037
# or
sudo lsof -i :8037
```

## Support

- **Repository**: https://github.com/dyne/lsget
- **Issues**: https://github.com/dyne/lsget/issues
- **Website**: https://dyne.org
- **License**: GNU Affero General Public License v3.0

## Additional Resources

### systemd
- [systemd documentation](https://www.freedesktop.org/wiki/Software/systemd/)
- [systemd service files](https://www.freedesktop.org/software/systemd/man/systemd.service.html)

### OpenRC
- [OpenRC documentation](https://wiki.gentoo.org/wiki/OpenRC)
- [Alpine Linux init system](https://wiki.alpinelinux.org/wiki/Alpine_Linux_Init_System)
