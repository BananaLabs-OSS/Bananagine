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
	"strings"
	"time"

	"github.com/bananalabs-oss/bananagine/internal/ips"
	"github.com/bananalabs-oss/bananagine/internal/ports"
	"github.com/bananalabs-oss/bananagine/internal/template"
	"github.com/bananalabs-oss/potassium/config"
	"github.com/bananalabs-oss/potassium/orchestrator"
	"github.com/bananalabs-oss/potassium/server"
	"github.com/bananalabs-oss/potassium/orchestrator/providers/docker"
	"github.com/bananalabs-oss/potassium/registry"
	"github.com/containerd/errdefs"
	"github.com/gin-gonic/gin"
)

type CreateServerRequest struct {
	Template  string            `json:"template"`
	Env       map[string]string `json:"env,omitempty"`
	Resources struct {
		MemoryLimit int64 `json:"memory_limit,omitempty"`
		CPUCount    int   `json:"cpu_count,omitempty"`
	} `json:"resources,omitempty"`
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
		ListenAddr:   config.Resolve(*listenAddr, config.EnvOrDefault("LISTEN_ADDR", ""), ":3000"),
		TemplatesDir: config.Resolve(*templatesDir, config.EnvOrDefault("TEMPLATES_DIR", ""), "./templates"),
		IPStart:      config.Resolve(*ipStart, config.EnvOrDefault("IP_POOL_START", ""), "10.99.0.10"),
		IPEnd:        config.Resolve(*ipEnd, config.EnvOrDefault("IP_POOL_END", ""), "10.99.0.250"),
		PortStart:    config.ResolveInt(*portStart, config.EnvOrDefaultInt("PORT_POOL_START", 0), 5521),
		PortEnd:      config.ResolveInt(*portEnd, config.EnvOrDefaultInt("PORT_POOL_END", 0), 5599),
		ExternalHost: config.Resolve(*externalHost, config.EnvOrDefault("EXTERNAL_HOST", ""), ""),
	}

	// Log config
	fmt.Printf("Listen: %s\n", config.ListenAddr)
	fmt.Printf("Templates: %s\n", config.TemplatesDir)
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
	portPool := ports.NewPool(config.PortStart, config.PortEnd)

	// Reconcile pools with already-running containers
	if existing, err := provider.List(context.Background(), nil); err == nil {
		for _, s := range existing {
			for _, p := range s.Ports {
				portPool.Reserve(p, s.ID)
			}
			if s.IP != "" {
				ipPool.Reserve(s.IP, s.ID)
			}
		}
		if len(existing) > 0 {
			fmt.Printf("Reconciled %d existing containers into pools\n", len(existing))
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

			// Generate server ID
			serverID := fmt.Sprintf("%s-%d", req.Template, time.Now().UnixNano())

			// Expand volume path templates (e.g. {{SERVER_ID}})
			for hostPath, containerPath := range container.Volumes {
				if strings.Contains(hostPath, "{{SERVER_ID}}") {
					expanded := strings.ReplaceAll(hostPath, "{{SERVER_ID}}", serverID)
					delete(container.Volumes, hostPath)
					container.Volumes[expanded] = containerPath
				}
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
				fmt.Printf("Overlay mode: %s -> %s:%d\n", serverID, ip, allocatedPort)
			} else {
				// Host mode - dynamic port
				port, err := portPool.Allocate(serverID)
				if err != nil {
					c.JSON(503, gin.H{"error": err.Error()})
					return
				}
				allocatedPort = port

				for i := range container.Ports {
					container.Ports[i].Host = port
					container.Ports[i].Container = port
				}

				container.Environment["SERVER_HOST"] = "0.0.0.0"
				fmt.Printf("Host mode: %s -> 0.0.0.0:%d\n", serverID, port)
			}

			container.Environment["SERVER_PORT"] = fmt.Sprintf("%d", allocatedPort)
			container.Environment["SERVER_ID"] = serverID

			if tmpl.Hooks.PreStart != "" {
				fmt.Println("Calling pre_start hook:", tmpl.Hooks.PreStart)

				// Call the hook URL
				resp, err := http.Get(tmpl.Hooks.PreStart)
				if err != nil {
					fmt.Println("Hook error:", err)
					c.JSON(500, gin.H{"error": "hook failed: " + err.Error()})
					return
				}
				defer resp.Body.Close()

				// Parse response
				var hookResp struct {
					Env map[string]string `json:"env"`
				}
				json.NewDecoder(resp.Body).Decode(&hookResp)

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
			if req.Resources.CPUCount > 0 {
				container.CPUCount = req.Resources.CPUCount
			}

			fmt.Println("Final environment:", container.Environment)

			ctx := c.Request.Context()
			server, err := provider.Allocate(ctx, container)
			if err != nil {
				if allocatedIP != "" {
					ipPool.Release(allocatedIP)
				} else {
					portPool.Release(allocatedPort)
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			// Re-key pool allocation from serverID to container ID so DELETE can release it
			if allocatedIP != "" {
				ipPool.ReKey(serverID, server.ID)
			} else {
				portPool.ReKey(serverID, server.ID)
			}

			// Add metadata to response
			server.Name = serverID

			// Ensure allocated port is in response (overlay mode returns empty port map)
			if server.Ports == nil {
				server.Ports = map[string]int{}
			}
			portKey := fmt.Sprintf("%d", allocatedPort)
			if _, ok := server.Ports[portKey]; !ok {
				server.Ports[portKey] = allocatedPort
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

			// Release from both pools (only one will match)
			portPool.ReleaseByServer(id)
			ipPool.ReleaseByServer(id)

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

			filepath.Walk(worldDir, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(worldDir, path)
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
			})
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

