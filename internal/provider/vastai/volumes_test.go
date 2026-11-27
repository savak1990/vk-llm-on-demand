package vastai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func TestSearchVolumeOffers(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v0/volumes/search/" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"offers":[
			{"id":55800361,"machine_id":40508,"storage_cost":0.667,"disk_space":1941},
			{"id":61644800,"machine_id":33732,"storage_cost":0.2,"disk_space":5}]}`))
	}))
	defer srv.Close()
	cl, _ := newTestClient(t, srv, fakeclock.New(time.Unix(0, 0)))
	offers, err := cl.SearchVolumeOffers(context.Background(), []int64{40508, 33732}, 34)
	if err != nil {
		t.Fatalf("SearchVolumeOffers: %v", err)
	}
	if len(offers) != 1 || offers[0].ID != 55800361 || offers[0].MachineID != 40508 || offers[0].StoragePerGBMonth != provider.FromUSD(0.667) {
		t.Fatalf("offers = %+v, want only the 1941 GB offer", offers)
	}
	mid, _ := got["machine_id"].(map[string]any)
	if ids, _ := mid["in"].([]any); len(ids) != 2 {
		t.Fatalf("machine_id filter = %v, want in [2 ids]", got["machine_id"])
	}
	if got["allocated_storage"] != float64(34) {
		t.Fatalf("allocated_storage = %v, want 34", got["allocated_storage"])
	}
}

func TestSearchVolumeOffersNoMachinesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request expected")
	}))
	defer srv.Close()
	cl, _ := newTestClient(t, srv, fakeclock.New(time.Unix(0, 0)))
	if offers, err := cl.SearchVolumeOffers(context.Background(), nil, 34); err != nil || offers != nil {
		t.Fatalf("got %v, %v; want nil, nil", offers, err)
	}
}

func TestListVolumesFiltersByLabelPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v0/volumes" || r.URL.Query().Get("owner") != "me" || r.URL.Query().Get("type") != "all_volume" {
			t.Errorf("request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"volumes":[
			{"id":62762324,"label":"vkllm_abc_v11","machine_id":40508,"status":"created","disk_space":34,"instances":[]},
			{"id":7,"label":"someone_else","machine_id":1,"status":"in-use","disk_space":10,"instances":[9]}]}`))
	}))
	defer srv.Close()
	cl, _ := newTestClient(t, srv, fakeclock.New(time.Unix(0, 0)))
	vols, err := cl.ListVolumes(context.Background(), "vkllm_abc_v")
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 1 || vols[0].ID != 62762324 || vols[0].Label != "vkllm_abc_v11" || vols[0].MachineID != 40508 || len(vols[0].Instances) != 0 {
		t.Fatalf("vols = %+v", vols)
	}
}

func TestDeleteVolumeStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{{200, nil}, {404, provider.ErrNotFound}, {409, provider.ErrVolumeInUse}}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/api/v0/volumes/" || r.URL.Query().Get("id") != "62762324" {
				t.Errorf("request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"success":true}`))
		}))
		cl, _ := newTestClient(t, srv, fakeclock.New(time.Unix(0, 0)))
		err := cl.DeleteVolume(context.Background(), 62762324)
		srv.Close()
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Fatalf("status %d: err = %v, want %v", c.status, err, c.want)
		}
	}
}
