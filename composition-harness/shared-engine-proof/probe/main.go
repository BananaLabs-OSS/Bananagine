package main

import (
	"fmt"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
	"github.com/bananalabs-oss/bananagine/templatecatalog"
	"github.com/vmihailenco/msgpack/v5"
)

const proofLuaTarget = "bananagine-shared-engine-proof-lua"

func init() {
	pulp.OnInit(func([]byte) error {
		router := pulpgin.New()
		router.GET("/proof/health", func(c *pulpgin.Context) {
			c.JSON(200, pulpgin.H{"status": "ok"})
		})
		router.POST("/proof/run", func(c *pulpgin.Context) {
			request := workflow.DispatchRequest{Event: "bananagine.shared-engine-proof.v1.run", Payload: map[string]any{}}
			if err := request.Validate(); err != nil {
				c.JSON(500, pulpgin.H{"error": err.Error()})
				return
			}
			encoded, err := msgpack.Marshal(request)
			if err != nil {
				c.JSON(500, pulpgin.H{"error": err.Error()})
				return
			}
			response, err := pulp.Call(proofLuaTarget, workflow.FnDispatch, encoded)
			if err != nil {
				c.JSON(503, pulpgin.H{"error": fmt.Sprintf("dispatch: %v", err)})
				return
			}
			var result workflow.DispatchResult
			if err := msgpack.Unmarshal(response, &result); err != nil {
				c.JSON(500, pulpgin.H{"error": fmt.Sprintf("decode dispatch: %v", err)})
				return
			}
			if err := result.Validate(); err != nil {
				c.JSON(500, pulpgin.H{"error": fmt.Sprintf("validate dispatch: %v", err)})
				return
			}
			value, err := workflow.DecodeValue[map[string]any](result)
			if err != nil {
				c.JSON(500, pulpgin.H{"error": fmt.Sprintf("decode proof result: %v", err)})
				return
			}
			c.JSON(200, value)
		})
		router.POST("/proof/template", func(c *pulpgin.Context) {
			var entry templatecatalog.Entry
			if err := c.ShouldBindJSON(&entry); err != nil {
				c.JSON(400, pulpgin.H{"error": err.Error()})
				return
			}
			request := templatecatalog.ReplaceRequest{RequestID: "proof-template/" + entry.Name, Entries: []templatecatalog.Entry{entry}}
			encoded, err := msgpack.Marshal(request)
			if err != nil {
				c.JSON(500, pulpgin.H{"error": err.Error()})
				return
			}
			if _, err := pulp.Call("template-catalog", templatecatalog.FnReplace, encoded); err != nil {
				c.JSON(503, pulpgin.H{"error": fmt.Sprintf("template replace: %v", err)})
				return
			}
			c.JSON(200, pulpgin.H{"status": "seeded"})
		})
		router.POST("/proof/create-plan", func(c *pulpgin.Context) {
			var payload map[string]any
			if err := c.ShouldBindJSON(&payload); err != nil {
				c.JSON(400, pulpgin.H{"error": err.Error()})
				return
			}
			request := workflow.DispatchRequest{Event: "bananagine.create-plan.v1.run", Payload: payload}
			encoded, err := msgpack.Marshal(request)
			if err != nil {
				c.JSON(500, pulpgin.H{"error": err.Error()})
				return
			}
			response, err := pulp.Call(proofLuaTarget, workflow.FnDispatch, encoded)
			if err != nil {
				c.JSON(503, pulpgin.H{"error": fmt.Sprintf("dispatch: %v", err)})
				return
			}
			var result workflow.DispatchResult
			if err := msgpack.Unmarshal(response, &result); err != nil {
				c.JSON(500, pulpgin.H{"error": fmt.Sprintf("decode dispatch: %v", err)})
				return
			}
			value, err := workflow.DecodeValue[map[string]any](result)
			if err != nil {
				c.JSON(500, pulpgin.H{"error": fmt.Sprintf("decode create plan: %v", err)})
				return
			}
			c.JSON(200, value)
		})
		if err := router.RegisterRoutes(); err != nil {
			return err
		}
		pulp.OnStep(router.Dispatch)
		return nil
	})
}

func main() {}
