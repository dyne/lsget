package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	if value == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, field := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(field))
		if err != nil || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q", field)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func (s *server) trustsProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func peerIP(remote string) netip.Addr {
	if addr, err := netip.ParseAddrPort(remote); err == nil {
		return addr.Addr().WithZone("").Unmap()
	}
	addr, _ := netip.ParseAddr(remote)
	return addr.WithZone("").Unmap()
}

func (s *server) clientIP(r *http.Request) string {
	peer := peerIP(r.RemoteAddr)
	if !peer.IsValid() {
		return "-"
	}
	values := r.Header.Values("X-Forwarded-For")
	if !s.trustsProxy(peer) || len(values) != 1 || len(values[0]) > 1024 {
		return peer.String()
	}
	chain := strings.Split(values[0], ",")
	if len(chain) > 16 {
		return peer.String()
	}
	// Validate the entire list before relying on any part of it. Walk from the
	// socket peer toward the client and stop at the first untrusted address.
	var hops [16]netip.Addr
	for i, field := range chain {
		addr, err := netip.ParseAddr(strings.TrimSpace(field))
		if err != nil || addr.Zone() != "" {
			return peer.String()
		}
		hops[i] = addr.Unmap()
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if i == 0 || !s.trustsProxy(hops[i]) {
			return hops[i].String()
		}
	}
	return peer.String()
}

func (s *server) publicBaseURL(r *http.Request) (string, error) {
	if s.baseURL != "" {
		return strings.TrimRight(s.baseURL, "/"), nil
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if s.trustsProxy(peerIP(r.RemoteAddr)) {
		values := r.Header.Values("X-Forwarded-Proto")
		if len(values) == 1 && values[0] == "https" {
			scheme = "https"
		}
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	if strings.ContainsAny(host, "/#?@\\") {
		return "", errors.New("invalid request host; configure LSGET_BASEURL")
	}
	base, err := sitemapBaseURL(scheme + "://" + host)
	if err != nil {
		return "", errors.New("invalid request host; configure LSGET_BASEURL")
	}
	return base, nil
}
