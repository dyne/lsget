# lsget native service quick start

Use the recipe for your init system. Run the commands from the repository root. Build with Go 1.24.5 or later, or place your release binary at `./lsget` and start with the version check.

```sh
go build -o lsget .
./lsget -version
```

## Native service defaults

| Setting | Standalone binary | systemd | OpenRC |
| --- | --- | --- | --- |
| Listener | `localhost:8080` | `127.0.0.1:8037` | `127.0.0.1:8037` |
| Content root | Current working directory | `/srv/ftp` | `/srv/ftp` |
| Working directory | Current working directory | `/var/lib/lsget` | `/var/lib/lsget` |
| Process user | User who runs the binary | `lsget` | `lsget` |
| Access log file | Unset. Request logs go to stdout | `/var/log/lsget/access.log` | `/var/log/lsget/lsget.log` |
| Application PID file | Unset | Unset | Unset |

The services use the binary's 4,096-byte `cat` limit and on-demand sitemap generation unless configured otherwise. Use [`./lsget -h` and the environment settings](../README.md#configuration-flags) to change binary options. Each named service needs its own listener. Named instances use `access-INSTANCE.log` under systemd and `lsget.INSTANCE.log` under OpenRC. The [systemd instance instructions](systemd/README.md#multiple-instances) and [OpenRC instance instructions](openrc/README.md#multiple-instances) show where to put their configuration.

Create or select a content directory before starting the service. The process needs read access to the files and traversal access through the directories. Keep the files read-only for `lsget`. Keep writable state in `/var/lib/lsget` and access logs in `/var/log/lsget`, which the installers create for the new service account. OpenRC keeps its supervisor PID in `/run/lsget/lsget.pid` for the default service. If you configure an application PID file, use a unique path in `/var/lib/lsget` for each service.

## systemd

Use this recipe on Debian, Ubuntu, Fedora, RHEL, Arch, and other systemd distributions. Run the installation and management commands as root or with `sudo`.

```sh
install -m 0755 lsget /usr/local/bin/lsget
bash deploy/systemd/install.sh
```

Edit `LSGET_DIR` and `LSGET_ADDR` in the configuration file printed by the installer. A fresh installation uses `/etc/sysconfig/lsget` where `/etc/sysconfig` exists and `/etc/default/lsget` elsewhere. If both files exist, systemd reads `/etc/default/lsget` first and `/etc/sysconfig/lsget` second.

The permission check below uses the default content directory. Substitute the directory you configured if it differs.

```sh
runuser -u lsget -- ls /srv/ftp
systemctl enable --now lsget.service
systemctl status --no-pager lsget.service
journalctl -u lsget.service -n 50 --no-pager
```

## OpenRC

Use this recipe on Alpine Linux, Gentoo, and other OpenRC distributions. Run the installation and management commands as root.

```sh
install -m 0755 lsget /usr/local/bin/lsget
sh deploy/openrc/install.sh
```

Edit `LSGET_DIR` and `LSGET_ADDR` in `/etc/conf.d/lsget`. OpenRC reads this as a shell file. If you use `LSGET_OPTS` to pass command-line flags, those flags take precedence over the environment settings. Substitute your chosen content directory for `/srv/ftp` in the permission check.

```sh
su -s /bin/sh -c 'ls /srv/ftp' lsget
rc-update add lsget default
rc-service lsget start
rc-service lsget status
tail -n 50 /var/log/lsget/lsget.out /var/log/lsget/lsget.err
```

## Verify the listener

Place a known `hello.txt` in the served directory. Check its contents at `http://127.0.0.1:8037/?file=hello.txt` on the service host. With the default settings, the request appears in the service's access log listed above. For file URLs and checksum checks, see [Direct file links](../README.md#direct-file-links).

## HTTPS reverse proxy

For a reverse proxy on the same machine that accepts public traffic only over HTTPS, keep lsget on the loopback listener and add these settings to the service configuration. Substitute the public URL and the proxy's actual peer address or CIDR.

```sh
LSGET_ADDR="127.0.0.1:8037"
LSGET_BASEURL="https://files.example.com"
LSGET_SECURE_SESSION_COOKIE="true"
LSGET_TRUSTED_PROXIES="127.0.0.1/32"
```

Send the public `Host` and have the edge proxy replace client-supplied `X-Forwarded-For` and `X-Forwarded-Proto`. A single Nginx edge can use these directives in its HTTPS location.

```nginx
proxy_pass http://127.0.0.1:8037;
proxy_set_header Host $http_host;
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header X-Forwarded-Proto $scheme;
```

The trusted peer setting controls which forwarding headers lsget uses for the client IP and for share-link scheme detection when there is no public base URL. Browser origin checks use the request host and scheme or `LSGET_BASEURL`. `LSGET_SECURE_SESSION_COOKIE=true` selects HTTPS for origin checks on a host-preserving proxy and sets the cookie's `Secure` attribute. See [proxy metadata and access logs](../README.md#proxy-metadata-and-access-logs) for multiple proxy hops.

## Troubleshooting

Read the recent journal on systemd or the `.err` file on OpenRC using the commands above. Identify the failing setting or path before restarting.

- **Bind error.** Check the address and port in `LSGET_ADDR`. Stop an older service using the same socket or choose a free port. Give each instance a distinct listener and update its proxy upstream when changing ports.
- **Permission error or missing content.** Run the content access check as `lsget`. Check traversal access on each parent directory and read access to the file. The systemd sandbox hides home directories. Use a service-readable path such as `/srv/ftp`. For log or PID errors, check write access to the configured directories and existing log file. If an old application PID file blocks startup, verify that its process has exited before removing it.
- **Configuration error.** Check the setting named in the startup message against `./lsget -h`. Check both systemd environment files for overrides, or shell syntax and `LSGET_OPTS` on OpenRC. To serve a manual sitemap or recover a failed refresh, follow [the sitemap instructions](../README.md#sitemap).

Apply configuration changes by restarting the service. If the systemd journal reports `start-limit-hit`, run `systemctl reset-failed lsget.service` after fixing the cause, then restart.

```sh
# systemd
systemctl restart lsget.service

# OpenRC
rc-service lsget restart
```

For more deployment options, see the [service deployment guide](README.md), [systemd recipe](systemd/README.md), or [OpenRC recipe](openrc/README.md).
