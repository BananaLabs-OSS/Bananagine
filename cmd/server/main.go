package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/bananalabs-oss/bananagine/internal/ips"
	"github.com/bananalabs-oss/bananagine/internal/ports"
	"github.com/bananalabs-oss/bananagine/internal/template"
	"github.com/bananalabs-oss/potassium/registry"
	"github.com/gin-gonic/gin"
)
import "github.com/bananalabs-oss/potassium/orchestrator/providers/docker"

type CreateServerRequest struct {
	Template string `json:"template"`
}

func main() {
	// Load templates at startup
	templates, err := template.LoadTemplates("./templates")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Loaded %d templates\n", len(templates))

	// Start docker provider
	provider, err := docker.New()
	if err != nil {
		panic(err)
	}

	ipPool := ips.NewPool("10.99.0.10", "10.99.0.250")
	portPool := ports.NewPool(5521, 5599)

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

		orchestration.POST("/servers/", func(c *gin.Context) {
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
		registryGroup.POST("/servers/", func(c *gin.Context) {
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

		registryGroup.GET("/servers/", func(c *gin.Context) {
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

	err = r.Run(":3000")
	if err != nil {
		return
	}
}
