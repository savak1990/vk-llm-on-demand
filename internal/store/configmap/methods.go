package configmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

type rawMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
}

type rawConfigMap struct {
	Kind       string            `json:"kind,omitempty"`
	APIVersion string            `json:"apiVersion,omitempty"`
	Metadata   rawMetadata       `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
}

func (c *Client) doOnce(ctx context.Context, method, url string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("configmap: build request: %w", err)
	}
	c.tokenMu.Lock()
	tok := c.token
	c.tokenMu.Unlock()
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("configmap: %s %s: %w", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("configmap: read response: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

// do retries exactly once on 401 (the mounted token rotates); a second 401
// is terminal, never a third request.
func (c *Client) do(ctx context.Context, method, url string, body []byte) (int, []byte, error) {
	status, respBody, err := c.doOnce(ctx, method, url, body)
	if err != nil || status != http.StatusUnauthorized {
		return status, respBody, err
	}
	if err := c.loadToken(); err != nil {
		return 0, nil, err
	}
	status, respBody, err = c.doOnce(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	if status == http.StatusUnauthorized {
		return 0, nil, fmt.Errorf("configmap: unauthorized after token reload")
	}
	return status, respBody, nil
}

func (c *Client) url() string {
	return fmt.Sprintf("%s/api/v1/namespaces/%s/configmaps/%s", c.cfg.APIServer, c.cfg.Namespace, c.cfg.Name)
}

func (c *Client) Load(ctx context.Context) (store.Document, store.Version, error) {
	status, body, err := c.do(ctx, http.MethodGet, c.url(), nil)
	if err != nil {
		store.RecordOp("load", "error")
		return store.Document{}, "", err
	}
	switch status {
	case http.StatusOK:
		var cm rawConfigMap
		if err := json.Unmarshal(body, &cm); err != nil {
			store.RecordOp("load", "error")
			return store.Document{}, "", fmt.Errorf("configmap: decode response: %w", err)
		}
		c.cacheMeta(cm.Metadata)
		raw, ok := cm.Data["state.json"]
		if !ok {
			doc, err := store.NewDocument(c.cfg.InstallationID)
			if err != nil {
				store.RecordOp("load", "error")
				return store.Document{}, "", err
			}
			store.RecordOp("load", "ok")
			return doc, store.Version(cm.Metadata.ResourceVersion), nil
		}
		// Decode's only failures are ErrCorrupt and ErrSchemaTooNew; the
		// metrics enum has no separate "schema_too_new" label, so both fold
		// into "corrupt".
		doc, err := store.Decode([]byte(raw))
		if err != nil {
			store.RecordOp("load", "corrupt")
			return store.Document{}, "", err
		}
		if doc.InstallationID != c.cfg.InstallationID {
			store.RecordOp("load", "installation")
			return store.Document{}, "", store.ErrInstallation
		}
		store.RecordOp("load", "ok")
		return doc, store.Version(cm.Metadata.ResourceVersion), nil
	case http.StatusNotFound:
		store.RecordOp("load", "missing")
		return store.Document{}, "", fmt.Errorf("%w: ConfigMap %s not found: install via the chart or create an empty ConfigMap", store.ErrStoreMissing, c.cfg.Name)
	case http.StatusForbidden:
		store.RecordOp("load", "error")
		return store.Document{}, "", fmt.Errorf("configmap: get forbidden, check RBAC for %s", c.cfg.Name)
	default:
		store.RecordOp("load", "error")
		return store.Document{}, "", fmt.Errorf("configmap: get status %d", status)
	}
}

func (c *Client) cacheMeta(m rawMetadata) {
	c.metaMu.Lock()
	c.labels, c.annots = m.Labels, m.Annotations
	c.metaMu.Unlock()
}

// Save echoes the labels/annotations cached by the most recent Load or
// Save. This relies on store.Transition always calling Load before Save; a
// direct Save with no prior Load on this Client sends empty metadata.
func (c *Client) Save(ctx context.Context, d store.Document, v store.Version) (store.Version, error) {
	if v == "" {
		return "", fmt.Errorf("configmap: save with empty version is a programming error")
	}
	encoded, err := store.Encode(d)
	if err != nil {
		outcome := "error"
		if errors.Is(err, store.ErrTooLarge) {
			outcome = "too_large"
		}
		store.RecordOp("save", outcome)
		return "", err
	}
	c.metaMu.Lock()
	labels, annots := c.labels, c.annots
	c.metaMu.Unlock()
	cm := rawConfigMap{
		Kind: "ConfigMap", APIVersion: "v1",
		Metadata: rawMetadata{
			Name: c.cfg.Name, Namespace: c.cfg.Namespace, ResourceVersion: string(v),
			Labels: labels, Annotations: annots,
		},
		Data: map[string]string{"state.json": string(encoded)},
	}
	body, err := json.Marshal(cm)
	if err != nil {
		store.RecordOp("save", "error")
		return "", fmt.Errorf("configmap: encode request: %w", err)
	}
	status, respBody, err := c.do(ctx, http.MethodPut, c.url(), body)
	if err != nil {
		store.RecordOp("save", "error")
		return "", err
	}
	switch status {
	case http.StatusOK:
		var updated rawConfigMap
		if err := json.Unmarshal(respBody, &updated); err != nil {
			store.RecordOp("save", "error")
			return "", fmt.Errorf("configmap: decode response: %w", err)
		}
		c.cacheMeta(updated.Metadata)
		store.RecordOp("save", "ok")
		return store.Version(updated.Metadata.ResourceVersion), nil
	case http.StatusConflict:
		store.RecordOp("save", "conflict")
		return "", store.ErrConflict
	case http.StatusNotFound:
		store.RecordOp("save", "missing")
		return "", fmt.Errorf("%w: ConfigMap %s not found", store.ErrStoreMissing, c.cfg.Name)
	default:
		store.RecordOp("save", "error")
		return "", fmt.Errorf("configmap: put status %d", status)
	}
}
