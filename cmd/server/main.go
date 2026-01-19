package main

import (
	"github.com/bananalabs-oss/potassium/orchestrator"
	"github.com/gin-gonic/gin"
)
import "github.com/bananalabs-oss/potassium/orchestrator/providers/docker"

func main() {
	provider, err := docker.New()
	if err != nil {
		panic(err)
	}

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.GET("/servers", func(c *gin.Context) {
		ctx := c.Request.Context()
		servers, err := provider.List(ctx, nil)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, servers)
	})

	r.GET("/servers/:id", func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		servers, err := provider.Get(ctx, id)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, servers)
	})

	r.POST("/servers/", func(c *gin.Context) {
		var req orchestrator.AllocateRequest

		if err := c.BindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		ctx := c.Request.Context()
		server, err := provider.Allocate(ctx, req)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(201, server)
	})

	r.DELETE("/servers/:id", func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		err := provider.Deallocate(ctx, id)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(204, nil)
	})

	err = r.Run(":3000")
	if err != nil {
		return
	}
}
