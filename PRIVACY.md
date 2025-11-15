# Privacy & Security

lsget is designed with privacy and security as first-class concerns.

## No External Dependencies

**Zero CDN requests** - All JavaScript assets are self-hosted and embedded in the binary:

- ✅ No requests to jsdelivr.net
- ✅ No requests to unpkg.com
- ✅ No requests to any third-party servers
- ✅ Works completely offline
- ✅ No DNS leaks to external parties
- ✅ No tracking or analytics from CDN providers

## Vendored JavaScript Assets

All JavaScript dependencies are vendored (self-hosted) in the `static/` directory:

| Library | Size | Purpose | License |
|---------|------|---------|---------|
| marked.min.js | ~39 KB | Markdown parser for README rendering | MIT |
| datastar.js | ~30 KB | Reactivity framework (Alpine-like) | MIT |

Total overhead: **~69 KB** embedded in the binary.

### How It Works

1. **Build time**: JavaScript files are downloaded and stored in `static/`
2. **Compile time**: Files are embedded using Go's `//go:embed` directive
3. **Runtime**: Assets served from `/static/` endpoint (no external requests)

### Updating Dependencies

```bash
cd static
./download-assets.sh
go build ..
```

## Network Activity

lsget only makes **local network requests**:

- Listens on configured address (default: `localhost:8080`)
- Serves files from specified directory
- No outbound connections to the internet
- No telemetry or phone-home functionality

## Security Features

### Filesystem Isolation

- Serves only files within specified root directory
- Path traversal protection (`..,` symlinks checked)
- `.lsgetignore` support for hiding sensitive files
- Read-only access (no file modification)

### HTTP Security

- Session isolation via secure cookies
- Input validation and sanitization
- MIME type detection
- Content-Disposition headers for downloads

### systemd Hardening

When deployed as a service, additional sandboxing:

- Unprivileged user (`lsget:lsget`)
- `NoNewPrivileges=true`
- `ProtectSystem=strict` (read-only system directories)
- `ProtectHome=true` (no access to home directories)
- `PrivateTmp=true` (private /tmp)
- `RestrictAddressFamilies` (limit network protocols)

See [`deploy/systemd/lsget.service`](deploy/systemd/lsget.service) for full configuration.

## Recommended Deployment

### Behind Reverse Proxy

For production use, deploy behind nginx with HTTPS:

```nginx
server {
    listen 443 ssl http2;
    server_name files.example.com;

    ssl_certificate /etc/letsencrypt/live/files.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/files.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8037;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

### Firewall Configuration

Restrict direct access:

```bash
# Only allow localhost access to lsget port
sudo ufw allow from 127.0.0.1 to any port 8037

# Allow HTTPS through nginx
sudo ufw allow 443/tcp
```

### Directory Permissions

Grant minimal required permissions:

```bash
# Read-only access
chmod -R o+rX /srv/files

# Or use group permissions
chown -R :lsget /srv/files
chmod -R g+rX /srv/files
```

## Privacy Checklist

- [x] No CDN dependencies
- [x] No external network requests
- [x] No telemetry or analytics
- [x] No cookies except session ID
- [x] All assets embedded in binary
- [x] Works completely offline
- [x] Open source (AGPL-3.0)
- [x] Minimal attack surface
- [x] Read-only filesystem access
- [x] Sandboxed when using systemd

## Audit

To verify no external requests are made:

```bash
# Start lsget
./lsget &
PID=$!

# Monitor network activity
sudo tcpdump -i any -n host $(hostname -I | awk '{print $1}') &
TCPDUMP_PID=$!

# Access the interface
curl http://localhost:8080

# Check: should only see localhost traffic
sudo kill $TCPDUMP_PID
kill $PID
```

Or use browser developer tools:
1. Open Network tab
2. Access lsget interface
3. Verify all requests are to localhost only

## License Compliance

All vendored dependencies are MIT licensed and compatible with lsget's AGPL-3.0 license:

- **marked**: https://github.com/markedjs/marked/blob/master/LICENSE.md
- **datastar**: https://github.com/starfederation/datastar/blob/main/LICENSE

Full license texts are available in their respective repositories.

## Reporting Security Issues

If you discover a security vulnerability, please email: security@dyne.org

Please include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

We aim to respond within 48 hours and will credit reporters in release notes (unless you prefer to remain anonymous).

## Contact

- **Security**: security@dyne.org
- **Issues**: https://github.com/dyne/lsget/issues
- **Website**: https://dyne.org
