# lsget Service Deployment - Quick Start Guide

Choose your distribution and follow the steps below.

## 📋 Prerequisites

1. **Build the binary:**
   ```bash
   go build -o lsget .
   ```

2. **Verify it works:**
   ```bash
   ./lsget -version
   ```

---

## 🐧 Debian / Ubuntu / Mint

```bash
# Install binary
sudo cp lsget /usr/local/bin/
sudo chmod +x /usr/local/bin/lsget

# Deploy service
cd deploy/systemd
sudo bash install.sh

# Configure (edit LSGET_DIR and LSGET_ADDR)
sudo nano /etc/default/lsget

# Ensure directory permissions
sudo chmod -R o+rX /srv/ftp  # or your chosen directory

# Enable and start
sudo systemctl enable lsget.service
sudo systemctl start lsget.service
sudo systemctl status lsget.service

# View logs
sudo journalctl -u lsget.service -f
```

---

## 🎩 Fedora / RHEL / CentOS / Rocky / Alma

```bash
# Install binary
sudo cp lsget /usr/local/bin/
sudo chmod +x /usr/local/bin/lsget

# Deploy service
cd deploy/systemd
sudo bash install.sh

# Configure (edit LSGET_DIR and LSGET_ADDR)
sudo vi /etc/default/lsget

# Ensure directory permissions
sudo chmod -R o+rX /srv/ftp

# SELinux (if enabled)
sudo semanage fcontext -a -t httpd_sys_content_t "/srv/ftp(/.*)?"
sudo restorecon -R /srv/ftp

# Enable and start
sudo systemctl enable lsget.service
sudo systemctl start lsget.service
sudo systemctl status lsget.service

# View logs
sudo journalctl -u lsget.service -f

# Firewall (if needed)
sudo firewall-cmd --permanent --add-port=8037/tcp
sudo firewall-cmd --reload
```

---

## 🏔️ Alpine Linux

```sh
# Install binary
cp lsget /usr/local/bin/
chmod +x /usr/local/bin/lsget

# Deploy service
cd deploy/openrc
sh install.sh

# Configure (edit LSGET_DIR and LSGET_ADDR)
vi /etc/conf.d/lsget

# Ensure directory permissions
chmod -R o+rX /srv/ftp

# Enable and start
rc-update add lsget default
rc-service lsget start
rc-service lsget status

# View logs
tail -f /var/log/lsget/lsget.log
tail -f /var/log/lsget/lsget.err
```

---

## 🦄 Arch Linux / Manjaro

```bash
# Install binary
sudo cp lsget /usr/local/bin/
sudo chmod +x /usr/local/bin/lsget

# Deploy service
cd deploy/systemd
sudo bash install.sh

# Configure (edit LSGET_DIR and LSGET_ADDR)
sudo nano /etc/default/lsget

# Ensure directory permissions
sudo chmod -R o+rX /srv/ftp

# Enable and start
sudo systemctl enable lsget.service
sudo systemctl start lsget.service
sudo systemctl status lsget.service

# View logs
sudo journalctl -u lsget.service -f
```

---

## 💜 Gentoo

```sh
# Install binary
cp lsget /usr/local/bin/
chmod +x /usr/local/bin/lsget

# Deploy service
cd deploy/openrc
sh install.sh

# Configure (edit LSGET_DIR and LSGET_ADDR)
vi /etc/conf.d/lsget

# Ensure directory permissions
chmod -R o+rX /srv/ftp

# Enable and start
rc-update add lsget default
rc-service lsget start
rc-service lsget status

# View logs
tail -f /var/log/lsget/lsget.log
```

---

## 🌐 Production Setup with Nginx + HTTPS

### Install Nginx

**Debian/Ubuntu:**
```bash
sudo apt install nginx certbot python3-certbot-nginx
```

**Fedora/RHEL:**
```bash
sudo dnf install nginx certbot python3-certbot-nginx
```

**Alpine:**
```sh
apk add nginx certbot certbot-nginx
```

### Configure Nginx

Create `/etc/nginx/sites-available/lsget` (Debian/Ubuntu) or `/etc/nginx/conf.d/lsget.conf` (others):

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

**Debian/Ubuntu only:**
```bash
sudo ln -s /etc/nginx/sites-available/lsget /etc/nginx/sites-enabled/
```

**Enable and test:**
```bash
sudo nginx -t
sudo systemctl restart nginx
```

### Enable HTTPS

```bash
sudo certbot --nginx -d files.example.com
```

---

## 🔒 Security Checklist

- [ ] Service runs as unprivileged `lsget` user
- [ ] `LSGET_DIR` has minimal permissions (no write access)
- [ ] `.lsgetignore` files created for sensitive directories
- [ ] Listening on `127.0.0.1` (not `0.0.0.0`) if behind proxy
- [ ] Nginx reverse proxy configured with HTTPS
- [ ] Firewall rules configured to block direct access to port 8037
- [ ] Logrotate configured for log management

---

## 🔧 Common Configuration Examples

### Public File Server
```bash
LSGET_ADDR="127.0.0.1:8037"
LSGET_DIR="/srv/public"
LSGET_CATMAX="1048576"  # 1 MB
LSGET_LOGFILE="/var/log/lsget/access.log"
```

### Documentation Server
```bash
LSGET_ADDR="127.0.0.1:8038"
LSGET_DIR="/srv/docs"
LSGET_CATMAX="524288"  # 512 KB
LSGET_LOGFILE="/var/log/lsget/docs.log"
```

### Large File Archive
```bash
LSGET_ADDR="127.0.0.1:8037"
LSGET_DIR="/srv/files"
LSGET_CATMAX="262144"  # 256 KB (cat disabled for large files)
LSGET_LOGFILE="/var/log/lsget/archive.log"
```

---

## 📊 Verify Installation

```bash
# Check service status
sudo systemctl status lsget.service  # systemd
rc-service lsget status              # OpenRC

# Test locally
curl http://127.0.0.1:8037

# Check if listening
sudo netstat -tulpn | grep 8037
# or
sudo lsof -i :8037

# View access logs
sudo tail -f /var/log/lsget/access.log
```

---

## 🆘 Troubleshooting

**Service won't start:**
```bash
# systemd
sudo journalctl -u lsget.service -n 50 --no-pager

# OpenRC
tail -n 50 /var/log/lsget/lsget.err
```

**Permission denied:**
```bash
# Test if lsget user can read directory
sudo -u lsget ls /srv/ftp  # systemd
su -s /bin/sh lsget -c "ls /srv/ftp"  # OpenRC
```

**Port in use:**
```bash
sudo netstat -tulpn | grep 8037
sudo lsof -i :8037
```

---

## 📚 Next Steps

- Read full documentation: [`deploy/README.md`](README.md)
- systemd details: [`deploy/systemd/README.md`](systemd/README.md)
- OpenRC details: [`deploy/openrc/README.md`](openrc/README.md)
- Main project: [`README.md`](../README.md)

---

## 🔗 Links

- **GitHub**: https://github.com/dyne/lsget
- **Issues**: https://github.com/dyne/lsget/issues
- **Website**: https://dyne.org
