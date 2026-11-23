package vastai

import "github.com/savak1990/vk-llm-on-demand/internal/provider"

func buildFilterBody(f provider.OfferFilter) map[string]any {
	b := map[string]any{
		"dph_total":      map[string]any{"lte": f.MaxHourly.USD()},
		"inet_down_cost": map[string]any{"lte": f.MaxDownloadPerGB.USD()},
		"storage_cost":   map[string]any{"lte": f.MaxStoragePerGBMonth.USD()},
		"verified":       map[string]any{"eq": true},
		"rentable":       map[string]any{"eq": true},
		"type":           "ondemand",
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
		// dph_total includes storage only for the size asked for here, so ask for the disk we create.
		b["allocated_storage"] = f.MinDiskGB
	}
	if f.MinReliability != 0 {
		b["reliability"] = map[string]any{"gte": f.MinReliability}
	}
	if f.MinInetDownMbps != 0 {
		b["inet_down"] = map[string]any{"gte": f.MinInetDownMbps}
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
