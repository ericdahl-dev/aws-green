// Package webhooks POSTs stuck-resource events to user-configured endpoints.
package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/logx"
)

// SignatureHeader carries the HMAC-SHA256 signature of the request body when
// the webhook is configured with a secret.
const SignatureHeader = "X-Aws-Green-Signature"

// Resource type values used in Event.ResourceType.
const (
	ResourcePipeline   = "pipeline"
	ResourceStack      = "stack"
	ResourceECSService = "ecs_service"
)

// Event is the JSON payload POSTed to each webhook.
type Event struct {
	Event        string    `json:"event"`
	Reason       string    `json:"reason"`
	Project      string    `json:"project"`
	Account      string    `json:"account,omitempty"`
	Region       string    `json:"region,omitempty"`
	ResourceType string    `json:"resource_type"`
	Resource     string    `json:"resource"`
	Cluster      string    `json:"cluster,omitempty"`
	Status       string    `json:"status,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	StuckSince   time.Time `json:"stuck_since"`
	Timestamp    time.Time `json:"timestamp"`
}

// Dispatcher sends webhook events to configured endpoints.
type Dispatcher struct {
	hooks  []config.Webhook
	client *http.Client
}

// New creates a Dispatcher for the given webhook configs.
func New(hooks []config.Webhook) *Dispatcher {
	return &Dispatcher{
		hooks:  hooks,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Dispatch POSTs evt to all configured webhooks. Failures are logged but not
// returned — they must not interrupt the poll cycle.
func (d *Dispatcher) Dispatch(evt Event) {
	if len(d.hooks) == 0 {
		return
	}

	body, err := json.Marshal(evt)
	if err != nil {
		logx.Debug("webhook marshal failed", "err", err)
		return
	}

	for _, wh := range d.hooks {
		if err := d.post(wh, body); err != nil {
			logx.Debug("webhook POST failed", "url", wh.URL, "err", err)
		}
	}
}

func (d *Dispatcher) post(wh config.Webhook, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		req.Header.Set(SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}
