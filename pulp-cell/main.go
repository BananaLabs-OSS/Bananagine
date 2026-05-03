// Bananagine — Pulp cell port.
//
// Container orchestrator. Manages Docker containers from templates,
// with port/IP pool allocation, capacity tracking, and a server
// registry. Originally a standalone Gin+Potassium service.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bananagine.wasm .
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	"github.com/BananaLabs-OSS/Fiber/pulp/gin/middleware"
)

// orchestrationEventsPath is the SSE route for container lifecycle events.
// Registered in bootstrap and emitted from the step loop.
const orchestrationEventsPath = "/orchestration/events"

// wireEvent is the JSON shape emitted over SSE and returned by the
// polling /orchestration/events endpoint. Matches potassium's
// orchestrator/providers/docker.ContainerEvent byte-for-byte so
// existing clients (Evolution SSE listener) don't care whether the
// backend is native Gin or a pulp cell. docker.Event on the host-call
// side uses msgpack tags only and no json tags, so encoding it
// directly would yield capital-letter Go field names and a "timestamp"
// key where native emitted "time" — that would break Evolution.
type wireEvent struct {
	ContainerID string `json:"container_id"`
	Name        string `json:"name"`
	Action      string `json:"action"`
	Time        int64  `json:"time"`
}

func toWireEvent(de docker.Event) wireEvent {
	return wireEvent{
		ContainerID: de.ContainerID,
		Name:        de.Name,
		Action:      de.Action,
		Time:        de.Timestamp,
	}
}

func main() {}

func init() {
	pulp.OnInit(bootstrap)
}

type appConfig struct {
	TemplateFiles []string
	IPStart       string
	IPEnd         string
	PortStart     int
	PortEnd       int
	ExternalHost  string
	ServiceToken  string
	CPUBudget     float64
	MemBudget     float64
	WorldsDir     string

	// TemplatesDir is the build context passed to docker.Build when the
	// /admin/build-image endpoint is hit. Mirrors the original service's
	// TEMPLATES_DIR env var — defaults to /app/templates (the paper-server
	// repo mount), can be overridden from the manifest for local dev.
	TemplatesDir string

	// Node hardware descriptors — surfaced by /orchestration/stats so
	// callers (Evolution admin desk) can show node capacity. The host
	// OS is opaque from WASM, so the operator fills these in via the
	// manifest. Zeros are tolerated and reported as 0 with a log-once
	// warning at startup.
	NodeCPUCores   int
	NodeTotalMem   uint64 // bytes
	NodeDiskTotal  uint64 // bytes
	NodeDiskUsed   uint64 // bytes
}

func parseConfig(data []byte) (appConfig, error) {
	var cfg appConfig
	if len(data) == 0 {
		return cfg, fmt.Errorf("missing [config]")
	}
	var raw map[string]any
	if err := decodeMsgpack(data, &raw); err != nil {
		return cfg, err
	}
	jbytes, _ := json.Marshal(raw)
	var tmp struct {
		Templates    string  `json:"templates"`
		IPStart      string  `json:"ip_pool_start"`
		IPEnd        string  `json:"ip_pool_end"`
		PortStart    int     `json:"port_pool_start"`
		PortEnd      int     `json:"port_pool_end"`
		ExternalHost string  `json:"external_host"`
		ServiceToken string  `json:"service_token"`
		CPUBudget    float64 `json:"cpu_budget"`
		MemBudget    float64 `json:"memory_budget"`
		WorldsDir    string  `json:"worlds_dir"`
		TemplatesDir string  `json:"templates_dir"`
		NodeCPUCores  int    `json:"node_cpu_cores"`
		NodeTotalMem  uint64 `json:"node_total_memory"`
		NodeDiskTotal uint64 `json:"node_disk_total"`
		NodeDiskUsed  uint64 `json:"node_disk_used"`
	}
	if err := json.Unmarshal(jbytes, &tmp); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	if tmp.Templates != "" {
		cfg.TemplateFiles = strings.Split(tmp.Templates, ",")
	}
	cfg.IPStart = tmp.IPStart
	if cfg.IPStart == "" {
		cfg.IPStart = "10.99.0.10"
	}
	cfg.IPEnd = tmp.IPEnd
	if cfg.IPEnd == "" {
		cfg.IPEnd = "10.99.0.250"
	}
	cfg.PortStart = tmp.PortStart
	if cfg.PortStart == 0 {
		cfg.PortStart = 5521
	}
	cfg.PortEnd = tmp.PortEnd
	if cfg.PortEnd == 0 {
		cfg.PortEnd = 5599
	}
	cfg.ExternalHost = tmp.ExternalHost
	cfg.ServiceToken = tmp.ServiceToken
	cfg.CPUBudget = tmp.CPUBudget
	cfg.MemBudget = tmp.MemBudget
	cfg.WorldsDir = tmp.WorldsDir
	if cfg.WorldsDir == "" {
		// Default mirrors cmd/server/main.go (WORLDS_DIR env fallback).
		// The cell's FS is scoped so the absolute path is meaningless
		// inside WASM; we keep it here for manifest/documentation parity
		// and normalize at use time.
		cfg.WorldsDir = "/var/sessions/worlds"
	}
	cfg.TemplatesDir = tmp.TemplatesDir
	if cfg.TemplatesDir == "" {
		cfg.TemplatesDir = "/app/templates"
	}
	cfg.NodeCPUCores = tmp.NodeCPUCores
	cfg.NodeTotalMem = tmp.NodeTotalMem
	cfg.NodeDiskTotal = tmp.NodeDiskTotal
	cfg.NodeDiskUsed = tmp.NodeDiskUsed
	// runtime.NumCPU inside wasip1 returns the GOMAXPROCS the host
	// configured the WASM runtime with (typically 1), not the real
	// host core count, so we only fall back to it when no explicit
	// value was provided. It's still a better-than-zero default.
	if cfg.NodeCPUCores == 0 {
		cfg.NodeCPUCores = runtime.NumCPU()
	}
	return cfg, nil
}

