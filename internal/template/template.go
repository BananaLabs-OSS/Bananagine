package template

import (
	"os"
	"path/filepath"

	"github.com/bananalabs-oss/potassium/orchestrator"
)
import "gopkg.in/yaml.v3"

type Hooks struct {
	PreStart string `yaml:"pre_start"`
}

type Template struct {
	Name      string                       `json:"name"`
	Container orchestrator.AllocateRequest `json:"container"`
	Server    map[string]string            `json:"server"`
	Hooks     Hooks                        `json:"hooks"`
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
