package vastai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

// SearchVolumeOffers asks only for the given machines, so the search stays
// bounded; offers smaller than the volume are dropped here as well.
func (c *Client) SearchVolumeOffers(ctx context.Context, machineIDs []int64, minSizeGB int) ([]provider.VolumeOffer, error) {
	if len(machineIDs) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{
		"verified":          map[string]any{"eq": true},
		"external":          map[string]any{"eq": false},
		"disk_space":        map[string]any{"gte": minSizeGB},
		"machine_id":        map[string]any{"in": machineIDs},
		"allocated_storage": minSizeGB,
		"order":             [][]string{{"storage_cost", "asc"}},
	})
	status, respBody, err := c.do(ctx, "search_volumes", 30*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, c.baseURL+"/api/v0/volumes/search/", bytes.NewReader(body))
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		fe := fatalOrTerminal("search_volumes", status)
		c.recordOutcome("search_volumes", outcomeLabel(fe), status)
		return nil, fe
	}
	var decoded struct {
		Offers []struct {
			ID          int64   `json:"id"`
			MachineID   int64   `json:"machine_id"`
			StorageCost float64 `json:"storage_cost"`
			DiskSpace   float64 `json:"disk_space"`
		} `json:"offers"`
	}
	if err := decodeJSON(respBody, &decoded); err != nil {
		c.recordOutcome("search_volumes", "terminal", status)
		return nil, &provider.Error{Op: "search_volumes", Class: provider.Terminal, Err: fmt.Errorf("decode: %w", err)}
	}
	var out []provider.VolumeOffer
	for _, o := range decoded.Offers {
		if o.DiskSpace < float64(minSizeGB) {
			continue
		}
		out = append(out, provider.VolumeOffer{ID: o.ID, MachineID: o.MachineID, StoragePerGBMonth: provider.FromUSD(o.StorageCost), DiskSpaceGB: o.DiskSpace})
	}
	c.recordOutcome("search_volumes", "ok", status)
	return out, nil
}

func (c *Client) ListVolumes(ctx context.Context, labelPrefix string) ([]provider.Volume, error) {
	status, respBody, err := c.do(ctx, "list_volumes", 15*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.baseURL+"/api/v0/volumes?owner=me&type=all_volume", nil)
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		fe := fatalOrTerminal("list_volumes", status)
		c.recordOutcome("list_volumes", outcomeLabel(fe), status)
		return nil, fe
	}
	var decoded struct {
		Volumes []struct {
			ID        int64   `json:"id"`
			Label     string  `json:"label"`
			MachineID int64   `json:"machine_id"`
			Status    string  `json:"status"`
			DiskSpace float64 `json:"disk_space"`
			Instances []int64 `json:"instances"`
		} `json:"volumes"`
	}
	if err := decodeJSON(respBody, &decoded); err != nil {
		c.recordOutcome("list_volumes", "terminal", status)
		return nil, &provider.Error{Op: "list_volumes", Class: provider.Terminal, Err: fmt.Errorf("decode: %w", err)}
	}
	var out []provider.Volume
	for _, v := range decoded.Volumes {
		if labelPrefix != "" && !strings.HasPrefix(v.Label, labelPrefix) {
			continue
		}
		vol := provider.Volume{ID: provider.VolumeID(v.ID), Label: v.Label, MachineID: v.MachineID, Status: v.Status, SizeGB: v.DiskSpace}
		for _, id := range v.Instances {
			vol.Instances = append(vol.Instances, provider.InstanceID(id))
		}
		out = append(out, vol)
	}
	c.recordOutcome("list_volumes", "ok", status)
	return out, nil
}

// DeleteVolume uses the trailing-slash path, so a redirected DELETE can
// never collapse into a GET. A 409 means an instance still uses it.
func (c *Client) DeleteVolume(ctx context.Context, id provider.VolumeID) error {
	status, _, err := c.do(ctx, "delete_volume", 30*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/v0/volumes/?id=%d", c.baseURL, id), nil)
	}, "volume_id", id)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound:
		c.recordOutcome("delete_volume", "not_found", status)
		return &provider.Error{Op: "delete_volume", Class: provider.Terminal, Status: 404, Err: fmt.Errorf("volume %d: %w", id, provider.ErrNotFound)}
	case status == http.StatusConflict:
		c.recordOutcome("delete_volume", "terminal", status)
		return &provider.Error{Op: "delete_volume", Class: provider.Terminal, Status: 409, Err: fmt.Errorf("volume %d: %w", id, provider.ErrVolumeInUse)}
	case status < 200 || status >= 300:
		fe := fatalOrTerminal("delete_volume", status)
		c.recordOutcome("delete_volume", outcomeLabel(fe), status)
		return fe
	}
	c.recordOutcome("delete_volume", "ok", status)
	return nil
}
