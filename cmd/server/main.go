package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bananalabs-oss/bananagine/internal/ips"
	"github.com/bananalabs-oss/bananagine/internal/ports"
	"github.com/bananalabs-oss/bananagine/internal/template"
	potconfig "github.com/bananalabs-oss/potassium/config"
	"github.com/bananalabs-oss/potassium/orchestrator"
	"github.com/bananalabs-oss/potassium/server"
	"github.com/bananalabs-oss/potassium/orchestrator/providers/docker"
	"github.com/bananalabs-oss/potassium/registry"
	"github.com/containerd/errdefs"
	"github.com/gin-gonic/gin"
)

type CreateServerRequest struct {
	Template  string            `json:"template"`
	ServerID  string            `json:"server_id,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Resources struct {
		MemoryLimit int64   `json:"memory_limit,omitempty"`
		CPULimit    float64 `json:"cpu_limit,omitempty"`
	} `json:"resources,omitempty"`
}

// capacityTracker tracks allocated CPU and memory across active containers.
type capacityTracker struct {
	mu         sync.Mutex
	cpuBudget  float64
	memBudget  float64 // GiB
	allocCPU   float64
	allocMem   float64 // GiB
	// Per-container resources for subtraction on delete
	containers map[string]struct{ cpu float64; memGiB float64 }
}

func newCapacityTracker(cpuBudget, memBudget float64) *capacityTracker {
	return &capacityTracker{
		cpuBudget:  cpuBudget,
		memBudget:  memBudget,
		containers: make(map[string]struct{ cpu float64; memGiB float64 }),
	}
}

func (ct *capacityTracker) tryAllocate(containerID string, cpuLimit float64, memLimitBytes int64) error {
	memGiB := float64(memLimitBytes) / (1024 * 1024 * 1024)
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.cpuBudget > 0 && ct.allocCPU+cpuLimit > ct.cpuBudget {
		return fmt.Errorf("CPU capacity exceeded (%.2f + %.2f > %.2f)", ct.allocCPU, cpuLimit, ct.cpuBudget)
	}
	if ct.memBudget > 0 && ct.allocMem+memGiB > ct.memBudget {
		return fmt.Errorf("memory capacity exceeded (%.2f + %.2f > %.2f GiB)", ct.allocMem, memGiB, ct.memBudget)
	}
	ct.allocCPU += cpuLimit
	ct.allocMem += memGiB
	ct.containers[containerID] = struct{ cpu float64; memGiB float64 }{cpuLimit, memGiB}
	return nil
}

func (ct *capacityTracker) commit(tempID, realID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if res, ok := ct.containers[tempID]; ok {
		delete(ct.containers, tempID)
		ct.containers[realID] = res
	}
}

func (ct *capacityTracker) release(containerID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if res, ok := ct.containers[containerID]; ok {
		ct.allocCPU -= res.cpu
		ct.allocMem -= res.memGiB
		delete(ct.containers, containerID)
	}
}

func main() {
	// CLI flags
	listenAddr := flag.String("listen", "", "Listen address (default :3000)")
	templatesDir := flag.String("templates", "", "Templates directory (default ./templates)")
	ipStart := flag.String("ip-start", "", "IP pool start (default 10.99.0.10)")
	ipEnd := flag.String("ip-end", "", "IP pool end (default 10.99.0.250)")
	portStart := flag.Int("port-start", 0, "Port pool start (default 5521)")
	portEnd := flag.Int("port-end", 0, "Port pool end (default 5599)")
	externalHost := flag.String("external-host", "", "External host address for host-mode containers")
	flag.Parse()

	// Resolve: CLI > Env > Default
	config := struct {
		ListenAddr   string
		TemplatesDir string
		IPStart      string
		IPEnd        string
		PortStart    int
		PortEnd      int
		ExternalHost string
	}{
		ListenAddr:   potconfig.Resolve(*listenAddr, potconfig.EnvOrDefault("LISTEN_ADDR", ""), ":3000"),
		TemplatesDir: potconfig.Resolve(*templatesDir, potconfig.EnvOrDefault("TEMPLATES_DIR", ""), "./templates"),
		IPStart:      potconfig.Resolve(*ipStart, potconfig.EnvOrDefault("IP_POOL_START", ""), "10.99.0.10"),
		IPEnd:        potconfig.Resolve(*ipEnd, potconfig.EnvOrDefault("IP_POOL_END", ""), "10.99.0.250"),
		PortStart:    potconfig.ResolveInt(*portStart, potconfig.EnvOrDefaultInt("PORT_POOL_START", 0), 5521),
		PortEnd:      potconfig.ResolveInt(*portEnd, potconfig.EnvOrDefaultInt("PORT_POOL_END", 0), 5599),
		ExternalHost: potconfig.Resolve(*externalHost, potconfig.EnvOrDefault("EXTERNAL_HOST", ""), ""),
	}

	cpuBudget := potconfig.EnvOrDefaultFloat("CPU_BUDGET", 0)  // 0 = no limit
	memBudget := potconfig.EnvOrDefaultFloat("MEMORY_BUDGET", 0) // 0 = no limit
	capacity := newCapacityTracker(cpuBudget, memBudget)

	// Log config
	fmt.Printf("Listen: %s\n", config.ListenAddr)
	fmt.Printf("Templates: %s\n", config.TemplatesDir)
	fmt.Printf("CPU budget: %.2f cores\n", cpuBudget)
	fmt.Printf("Memory budget: %.2f GiB\n", memBudget)
	fmt.Printf("IP pool: %s - %s\n", config.IPStart, config.IPEnd)
	fmt.Printf("Port pool: %d - %d\n", config.PortStart, config.PortEnd)
	if config.ExternalHost != "" {
		fmt.Printf("External host: %s\n", config.ExternalHost)
	}

	// Load templates at startup
	templates, err := template.LoadTemplates(config.TemplatesDir)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Loaded %d templates\n", len(templates))

	// Start docker provider
	provider, err := docker.New()
	if err != nil {
		panic(err)
	}

	ipPool := ips.NewPool(config.IPStart, config.IPEnd)
	fallbackPool := ports.NewPool(config.PortStart, config.PortEnd)
	portPools := ports.NewPoolSet(fallbackPool)

	// Pre-create pools from template port ranges so reconciliation works
	for _, tmpl := range templates {
		for _, p := range tmpl.Container.Ports {
			if p.Range != "" {
				if _, err := portPools.Allocate(p.Range, "__init__"); err == nil {
					// Pool created; release the dummy allocation
					portPools.ReleaseByServer("__init__")
				}
			}
		}
	}

	// Reconcile pools and capacity with already-running containers
	if existing, err := provider.List(context.Background(), nil); err == nil {
		for _, s := range existing {
			for _, p := range s.Ports {
				portPools.Reserve(p, s.ID)
			}
			if s.IP != "" {
				ipPool.Reserve(s.IP, s.ID)
			}
			// Reconcile capacity: match container to template by name prefix
			for tName, tmpl := range templates {
				if strings.HasPrefix(s.Name, tName) {
					cpuLim := tmpl.Container.CPULimit
					memLim := tmpl.Container.MemoryLimit
					if cpuLim > 0 || memLim > 0 {
						_ = capacity.tryAllocate(s.ID, cpuLim, memLim)
					}
					break
				}
			}
		}
		if len(existing) > 0 {
			fmt.Printf("Reconciled %d existing containers into pools\n", len(existing))
			fmt.Printf("Reconciled capacity: %.2f CPU, %.2f GiB memory allocated\n", capacity.allocCPU, capacity.allocMem)
		}
	}

	reg, err := registry.New()
	if err != nil {
		panic(err)
	}

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// List all loaded templates with game, label, and resource metadata
	type templateInfo struct {
		Name        string  `json:"name"`
		Game        string  `json:"game"`
		Label       string  `json:"label"`
		CPULimit    float64 `json:"cpu_limit"`
		MemoryLimit int64   `json:"memory_limit"`
	}
	r.GET("/templates", func(c *gin.Context) {
		result := make([]templateInfo, 0, len(templates))
		for _, t := range templates {
			result = append(result, templateInfo{
				Name:        t.Name,
				Game:        t.Game,
				Label:       t.Label,
				CPULimit:    t.Container.CPULimit,
				MemoryLimit: t.Container.MemoryLimit,
			})
		}
		c.JSON(200, result)
	})

	// Template config endpoint
	r.GET("/templates/:name/config", func(c *gin.Context) {
		name := c.Param("name")
		tmpl, ok := templates[name]
		if !ok {
			c.JSON(404, gin.H{"error": "template not found"})
			return
		}
		c.JSON(200, tmpl.Config)
	})

	// Orchestration routes
	orchestration := r.Group("/orchestration")
	{
		orchestration.GET("/servers", func(c *gin.Context) {
			ctx := c.Request.Context()
			servers, err := provider.List(ctx, nil)
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, servers)
		})

		orchestration.GET("/servers/:id", func(c *gin.Context) {
			ctx := c.Request.Context()
			id := c.Param("id")
			server, err := provider.Get(ctx, id)
			if err != nil {
				if errdefs.IsNotFound(err) {
					c.JSON(404, gin.H{"error": "server not found"})
					return
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, server)
		})

		orchestration.POST("/servers", func(c *gin.Context) {
			var req CreateServerRequest

			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			// Look up template
			tmpl, ok := templates[req.Template]
			if !ok {
				c.JSON(404, gin.H{"error": "template not found"})
				return
			}

			// Deep copy so we don't mutate the original template
			container := deepCopyAllocateRequest(tmpl.Container)

			// Use provided server ID or generate one
			serverID := req.ServerID
			if serverID == "" {
				serverID = fmt.Sprintf("%s-%d", req.Template, time.Now().UnixNano())
			}

			// Expand volume path templates (e.g. {{SERVER_ID}})
			// Collect changes first to avoid modifying map during iteration
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

			// Merge server config into environment
			if container.Environment == nil {
				container.Environment = make(map[string]string)
			}
			for k, v := range tmpl.Server {
				container.Environment[k] = v
			}

			var allocatedIP string
			var allocatedPort int

			if container.Network != "" {
				// Overlay mode - static IP
				ip, err := ipPool.Allocate(serverID)
				if err != nil {
					c.JSON(503, gin.H{"error": err.Error()})
					return
				}
				allocatedIP = ip
				container.IP = ip

				// Get port from template (default 5520)
				allocatedPort = 5520
				if len(container.Ports) > 0 {
					allocatedPort = container.Ports[0].Container
				}

				container.Environment["SERVER_HOST"] = ip

				// Inject PORT_{NAME} env vars for named ports (overlay uses container port)
				for _, p := range container.Ports {
					if p.Name != "" {
						key := "PORT_" + strings.ToUpper(p.Name)
						container.Environment[key] = fmt.Sprintf("%d", p.Container)
					}
				}

				fmt.Printf("Overlay mode: %s -> %s:%d\n", serverID, ip, allocatedPort)
			} else {
				// Host mode - allocate each port from its own range (or fallback pool)
				var allocatedPorts []int
				for i := range container.Ports {
					port, err := portPools.Allocate(container.Ports[i].Range, serverID)
					if err != nil {
						// Release any ports we already allocated for this server
						portPools.ReleaseByServer(serverID)
						c.JSON(503, gin.H{"error": err.Error()})
						return
					}
					allocatedPorts = append(allocatedPorts, port)
					container.Ports[i].Host = port
					container.Ports[i].Container = port
				}
				if len(allocatedPorts) == 0 {
					port, err := portPools.Allocate("", serverID)
					if err != nil {
						c.JSON(503, gin.H{"error": err.Error()})
						return
					}
					allocatedPorts = append(allocatedPorts, port)
				}
				allocatedPort = allocatedPorts[0]

				container.Environment["SERVER_HOST"] = "0.0.0.0"

				// Inject PORT_{NAME} env vars for named ports
				for i, p := range container.Ports {
					if p.Name != "" {
						key := "PORT_" + strings.ToUpper(p.Name)
						container.Environment[key] = fmt.Sprintf("%d", allocatedPorts[i])
					}
				}

				fmt.Printf("Host mode: %s -> 0.0.0.0:%d\n", serverID, allocatedPort)
			}

			container.Environment["SERVER_PORT"] = fmt.Sprintf("%d", allocatedPort)
			container.Environment["SERVER_ID"] = serverID

			if tmpl.Hooks.PreStart != "" {
				fmt.Println("Calling pre_start hook:", tmpl.Hooks.PreStart)

				// Call the hook URL
				resp, err := http.Get(tmpl.Hooks.PreStart)
				if err != nil {
					fmt.Println("Hook error:", err)
					// Release allocated resources before returning
					if allocatedIP != "" {
						ipPool.Release(allocatedIP)
					} else {
						portPools.ReleaseByServer(serverID)
					}
					c.JSON(500, gin.H{"error": "hook failed: " + err.Error()})
					return
				}
				defer resp.Body.Close()

				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					fmt.Printf("Hook returned status %d\n", resp.StatusCode)
					if allocatedIP != "" {
						ipPool.Release(allocatedIP)
					} else {
						portPools.ReleaseByServer(serverID)
					}
					c.JSON(500, gin.H{"error": fmt.Sprintf("hook returned %d", resp.StatusCode)})
					return
				}

				// Parse response
				var hookResp struct {
					Env map[string]string `json:"env"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&hookResp); err != nil {
					fmt.Println("Hook response decode error:", err)
					// Release allocated resources before returning
					if allocatedIP != "" {
						ipPool.Release(allocatedIP)
					} else {
						portPools.ReleaseByServer(serverID)
					}
					c.JSON(500, gin.H{"error": "hook response decode failed: " + err.Error()})
					return
				}

				fmt.Println("Hook returned env vars:", hookResp.Env)

				// Merge into container env
				for k, v := range hookResp.Env {
					container.Environment[k] = v
				}
			} else {
				fmt.Println("No pre_start hook defined")
			}

			// Merge caller env (last wins)
			for k, v := range req.Env {
				container.Environment[k] = v
			}

			// Wire resource limits from request
			if req.Resources.MemoryLimit > 0 {
				container.MemoryLimit = req.Resources.MemoryLimit
			}
			if req.Resources.CPULimit > 0 {
				container.CPULimit = req.Resources.CPULimit
			}

			// Capacity check — reject if budgets would be exceeded
			if err := capacity.tryAllocate(serverID, container.CPULimit, container.MemoryLimit); err != nil {
				if allocatedIP != "" {
					ipPool.Release(allocatedIP)
				} else {
					portPools.ReleaseByServer(serverID)
				}
				c.JSON(503, gin.H{"error": err.Error()})
				return
			}

			fmt.Println("Final environment:", container.Environment)

			ctx := c.Request.Context()
			server, err := provider.Allocate(ctx, container)
			if err != nil {
				capacity.release(serverID)
				if allocatedIP != "" {
					ipPool.Release(allocatedIP)
				} else {
					portPools.ReleaseByServer(serverID)
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			// Re-key pool and capacity allocations from serverID to container ID so DELETE can release them
			capacity.commit(serverID, server.ID)
			if allocatedIP != "" {
				ipPool.ReKey(serverID, server.ID)
			} else {
				portPools.ReKey(serverID, server.ID)
			}

			// Add metadata to response
			server.Name = serverID

			// Ensure all allocated ports are in response
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

			// Override IP with external host when configured (host-mode hosting)
			if config.ExternalHost != "" {
				server.IP = config.ExternalHost
			}

			c.JSON(201, server)
		})

		orchestration.DELETE("/servers/:id", func(c *gin.Context) {
			ctx := c.Request.Context()
			id := c.Param("id")

			// Release capacity budget
			capacity.release(id)

			// keep_ports=1 preserves port reservations (for recreate)
			if c.Query("keep_ports") != "1" {
				portPools.ReleaseByServer(id)
				ipPool.ReleaseByServer(id)
			} else if newID := c.Query("server_id"); newID != "" {
				// Re-key ports to the new server ID so AllocateN can find them
				portPools.ReKey(id, newID)
				ipPool.ReKey(id, newID)
			}

			err := provider.Deallocate(ctx, id)
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			c.Status(204)
		})

		orchestration.POST("/servers/:id/restart", func(c *gin.Context) {
			ctx := c.Request.Context()
			id := c.Param("id")

			err := provider.Restart(ctx, id)
			if err != nil {
				if errdefs.IsNotFound(err) {
					c.JSON(404, gin.H{"error": "server not found"})
					return
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			c.JSON(200, gin.H{"status": "restarted"})
		})

		orchestration.POST("/servers/:id/exec", func(c *gin.Context) {
			ctx := c.Request.Context()
			id := c.Param("id")

			var req struct {
				Cmd []string `json:"cmd"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			if len(req.Cmd) == 0 {
				c.JSON(400, gin.H{"error": "cmd is required"})
				return
			}

			output, err := provider.Exec(ctx, id, req.Cmd)
			if err != nil {
				if errdefs.IsNotFound(err) {
					c.JSON(404, gin.H{"error": "server not found"})
					return
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			c.JSON(200, gin.H{"output": output})
		})

		// GET /orchestration/servers/:id/logs - retrieve container log output
		orchestration.GET("/servers/:id/logs", func(c *gin.Context) {
			ctx := c.Request.Context()
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

			logs, err := provider.Logs(ctx, id, tail)
			if err != nil {
				if errdefs.IsNotFound(err) {
					c.JSON(404, gin.H{"error": "server not found"})
					return
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			c.JSON(200, gin.H{"logs": logs})
		})

		// GET /orchestration/worlds/:name - zip and stream a server's world data
		orchestration.GET("/worlds/:name", func(c *gin.Context) {
			name := c.Param("name")
			worldsBase := os.Getenv("WORLDS_DIR")
			if worldsBase == "" {
				worldsBase = "/var/sessions/worlds"
			}
			worldDir := filepath.Join(worldsBase, name)

			info, err := os.Stat(worldDir)
			if err != nil || !info.IsDir() {
				c.JSON(404, gin.H{"error": "world not found"})
				return
			}

			c.Header("Content-Type", "application/zip")
			c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.zip", name))

			zw := zip.NewWriter(c.Writer)
			defer zw.Close()

			if err := filepath.Walk(worldDir, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				rel, err := filepath.Rel(worldDir, path)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel) // Ensure forward slashes for Linux containers
				w, err := zw.Create(rel)
				if err != nil {
					return err
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(w, f)
				return err
			}); err != nil {
				log.Printf("Error walking world directory %s: %v", worldDir, err)
			}
		})

		// DELETE /orchestration/worlds/:name - remove a server's world data from disk
		orchestration.DELETE("/worlds/:name", func(c *gin.Context) {
			name := c.Param("name")
			worldsBase := os.Getenv("WORLDS_DIR")
			if worldsBase == "" {
				worldsBase = "/var/sessions/worlds"
			}
			worldDir := filepath.Join(worldsBase, name)

			// Safety: ensure the resolved path is under worldsBase
			absWorld, _ := filepath.Abs(worldDir)
			absBase, _ := filepath.Abs(worldsBase)
			if !strings.HasPrefix(absWorld, absBase+string(filepath.Separator)) {
				c.JSON(400, gin.H{"error": "invalid world name"})
				return
			}

			if err := os.RemoveAll(worldDir); err != nil {
				c.JSON(500, gin.H{"error": "failed to remove world: " + err.Error()})
				return
			}

			c.Status(204)
		})
	}

	// Registry routes
	registryGroup := r.Group("/registry")
	{
		registryGroup.POST("/servers", func(c *gin.Context) {
			// register server
			var server registry.ServerInfo
			if err := c.ShouldBindJSON(&server); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			if err := reg.Register(server); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			c.JSON(201, server)
		})

		registryGroup.GET("/servers", func(c *gin.Context) {
			// list servers
			filter := &registry.ListFilter{}

			// Parse query params
			if t := c.Query("type"); t != "" {
				filter.Type = registry.ServerType(t)
			}
			if m := c.Query("mode"); m != "" {
				filter.Mode = m
			}
			if c.Query("hasCapacity") == "true" {
				filter.HasCapacity = true
			}
			if c.Query("hasReadyMatch") == "true" {
				filter.HasReadyMatch = true
			}

			servers := reg.List(filter)
			c.JSON(200, servers)
		})

		registryGroup.GET("/servers/:id", func(c *gin.Context) {
			// get server
			id := c.Param("id")
			server, ok := reg.Get(id)
			if !ok {
				c.JSON(404, gin.H{"error": "server not found"})
				return
			}
			c.JSON(200, server)
		})

		registryGroup.PUT("/servers/:id", func(c *gin.Context) {
			// update server
			id := c.Param("id")

			var updates struct {
				Players    *int              `json:"players"`
				MaxPlayers *int              `json:"maxPlayers"`
				Metadata   map[string]string `json:"metadata"`
			}

			if err := c.ShouldBindJSON(&updates); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			err := reg.Update(id, func(s *registry.ServerInfo) {
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
				c.JSON(404, gin.H{"error": err.Error()})
				return
			}

			server, _ := reg.Get(id)
			c.JSON(200, server)
		})

		registryGroup.DELETE("/servers/:id", func(c *gin.Context) {
			// unregister server
			id := c.Param("id")
			reg.Unregister(id)
			c.Status(204)
		})

		registryGroup.PUT("/servers/:id/players", func(c *gin.Context) {
			serverID := c.Param("id")

			var req struct {
				Players int `json:"players"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			err := reg.Update(serverID, func(s *registry.ServerInfo) {
				s.Players = req.Players
			})
			if err != nil {
				c.JSON(404, gin.H{"error": err.Error()})
				return
			}

			c.JSON(200, gin.H{"status": "ok"})
		})

		registryGroup.PUT("/servers/:id/matches/:matchId", func(c *gin.Context) {
			// update match
			serverID := c.Param("id")
			matchID := c.Param("matchId")

			var match registry.MatchInfo
			if err := c.ShouldBindJSON(&match); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			if err := reg.UpdateMatch(serverID, matchID, match); err != nil {
				c.JSON(404, gin.H{"error": err.Error()})
				return
			}

			c.JSON(200, match)
		})

		registryGroup.DELETE("/servers/:id/matches/:matchId", func(c *gin.Context) {
			// remove match
			serverID := c.Param("id")
			matchID := c.Param("matchId")

			if err := reg.RemoveMatch(serverID, matchID); err != nil {
				c.JSON(404, gin.H{"error": err.Error()})
				return
			}

			c.Status(204)
		})
	}

	server.ListenAndShutdown(config.ListenAddr, r, "Bananagine")
}

func deepCopyAllocateRequest(src orchestrator.AllocateRequest) orchestrator.AllocateRequest {
	dst := src
	if src.Environment != nil {
		dst.Environment = make(map[string]string, len(src.Environment))
		for k, v := range src.Environment {
			dst.Environment[k] = v
		}
	}
	if src.Ports != nil {
		dst.Ports = make([]orchestrator.PortBinding, len(src.Ports))
		copy(dst.Ports, src.Ports)
	}
	if src.Volumes != nil {
		dst.Volumes = make(map[string]string, len(src.Volumes))
		for k, v := range src.Volumes {
			dst.Volumes[k] = v
		}
	}
	return dst
}

