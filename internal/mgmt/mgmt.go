package mgmt

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

const maxBodyBytes = 65536

// Controller is the subset of *controller.Controller this package may
// call; Admit/RequestEnd are deliberately absent so the management
// API can never touch the inference path.
type Controller interface {
	Start(ctx context.Context) (controller.Snapshot, error)
	Stop(ctx context.Context, force bool) (controller.Snapshot, error)
	Status() controller.Snapshot
}

type Deps struct {
	Controller Controller
	Keys       *auth.Keys
	Limiter    *auth.Limiter
	Clock      clock.Clock
	Config     config.Mgmt
	Reg        prometheus.Registerer
	Log        *slog.Logger
}

type server struct {
	d           Deps
	mux         *http.ServeMux
	start, stop *coalescer
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// New builds the /api/v1/gpu/* handler; the caller mounts it at that
// prefix and wraps it with its own request-id middleware — neither
// belongs to this package.
func New(d Deps) http.Handler {
	s := &server{d: d, mux: http.NewServeMux(), start: newCoalescer(), stop: newCoalescer()}
	registerMetrics(d.Reg)
	s.routes()
	return withMiddleware(s, d)
}
