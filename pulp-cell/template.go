package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/docker"
	"gopkg.in/yaml.v3"
)

type Hooks struct {
	PreStart string `yaml:"pre_start" json:"pre_start,omitempty"`
}

type ConfigOption struct {
	Type    string   `yaml:"type" json:"type"`
	Default any      `yaml:"default" json:"default"`
	Label   string   `yaml:"label" json:"label"`
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	Hint    string   `yaml:"hint,omitempty" json:"hint,omitempty"`
}

// EngineOption is a selectable server engine, declared per game in the template
// YAML so the configurator renders engine choices from sidecar data instead of
// hardcoding them. `Platforms` gates the player-platform pickers; `Mods` names
// the modification method offered (fabric / datapacks / addons).
type EngineOption struct {
	Value     string   `yaml:"value" json:"value"`
	Label     string   `yaml:"label" json:"label"`
	Hint      string   `yaml:"hint,omitempty" json:"hint,omitempty"`
	Platforms []string `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Mods      string   `yaml:"mods,omitempty" json:"mods,omitempty"`
	Group     string   `yaml:"group,omitempty" json:"group,omitempty"`
	Default   bool     `yaml:"default,omitempty" json:"default,omitempty"`
}

type ConfigSchema struct {
	Settings  map[string]ConfigOption `yaml:"settings,omitempty" json:"settings,omitempty"`
	GameRules map[string]ConfigOption `yaml:"game_rules,omitempty" json:"game_rules,omitempty"`
	Engines   []EngineOption          `yaml:"engines,omitempty" json:"engines,omitempty"`
}

type PortSpec struct {
	Host      int    `yaml:"host" json:"host"`
	Container int    `yaml:"container" json:"container"`
	Protocol  string `yaml:"protocol" json:"protocol"`
	Name      string `yaml:"name,omitempty" json:"name,omitempty"`
	Range     string `yaml:"range,omitempty" json:"range,omitempty"`
}

type ContainerSpec struct {
	Image          string            `yaml:"image" json:"image"`
	Name           string            `yaml:"-" json:"-"`
	Environment    map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Volumes        map[string]string `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Ports          []PortSpec        `yaml:"ports,omitempty" json:"ports,omitempty"`
	Network        string            `yaml:"network,omitempty" json:"network,omitempty"`
	IP             string            `yaml:"-" json:"-"`
	CPULimit       float64           `yaml:"cpu_limit,omitempty" json:"cpu_limit"`
	MemoryLimit    int64             `yaml:"memory_limit,omitempty" json:"memory_limit"`
	DiskIOReadBps  int64             `yaml:"disk_io_read_bps,omitempty" json:"disk_io_read_bps,omitempty"`
	DiskIOWriteBps int64             `yaml:"disk_io_write_bps,omitempty" json:"disk_io_write_bps,omitempty"`
	DiskSizeLimit  int64             `yaml:"disk_size_limit,omitempty" json:"disk_size_limit,omitempty"`
	PidsLimit      int64             `yaml:"pids_limit,omitempty" json:"pids_limit,omitempty"`
	MemorySwap     int64             `yaml:"memory_swap,omitempty" json:"memory_swap,omitempty"`
}

type Template struct {
	Name      string            `yaml:"name" json:"name"`
	Game      string            `yaml:"game" json:"game"`
	Label     string            `yaml:"label" json:"label"`
	Container ContainerSpec     `yaml:"container" json:"container"`
	Server    map[string]string `yaml:"server" json:"server"`
	Hooks     Hooks             `yaml:"hooks" json:"hooks"`
	Config    ConfigSchema      `yaml:"config" json:"config"`
}

// loadTemplates reads every *.yaml file under the cell's templates/
// directory and returns them keyed by the `name` field inside each file.
//
// Original Bananagine scans TEMPLATES_DIR (e.g. /app/templates, mounted
// from paper-server) with os.ReadDir picking *.yaml. Port uses
// pulp.FS.List which is scoped to the cell's storage root — the host
// is expected to mount templates/ into that scoped root. Any .yaml or
// .yml entry at the top level is considered a template.
func loadTemplates(overrideFilenames []string) (map[string]Template, error) {
	templates := make(map[string]Template)

	// Collect candidate filenames. If the operator supplied an explicit
	// list via the `templates` config key we honor it verbatim (back-compat
	// for pinning specific files). Otherwise we scan the directory.
	var names []string
	if len(overrideFilenames) > 0 {
		for _, n := range overrideFilenames {
			n = strings.TrimSpace(n)
			if n != "" {
				names = append(names, n)
			}
		}
	} else {
		entries, err := pulp.FS.List("templates")
		if err != nil {
			return nil, fmt.Errorf("list templates directory: %w", err)
		}
		for _, e := range entries {
			if e.IsDir {
				continue
			}
			lower := strings.ToLower(e.Name)
			if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
				continue
			}
			names = append(names, e.Name)
		}
	}

	for _, name := range names {
		path := "templates/" + name
		data, err := pulp.FS.Read(path)
		if err != nil {
			// Parity with native Bananagine/internal/template/template.go:55 — no prefix, capitalized.
			log.Printf("Failed to read template %s: %v", name, err)
			continue
		}
		var t Template
		if err := yaml.Unmarshal(data, &t); err != nil {
			// Parity with native Bananagine/internal/template/template.go:62.
			log.Printf("Failed to parse template %s: %v", name, err)
			continue
		}
		if t.Name == "" {
			// Parity with native Bananagine/internal/template/template.go:66.
			log.Printf("Template %s has no name, skipping", name)
			continue
		}
		templates[t.Name] = t
	}
	return templates, nil
}

func deepCopyContainer(src ContainerSpec) ContainerSpec {
	dst := src
	if src.Environment != nil {
		dst.Environment = make(map[string]string, len(src.Environment))
		for k, v := range src.Environment {
			dst.Environment[k] = v
		}
	}
	if src.Ports != nil {
		dst.Ports = make([]PortSpec, len(src.Ports))
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

func containerToCreateRequest(c ContainerSpec) docker.CreateRequest {
	ports := make([]docker.PortBinding, len(c.Ports))
	for i, p := range c.Ports {
		ports[i] = docker.PortBinding{
			Host:      p.Host,
			Container: p.Container,
			Protocol:  p.Protocol,
			Name:      p.Name,
			Range:     p.Range,
		}
	}
	return docker.CreateRequest{
		Image:          c.Image,
		Name:           c.Name,
		Environment:    c.Environment,
		Volumes:        c.Volumes,
		Ports:          ports,
		Network:        c.Network,
		IP:             c.IP,
		MemoryLimit:    c.MemoryLimit,
		CPULimit:       c.CPULimit,
		DiskIOReadBps:  c.DiskIOReadBps,
		DiskIOWriteBps: c.DiskIOWriteBps,
		DiskSizeLimit:  c.DiskSizeLimit,
		PidsLimit:      c.PidsLimit,
		MemorySwap:     c.MemorySwap,
	}
}

