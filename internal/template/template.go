package template

import (
	"os"
	"path/filepath"

	"github.com/bananalabs-oss/potassium/orchestrator"
	"gopkg.in/yaml.v3"
)

type Hooks struct {
	PreStart string `yaml:"pre_start"`
}

type ConfigOption struct {
	Type    string   `yaml:"type" json:"type"`
	Default any      `yaml:"default" json:"default"`
	Label   string   `yaml:"label" json:"label"`
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	Hint    string   `yaml:"hint,omitempty" json:"hint,omitempty"`
}

type ConfigSchema struct {
	Settings  map[string]ConfigOption `yaml:"settings,omitempty" json:"settings,omitempty"`
	GameRules map[string]ConfigOption `yaml:"game_rules,omitempty" json:"game_rules,omitempty"`
}

type Template struct {
	Name      string                       `yaml:"name"`
	Container orchestrator.AllocateRequest `yaml:"container"`
	Server    map[string]string            `yaml:"server"`
	Hooks     Hooks                        `yaml:"hooks"`
	Config    ConfigSchema                 `yaml:"config" json:"config"`
}

func LoadTemplates(dir string) (map[string]Template, error) {
	// Read all YAML from the directory provided
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	templates := make(map[string]Template)

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".yaml" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			continue // Skip files that fail to read
		}

		// Parse into a template
		var t Template
		err = yaml.Unmarshal(data, &t)

		// store in map by name
		templates[t.Name] = t
	}

	// return the map
	return templates, nil
}