type createServerRequest struct {
	Template  string            `json:"template"`
	ServerID  string            `json:"server_id,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Resources struct {
		MemoryLimit int64   `json:"memory_limit,omitempty"`
		CPULimit    float64 `json:"cpu_limit,omitempty"`
	} `json:"resources,omitempty"`
}

func bootstrap(configBytes []byte) error {
	cfg, err := parseConfig(configBytes)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	templates, err := loadTemplates(cfg.TemplateFiles)
	if err != nil {
		return fmt.Errorf("load templates: %w", err)
	}

	// Startup config log — mirrors cmd/server/main.go so operators grepping
	// stderr see the same lines across native + cell deployments.
	fmt.Printf("CPU budget: %.2f cores\n", cfg.CPUBudget)
	fmt.Printf("Memory budget: %.2f GiB\n", cfg.MemBudget)
	fmt.Printf("IP pool: %s - %s\n", cfg.IPStart, cfg.IPEnd)
	fmt.Printf("Port pool: %d - %d\n", cfg.PortStart, cfg.PortEnd)
	if cfg.ExternalHost != "" {
		fmt.Printf("External host: %s\n", cfg.ExternalHost)
	}
	fmt.Printf("Loaded %d templates\n", len(templates))

	// Register the SSE route the host will stream container lifecycle
	// events over. The step loop below drains docker.EventsPoll and
	// fans each event into this path via pulp.SSE.Emit. The polling
	// endpoint `GET /orchestration/events` is also kept below for any
	// client that cannot hold an SSE connection.
	if err := pulp.SSE.Register(orchestrationEventsPath); err != nil {
		return fmt.Errorf("register SSE %s: %w", orchestrationEventsPath, err)
	}

	// Cursor the step loop uses to drain docker events. Cell is
	// single-threaded so a plain variable is fine.
	var eventsSinceNanos int64

	// Warn once on startup about missing node descriptors. The operator
	// is expected to fill these via the manifest [config] block because
	// WASM can't inspect the host OS.
	if cfg.NodeTotalMem == 0 {
		fmt.Println("[bananagine] warning: node_total_memory not configured — /orchestration/stats.node.total_memory will be 0")
	}
	if cfg.NodeDiskTotal == 0 {
		fmt.Println("[bananagine] warning: node_disk_total not configured — /orchestration/stats.node.disk_total will be 0")
	}

	capacity := newCapacityTracker(cfg.CPUBudget, cfg.MemBudget)
	ipp := newIPPool(cfg.IPStart, cfg.IPEnd)
	fallback := newPortPool(cfg.PortStart, cfg.PortEnd)
	portPools := newPortPoolSet(fallback)
	reg := newRegistry()

	for _, tmpl := range templates {
		for _, p := range tmpl.Container.Ports {
			if p.Range != "" {
				if _, err := portPools.allocate(p.Range, "__init__"); err == nil {
					portPools.releaseByServer("__init__")
				}
			}
		}
	}

	// Reconcile with already-running containers
	if existing, err := docker.List(nil); err == nil {
		reconciled := 0
		for _, s := range existing {
			for _, p := range s.Ports {
				portPools.reserve(p, s.ID)
			}
			if s.IP != "" {
				ipp.reserve(s.IP, s.ID)
			}
			name := strings.TrimPrefix(s.Name, "/")
			matched := false
			for _, tmpl := range templates {
				if strings.HasPrefix(name, tmpl.Name+"-") {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if s.CPULimit > 0 || s.MemoryLimit > 0 {
				_ = capacity.tryAllocate(s.ID, s.CPULimit, s.MemoryLimit)
				reconciled++
			}
		}
		if len(existing) > 0 {
			fmt.Printf("Reconciled %d existing containers into pools (%d managed)\n", len(existing), reconciled)
			fmt.Printf("Reconciled capacity: %.2f CPU, %.2f GiB memory allocated\n", capacity.allocCPU, capacity.allocMem)
		}
	}

	r := pulpgin.New()

	// --- Health & Templates ---

	r.GET("/health", func(c *pulpgin.Context) {
		if _, err := docker.List(nil); err != nil {
			c.JSON(503, pulpgin.H{"status": "degraded", "docker": "unreachable"})
			return
		}
		c.JSON(200, pulpgin.H{"status": "ok"})
	})

	r.GET("/templates", func(c *pulpgin.Context) {
		type info struct {
			Name        string  `json:"name"`
			Game        string  `json:"game"`
			Label       string  `json:"label"`
			CPULimit    float64 `json:"cpu_limit"`
			MemoryLimit int64   `json:"memory_limit"`
		}
		result := make([]info, 0, len(templates))
		for _, t := range templates {
			result = append(result, info{
				Name:        t.Name,
				Game:        t.Game,
				Label:       t.Label,
				CPULimit:    t.Container.CPULimit,
				MemoryLimit: t.Container.MemoryLimit,
			})
		}
		c.JSON(200, result)
	})

	r.POST("/reload-templates", func(c *pulpgin.Context) {
		fresh, err := loadTemplates(cfg.TemplateFiles)
		if err != nil {
			log.Printf("[Reload] Failed to reload templates: %v", err)
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		templates = fresh
		log.Printf("[Reload] Reloaded %d templates", len(templates))
		c.JSON(200, pulpgin.H{"reloaded": len(templates)})
	})

	r.GET("/templates/:name/config", func(c *pulpgin.Context) {
		name := c.Param("name")
		tmpl, ok := templates[name]
		if !ok {
			c.JSON(404, pulpgin.H{"error": "template not found"})
			return
		}
		c.JSON(200, tmpl.Config)
	})

	// --- Orchestration ---

	// Mirror upstream cmd/server/main.go bootstrap log: announce whether
	// service auth is enforced so operators can spot unprotected deployments.
	if cfg.ServiceToken != "" {
		log.Printf("Service auth enabled (X-Service-Token required)")
	} else {
		log.Printf("WARNING: SERVICE_TOKEN not set — all endpoints are unprotected")
	}

	auth := authMiddleware(cfg.ServiceToken)
	orch := r.Group("/orchestration", auth)

	orch.GET("/servers", func(c *pulpgin.Context) {
		servers, err := docker.List(nil)
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		// Upstream cmd/server initializes `servers := []orchestrator.Server{}`
		// so an empty list marshals as `[]`, not `null`. docker.List returns
		// nil on empty, so coerce before encoding to preserve the wire shape.
		if servers == nil {
			servers = []docker.Server{}
		}
		c.JSON(200, servers)
	})

	orch.GET("/servers/:id", func(c *pulpgin.Context) {
		id := c.Param("id")
		// docker.Get maps to provider.Get (Docker ContainerInspect), which
		// accepts either container ID or name. Matches native cmd/server
		// line 397-410 — returns 404 when the container doesn't exist,
		// 500 on any other Docker error.
		server, err := docker.Get(id)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, server)
	})

	orch.POST("/servers", func(c *pulpgin.Context) {
		var req createServerRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}

		tmpl, ok := templates[req.Template]
		if !ok {
			c.JSON(404, pulpgin.H{"error": "template not found"})
			return
		}

		container := deepCopyContainer(tmpl.Container)

		serverID := req.ServerID
		if serverID == "" {
			serverID = fmt.Sprintf("%s-%d", req.Template, time.Now().UnixNano())
		}

		// Expand volume path templates
		var volumeExpansions [][2]string
		for hostPath, containerPath := range container.Volumes {
			if strings.Contains(hostPath, "{{SERVER_ID}}") {
				volumeExpansions = append(volumeExpansions, [2]string{hostPath, containerPath})
			}
		}
		for _, exp := range volumeExpansions {
			delete(container.Volumes, exp[0])
			container.Volumes[strings.ReplaceAll(exp[0], "{{SERVER_ID}}", serverID)] = exp[1]
		}

		if container.Environment == nil {
			container.Environment = make(map[string]string)
		}
		for k, v := range tmpl.Server {
			container.Environment[k] = v
		}

		var allocatedIP string
		var allocatedPort int

		if container.Network != "" {
			ip, err := ipp.allocate(serverID)
			if err != nil {
				c.JSON(503, pulpgin.H{"error": err.Error()})
				return
			}
			allocatedIP = ip
			container.IP = ip

			allocatedPort = 5520
			if len(container.Ports) > 0 {
				allocatedPort = container.Ports[0].Container
			}

			container.Environment["SERVER_HOST"] = ip
			for _, p := range container.Ports {
				if p.Name != "" {
					container.Environment["PORT_"+strings.ToUpper(p.Name)] = fmt.Sprintf("%d", p.Container)
				}
			}

			fmt.Printf("Overlay mode: %s -> %s:%d\n", serverID, ip, allocatedPort)
		} else {
			var allocatedPorts []int
			for i := range container.Ports {
				port, err := portPools.allocate(container.Ports[i].Range, serverID)
				if err != nil {
					portPools.releaseByServer(serverID)
					c.JSON(503, pulpgin.H{"error": err.Error()})
					return
				}
				allocatedPorts = append(allocatedPorts, port)
				container.Ports[i].Host = port
				container.Ports[i].Container = port
			}
			if len(allocatedPorts) == 0 {
				port, err := portPools.allocate("", serverID)
				if err != nil {
					c.JSON(503, pulpgin.H{"error": err.Error()})
					return
				}
				allocatedPorts = append(allocatedPorts, port)
			}
			allocatedPort = allocatedPorts[0]

			container.Environment["SERVER_HOST"] = "0.0.0.0"
			for i, p := range container.Ports {
				if p.Name != "" {
					container.Environment["PORT_"+strings.ToUpper(p.Name)] = fmt.Sprintf("%d", allocatedPorts[i])
				}
			}

			fmt.Printf("Host mode: %s -> 0.0.0.0:%d\n", serverID, allocatedPort)
		}

		container.Environment["SERVER_PORT"] = fmt.Sprintf("%d", allocatedPort)
		container.Environment["SERVER_ID"] = serverID

		releaseResources := func() {
			if allocatedIP != "" {
				ipp.release(allocatedIP)
			} else {
				portPools.releaseByServer(serverID)
			}
		}

		// Pre-start hook
		if tmpl.Hooks.PreStart != "" {
			fmt.Println("Calling pre_start hook:", tmpl.Hooks.PreStart)
			resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
				Method: "GET",
				URL:    tmpl.Hooks.PreStart,
			})
			if err != nil {
				fmt.Println("Hook error:", err)
				releaseResources()
				c.JSON(500, pulpgin.H{"error": "hook failed: " + err.Error()})
				return
			}
			if resp.Status < 200 || resp.Status >= 300 {
				fmt.Printf("Hook returned status %d\n", resp.Status)
				releaseResources()
				c.JSON(500, pulpgin.H{"error": fmt.Sprintf("hook returned %d", resp.Status)})
				return
			}
			var hookResp struct {
				Env map[string]string `json:"env"`
			}
			if err := json.Unmarshal(resp.Body, &hookResp); err != nil {
				fmt.Println("Hook response decode error:", err)
				releaseResources()
				c.JSON(500, pulpgin.H{"error": "hook response decode failed: " + err.Error()})
				return
			}
			fmt.Println("Hook returned env vars:", hookResp.Env)
			for k, v := range hookResp.Env {
				container.Environment[k] = v
			}
		} else {
			fmt.Println("No pre_start hook defined")
		}

		// Caller env (last wins)
		for k, v := range req.Env {
			container.Environment[k] = v
		}

		if req.Resources.MemoryLimit > 0 {
			container.MemoryLimit = req.Resources.MemoryLimit
		}
		if req.Resources.CPULimit > 0 {
			container.CPULimit = req.Resources.CPULimit
		}

		if err := capacity.tryAllocate(serverID, container.CPULimit, container.MemoryLimit); err != nil {
			releaseResources()
			c.JSON(503, pulpgin.H{"error": err.Error()})
			return
		}

		fmt.Println("Final environment:", container.Environment)

		container.Name = serverID
		server, err := docker.Create(containerToCreateRequest(container))
		if err != nil {
			capacity.release(serverID)
			releaseResources()
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}

		capacity.commit(serverID, server.ID)
		if allocatedIP != "" {
			ipp.reKey(serverID, server.ID)
		} else {
			portPools.reKey(serverID, server.ID)
		}

		server.Name = serverID
		if server.Ports == nil {
			server.Ports = map[string]int{}
		}
		if len(container.Ports) > 0 {
			for _, p := range container.Ports {
				portKey := p.Name
				if portKey == "" {
					portKey = fmt.Sprintf("%d", p.Container)
				}
				if _, ok := server.Ports[portKey]; !ok {
					server.Ports[portKey] = p.Host
				}
			}
		} else {
			portKey := fmt.Sprintf("%d", allocatedPort)
			if _, ok := server.Ports[portKey]; !ok {
				server.Ports[portKey] = allocatedPort
			}
		}

		if cfg.ExternalHost != "" {
			server.IP = cfg.ExternalHost
		}

		c.JSON(201, server)
	})

	orch.DELETE("/servers/:id", func(c *pulpgin.Context) {
		id := c.Param("id")

		capacity.release(id)

		if c.Query("keep_ports") != "1" {
			portPools.releaseByServer(id)
			ipp.releaseByServer(id)
		} else if newID := c.Query("server_id"); newID != "" {
			portPools.reKey(id, newID)
			ipp.reKey(id, newID)
		}

		if err := docker.Destroy(id); err != nil {
			// Idempotent destroy: if the container is already gone (or
			// never existed), surface 204 so callers don't loop. The
			// resource releases above already ran — those are also
			// idempotent. Other docker errors still bubble as 500.
			//
			// The other action endpoints (restart, exec, logs) return
			// 404 here, but DELETE is semantically "make it gone" — if
			// it's already gone, that's success, not a "not found"
			// distinct from the goal. Returning 204 lets Evolution's
			// destroy retry loop terminate cleanly without needing the
			// 5-attempt give-up workaround we shipped on the engine
			// side.
			if isDockerNotFound(err) {
				c.Status(204)
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.Status(204)
	})

	orch.POST("/servers/:id/restart", func(c *pulpgin.Context) {
		id := c.Param("id")
		if err := docker.Restart(id); err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, pulpgin.H{"status": "restarted"})
	})

	orch.POST("/servers/:id/exec", func(c *pulpgin.Context) {
		id := c.Param("id")
		var req struct {
			Cmd []string `json:"cmd"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		if len(req.Cmd) == 0 {
			c.JSON(400, pulpgin.H{"error": "cmd is required"})
			return
		}
		output, err := docker.Exec(id, req.Cmd)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, pulpgin.H{"output": output})
	})

	orch.GET("/servers/:id/logs", func(c *pulpgin.Context) {
		id := c.Param("id")
		tail := 200
		if t := c.Query("tail"); t != "" {
			if n, err := strconv.Atoi(t); err == nil && n > 0 {
				if n > 1000 {
					n = 1000
				}
				tail = n
			}
		}
		logs, err := docker.Logs(id, tail)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, pulpgin.H{"logs": logs})
	})

	orch.GET("/servers/:id/stats", func(c *pulpgin.Context) {
		id := c.Param("id")
		stats, err := docker.Stats(id)
		if err != nil {
			if isDockerNotFound(err) {
				c.JSON(404, pulpgin.H{"error": "server not found"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, stats)
	})

	orch.GET("/stats", func(c *pulpgin.Context) {
		containers, err := docker.StatsAll()
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		allocCPU, allocMem, _ := capacity.snapshot()
		c.JSON(200, pulpgin.H{
			"containers": containers,
			"node": pulpgin.H{
				"cpu_cores":        cfg.NodeCPUCores,
				"total_memory":     cfg.NodeTotalMem,
				"allocated_cpu":    allocCPU,
				"allocated_memory": allocMem,
				"disk_total":       cfg.NodeDiskTotal,
				"disk_used":        cfg.NodeDiskUsed,
			},
		})
	})

	// GET /orchestration/events is wired two ways:
	//
	//   1. As a registered SSE route (see pulp.SSE.Register above). The
	//      step loop below polls docker events and fans each one into
	//      this path via pulp.SSE.Emit, so clients holding an SSE
	//      connection see events in real time — this mirrors the
	//      original cmd/server streaming behaviour.
	//
	//   2. As a plain GET that returns the current poll window as a JSON
	//      array. Kept for backward compatibility with any client that
	//      cannot hold an SSE connection. Accepts ?since=<nanos> and
	//      ?limit=<n> query parameters.
	//
	// The pulpgin router does not know about SSE, so a GET hitting this
	// path without the SSE-upgrade negotiation will reach the JSON
	// handler here; SSE-upgraded clients are peeled off by the host
	// before the request ever hits the cell.
	orch.GET("/events", func(c *pulpgin.Context) {
		var sinceNanos int64
		if s := c.Query("since"); s != "" {
			sinceNanos, _ = strconv.ParseInt(s, 10, 64)
		}
		limit := 100
		if l := c.Query("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				limit = n
			}
		}
		events, err := docker.EventsPoll(sinceNanos, limit)
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		// Convert to wire shape so field names match native SSE output.
		wire := make([]wireEvent, 0, len(events))
		for _, e := range events {
			wire = append(wire, toWireEvent(e))
		}
		c.JSON(200, wire)
	})

	// World operations — use host FS.
	//
	// Streaming note: the upstream service streamed the zip directly to
	// c.Writer (archive/zip writing frames to the HTTP socket). The Pulp
	// runtime hands responses back to the host as a single buffered body
	// per HTTPResponse envelope, so streaming is not available here. We
	// therefore buffer the zip in memory and hand it off with a single
	// c.Data allocation. Sessions worlds are capped at ~10GB on disk and
	// typically <1GB zipped (see technical-2026-03-22); acceptable for
	// now. Revisit if streaming chunked HTTP responses land in pulpgin.
	//
	// Path resolution: cfg.WorldsDir mirrors the native WORLDS_DIR env var
	// (defaults to /var/sessions/worlds). The cell's FS is scoped, so the
	// leading slash is stripped at use time — the operator is expected to
	// mount the world root at that path inside the cell's storage
	// namespace. Empty/root-only WorldsDir falls back to "worlds".
	worldsRoot := resolveWorldsRoot(cfg.WorldsDir)

	orch.GET("/worlds/:name", func(c *pulpgin.Context) {
		name := c.Param("name")
		if strings.Contains(name, "..") || strings.Contains(name, "/") {
			c.JSON(400, pulpgin.H{"error": "invalid name"})
			return
		}
		worldDir := worldsRoot + "/" + name
		_, err := pulp.FS.List(worldDir)
		if err != nil {
			c.JSON(404, pulpgin.H{"error": "world not found"})
			return
		}

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := walkAndZip(zw, worldDir, ""); err != nil {
			log.Printf("Error walking world directory %s: %v", worldDir, err)
			c.JSON(500, pulpgin.H{"error": "failed to build zip: " + err.Error()})
			return
		}
		if err := zw.Close(); err != nil {
			c.JSON(500, pulpgin.H{"error": "failed to close zip: " + err.Error()})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.zip", name))
		// c.Data sets Content-Type and hands the bytes to the host
		// directly — no intermediate copy beyond the single zip buffer.
		c.Data(200, "application/zip", buf.Bytes())
	})

	orch.POST("/worlds/:name/apply-gamerules", func(c *pulpgin.Context) {
		name := c.Param("name")
		if strings.Contains(name, "..") || strings.Contains(name, "/") {
			c.JSON(400, pulpgin.H{"error": "invalid name"})
			return
		}
		path := worldsRoot + "/" + name + "/.sessions-apply-gamerules"
		if err := pulp.FS.Write(path, []byte("1")); err != nil {
			c.JSON(500, pulpgin.H{"error": "failed to write flag: " + err.Error()})
			return
		}
		c.JSON(200, pulpgin.H{"ok": true})
	})

	orch.DELETE("/worlds/:name", func(c *pulpgin.Context) {
		name := c.Param("name")
		if strings.Contains(name, "..") || strings.Contains(name, "/") {
			c.JSON(400, pulpgin.H{"error": "invalid name"})
			return
		}
		path := worldsRoot + "/" + name
		// pulp.FS.RemoveAll mirrors os.RemoveAll semantics — missing path is
		// not an error, matching native cmd/server/main.go:930 which relied
		// on os.RemoveAll's nil-on-missing behaviour. The earlier hand-rolled
		// recursion surfaced ErrNotFound as a 500, diverging from native.
		if err := pulp.FS.RemoveAll(path); err != nil {
			c.JSON(500, pulpgin.H{"error": "failed to remove world: " + err.Error()})
			return
		}
		c.Status(204)
	})

	// --- Registry ---

	regGroup := r.Group("/registry", auth)

	regGroup.POST("/servers", func(c *pulpgin.Context) {
		var s serverInfo
		if err := c.ShouldBindJSON(&s); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		if err := reg.register(s); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(201, s)
	})

	regGroup.GET("/servers", func(c *pulpgin.Context) {
		f := &listFilter{}
		if t := c.Query("type"); t != "" {
			f.Type = serverType(t)
		}
		if m := c.Query("mode"); m != "" {
			f.Mode = m
		}
		if c.Query("hasCapacity") == "true" {
			f.HasCapacity = true
		}
		if c.Query("hasReadyMatch") == "true" {
			f.HasReadyMatch = true
		}
		c.JSON(200, reg.list(f))
	})

	regGroup.GET("/servers/:id", func(c *pulpgin.Context) {
		id := c.Param("id")
		s, ok := reg.get(id)
		if !ok {
			c.JSON(404, pulpgin.H{"error": "server not found"})
			return
		}
		c.JSON(200, s)
	})

	regGroup.PUT("/servers/:id", func(c *pulpgin.Context) {
		id := c.Param("id")
		var updates struct {
			Players    *int              `json:"players"`
			MaxPlayers *int              `json:"maxPlayers"`
			Metadata   map[string]string `json:"metadata"`
		}
		if err := c.ShouldBindJSON(&updates); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		err := reg.update(id, func(s *serverInfo) {
			if updates.Players != nil {
				s.Players = *updates.Players
			}
			if updates.MaxPlayers != nil {
				s.MaxPlayers = *updates.MaxPlayers
			}
			if updates.Metadata != nil {
				s.Metadata = updates.Metadata
			}
		})
		if err != nil {
			c.JSON(404, pulpgin.H{"error": err.Error()})
			return
		}
		s, _ := reg.get(id)
		c.JSON(200, s)
	})

	regGroup.DELETE("/servers/:id", func(c *pulpgin.Context) {
		id := c.Param("id")
		reg.unregister(id)
		c.Status(204)
	})

	regGroup.PUT("/servers/:id/players", func(c *pulpgin.Context) {
		id := c.Param("id")
		var req struct {
			Players int `json:"players"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		err := reg.update(id, func(s *serverInfo) {
			s.Players = req.Players
		})
		if err != nil {
			c.JSON(404, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, pulpgin.H{"status": "ok"})
	})

	regGroup.PUT("/servers/:id/matches/:matchId", func(c *pulpgin.Context) {
		serverID := c.Param("id")
		matchID := c.Param("matchId")
		var m matchInfo
		if err := c.ShouldBindJSON(&m); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		if err := reg.updateMatch(serverID, matchID, m); err != nil {
			c.JSON(404, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(200, m)
	})

	regGroup.DELETE("/servers/:id/matches/:matchId", func(c *pulpgin.Context) {
		serverID := c.Param("id")
		matchID := c.Param("matchId")
		if err := reg.removeMatch(serverID, matchID); err != nil {
			c.JSON(404, pulpgin.H{"error": err.Error()})
			return
		}
		c.Status(204)
	})

	// --- Admin ---

	admin := r.Group("/admin", auth)

	admin.POST("/build-image", func(c *pulpgin.Context) {
		var req struct {
			BuildArgs map[string]string `json:"build_args"`
			ImageTag  string            `json:"image_tag"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		if req.ImageTag == "" {
			c.JSON(400, pulpgin.H{"error": "image_tag required"})
			return
		}
		// Upstream read TEMPLATES_DIR from env, falling back to
		// /app/templates. os.Getenv is not reachable from wasip1 with the
		// pulp host surface, so the value is threaded through appConfig
		// (manifest [config].templates_dir) with the same default.
		//
		// Before issuing the host build, poll status. The ext-docker host
		// returns code 4 (generic "docker api error") for both TryLock
		// failure AND arbitrary runtime errors, so we can't distinguish
		// them after the fact. Instead, check the in-flight flag and
		// return 409 proactively — matching cmd/server/main.go:1120-1123
		// which short-circuits on buildMu.TryLock before dispatching.
		if status, serr := docker.GetBuildStatus(); serr == nil && status.Building {
			c.JSON(409, pulpgin.H{"error": "build already in progress"})
			return
		}
		if err := docker.Build(docker.BuildRequest{
			BuildArgs: req.BuildArgs,
			ImageTag:  req.ImageTag,
			BuildDir:  cfg.TemplatesDir,
		}); err != nil {
			// Race: another caller may have started a build between our
			// status check and this call. The host signals that narrow
			// case with code 11 → docker.ErrBuildInProgress. Only that
			// maps to 409; every other error (invalid request, generic
			// docker api failure, capability missing) is a true 500.
			// Earlier versions string-matched "api error" which also
			// matched code 4 (generic docker failures), wrongly returning
			// 409 for e.g. image-not-found — native returned 500 there.
			if errors.Is(err, docker.ErrBuildInProgress) {
				c.JSON(409, pulpgin.H{"error": "build already in progress"})
				return
			}
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(202, pulpgin.H{"building": true})
	})

	admin.GET("/build-status", func(c *pulpgin.Context) {
		status, err := docker.GetBuildStatus()
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		// Upstream marshalled a struct with LastBuildTime time.Time —
		// which Go's json encoder emits as an RFC3339 string. The host
		// capability hands us unix SECONDS (int64) instead, so convert
		// back to time.Time before returning. Unset (0) becomes the zero
		// value, which marshals to "0001-01-01T00:00:00Z" — same as the
		// original. See Pulp-ext-docker/docker.go: writes time.Now().Unix().
		var lastBuild time.Time
		if status.LastBuildTime != 0 {
			lastBuild = time.Unix(status.LastBuildTime, 0).UTC()
		}
		c.JSON(200, struct {
			Building      bool      `json:"building"`
			LastBuildTime time.Time `json:"last_build_time"`
			LastError     string    `json:"last_error"`
		}{
			Building:      status.Building,
			LastBuildTime: lastBuild,
			LastError:     status.LastError,
		})
	})

	// Register and wire step.
	//
	// Each step we:
	//   1. Drain any container events that have accumulated since the
	//      last cursor and fan them out over the SSE route. Events are
	//      polled (not pushed) because WASM can't hold a long-lived
	//      Docker events connection — the host buffers them for us.
	//   2. Forward the event itself to the pulpgin engine so HTTP and
	//      WS traffic gets dispatched normally.
	//
	// Everything runs on the single cell goroutine, which is why the
	// ports/ips/registry/capacity trackers do not need mutexes.
	if err := r.RegisterRoutes(); err != nil {
		return fmt.Errorf("register routes: %w", err)
	}
	pulp.OnStep(func(ev pulp.StepEvent) error {
		// Drain docker events every step — even when nobody is subscribed
		// — so the SSE cursor advances. Native cmd/server subscribes to
		// provider.Events at SSE connect time and only sees live events
		// from that point forward; replaying the host ring buffer on first
		// subscribe would flood new clients with up to 5 minutes of
		// backlog they never asked for. By draining-and-discarding when
		// there are no subscribers we keep cursor semantics aligned with
		// native's "live from connect" behaviour.
		hasSubs := pulp.SSE.HasSubscribers(orchestrationEventsPath)
		events, err := docker.EventsPoll(eventsSinceNanos, 100)
		if err == nil {
			for _, de := range events {
				if de.Timestamp > eventsSinceNanos {
					eventsSinceNanos = de.Timestamp
				}
				if !hasSubs {
					continue
				}
				// Emit the wire shape (json-tagged) — native SSE streamed
				// orchestrator/providers/docker.ContainerEvent, which uses
				// container_id/name/action/time; docker.Event on the cell
				// side has msgpack tags only, which would serialize to
				// capitalized Go field names.
				payload, err := json.Marshal(toWireEvent(de))
				if err != nil {
					continue
				}
				if err := pulp.SSE.Emit(orchestrationEventsPath, "", "", string(payload)); err != nil {
					log.Printf("[Events] SSE emit failed: %v", err)
				}
			}
		}
		return r.Dispatch(ev)
	})

	fmt.Printf("[bananagine] ready — %d templates, cpu_budget=%.1f mem_budget=%.1f\n",
		len(templates), cfg.CPUBudget, cfg.MemBudget)
	return nil
}

// isDockerNotFound best-effort maps an error returned by the pulp/docker
// capability to "not found" so handlers can respond 404 instead of 500.
//
// Upstream (cmd/server/main.go) used errdefs.IsNotFound on a Docker
// client error. The pulp/docker wrapper either returns pulp.ErrNotFound
// (host code 6, used by docker_get) or flattens the Docker error into an
// opaque "docker api error" (code 4) with the message surfaced by the
// host. Match both so handlers can respond 404.
func isDockerNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pulp.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such container")
}

// resolveWorldsRoot maps the operator-facing cfg.WorldsDir to a cell-FS
// relative prefix. The cell's storage capability is scoped so absolute
// paths are meaningless — strip any leading slashes, trim trailing
// slashes, and fall back to "worlds" when the result would be empty.
// This preserves the cmd/server WORLDS_DIR semantic (operator decides
// where worlds live) without requiring a non-scoped FS capability.
func resolveWorldsRoot(worldsDir string) string {
	trimmed := strings.Trim(worldsDir, "/")
	if trimmed == "" {
		return "worlds"
	}
	return trimmed
}

func walkAndZip(zw *zip.Writer, dir, prefix string) error {
	entries, err := pulp.FS.List(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		relPath := prefix + e.Name
		fullPath := dir + "/" + e.Name
		if e.IsDir {
			if err := walkAndZip(zw, fullPath, relPath+"/"); err != nil {
				return err
			}
			continue
		}
		data, err := pulp.FS.Read(fullPath)
		if err != nil {
			return err
		}
		w, err := zw.Create(relPath)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func authMiddleware(token string) pulpgin.HandlerFunc {
	if token == "" {
		return func(c *pulpgin.Context) { c.Next() }
	}
	return middleware.ServiceAuth(token)
}
