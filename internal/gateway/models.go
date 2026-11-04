package gateway

import (
	"encoding/json"
	"net/http"
)

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelsList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

// handleModels answers purely from configuration: no controller call,
// no Admit, no store read, so it works with the GPU stopped.
func (g *Gateway) handleModels(w http.ResponseWriter, _ *http.Request) {
	resp := modelsList{
		Object: "list",
		Data: []modelObject{{
			ID:      g.deps.Config.ModelAlias,
			Object:  "model",
			Created: g.startedAt.Unix(),
			OwnedBy: "gpu-on-demand",
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
