// Package orchestration defines Bananagine's transport-neutral public
// contract. The same value types are used by the Bananagine cell and its
// callers; HTTP JSON and Pulp sibling adapters can wrap them independently.
package orchestration

const (
	HealthPath    = "/health"
	TemplatesPath = "/templates"
	ServersPath   = "/orchestration/servers"
	StatsPath     = "/orchestration/stats"
)

// ResourceOverride applies caller-selected limits over a template's defaults.
type ResourceOverride struct {
	MemoryLimit int64   `json:"memory_limit,omitempty" msgpack:"memory_limit,omitempty"`
	CPULimit    float64 `json:"cpu_limit,omitempty" msgpack:"cpu_limit,omitempty"`
	MaxCpuCores float64 `json:"max_cpu_cores,omitempty" msgpack:"max_cpu_cores,omitempty"`
	MaxRamMb    int64   `json:"max_ram_mb,omitempty" msgpack:"max_ram_mb,omitempty"`
	JvmHeapMb   int64   `json:"jvm_heap_mb,omitempty" msgpack:"jvm_heap_mb,omitempty"`
}

// CreateServerRequest asks Bananagine to instantiate a named template.
type CreateServerRequest struct {
	Template  string            `json:"template" msgpack:"template"`
	ServerID  string            `json:"server_id,omitempty" msgpack:"server_id,omitempty"`
	Env       map[string]string `json:"env,omitempty" msgpack:"env,omitempty"`
	Resources *ResourceOverride `json:"resources,omitempty" msgpack:"resources,omitempty"`
}

// TemplateInfo is the public template catalog entry returned by Bananagine.
type TemplateInfo struct {
	Name        string  `json:"name" msgpack:"name"`
	Game        string  `json:"game" msgpack:"game"`
	Label       string  `json:"label" msgpack:"label"`
	Engine      string  `json:"engine,omitempty" msgpack:"engine,omitempty"`
	CPULimit    float64 `json:"cpu_limit" msgpack:"cpu_limit"`
	MemoryLimit int64   `json:"memory_limit" msgpack:"memory_limit"`
}

// Server is Bananagine's public projection of a managed runtime.
type Server struct {
	ID          string         `json:"id" msgpack:"id"`
	Name        string         `json:"name" msgpack:"name"`
	Status      string         `json:"status" msgpack:"status"`
	IP          string         `json:"ip" msgpack:"ip"`
	Ports       map[string]int `json:"ports" msgpack:"ports"`
	CPULimit    float64        `json:"cpu_limit,omitempty" msgpack:"cpu_limit,omitempty"`
	MemoryLimit int64          `json:"memory_limit,omitempty" msgpack:"memory_limit,omitempty"`
}

// ContainerStats is the runtime telemetry Bananagine reports per container.
type ContainerStats struct {
	ContainerID    string  `json:"container_id" msgpack:"container_id"`
	Name           string  `json:"name" msgpack:"name"`
	CPUPercent     float64 `json:"cpu_percent" msgpack:"cpu_percent"`
	MemoryUsed     int64   `json:"memory_used" msgpack:"memory_used"`
	MemoryLimit    int64   `json:"memory_limit" msgpack:"memory_limit"`
	NetRxBytes     int64   `json:"net_rx_bytes" msgpack:"net_rx_bytes"`
	NetTxBytes     int64   `json:"net_tx_bytes" msgpack:"net_tx_bytes"`
	DiskReadBytes  int64   `json:"disk_read_bytes" msgpack:"disk_read_bytes"`
	DiskWriteBytes int64   `json:"disk_write_bytes" msgpack:"disk_write_bytes"`
	Timestamp      int64   `json:"timestamp" msgpack:"timestamp"`
}

// NodeStats describes the capacity owned and reported by one Bananagine node.
type NodeStats struct {
	CPUCores        int     `json:"cpu_cores" msgpack:"cpu_cores"`
	TotalMemory     uint64  `json:"total_memory" msgpack:"total_memory"`
	AllocatedCPU    float64 `json:"allocated_cpu" msgpack:"allocated_cpu"`
	AllocatedMemory float64 `json:"allocated_memory" msgpack:"allocated_memory"`
	DiskTotal       uint64  `json:"disk_total" msgpack:"disk_total"`
	DiskUsed        uint64  `json:"disk_used" msgpack:"disk_used"`
	CPUBudget       float64 `json:"cpu_budget" msgpack:"cpu_budget"`
	MemoryBudget    float64 `json:"memory_budget" msgpack:"memory_budget"`
}

// StatsResponse is returned by GET /orchestration/stats.
type StatsResponse struct {
	Containers []ContainerStats `json:"containers" msgpack:"containers"`
	Node       NodeStats        `json:"node" msgpack:"node"`
}

// Event is the lifecycle event shape used by both polling and SSE.
type Event struct {
	ContainerID string `json:"container_id" msgpack:"container_id"`
	Name        string `json:"name" msgpack:"name"`
	Action      string `json:"action" msgpack:"action"`
	Time        int64  `json:"time" msgpack:"time"`
}

type ExecRequest struct {
	Cmd []string `json:"cmd" msgpack:"cmd"`
}

type ExecResponse struct {
	Output string `json:"output" msgpack:"output"`
}

type LogsResponse struct {
	Logs string `json:"logs" msgpack:"logs"`
}
