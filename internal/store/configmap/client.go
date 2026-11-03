// Package configmap is the stdlib net/http Store client against one
// Kubernetes ConfigMap.
package configmap

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

type Config struct {
	APIServer      string
	Namespace      string
	Name           string
	TokenPath      string
	CAPath         string
	InstallationID string
}

func InCluster(name, installationID string) (Config, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return Config{}, fmt.Errorf("configmap: KUBERNETES_SERVICE_HOST/PORT not set")
	}
	ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return Config{}, fmt.Errorf("configmap: read namespace: %w", err)
	}
	return Config{ //nolint:gosec // a well-known mount path, not a credential value
		APIServer:      "https://" + host + ":" + port,
		Namespace:      strings.TrimSpace(string(ns)),
		Name:           name,
		TokenPath:      "/var/run/secrets/kubernetes.io/serviceaccount/token",
		CAPath:         "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		InstallationID: installationID,
	}, nil
}

type Client struct {
	cfg        Config
	httpClient *http.Client
	tokenMu    sync.Mutex
	token      string
	metaMu     sync.Mutex
	labels     map[string]string
	annots     map[string]string
}

var _ store.Store = (*Client)(nil)

func New(cfg Config) (*Client, error) {
	c := &Client{cfg: cfg}
	if cfg.CAPath != "" {
		ca, err := os.ReadFile(cfg.CAPath)
		if err != nil {
			return nil, fmt.Errorf("configmap: read CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("configmap: no certs found in %s", cfg.CAPath)
		}
		c.httpClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
			Timeout:   10 * time.Second,
		}
	} else {
		c.httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if err := c.loadToken(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) loadToken() error {
	b, err := os.ReadFile(c.cfg.TokenPath)
	if err != nil {
		return fmt.Errorf("configmap: read token: %w", err)
	}
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(string(b))
	c.tokenMu.Unlock()
	return nil
}
