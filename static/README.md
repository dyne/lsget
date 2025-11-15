# Static JavaScript Assets

This directory contains vendored (self-hosted) JavaScript dependencies to eliminate CDN dependencies for privacy and offline use.

## Dependencies

| Library | Purpose | Original Source |
|---------|---------|-----------------|
| **marked.min.js** | Markdown parser for README rendering | https://cdn.jsdelivr.net/npm/marked/marked.min.js |
| **datastar.js** | Reactivity framework (Alpine-like) | https://cdn.jsdelivr.net/gh/starfederation/datastar@main/bundles/datastar.js |

## Updating Dependencies

To download/update the vendored JavaScript files:

```bash
cd static
./download-assets.sh
```

The script will:
1. Download the latest versions from their CDN sources
2. Save them to this directory
3. These files are then embedded in the Go binary via `//go:embed`

## Why Vendor?

**Privacy**: No external requests to CDN providers means:
- No tracking pixels or analytics from CDN providers
- Works completely offline
- No DNS leaks to third parties
- Full control over what JavaScript runs

**Reliability**: 
- Works without internet access
- No CDN downtime issues
- Consistent versions across deployments

**Security**:
- Audit the exact code being served
- No possibility of CDN compromise
- Subresource Integrity not needed (no external fetch)

## Build Process

When you run `go build`, these files are automatically embedded into the binary:

```go
//go:embed static/marked.min.js
var markedJS []byte

//go:embed static/datastar.js
var datastarJS []byte
```

They're served at:
- `/static/marked.min.js`
- `/static/datastar.js`

## File Sizes

- **marked.min.js**: ~39 KB
- **datastar.js**: ~30 KB
- **Total**: ~69 KB embedded in binary

This small overhead ensures complete privacy and offline functionality.

## License Information

- **marked**: MIT License - https://github.com/markedjs/marked
- **datastar**: MIT License - https://github.com/starfederation/datastar

See their respective repositories for full license texts.
