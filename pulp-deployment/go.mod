module bananagine-deployment

go 1.25.6

require (
	github.com/BananaLabs-OSS/Pulp v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-docker v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-fs v0.0.0
	github.com/BananaLabs-OSS/Pulp-ext-http v0.0.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/Microsoft/go-winio v0.4.21 // indirect
	github.com/bananalabs-oss/potassium v0.9.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/docker v28.5.2+incompatible // indirect
	github.com/docker/go-connections v0.6.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/tetratelabs/wazero v1.11.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.64.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	google.golang.org/grpc v1.80.0 // indirect
)

replace (
	github.com/BananaLabs-OSS/Pulp => ../../Pulp
	github.com/BananaLabs-OSS/Pulp-ext-docker => ../../Pulp-ext-docker
	github.com/BananaLabs-OSS/Pulp-ext-fs => ../../Pulp-ext-fs
	github.com/BananaLabs-OSS/Pulp-ext-http => ../../Pulp-ext-http
	github.com/bananalabs-oss/potassium => ../../Potassium
)
