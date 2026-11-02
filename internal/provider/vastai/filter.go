package vastai

import "github.com/savak1990/vk-llm-on-demand/internal/provider"

func buildFilterBody(f provider.OfferFilter) map[string]any {
	b := map[string]any{
		"dph_total": map[string]any{"lte": f.MaxHourly.USD()},
		"verified":  map[string]any{"eq": true},
		"rentable":  map[string]any{"eq": true},
		"type":      "ondemand",
	}
	if f.GPUName != "" {
		b["gpu_name"] = map[string]any{"eq": f.GPUName}
	}
	if f.NumGPUs != 0 {
		b["num_gpus"] = map[string]any{"eq": f.NumGPUs}
	}
	if f.MinCPUCores != 0 {
		b["cpu_cores"] = map[string]any{"gte": f.MinCPUCores}
	}
	if f.MinCPURAMGB != 0 {
		b["cpu_ram"] = map[string]any{"gte": f.MinCPURAMGB * 1000}
	}
	if f.MinDiskGB != 0 {
		b["disk_space"] = map[string]any{"gte": f.MinDiskGB}
	}
	if f.MinReliability != 0 {
		b["reliability"] = map[string]any{"gte": f.MinReliability}
	}
	if f.MinInetDownMBps != 0 {
		b["inet_down"] = map[string]any{"gte": f.MinInetDownMBps}
	}
	if f.MinDiskBwMBps != 0 {
		b["disk_bw"] = map[string]any{"gte": f.MinDiskBwMBps}
	}
	if f.MinCUDA != 0 {
		b["cuda_max_good"] = map[string]any{"gte": f.MinCUDA}
	}
	if len(f.Geolocation) > 0 {
		b["geolocation"] = map[string]any{"in": f.Geolocation}
	}
	if len(f.ExcludeMachineIDs) > 0 {
		b["machine_id"] = map[string]any{"notin": f.ExcludeMachineIDs}
	}
	return b
}
