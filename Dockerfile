# Bananagine's production image stages the complete Pulp application, not a
# legacy single-cell manifest. Build with the workspace root as context so the
# shared Pulp runtime, extensions, and Pulp-Lua sources are available.

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY . .

# The public Bananagine cell remains the compatibility HTTP facade and the
# privileged host-effect boundary. Lua coordinates the independent owners.
RUN mkdir -p \
    /out/application/Bananagine/pulp-cell \
    /out/application/Bananagine/registry-cell \
    /out/application/Bananagine/template-catalog-cell \
    /out/application/Bananagine/state-cell \
    /out/application/Bananagine/worker-cell \
    /out/application/pulp-engines/workload-inventory-sqlite-cell \
    /out/application/pulp-engines/capacity-scheduler-sqlite-cell \
    /out/application/pulp-engines/workload-provisioning-sqlite-cell \
    /out/application/pulp-engines/runtime-control-sqlite-cell \
    /out/application/Pulp-Lua/pulp-cell

WORKDIR /src/Bananagine/pulp-cell
RUN GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared \
    -o /out/application/Bananagine/pulp-cell/bananagine.wasm .

WORKDIR /src/Bananagine/state-cell
RUN GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared \
    -o /out/application/Bananagine/state-cell/runtime-catalog-state.wasm .

WORKDIR /src/Bananagine/worker-cell
RUN GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared \
    -o /out/application/Bananagine/worker-cell/async-http-job.wasm .

WORKDIR /src/Pulp-Lua/pulp-cell
RUN GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared \
    -o /out/application/Pulp-Lua/pulp-cell/lua-orchestrator.wasm .

RUN for engine in workload-inventory capacity-scheduler workload-provisioning runtime-control; do \
      source="/src/pulp-engines/${engine}-sqlite-cell"; \
      destination="/out/application/pulp-engines/${engine}-sqlite-cell"; \
      cd "$source/cmd/$engine"; \
      GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o "$destination/$engine.wasm" .; \
      cp "$source/pulp.cell.toml" "$destination/pulp.cell.toml"; \
    done

# Keep source-relative paths intact in the staged tree. The application
# manifest therefore resolves every owner and the Lua runtime without an
# image-specific path rewrite. Pin every staged WASM byte before the app loader
# accepts the bundle.
RUN mkdir -p /out/application/Bananagine/composition /out/templates && \
    cp /src/Bananagine/pulp-cell/pulp.cell.toml \
      /out/application/Bananagine/pulp-cell/pulp.cell.toml && \
    cp /src/Bananagine/state-cell/pulp.cell.toml \
      /out/application/Bananagine/state-cell/pulp.cell.toml && \
    cp /src/Bananagine/registry-cell/pulp.cell.toml \
      /out/application/Bananagine/registry-cell/pulp.cell.toml && \
    cp /src/Bananagine/template-catalog-cell/pulp.cell.toml \
      /out/application/Bananagine/template-catalog-cell/pulp.cell.toml && \
    cp /src/Bananagine/worker-cell/pulp.cell.toml \
      /out/application/Bananagine/worker-cell/pulp.cell.toml && \
    cp /src/Bananagine/composition/pulp.app.toml \
      /src/Bananagine/composition/bananagine.lua \
      /src/Bananagine/composition/lua-orchestrator.pulp.cell.toml \
      /out/application/Bananagine/composition/ && \
    cp /src/paper-server/*.yaml /out/templates/ && \
    for manifest in \
      /out/application/Bananagine/pulp-cell/pulp.cell.toml \
      /out/application/Bananagine/state-cell/pulp.cell.toml \
      /out/application/Bananagine/worker-cell/pulp.cell.toml \
      /out/application/pulp-engines/workload-inventory-sqlite-cell/pulp.cell.toml \
      /out/application/pulp-engines/capacity-scheduler-sqlite-cell/pulp.cell.toml \
      /out/application/pulp-engines/workload-provisioning-sqlite-cell/pulp.cell.toml \
      /out/application/pulp-engines/runtime-control-sqlite-cell/pulp.cell.toml \
      /out/application/Bananagine/composition/lua-orchestrator.pulp.cell.toml; do \
      case "$manifest" in \
        */Bananagine/pulp-cell/*) wasm_file=/out/application/Bananagine/pulp-cell/bananagine.wasm ;; \
        */Bananagine/state-cell/*) wasm_file=/out/application/Bananagine/state-cell/runtime-catalog-state.wasm ;; \
        */Bananagine/worker-cell/*) wasm_file=/out/application/Bananagine/worker-cell/async-http-job.wasm ;; \
        */pulp-engines/*) engine="$(basename "$(dirname "$manifest")" -sqlite-cell)"; wasm_file="$(dirname "$manifest")/$engine.wasm" ;; \
        *) wasm_file=/out/application/Pulp-Lua/pulp-cell/lua-orchestrator.wasm ;; \
      esac; \
      wasm_sha="$(sha256sum "$wasm_file" | awk '{print $1}')"; \
      sed -i "/^wasm = /a wasm_sha256 = \"${wasm_sha}\"" "$manifest"; \
    done && \
    sed -i '/^version = /a require_wasm_sha256 = true' \
      /out/application/Bananagine/composition/pulp.app.toml

# The native host registers Docker, filesystem, HTTP, and worker extensions.
WORKDIR /src/Bananagine/pulp-deployment
RUN go build -o /out/app ./...

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget tini gettext-base \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/app /app/app
COPY --from=build /out/application /app/application
COPY --from=build /out/templates /app/templates

# A node with no staged templates cannot safely create game servers.
RUN find /app/templates -maxdepth 1 -type f \
      \( -name '*.yaml' -o -name '*.yml' \) -print -quit | grep -q .

HEALTHCHECK --interval=15s --timeout=5s --retries=3 \
    CMD wget -q -O /dev/null "http://localhost:${HTTP_PORT:-8080}/health" || exit 1

ENTRYPOINT ["/usr/bin/tini", "--"]

# storage.fs remains scoped to the facade cell. Templates are immutable image
# assets, copied into that scope before Pulp instantiates the application.
CMD ["sh", "-c", "set -eu; cell_storage=/app/data/apps/bananagine/default/cells/bananagine/primary; mkdir -p \"$cell_storage/templates\"; cp -a /app/templates/. \"$cell_storage/templates/\"; cp -R /app/application /tmp/application; envsubst < /tmp/application/Bananagine/pulp-cell/pulp.cell.toml > /tmp/application/Bananagine/pulp-cell/pulp.cell.expanded.toml; mv /tmp/application/Bananagine/pulp-cell/pulp.cell.expanded.toml /tmp/application/Bananagine/pulp-cell/pulp.cell.toml; exec /app/app -app /tmp/application/Bananagine/composition/pulp.app.toml"]
