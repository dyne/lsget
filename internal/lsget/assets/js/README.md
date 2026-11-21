# Vendored browser dependencies

| Dependency | Pinned release and source revision | Download | License notice |
|---|---|---|---|
| Marked | 15.0.12, [`b4eb83bbb48107720d95162ccecf829ba16be91e`](https://github.com/markedjs/marked/tree/b4eb83bbb48107720d95162ccecf829ba16be91e) | [`marked-15.0.12.tgz`](https://registry.npmjs.org/marked/-/marked-15.0.12.tgz), members `package/marked.min.js` and `package/LICENSE.md` | [Marked and Markdown notices](marked.LICENSE.md) |
| Datastar | 1.0.0-RC.5, [`a7adbacbc9c1a3b27707d7cceba3c02a3f81a86c`](https://github.com/starfederation/datastar/tree/a7adbacbc9c1a3b27707d7cceba3c02a3f81a86c) | [`bundles/datastar.js`](https://raw.githubusercontent.com/starfederation/datastar/a7adbacbc9c1a3b27707d7cceba3c02a3f81a86c/bundles/datastar.js) and [`LICENSE.md`](https://raw.githubusercontent.com/starfederation/datastar/a7adbacbc9c1a3b27707d7cceba3c02a3f81a86c/LICENSE.md) | [Datastar notice](datastar.LICENSE.md) |

`vendor.sha256` records the SHA-256 of each checked-in bundle and notice. The refresh script also checks the Marked package archive against SHA-256 `39a847a896af15b2846f927da55d444619ea55a55c236e10333056474b793aaa` before extracting the two members.

Run `task vendor-check` or `sh scripts/vendor-js.sh check` from the repository root to verify the files offline. Run `task vendor` or `sh scripts/vendor-js.sh refresh` to fetch the pinned bytes. The refresh validates every staged file before replacing any checked-in file. For an intentional update, review the upstream release and its notices, then update the sources in `scripts/vendor-js.sh`, the hashes in `vendor.sha256`, and the records above together.
