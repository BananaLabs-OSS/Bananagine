# Bananagine

Game server orchestration and registry service.

From [BananaLabs OSS](https://github.com/bananalabs-oss).

## Overview

Bananagine handles:
- **Orchestration**: Spin up/down game server containers via Docker/Podman
- **Registry**: Track running servers, their capacity, and active matches
- **Templates**: Define server configurations declaratively

## Quick Start
```bash
go run ./cmd/server
```

## Configuration

Configuration priority: CLI flags > Environment variables > Defaults

| Setting | Env Var | CLI Flag | Default |
|---------|---------|----------|---------|
| Listen address | `LISTEN_ADDR` | `-listen` | `:3000` |
| Templates directory | `TEMPLATES_DIR` | `-templates` | `./templates` |
| IP pool start | `IP_POOL_START` | `-ip-start` | `10.99.0.10` |
| IP pool end | `IP_POOL_END` | `-ip-end` | `10.99.0.250` |
| Port pool start | `PORT_POOL_START` | `-port-start` | `5521` |
| Port pool end | `PORT_POOL_END` | `-port-end` | `5599` |

**CLI:**
```bash
./bananagine -listen :3000 -templates ./templates -ip-start 10.99.0.10 -ip-end 10.99.0.250
```

**Docker Compose:**
```yaml
bananagine:
  image: localhost/bananagine:local
  ports:
    - "3000:3000"
  volumes:
    - ./templates:/app/templates
  environment:
    - IP_POOL_START=10.99.0.10
    - IP_POOL_END=10.99.0.250
```

## API Reference

### Orchestration

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/orchestration/servers` | List running containers |
| `GET` | `/orchestration/servers/:id` | Get container details |
| `POST` | `/orchestration/servers` | Create server from template |
| `DELETE` | `/orchestration/servers/:id` | Destroy container |

**Create Server:**
```json
{"template": "hytale-test"}
```

### Registry

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/registry/servers` | List registered servers |
| `GET` | `/registry/servers/:id` | Get server details |
| `POST` | `/registry/servers` | Register server |
| `PUT` | `/registry/servers/:id` | Update server |
| `DELETE` | `/registry/servers/:id` | Unregister server |
| `PUT` | `/registry/servers/:id/matches/:matchId` | Update match |
| `DELETE` | `/registry/servers/:id/matches/:matchId` | Remove match |
| `PUT` | `/registry/servers/:id/players` | Update player count |

**Query Parameters:**
- `type` - Filter by server type (`lobby`, `game`)
- `mode` - Filter by game mode
- `hasCapacity` - Only servers with player capacity
- `hasReadyMatch` - Only servers with ready matches

**Register Server:**
```json
{
  "id": "skywars-1",
  "type": "game",
  "mode": "skywars",
  "host": "10.99.0.10",
  "port": 5520,
  "maxPlayers": 8,
  "players": 0,
  "matches": {
    "match-1": {
      "status": "ready",
      "need": 2,
      "players": []
    }
  }
}
```

## Templates

Place YAML files in `./templates/`.

**Host Mode (local dev):**
```yaml
name: hytale-test
container:
  image: localhost/hytale-server
  ports:
    - host: 0
      container: 0
      protocol: udp
  environment:
    SERVER_TYPE: "game"
    SERVER_MODE: "skywars"
    MAX_PLAYERS: "8"
    BANANAGINE_URL: "http://host.containers.internal:3000"
    BANANASPLIT_URL: "http://host.containers.internal:3001"
server: {}
hooks:
  pre_start: "http://host.containers.internal:3002/tokens"
```

**Overlay Mode (production):**
```yaml
name: hytale-prod
container:
  image: localhost/hytale-server
  network: banananet
  ports:
    - container: 5520
      protocol: udp
  environment:
    SERVER_TYPE: "game"
    SERVER_MODE: "skywars"
server: {}
hooks:
  pre_start: "http://10.99.0.3:3002/tokens"
```

### Modes

| Mode | Network Field | Allocation | SERVER_HOST |
|------|---------------|------------|-------------|
| Host | absent | Port pool (5521-5599) | 127.0.0.1 |
| Overlay | present | IP pool (10.99.0.10-250) | allocated IP |

### Injected Environment Variables

| Variable | Description |
|----------|-------------|
| `SERVER_ID` | Unique server identifier |
| `SERVER_HOST` | IP address |
| `SERVER_PORT` | Port number |

## Dependencies

- [Potassium](https://github.com/bananalabs-oss/potassium) - Orchestration library

## License

MIT