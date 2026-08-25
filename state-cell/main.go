// bananagine-state-cell is the first Pulp fusible artifact. It deliberately
// compiles the two capability-free state owners into one Go/Wasm program while
// retaining every existing provider name. Callers therefore keep their logical
// module contracts even though one Wasm instance owns both heaps.
package main

import (
	"fmt"

	"bananagine-registry-cell/handlers"
	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/bananalabs-oss/bananagine/registry"
	"github.com/bananalabs-oss/bananagine/templatecatalog"
	"github.com/vmihailenco/msgpack/v5"
)

func init() {
	pulp.OnInit(func([]byte) error {
		for name, provider := range handlers.New(registry.NewState()).Providers() {
			pulp.Provide(name, pulp.Provider(provider))
		}
		for name, provider := range templateProviders(templatecatalog.NewState()) {
			pulp.Provide(name, pulp.Provider(provider))
		}
		return nil
	})
}

func main() {}

type provider func([]byte) ([]byte, error)

func templateProviders(state *templatecatalog.State) map[string]provider {
	return map[string]provider{
		templatecatalog.FnReplace: func(input []byte) ([]byte, error) {
			var request templatecatalog.ReplaceRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			value, err := state.Replace(request)
			return encode(value, err)
		},
		templatecatalog.FnList: func(input []byte) ([]byte, error) {
			if len(input) > 0 {
				var ignored map[string]any
				if err := decode(input, &ignored); err != nil {
					return nil, err
				}
			}
			return encode(state.List(), nil)
		},
		templatecatalog.FnGet: func(input []byte) ([]byte, error) {
			var request templatecatalog.GetRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			value, err := state.Get(request.Name)
			return encode(value, err)
		},
		templatecatalog.FnSnapshotExport: func([]byte) ([]byte, error) { return encode(state.Export(), nil) },
		templatecatalog.FnSnapshotImport: func(input []byte) ([]byte, error) {
			var request templatecatalog.ImportRequest
			if err := decode(input, &request); err != nil {
				return nil, err
			}
			value, err := state.Import(request)
			return encode(value, err)
		},
	}
}

func decode(input []byte, output any) error {
	if len(input) == 0 {
		return fmt.Errorf("decode: empty request")
	}
	if err := msgpack.Unmarshal(input, output); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func encode[T any](value T, err error) ([]byte, error) {
	result := templatecatalog.Success(value)
	if err != nil {
		result = templatecatalog.Failure[T](err)
	}
	output, marshalErr := msgpack.Marshal(result)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode: %w", marshalErr)
	}
	return output, nil
}
