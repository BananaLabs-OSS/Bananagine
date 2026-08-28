package main

import (
	_ "github.com/BananaLabs-OSS/Pulp-ext-docker"
	_ "github.com/BananaLabs-OSS/Pulp-ext-fs"
	_ "github.com/BananaLabs-OSS/Pulp-ext-http"
	_ "github.com/BananaLabs-OSS/Pulp-ext-sqlite"
	_ "github.com/BananaLabs-OSS/Pulp-ext-workers"

	"github.com/BananaLabs-OSS/Pulp/run"
)

func main() {
	run.Main()
}
