// internal/provider/provider.go
package provider

import (
	"context"
	"time"
)

type Provider interface {
	SearchOffers(ctx context.Context, f OfferFilter) ([]Offer, error)
	CreateInstance(ctx context.Context, r CreateRequest) (InstanceID, error)
	GetInstance(ctx context.Context, id InstanceID) (Instance, error)
	ListInstances(ctx context.Context, labelPrefix string) ([]Instance, error)
	DestroyInstance(ctx context.Context, id InstanceID) error
	Balance(ctx context.Context) (Money, error)
}

type InstanceID int64

// Money is micro-USD; avoids float drift when the ledger sums many values.
type Money int64

func (m Money) USD() float64 { return float64(m) / 1e6 }

type OfferFilter struct {
	GPUName           string
	NumGPUs           int
	MinCPUCores       int
	MinCPURAMGB       int
	MinDiskGB         int
	MinReliability    float64
	MinInetDownMBps   float64
	MinDiskBwMBps     float64
	MinCUDA           float64
	Geolocation       []string
	MaxHourly         Money
	ExcludeMachineIDs []int64
}

type Offer struct {
	ID, MachineID                       int64
	GPUName, Geolocation                string
	NumGPUs                             int
	Hourly                              Money
	Reliability, InetDownMBps, DiskBwMBps, CUDAMaxGood float64
}

type CreateRequest struct {
	OfferID int64
	Image   string
	DiskGB  int
	RunType string
	Env     map[string]string
	Onstart string
	Label   string
}

type InstanceStatus int

const (
	StatusOther InstanceStatus = iota
	StatusRunning
	StatusExited
	StatusOffline
	StatusUnknown
)

func (s InstanceStatus) Terminal() bool {
	switch s {
	case StatusExited, StatusOffline, StatusUnknown:
		return true
	default:
		return false
	}
}

// MapStatus turns Vast's actual_status string into InstanceStatus; any
// string not in this table is non-terminal, with the raw value preserved.
func MapStatus(raw string) InstanceStatus {
	switch raw {
	case "running":
		return StatusRunning
	case "exited":
		return StatusExited
	case "offline":
		return StatusOffline
	case "unknown":
		return StatusUnknown
	default:
		return StatusOther
	}
}

type Instance struct {
	ID               InstanceID
	Label, StatusMsg string
	Status           InstanceStatus
	RawStatus        string
	MachineID        int64
	PublicIP, SSHHost string
	SSHPort          int
	Hourly           Money
	StartedAt        time.Time
}
