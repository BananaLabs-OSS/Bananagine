package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTemplates_NamedPorts(t *testing.T) {
	dir := t.TempDir()

	yaml := `name: multi-port-test
container:
  image: test-server:latest
  ports:
    - host: 0
      container: 25565
      protocol: tcp
      name: game
    - host: 0
      container: 19132
      protocol: udp
      name: bedrock
  environment:
    MOTD: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "multi-port.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	templates, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tmpl, ok := templates["multi-port-test"]
	if !ok {
		t.Fatal("template 'multi-port-test' not found")
	}

	if len(tmpl.Container.Ports) != 2 {
		t.Fatalf("expected 2 port bindings, got %d", len(tmpl.Container.Ports))
	}

	// First port
	p0 := tmpl.Container.Ports[0]
	if p0.Container != 25565 {
		t.Errorf("port[0].Container = %d, want 25565", p0.Container)
	}
	if p0.Protocol != "tcp" {
		t.Errorf("port[0].Protocol = %s, want tcp", p0.Protocol)
	}
	if p0.Name != "game" {
		t.Errorf("port[0].Name = %s, want game", p0.Name)
	}

	// Second port
	p1 := tmpl.Container.Ports[1]
	if p1.Container != 19132 {
		t.Errorf("port[1].Container = %d, want 19132", p1.Container)
	}
	if p1.Protocol != "udp" {
		t.Errorf("port[1].Protocol = %s, want udp", p1.Protocol)
	}
	if p1.Name != "bedrock" {
		t.Errorf("port[1].Name = %s, want bedrock", p1.Name)
	}
}

func TestLoadTemplates_NoName(t *testing.T) {
	dir := t.TempDir()

	yaml := `name: single-port
container:
  image: test-server:latest
  ports:
    - host: 0
      container: 25565
      protocol: tcp
  environment:
    MOTD: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "single.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	templates, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tmpl := templates["single-port"]
	if tmpl.Container.Ports[0].Name != "" {
		t.Errorf("expected empty Name for unnamed port, got %q", tmpl.Container.Ports[0].Name)
	}
}
