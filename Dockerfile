# Bananagine Pulp Dockerfile — builds bananagine.wasm (the WASM cell)
# and the deployment binary (pulp-deployment/), then runs them under a
# single Pulp host process.
#
# Build context MUST be the GolandProjects parent dir so the Docker
# build can see every repo Bananagine depends on (Pulp, Pulp-ext-*,
# Potassium, Fiber).
#
# Usage from docker-compose (context = GolandProjects/):
#
#   services:
#     bananagine:
#       build:
#         context: ..
#         dockerfile: Bananagine/Dockerfile

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Pull every sibling repo into the build context.
COPY . .

# Build the WASM cell.
WORKDIR /src/Bananagine/pulp-cell
RUN GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared \
    -o /out/bananagine.wasm .

# Build the deployment binary (imports Pulp + ext-docker + ext-fs + ext-http).
WORKDIR /src/Bananagine/pulp-deployment
RUN go build -o /out/app ./...

# Copy the manifest alongside for runtime.
RUN cp /src/Bananagine/pulp-cell/pulp.cell.toml /out/pulp.cell.toml

# -------------------------------------------------------------
# Runtime — minimal, no Go toolchain
# -------------------------------------------------------------
FROM debian:bookworm-slim

# ca-certificates for outbound HTTPS; wget for healthchecks;
# tini for clean signal handling; gettext-base for envsubst
# (rewrites ${EXTERNAL_HOST}, ${SERVICE_TOKEN}, etc. in the
# manifest before the Pulp host parses it).
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget tini gettext-base \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/app             /app/app
COPY --from=build /out/bananagine.wasm /app/pulp-cell/bananagine.wasm
COPY --from=build /out/pulp.cell.toml  /app/pulp-cell/pulp.cell.toml

# Rewrite the manifest's wasm path to the in-container location.
RUN sed -i 's|^wasm = .*|wasm = "/app/pulp-cell/bananagine.wasm"|' \
    /app/pulp-cell/pulp.cell.toml

HEALTHCHECK --interval=15s --timeout=5s --retries=3 \
    CMD wget -q -O /dev/null http://localhost:8080/health || exit 1

ENTRYPOINT ["/usr/bin/tini", "--"]
# Expand ${VAR} placeholders in the manifest at startup, then launch.
CMD ["sh", "-c", "envsubst < /app/pulp-cell/pulp.cell.toml > /tmp/pulp.cell.toml && exec /app/app -manifest /tmp/pulp.cell.toml"]
