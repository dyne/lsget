#!/bin/sh
set -eu

hash_files() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$@"
    else
        shasum -a 256 "$@"
    fi
}

download() {
    curl --disable --fail --silent --show-error --location \
        --proto '=https' --proto-redir '=https' \
        --connect-timeout 10 --max-time 60 --output "$2" "$1"
}

verify_assets() {
    actual=$(hash_files "$@") || return 1
    if ! printf '%s\n' "$actual" | diff -u "$assets/vendor.sha256" - >&2; then
        printf 'Vendored browser dependency checksum mismatch\n' >&2
        return 1
    fi
    printf '%s\n' "$actual"
}

mode=
case "${1:-}" in
    check|refresh)
        if [ "$#" -eq 1 ]; then mode=$1; fi
        ;;
esac
if [ -z "$mode" ]; then
    printf 'Usage: %s check|refresh\n' "$0" >&2
    exit 2
fi

assets=$(CDPATH='' cd "$(dirname "$0")/../assets/js" && pwd)
cd "$assets"
set -- marked.min.js marked.LICENSE.md datastar.js datastar.LICENSE.md

if [ "$mode" = check ]; then
    verify_assets "$@"
    exit
fi

marked_version=15.0.12
marked_archive_sha256=39a847a896af15b2846f927da55d444619ea55a55c236e10333056474b793aaa
datastar_commit=a7adbacbc9c1a3b27707d7cceba3c02a3f81a86c
datastar_source="https://raw.githubusercontent.com/starfederation/datastar/$datastar_commit"

stage=$(mktemp -d .vendor-js.XXXXXXXX)
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM

download "https://registry.npmjs.org/marked/-/marked-$marked_version.tgz" "$stage/marked.tgz"
printf '%s  %s\n' "$marked_archive_sha256" "$stage/marked.tgz" | hash_files -c
tar -xOzf "$stage/marked.tgz" package/marked.min.js > "$stage/marked.min.js"
tar -xOzf "$stage/marked.tgz" package/LICENSE.md > "$stage/marked.LICENSE.md"
download "$datastar_source/bundles/datastar.js" "$stage/datastar.js"
download "$datastar_source/LICENSE.md" "$stage/datastar.LICENSE.md"

# Verify the complete set before replacing any file. Staging in the destination
# directory keeps each replacement on the same filesystem.
(cd "$stage" && verify_assets "$@")
for file do
    if ! cmp -s "$stage/$file" "$file"; then
        chmod 0644 "$stage/$file"
        mv -f "$stage/$file" "$file"
    fi
done
