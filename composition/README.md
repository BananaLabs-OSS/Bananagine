# Bananagine recursive composition

This directory contains the first safe application-composition slice:

```text
HTTP or sibling façade
        |
        v
bananagine-lua
        |
        v
bananagine-registry.wasm
```

`bananagine.lua` is application logic. The registry cell owns reusable,
stateful server/match records and exposes `bananagine.registry.v1`.

The production `pulp-cell` remains the HTTP/API façade, but its `/registry/*`
handlers now dispatch through `bananagine-lua`. The reusable registry cell
returns typed in-band errors; the façade maps those errors back to the exact
legacy HTTP statuses and body text. All non-registry Bananagine routes remain
inside the existing cell.

The executable test in `../composition-harness` builds and launches three real
WASM cells through Pulp:

1. `bananagine-registry`
2. `bananagine-lua`
3. a test-only HTTP probe

It then registers and queries a server over HTTP, proving the complete
HTTP -> Lua -> sibling MessagePack -> registry path.

Production deployment uses the hash-pinned Pulp application manifest:

```text
-app pulp.app.toml
```

`pulp.app.toml` owns the cell composition and injects `bananagine.lua` into the
Lua cell only after verifying the script's SHA-256 digest. `Pulp.Dockerfile`
preserves the manifest's relative layout and launches this application when
`SERVICE=Bananagine`; other services retain the legacy single-manifest path.

Registry lifecycle events are deliberately not emitted yet. A later slice
should add them together with a validated event sink so application events are
observable instead of silently discarded at the HTTP compatibility boundary.
