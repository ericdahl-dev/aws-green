package webhooks_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/webhooks"
)

type capture struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  [][]byte
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *capture) header(i int, key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headers[i].Get(key)
}

func (c *capture) body(i int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[i]
}

// collectRequests spins up a server that records every request it receives.
func collectRequests(t *testing.T) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.headers = append(c.headers, r.Header.Clone())
		c.bodies = append(c.bodies, buf)
		methods = append(methods, r.Method)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		for _, m := range methods {
			if m != http.MethodPost {
				t.Errorf("expected POST, got %s", m)
			}
		}
	})
	return srv, c
}

func sampleEvent() webhooks.Event {
	return webhooks.Event{
		Event:        "pipeline_stuck",
		Reason:       "pipeline_in_progress",
		Project:      "my-app",
		Account:      "production",
		Region:       "us-east-1",
		ResourceType: webhooks.ResourcePipeline,
		Resource:     "my-app-DeploymentPipeline",
		Status:       "InProgress",
		Detail:       "stage Deploy",
		StuckSince:   time.Now().Add(-35 * time.Minute),
		Timestamp:    time.Now(),
	}
}

func TestDispatchPostsJSON(t *testing.T) {
	srv, c := collectRequests(t)

	webhooks.New([]config.Webhook{{URL: srv.URL}}).Dispatch(sampleEvent())

	if c.count() != 1 {
		t.Fatalf("expected 1 request, got %d", c.count())
	}
	if ct := c.header(0, "Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}

func TestDispatchHMACSignature(t *testing.T) {
	srv, c := collectRequests(t)

	secret := "topsecret"
	webhooks.New([]config.Webhook{{URL: srv.URL, Secret: secret}}).Dispatch(sampleEvent())

	if c.count() != 1 {
		t.Fatalf("expected 1 request, got %d", c.count())
	}

	sig := c.header(0, webhooks.SignatureHeader)
	if sig == "" {
		t.Fatalf("expected %s header", webhooks.SignatureHeader)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(c.body(0))
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != expected {
		t.Errorf("signature mismatch: got %s, want %s", sig, expected)
	}
}

func TestDispatchNoSignatureWithoutSecret(t *testing.T) {
	srv, c := collectRequests(t)

	webhooks.New([]config.Webhook{{URL: srv.URL}}).Dispatch(sampleEvent())

	if c.count() != 1 {
		t.Fatalf("expected 1 request, got %d", c.count())
	}
	if sig := c.header(0, webhooks.SignatureHeader); sig != "" {
		t.Errorf("expected no signature header, got %s", sig)
	}
}

func TestDispatchPayloadShape(t *testing.T) {
	srv, c := collectRequests(t)

	evt := webhooks.Event{
		Event:        "ecs_service_stuck",
		Reason:       "ecs_count_mismatch",
		Project:      "my-app",
		Account:      "production",
		Region:       "us-west-2",
		ResourceType: webhooks.ResourceECSService,
		Resource:     "my-app-web",
		Cluster:      "my-app-cluster",
		Detail:       "1 running / 3 desired (0 pending)",
		StuckSince:   time.Now().Add(-35 * time.Minute).Truncate(time.Second),
		Timestamp:    time.Now().Truncate(time.Second),
	}
	webhooks.New([]config.Webhook{{URL: srv.URL}}).Dispatch(evt)

	if c.count() != 1 {
		t.Fatalf("expected 1 request, got %d", c.count())
	}

	var got webhooks.Event
	if err := json.Unmarshal(c.body(0), &got); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if got.Event != evt.Event {
		t.Errorf("event: got %q, want %q", got.Event, evt.Event)
	}
	if got.Reason != evt.Reason {
		t.Errorf("reason: got %q, want %q", got.Reason, evt.Reason)
	}
	if got.Resource != evt.Resource || got.Cluster != evt.Cluster {
		t.Errorf("resource: got %q/%q", got.Cluster, got.Resource)
	}
	if !got.StuckSince.Equal(evt.StuckSince) {
		t.Errorf("stuck_since: got %v, want %v", got.StuckSince, evt.StuckSince)
	}

	// Optional fields must not appear when empty.
	var raw map[string]any
	if err := json.Unmarshal(c.body(0), &raw); err != nil {
		t.Fatalf("decoding raw payload: %v", err)
	}
	if _, ok := raw["status"]; ok {
		t.Error("expected empty status to be omitted")
	}
	for _, key := range []string{"event", "reason", "project", "resource_type", "resource", "stuck_since", "timestamp"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("expected %q in payload", key)
		}
	}
}

func TestDispatchNoWebhooksIsNoop(t *testing.T) {
	// Should not panic with an empty or nil hook list.
	webhooks.New(nil).Dispatch(sampleEvent())
}

func TestDispatchFailureSilent(t *testing.T) {
	// Nothing listening, plus a server that rejects the request: neither
	// failure may panic or propagate.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	d := webhooks.New([]config.Webhook{
		{URL: "http://127.0.0.1:1"},
		{URL: bad.URL},
	})
	d.Dispatch(sampleEvent())
}

func TestDispatchMultipleWebhooks(t *testing.T) {
	srv1, c1 := collectRequests(t)
	srv2, c2 := collectRequests(t)

	webhooks.New([]config.Webhook{{URL: srv1.URL}, {URL: srv2.URL}}).Dispatch(sampleEvent())

	if c1.count() != 1 {
		t.Errorf("srv1: expected 1 request, got %d", c1.count())
	}
	if c2.count() != 1 {
		t.Errorf("srv2: expected 1 request, got %d", c2.count())
	}
}
