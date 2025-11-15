# Vendored JavaScript Dependencies

This file lists all third-party JavaScript libraries included in lsget.

## Dependencies

### marked.js v15.0.12
- **Purpose**: Markdown parser for rendering README files
- **License**: MIT License
- **Repository**: https://github.com/markedjs/marked
- **CDN Source**: https://cdn.jsdelivr.net/npm/marked/marked.min.js
- **License URL**: https://github.com/markedjs/marked/blob/master/LICENSE.md
- **Size**: ~39 KB (minified)

### datastar.js (main branch)
- **Purpose**: Reactivity framework for dynamic UI updates
- **License**: MIT License
- **Repository**: https://github.com/starfederation/datastar
- **CDN Source**: https://cdn.jsdelivr.net/gh/starfederation/datastar@main/bundles/datastar.js
- **License URL**: https://github.com/starfederation/datastar/blob/main/LICENSE
- **Size**: ~30 KB

## License Compatibility

Both dependencies are MIT licensed, which is compatible with lsget's AGPL-3.0 license:
- MIT is permissive and allows inclusion in AGPL projects
- Attribution is maintained via this file
- Source code is available at the repository links above

## Updating

To update to the latest versions:

```bash
cd static
./download-assets.sh
```

This will fetch the current versions from their CDN sources.

## Integrity

Files are downloaded at build time and embedded in the binary via Go's `//go:embed` directive. To verify integrity:

```bash
# Check file sizes
ls -lh marked.min.js datastar.js

# Compute checksums
sha256sum marked.min.js datastar.js
```

## Why These Libraries?

**marked.js**: Industry-standard Markdown parser with excellent CommonMark support. Used to render README.md files when navigating directories.

**datastar.js**: Lightweight Alpine.js-inspired framework that provides reactive data binding without the complexity of larger frameworks like React or Vue. Perfect for the interactive terminal interface.

## Alternatives Considered

- **Markdown**: markdown-it, showdown, micromark (chose marked for balance of size/features)
- **Reactivity**: Alpine.js, Petite-Vue, htmx (chose datastar for simplicity and size)

## Attribution

Full attribution and copyright notices:

```
marked - A markdown parser and compiler. Built for speed.
Copyright (c) 2011-2025, Christopher Jeffrey. (MIT Licensed)
https://github.com/markedjs/marked

datastar - The alliance of the 🌠 and the ✨ for hypermedia systems
Copyright (c) 2024 Caleb Porzio and contributors. (MIT Licensed)
https://github.com/starfederation/datastar
```

## Build Requirements

For developers building from source:
1. Run `./download-assets.sh` in the `static/` directory
2. Files are then embedded during `go build`
3. No runtime downloads or CDN access required

## Runtime Behavior

At runtime:
- Assets served from `/static/marked.min.js` and `/static/datastar.js`
- Loaded from embedded byte arrays (no disk I/O)
- Cached with `max-age=31536000, immutable` headers
- Zero external network requests

## Security

All vendored code is:
- Downloaded from official CDN sources
- Stored in version control for audit
- Embedded at compile time (no runtime modification)
- Served with strict CSP headers
- Reviewable in the repository

To audit the code:
```bash
less static/marked.min.js
less static/datastar.js
```
