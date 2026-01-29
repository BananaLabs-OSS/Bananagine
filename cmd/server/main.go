package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bananalabs-oss/bananagine/internal/ips"
	"github.com/bananalabs-oss/bananagine/internal/ports"
	"github.com/bananalabs-oss/bananagine/internal/template"
	"github.com/bananalabs-oss/potassium/orchestrator/providers/docker"
	"github.com/bananalabs-oss/potassium/registry"
	"github.com/gin-gonic/gin"
)

type CreateServerRequest struct {
	Template string `json:"template"`
}

func main() {
	// CLI flags
	listenAddr := flag.String("listen", "", "Listen address (default :3000)")
	templatesDir := flag.String("templates", "", "Templates directory (default ./templates)")
	ipStart := flag.String("ip-start", "", "IP pool start (default 10.99.0.10)")
	ipEnd := flag.String("ip-end", "", "IP pool end (default 10.99.0.250)")
	portStart := flag.Int("port-start", 0, "Port pool start (default 5521)")
	portEnd := flag.Int("port-end", 0, "Port pool end (default 5599)")
	flag.Parse()

	// Resolve: CLI > Env > Default
	config := struct {
		ListenAddr   string
		TemplatesDir string
		IPStart      string
		IPEnd        string
		PortStart    int
		PortEnd      int
	}{
		ListenAddr:   resolve(*listenAddr, getEnv("LISTEN_ADDR", ""), ":3000"),
		TemplatesDir: resolve(*templatesDir, getEnv("TEMPLATES_DIR", ""), "./templates"),
		IPStart:      resolve(*ipStart, getEnv("IP_POOL_START", ""), "10.99.0.10"),
		IPEnd:        resolve(*ipEnd, getEnv("IP_POOL_END", ""), "10.99.0.250"),
		PortStart:    resolveInt(*portStart, getEnvInt("PORT_POOL_START", 0), 5521),
		PortEnd:      resolveInt(*portEnd, getEnvInt("PORT_POOL_END", 0), 5599),
	}

	// Log config
	fmt.Printf("Listen: %s\n", config.ListenAddr)
	fmt.Printf("Templates: %s\n", config.TemplatesDir)
	fmt.Printf("IP pool: %s - %s\n", config.IPStart, config.IPEnd)
	fmt.Printf("Port pool: %d - %d\n", config.PortStart, config.PortEnd)

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
			servers, err := provider.Get(ctx, id)
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, servers)
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

			// Generate server ID
			serverID := fmt.Sprintf("%s-%d", req.Template, time.Now().UnixNano())

			// Merge server config into environment
			if tmpl.Container.Environment == nil {
				tmpl.Container.Environment = make(map[string]string)
			}
			for k, v := range tmpl.Server {
				tmpl.Container.Environment[k] = v
			}

			var allocatedIP string
			var allocatedPort int

			if tmpl.Container.Network != "" {
				// Overlay mode - static IP
				ip, err := ipPool.Allocate(serverID)
				if err != nil {
					c.JSON(503, gin.H{"error": err.Error()})
					return
				}
				allocatedIP = ip
				tmpl.Container.IP = ip

				// Get port from template (default 5520)
				allocatedPort = 5520
				if len(tmpl.Container.Ports) > 0 {
					allocatedPort = tmpl.Container.Ports[0].Container
				}

				tmpl.Container.Environment["SERVER_HOST"] = ip
				fmt.Printf("Overlay mode: %s -> %s:%d\n", serverID, ip, allocatedPort)
			} else {
				// Host mode - dynamic port
				port, err := portPool.Allocate(serverID)
				if err != nil {
					c.JSON(503, gin.H{"error": err.Error()})
					return
				}
				allocatedPort = port

				for i := range tmpl.Container.Ports {
					tmpl.Container.Ports[i].Host = port
					tmpl.Container.Ports[i].Container = port
				}

				tmpl.Container.Environment["SERVER_HOST"] = "127.0.0.1"
				fmt.Printf("Host mode: %s -> 127.0.0.1:%d\n", serverID, port)
			}

			tmpl.Container.Environment["SERVER_PORT"] = fmt.Sprintf("%d", allocatedPort)
			tmpl.Container.Environment["SERVER_ID"] = serverID

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
					tmpl.Container.Environment[k] = v
				}
			} else {
				fmt.Println("No pre_start hook defined")
			}

			fmt.Println("Final environment:", tmpl.Container.Environment)

			ctx := c.Request.Context()
			server, err := provider.Allocate(ctx, tmpl.Container)
			if err != nil {
				if allocatedIP != "" {
					ipPool.Release(allocatedIP)
				} else {
					portPool.Release(allocatedPort)
				}
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}

			// Add metadata to response
			server.Name = serverID

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

			c.JSON(201, nil)
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
			c.JSON(204, nil)
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

			c.JSON(204, nil)
		})
	}

	fmt.Printf("Bananagine running on %s\n", config.ListenAddr)
	err = r.Run(config.ListenAddr)
	if err != nil {
		return
	}
}

// resolve returns first non-empty value: cli > env > fallback
func resolve(cli, env, fallback string) string {
	if cli != "" {
		return cli
	}
	if env != "" {
		return env
	}
	return fallback
}

func resolveInt(cli, env, fallback int) int {
	if cli != 0 {
		return cli
	}
	if env != 0 {
		return env
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}
