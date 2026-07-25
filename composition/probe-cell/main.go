// composition-probe is test-only. It provides an HTTP entrance into the real
// Lua orchestrator so the Bananagine repository can exercise a complete
// HTTP -> Lua -> registry-cell composition without changing production routes.
package main

import (
	"fmt"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
	"github.com/bananalabs-oss/bananagine/registry"
	"github.com/vmihailenco/msgpack/v5"

	"bananagine-cell/registryproxy"
)

const luaTarget = "bananagine-lua"

func dispatch[T any](event string, payload any) (registry.Result[T], error) {
	var zero registry.Result[T]
	request := workflow.DispatchRequest{Event: event, Payload: payload}
	if err := request.Validate(); err != nil {
		return zero, fmt.Errorf("validate dispatch: %w", err)
	}
	encoded, err := msgpack.Marshal(request)
	if err != nil {
		return zero, fmt.Errorf("encode dispatch: %w", err)
	}
	response, err := pulp.Call(luaTarget, workflow.FnDispatch, encoded)
	if err != nil {
		return zero, fmt.Errorf("dispatch: %w", err)
	}
	var result workflow.DispatchResult
	if err := msgpack.Unmarshal(response, &result); err != nil {
		return zero, fmt.Errorf("decode dispatch: %w", err)
	}
	if err := result.Validate(); err != nil {
		return zero, fmt.Errorf("validate dispatch result: %w", err)
	}
	registryResult, err := workflow.DecodeValue[registry.Result[T]](result)
	if err != nil {
		return zero, fmt.Errorf("decode typed value: %w", err)
	}
	return registryResult, nil
}

func writeFailure(c *pulpgin.Context, operation string, resultError *registry.ServiceError, transportError error) {
	if transportError != nil {
		c.JSON(503, pulpgin.H{"error": registryproxy.UnavailableMessage})
		return
	}
	status, message := registryproxy.LegacyHTTPFailure(operation, resultError)
	c.JSON(status, pulpgin.H{"error": message})
}

func init() {
	pulp.OnInit(func([]byte) error {
		router := pulpgin.New()

		router.GET("/composition/health", func(c *pulpgin.Context) {
			c.JSON(200, pulpgin.H{"status": "ok"})
		})

		router.POST("/composition/registry/servers", func(c *pulpgin.Context) {
			var server registry.Server
			if err := c.ShouldBindJSON(&server); err != nil {
				c.JSON(400, pulpgin.H{"error": err.Error()})
				return
			}
			result, err := dispatch[registry.Server](registry.FnRegister, server)
			if err != nil {
				writeFailure(c, registry.FnRegister, nil, err)
				return
			}
			if !result.OK {
				writeFailure(c, registry.FnRegister, result.Error, nil)
				return
			}
			c.JSON(201, server)
		})

		router.GET("/composition/registry/servers", func(c *pulpgin.Context) {
			filter := registry.ListRequest{
				Type:          registry.ServerType(c.Query("type")),
				Mode:          c.Query("mode"),
				HasCapacity:   c.Query("hasCapacity") == "true",
				HasReadyMatch: c.Query("hasReadyMatch") == "true",
			}
			result, err := dispatch[[]registry.Server](registry.FnList, filter)
			if err != nil {
				writeFailure(c, registry.FnList, nil, err)
				return
			}
			if !result.OK {
				writeFailure(c, registry.FnList, result.Error, nil)
				return
			}
			c.JSON(200, result.Value)
		})

		router.GET("/composition/registry/servers/:id", func(c *pulpgin.Context) {
			result, err := dispatch[registry.Server](
				registry.FnGet,
				registry.GetRequest{ID: c.Param("id")},
			)
			if err != nil {
				writeFailure(c, registry.FnGet, nil, err)
				return
			}
			if !result.OK {
				writeFailure(c, registry.FnGet, result.Error, nil)
				return
			}
			c.JSON(200, result.Value)
		})

		if err := router.RegisterRoutes(); err != nil {
			return err
		}
		pulp.OnStep(router.Dispatch)
		return nil
	})
}

func main() {}
