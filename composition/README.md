# Bananagine recursive composition

Bananagine is one versioned game-platform application. Lua owns sequencing,
while reusable state lives in capability-free WASM owners:

```text
HTTP facade or sibling
          |
          v
    bananagine-lua
      /    |    \
     v     v     v
registry catalog worker
```

The registry owns server and match records. The template catalog owns the
runtime-independent template view and explicit snapshots. The worker owner
owns idempotency and receipts while the host worker extension owns goroutines,
network access, quotas, and cancellation.

The production `pulp-cell` remains the compatibility facade and coarse runtime
manager. Docker, filesystem, port allocation, capacity reconciliation, and
other privileged game-node effects remain host-owned there. Its registry and
template routes dispatch through Lua without changing their public HTTP shape.
The existing `archiveowner.V2WorldSource` stays a scoped host adapter: it owns
credentials, HTTP access, save flushing, and staging instead of exposing those
effects to application WASM.

The executable test in `../composition-harness` builds the real WASI cells and
launches them through Pulp. It proves HTTP -> Lua -> sibling MessagePack ->
state owner calls, host-backed worker execution, per-instance isolation, and a
clean host restart.

Production deployment uses the hash-pinned Pulp application manifest:

```text
-app pulp.app.toml
```

`pulp.app.toml` owns the cell composition and injects `bananagine.lua` into the
Lua cell only after verifying the script's SHA-256 digest. The VM bundle must
preserve this relative layout and launch the application manifest rather than
the legacy single-cell manifest.

Snapshots are explicit contracts; VM deployment still needs to choose and wire
durable snapshot storage and restore policy. Registry lifecycle events are
also deliberately deferred until a validated event sink is available.
