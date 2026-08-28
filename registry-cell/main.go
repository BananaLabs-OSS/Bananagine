package main

import (
	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/bananalabs-oss/bananagine/registry"

	"runtime-directory-cell/handlers"
)

func init() {
	pulp.OnInit(func([]byte) error {
		set := handlers.New(registry.NewState())
		for name, provider := range set.Providers() {
			pulp.Provide(name, pulp.Provider(provider))
		}
		return nil
	})
}

func main() {}
