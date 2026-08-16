package runneragent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to a runner agent's control API. vibeD uses it to inject user
// source into a pooled runner pod and to observe the user process. The control
// API is reachable only in-cluster; requests carry a bearer token.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient returns a Client for the agent at controlURL (e.g.
// "http://vibed-runner-python-ab12.vibed-runners.svc.cluster.local:9000").
// token must match the agent's VIBED_AGENT_TOKEN; pass "" if the agent runs
// without auth.
//
// Total request duration is governed by the caller's context deadline — every
// production call site passes one. The client deliberately sets no
// whole-request Timeout: it would silently truncate any caller deadline above
// it (e.g. the injector's 60s cold-start budget). The transport only bounds
// the individual phases of a hung connection.
// sharedTransport is process-wide on purpose. A Transport owns its connection
// pool, and every production call site builds a Client per call (readiness
// probe per reconcile, source injection, warm-pool image validation). A
// per-Client Transport therefore reused no connection and — with no
// IdleConnTimeout, whose zero value means "never expire" — parked each one in a
// private pool that nothing could ever drain, leaking sockets and their reader
// goroutines for the controller's lifetime. One shared Transport both fixes the
// leak and lets keep-alives actually do their job.
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
	// /inject answers only after a synchronous tarball download + process
	// start, which can legitimately approach the injector's 60s budget. Keep
	// this comfortably above it so it trips only on a truly wedged agent,
	// never on a slow cold start.
	ResponseHeaderTimeout: 2 * time.Minute,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   4,
	IdleConnTimeout:       90 * time.Second,
}

func NewClient(controlURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(controlURL, "/"),
		token:   token,
		hc:      &http.Client{Transport: sharedTransport},
	}
}

// Inject writes the user's source into the runner and (re)starts the user
// process, returning the resulting process status.
func (c *Client) Inject(ctx context.Context, req InjectRequest) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.do(ctx, http.MethodPost, PathInject, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status returns the current user-process status.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.do(ctx, http.MethodGet, PathStatus, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Logs returns recent captured lines of the user process's output. lines <= 0
// returns everything the agent still has buffered.
func (c *Client) Logs(ctx context.Context, lines int) (*LogsResponse, error) {
	path := PathLogs
	if lines > 0 {
		path += "?" + url.Values{"lines": {strconv.Itoa(lines)}}.Encode()
	}
	var out LogsResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Stop stops the user process, returning the runner to idle.
func (c *Client) Stop(ctx context.Context) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.do(ctx, http.MethodPost, PathStop, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Healthz reports whether the agent's control API is reachable. It does not
// say anything about the user process — use Status for that.
func (c *Client) Healthz(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, PathHealthz, nil, nil)
}

// Info returns the image's declared language + runtime-probe result, used by
// bring-your-own base-image validation.
func (c *Client) Info(ctx context.Context) (*InfoResponse, error) {
	var out InfoResponse
	if err := c.do(ctx, http.MethodGet, PathInfo, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// do performs one control-API request. body, when non-nil, is JSON-encoded;
// out, when non-nil, receives the decoded JSON response.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("runner agent %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var er ErrorResponse
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if json.Unmarshal(raw, &er) == nil && er.Error != "" {
			return fmt.Errorf("runner agent %s %s: %s (%d)", method, path, er.Error, resp.StatusCode)
		}
		return fmt.Errorf("runner agent %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if out == nil {
		// Drain so the underlying connection can be reused. Bounded: a
		// well-behaved agent sends at most a short acknowledgement here.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding runner agent response: %w", err)
	}
	// The decoder stops at the end of the JSON value; drain any trailing
	// bytes (e.g. a final newline) so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}
