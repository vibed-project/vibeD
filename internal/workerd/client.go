package workerd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"time"
)

// Client talks to the workerd loader sidecars from vibed-controller. The
// loader runs as a StatefulSet of Replicas pods; each app is pinned to one
// replica by hash(app_id) % Replicas (refactor.md §5.6), so the same app
// always lands on the same pod and its isolate is reused across redeploys.
type Client struct {
	// Replicas is the workerd StatefulSet size.
	Replicas int
	// ControlPort is the loader's control-API port on each replica.
	ControlPort int
	// ReplicaHost returns the in-cluster DNS name (no port) of replica idx,
	// e.g. "vibed-workerd-2.vibed-workerd.vibed-system.svc.cluster.local".
	ReplicaHost func(idx int) string

	Token string
	HTTP  *http.Client
}

// Target is where a deployed worker is reachable: the replica host and the
// socket port the loader assigned. vibed-router proxies the app's hostname
// here.
type Target struct {
	Host string
	Port int
}

// Addr renders the target as host:port.
func (t Target) Addr() string { return fmt.Sprintf("%s:%d", t.Host, t.Port) }

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// shard returns the replica index an app id is pinned to.
func (c *Client) shard(appID string) int {
	if c.Replicas <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(appID))
	return int(h.Sum32() % uint32(c.Replicas))
}

// Deploy pushes the app's source to its replica's loader and returns the
// reachable Target.
func (c *Client) Deploy(ctx context.Context, appID, sourceURL, entrypoint string) (Target, error) {
	idx := c.shard(appID)
	host := c.ReplicaHost(idx)
	base := fmt.Sprintf("http://%s:%d", host, c.ControlPort)

	body, _ := json.Marshal(DeployRequest{AppID: appID, SourceURL: sourceURL, Entrypoint: entrypoint})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/deploy", bytes.NewReader(body))
	if err != nil {
		return Target{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.client().Do(req)
	if err != nil {
		return Target{}, fmt.Errorf("workerd deploy (replica %d): %w", idx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Target{}, fmt.Errorf("workerd deploy (replica %d): status %d: %s", idx, resp.StatusCode, msg)
	}
	var dr DeployResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return Target{}, fmt.Errorf("workerd deploy decode: %w", err)
	}
	return Target{Host: host, Port: dr.Port}, nil
}

// Remove deletes the app from its replica's loader. A 404 is success.
func (c *Client) Remove(ctx context.Context, appID string) error {
	idx := c.shard(appID)
	base := fmt.Sprintf("http://%s:%d", c.ReplicaHost(idx), c.ControlPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/apps/"+appID, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("workerd remove (replica %d): %w", idx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("workerd remove (replica %d): status %d", idx, resp.StatusCode)
}

func (c *Client) setAuth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}
