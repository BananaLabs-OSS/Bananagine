# Bananagine

Game server orchestration and registry service.

From [BananaLabs OSS](https://github.com/bananalabs-oss).

## Overview

Bananagine handles:
- **Orchestration**: Spin up/down game server containers via Docker
- **Registry**: Track running servers, their capacity, and active matches
- **Templates**: Define server configurations declaratively

## Deployment paths

There are two builds in this repo. The **Pulp cell** (`pulp-cell/`) is the
canonical production path. The native binary (`cmd/server/`) is the legacy
build kept for reference; the Dockerfile still targets it but production runs
the cell.

| Path | Status | Used by |
|------|--------|---------|
| `pulp-cell/` | **Canonical** | Production (Pulp host via `pulp-deployment/`) |
| `cmd/server/` | Legacy / reference | Dockerfile (not deployed) |

Build the cell (from `pulp-cell/`):
```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bananagine.wasm .
```

Run the host (from `pulp-deployment/`):
```bash
go run . --manifest test-manifest.toml
```

## Configuration

The cell is configured via the `[config]` block in the manifest TOML
(`pulp-cell/pulp.cell.toml` or a deployment-specific override). All keys
are required unless noted.

| Key | Default | Notes |
|-----|---------|-------|
| `service_token` | — | **Required**; server refuses to start without it |
| `ip_pool_start` | `10.99.0.10` | First IP for overlay mode |
| `ip_pool_end` | `10.99.0.250` | Last IP for overlay mode |
| `port_pool_start` | `5521` | First port for host mode |
| `port_pool_end` | `5599` | Last port for host mode |
| `external_host` | _(empty)_ | Public IP returned to callers in host mode |
| `worlds_dir` | `/var/sessions/worlds` | Host path for world archives |
| `cpu_budget` | `0` | Max CPU cores across all containers (0 = unlimited) |
| `memory_budget` | `0` | Max GiB across all containers (0 = unlimited) |
| `templates` | _(scan)_ | Comma-separated filenames; omit to scan `templates/` |
| `node_cpu_cores` | `0` | Surfaced in `/orchestration/stats`; WASM can't read `/proc` |
| `node_total_memory` | `0` | Bytes; same |
| `node_disk_total` | `0` | Bytes; same |
| `node_disk_used` | `0` | Bytes; same |

### Auth

Every route under `/orchestration`, `/registry`, and `/admin` requires
`X-Service-Token: <service_token>`. The service refuses to start if
`service_token` is empty (fail-closed). `/health` and `/templates` are
unauthenticated.

## API Reference

### Orchestration (auth required)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Service health check (no auth) |
| `GET` | `/templates` | List loaded templates (no auth) |
| `GET` | `/templates/:name/config` | Template config schema (no auth) |
| `GET` | `/orchestration/servers` | List running containers |
| `GET` | `/orchestration/servers/:id` | Get container details |
| `POST` | `/orchestration/servers` | Create server from template |
| `POST` | `/orchestration/servers/:id/restart` | Restart container |
| `POST` | `/orchestration/servers/:id/exec` | Run allowlisted command in container |
| `GET` | `/orchestration/servers/:id/logs` | Tail container logs |
| `GET` | `/orchestration/servers/:id/stats` | Single container stats |
| `GET` | `/orchestration/stats` | All containers + node summary |
| `DELETE` | `/orchestration/servers/:id` | Destroy container (idempotent 204) |
| `GET` | `/orchestration/events` | SSE stream / polling fallback for container events |
| `GET` | `/orchestration/worlds/:name` | Zip and download world data |
| `POST` | `/orchestration/worlds/:name/apply-gamerules` | Write gamerule sentinel flag |
| `DELETE` | `/orchestration/worlds/:name` | Remove world from disk |

**Create Server:**
```json
{
  "template": "paper-1.21",
  "server_id": "mc-abc123",
  "resources": {
    "max_ram_mb": 6144,
    "max_cpu_cores": 3.0
  },
  "env": {
    "MOTD": "My server"
  }
}
```

Resources precedence (lowest to highest): YAML template defaults →
legacy `memory_limit`/`cpu_limit` (bytes/cores) → `max_ram_mb`/`max_cpu_cores`
(MB/cores) → caller-supplied `env.MEMORY`. JVM heap (`MEMORY`) defaults to
`max_ram_mb - 1536` when not explicitly set.

### Registry (auth required)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/registry/servers` | List registered servers |
| `GET` | `/registry/servers/:id` | Get server details |
| `POST` | `/registry/servers` | Register server |
| `PUT` | `/registry/servers/:id` | Update server (players, maxPlayers, metadata) |
| `DELETE` | `/registry/servers/:id` | Unregister server |
| `PUT` | `/registry/servers/:id/matches/:matchId` | Update match |
| `DELETE` | `/registry/servers/:id/matches/:matchId` | Remove match |
| `PUT` | `/registry/servers/:id/players` | Update player count |

**Query Parameters for GET /registry/servers:**
- `type` — filter by server type (`lobby`, `game`)
- `mode` — filter by game mode
- `hasCapacity` — only servers with available player slots
- `hasReadyMatch` — only servers with a ready match

### Admin (auth required)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/admin/reload-templates` | Hot-reload templates from disk |
| `POST` | `/admin/build-image` | Trigger Docker image build (async) |
| `GET` | `/admin/build-status` | Check build status |

## Templates

Place YAML files in `templates/` (scoped to the cell's storage root). See
`templates/paper-1.21.yaml` and `templates/bedrock-server.yaml` for full
examples.

Do not bake `memory_limit`, `cpu_limit`, or `MEMORY` into templates — the
orchestrator supplies these per-tier at provision time via the `resources`
field on `POST /orchestration/servers`. Templates define shape; tiers define
resources.

**Host Mode (port pool, local dev):** omit `network` from the container spec.
**Overlay Mode (IP pool, production):** set `container.network` to a Docker
overlay network name.

| Mode | Allocation | `SERVER_HOST` |
|------|------------|---------------|
| Host | Port pool (`port_pool_start`–`port_pool_end`) | `0.0.0.0` |
| Overlay | IP pool (`ip_pool_start`–`ip_pool_end`) | allocated IP |

When `external_host` is set the returned `server.IP` is overridden to that
value so callers receive the public address players connect to.

### Volume Templating

Host paths in `volumes` support `{{SERVER_ID}}` expansion:

```yaml
volumes:
  "/var/sessions/worlds/{{SERVER_ID}}": "/data/world"
```

### Named Ports

Name ports to get `PORT_<NAME>` injected into the container environment:

```yaml
ports:
  - container: 25565
    protocol: tcp
    name: java
    range: "25565-25599"
```

### Injected Environment Variables

| Variable | Value |
|----------|-------|
| `SERVER_ID` | Unique server identifier |
| `SERVER_HOST` | IP or `0.0.0.0` depending on mode |
| `SERVER_PORT` | Primary allocated port |
| `PORT_<NAME>` | Per-named-port allocation |

## Startup

On startup, Bananagine reconciles its in-memory port/IP pools and capacity
tracker with already-running containers. Only containers whose name matches a
loaded template prefix count toward capacity. This prevents bind conflicts and
budget drift after a host restart.

## Exec security

The `POST /orchestration/servers/:id/exec` endpoint gates every command
against an anchored regex allowlist (`pulp-cell/execallow/`). The gate is unit-
tested with real Evolution caller strings and injection payloads. A prefix-only
match would allow in-container RCE; the gate matches the full command string.

## License

MIT
