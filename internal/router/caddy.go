// Package router talks to a Caddy instance's admin API to keep its route
// table in sync with the set of Ready VibedApps observed by vibed-router.
//
// vibeD targets Caddy because its DNS-01 wildcard cert story is the
// simplest path to "every app gets *.vibed.example.com without per-app
// ACME" (refactor.md §5.5). The admin API at :2019 lets us add and remove
// routes by @id without re-loading the whole config — important when
// hundreds of apps come and go on the warm pool.
//
// Caddy expects the apps/http/servers/srv0/routes array to be addressable;
// the vibeD convention is one route per app, with @id = "vibed-app/<label>"
// (where label is the 12-char DNS label from
// controller.AppLabel). DeleteRoute targets the @id directly so misordered
// reconciles never delete the wrong app's route.
package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Route is the shape vibed-router cares about — host name to match, and
// the upstream pod address to reverse-proxy to. Caddy's JSON config has
// many more knobs; the marshaler here picks safe defaults for everything
// else.
type Route struct {
	// ID is the Caddy @id tag for this route. Stable across reconciles so
	// updates and deletes can target it directly.
	ID string
	// Host is the matched FQDN, e.g. "abcdef012345.vibed.example.com".
	Host string
	// Upstream is the dial target Caddy reverse-proxies to,
	// e.g. "10.0.0.42:8080".
	Upstream string
}

// CaddyClient is a thin wrapper around Caddy's admin API. Safe for
// concurrent use; each method makes its own HTTP request.
type CaddyClient struct {
	// AdminURL is the base of Caddy's admin API, typically
	// "http://localhost:2019" (in-process) or
	// "http://vibed-caddy.vibed-system.svc.cluster.local:2019" (separate
	// Deployment).
	AdminURL string
	// HTTP is the client used for requests. Zero value uses http.DefaultClient
	// with a 5 s timeout.
	HTTP *http.Client
	// RoutesPath is the JSON path to the routes array on the target server.
	// Defaults to /config/apps/http/servers/srv0/routes. Override for
	// alternate Caddy configs (e.g. multiple servers).
	RoutesPath string
}

// NewCaddyClient returns a client with sensible defaults pointing at adminURL.
func NewCaddyClient(adminURL string) *CaddyClient {
	return &CaddyClient{
		AdminURL:   adminURL,
		HTTP:       &http.Client{Timeout: 5 * time.Second},
		RoutesPath: "/config/apps/http/servers/srv0/routes",
	}
}

// EnsureRoute creates the route if it doesn't already exist, or replaces
// the existing route with the same @id. Implementation: try PATCH first
// (replaces by @id); on 404, POST to append.
func (c *CaddyClient) EnsureRoute(ctx context.Context, r Route) error {
	if err := r.validate(); err != nil {
		return err
	}
	body, err := routeJSON(r)
	if err != nil {
		return err
	}

	// PATCH /id/<id> replaces the existing route in place. Caddy returns
	// 404 if no route with that @id exists yet — we fall back to POST on
	// the routes array to append.
	if status, err := c.do(ctx, http.MethodPatch, "/id/"+r.ID, body); err == nil && status/100 == 2 {
		return nil
	} else if err != nil {
		return err
	}
	// PATCH 404'd. POST the route object to the array path to append it.
	// (Caddy's "/..." token expects an ARRAY body and expands it; posting a
	// single object to the bare array path appends that one element.)
	if status, err := c.do(ctx, http.MethodPost, c.routesPath(), body); err != nil {
		return err
	} else if status/100 != 2 {
		return fmt.Errorf("caddy POST routes: status %d", status)
	}
	return nil
}

// DeleteRoute removes the route with the given @id. A 404 is treated as
// success — the route is already gone, which is the desired state.
func (c *CaddyClient) DeleteRoute(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("CaddyClient.DeleteRoute: id is empty")
	}
	status, err := c.do(ctx, http.MethodDelete, "/id/"+id, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound || status/100 == 2 {
		return nil
	}
	return fmt.Errorf("caddy DELETE id/%s: status %d", id, status)
}

// ListRouteIDs returns every @id currently in the routes array that
// matches the vibed-app/* prefix. Used by the reconciler to compute the
// set of routes to delete (routes Caddy has but no VibedApp claims).
func (c *CaddyClient) ListRouteIDs(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.AdminURL+c.routesPath(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy GET routes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Caddy has no routes block at all yet — fine, the next EnsureRoute
		// will create it via POST.
		return nil, nil
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("caddy GET routes: status %d", resp.StatusCode)
	}
	var rs []struct {
		ID string `json:"@id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
		return nil, fmt.Errorf("decode routes: %w", err)
	}
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.ID == "" {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// ---- helpers ----

func (c *CaddyClient) routesPath() string {
	if c.RoutesPath != "" {
		return c.RoutesPath
	}
	return "/config/apps/http/servers/srv0/routes"
}

func (c *CaddyClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *CaddyClient) do(ctx context.Context, method, path string, body []byte) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.AdminURL+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("caddy %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	// Drain so the underlying connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (r Route) validate() error {
	if r.ID == "" {
		return fmt.Errorf("route ID is empty")
	}
	if r.Host == "" {
		return fmt.Errorf("route Host is empty")
	}
	if r.Upstream == "" {
		return fmt.Errorf("route Upstream is empty")
	}
	return nil
}

// routeJSON marshals a Route into the Caddy server-route JSON shape. The
// matcher block is host-only (one hostname per app), the handler is
// reverse_proxy to the configured upstream.
func routeJSON(r Route) ([]byte, error) {
	doc := map[string]any{
		"@id": r.ID,
		"match": []map[string]any{
			{"host": []string{r.Host}},
		},
		"handle": []map[string]any{
			{
				"handler": "reverse_proxy",
				"upstreams": []map[string]any{
					{"dial": r.Upstream},
				},
			},
		},
		"terminal": true,
	}
	return json.Marshal(doc)
}
