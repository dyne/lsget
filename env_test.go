package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	defaults := config{addr: "localhost:8080", dir: ".", catMax: 4096, shellCSP: "enforce"}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want config
	}{
		{name: "defaults", want: defaults},
		{
			name: "environment",
			env: map[string]string{
				"LSGET_ADDR": "0.0.0.0:9090", "LSGET_DIR": "data", "LSGET_CATMAX": "08200", "LSGET_DEV_TEMPLATE": "dev.html",
				"LSGET_SHELL_CSP": "report-only", "LSGET_SECURE_SESSION_COOKIE": "true", "LSGET_PID": "state/lsget.pid",
				"LSGET_LOGFILE": "state/access.log", "LSGET_BASEURL": "https://files.example:8443/releases%20here", "LSGET_SITEMAP": "60",
				"LSGET_SITE_TITLE": "Café files", "LSGET_SITE_DESCRIPTION": "Read and download", "LSGET_PREVIEW_IMAGE": "http://img.example/café.png?v=1&width=640",
			},
			want: config{
				addr: "0.0.0.0:9090", dir: "data", catMax: 8200, devTemplate: "dev.html", shellCSP: "report-only", secureSessionCookie: true,
				pidFile: "state/lsget.pid", logFile: "state/access.log", baseURL: "https://files.example:8443/releases%20here", sitemapInterval: time.Hour,
				siteMetadata: siteMetadata{Title: "Café files", Description: "Read and download", PreviewImage: "http://img.example/caf%C3%A9.png?v=1&width=640"},
			},
		},
		{
			name: "flags override environment even when empty false zero or the environment is invalid",
			args: []string{"-addr", "127.0.0.1:0", "-dir", "cli", "-catmax=0", "-dev-template=", "-shell-csp=enforce",
				"-secure-session-cookie=false", "-pid=", "-logfile=", "-baseurl=", "-sitemap=0", "-site-title=", "-site-description=CLI description", "-preview-image="},
			env: map[string]string{
				"LSGET_ADDR": "missing port", "LSGET_DIR": "env", "LSGET_CATMAX": "invalid", "LSGET_DEV_TEMPLATE": "env.html",
				"LSGET_SHELL_CSP": "invalid", "LSGET_SECURE_SESSION_COOKIE": "invalid", "LSGET_PID": "env.pid",
				"LSGET_LOGFILE": "env.log", "LSGET_BASEURL": "http://%", "LSGET_SITEMAP": "invalid",
				"LSGET_SITE_TITLE": "Environment title", "LSGET_SITE_DESCRIPTION": "Environment description", "LSGET_PREVIEW_IMAGE": "invalid",
			},
			want: config{addr: "127.0.0.1:0", dir: "cli", shellCSP: "enforce", siteMetadata: siteMetadata{Description: "CLI description"}},
		},
		{
			name: "empty environment entries use defaults",
			env:  map[string]string{"LSGET_ADDR": "", "LSGET_CATMAX": "", "LSGET_SITEMAP": "", "LSGET_SECURE_SESSION_COOKIE": ""},
			want: defaults,
		},
		{
			name: "zero size and interval from environment",
			env:  map[string]string{"LSGET_CATMAX": "0", "LSGET_SITEMAP": "0"},
			want: config{addr: defaults.addr, dir: defaults.dir, shellCSP: defaults.shellCSP},
		},
		{
			name: "explicit empty bind address and prefixed CLI integer",
			args: []string{"-addr=", "-catmax=0x2000"},
			want: config{addr: ":http", dir: defaults.dir, catMax: 8192, shellCSP: defaults.shellCSP},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConfig(tc.args, func(key string) string { return tc.env[key] }, io.Discard)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseConfig = (%+v, %v), want %+v", got, err, tc.want)
			}
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		key    string
		value  string
		want   string
		hidden string
	}{
		{name: "catmax trailing data", key: "LSGET_CATMAX", value: "1024 MB", want: "LSGET_CATMAX must be a decimal integer"},
		{name: "catmax exceeds int64", key: "LSGET_CATMAX", value: "9223372036854775808", want: "LSGET_CATMAX"},
		{name: "negative catmax flag", args: []string{"-catmax=-1"}, key: "LSGET_CATMAX", value: "4096", want: "-catmax must be nonnegative"},
		{name: "malformed catmax flag", args: []string{"-catmax=no"}, want: "invalid value \"no\" for flag -catmax"},
		{name: "negative sitemap environment", key: "LSGET_SITEMAP", value: "-1", want: "LSGET_SITEMAP must be between"},
		{name: "sitemap duration overflow", args: []string{"-sitemap=153722868"}, want: "-sitemap must be between"},
		{name: "sitemap fractional", key: "LSGET_SITEMAP", value: "1.5", want: "LSGET_SITEMAP must be a decimal integer"},
		{name: "relative URL", args: []string{"-baseurl=/files"}, want: "-baseurl: invalid sitemap base URL"},
		{name: "base URL scheme", key: "LSGET_BASEURL", value: "ftp://files.example", want: "LSGET_BASEURL: invalid sitemap base URL"},
		{name: "base URL query", key: "LSGET_BASEURL", value: "https://files.example/?key=sample-query", want: "LSGET_BASEURL: invalid sitemap base URL", hidden: "sample-query"},
		{name: "base URL fragment", key: "LSGET_BASEURL", value: "https://files.example/#section", want: "LSGET_BASEURL: invalid sitemap base URL"},
		{name: "base URL credentials", args: []string{"-baseurl=https://reader:sample-password@files.example"}, want: "-baseurl: invalid sitemap base URL", hidden: "sample-password"},
		{name: "relative preview image", args: []string{"-preview-image=/cover.png"}, want: "-preview-image: expected an absolute HTTP(S) URL"},
		{name: "non-HTTP preview image", key: "LSGET_PREVIEW_IMAGE", value: "file:///etc/example.png", want: "LSGET_PREVIEW_IMAGE: expected an absolute HTTP(S) URL"},
		{name: "hostless preview image", key: "LSGET_PREVIEW_IMAGE", value: "https:///image.png", want: "LSGET_PREVIEW_IMAGE: expected an absolute HTTP(S) URL"},
		{name: "preview image credentials", key: "LSGET_PREVIEW_IMAGE", value: "https://reader:sample-password@img.example/cover.png", want: "LSGET_PREVIEW_IMAGE: expected an absolute HTTP(S) URL", hidden: "sample-password"},
		{name: "preview image port", key: "LSGET_PREVIEW_IMAGE", value: "https://img.example:65536/cover.png", want: "LSGET_PREVIEW_IMAGE: expected an absolute HTTP(S) URL"},
		{name: "invalid preview image query escape", key: "LSGET_PREVIEW_IMAGE", value: "https://img.example/cover.png?v=%2", want: "LSGET_PREVIEW_IMAGE: expected an absolute HTTP(S) URL"},
		{name: "invalid boolean", key: "LSGET_SECURE_SESSION_COOKIE", value: "yes", want: "invalid LSGET_SECURE_SESSION_COOKIE value"},
		{name: "invalid CSP", args: []string{"-shell-csp=disabled"}, want: "-shell-csp must be enforce or report-only"},
		{name: "invalid bind address", key: "LSGET_ADDR", value: "localhost", want: "invalid LSGET_ADDR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := runCLI(t.Context(), tc.args, func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return ""
			}, &stdout, &stderr)
			got := stderr.String()
			if exit != 2 || stdout.Len() != 0 || !strings.Contains(got, "configuration error: "+tc.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q, want configuration error %q", exit, &stdout, got, tc.want)
			}
			if tc.hidden != "" && strings.Contains(got, tc.hidden) {
				t.Fatalf("configuration error disclosed URL data: %q", got)
			}
		})
	}
}

func TestCLIMetadataFlags(t *testing.T) {
	for _, tc := range []struct {
		flag string
		out  string
		err  string
	}{
		{flag: "-version", out: "lsget " + version},
		{flag: "-help", err: "Usage of lsget:"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := runCLI(t.Context(), []string{tc.flag}, func(string) string { return "invalid environment" }, &stdout, &stderr)
			if exit != 0 || !strings.HasPrefix(stdout.String(), tc.out) || !strings.HasPrefix(stderr.String(), tc.err) ||
				(tc.out == "" && stdout.Len() != 0) || (tc.err == "" && stderr.Len() != 0) {
				t.Fatalf("exit %d, stdout %q, stderr %q", exit, &stdout, &stderr)
			}
		})
	}
}

func TestCLIInvalidRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, message string }{
		{"missing", filepath.Join(root, "missing"), "failed to access dir"},
		{"regular file", file, "dir is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(t.Context(), []string{"-dir", tc.path}, func(string) string { return "" }, &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "configuration error: "+tc.message) ||
				!strings.Contains(stderr.String(), tc.path) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, &stdout, &stderr)
			}
		})
	}
}
