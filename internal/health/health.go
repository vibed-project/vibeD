// Package health provides liveness and readiness HTTP endpoints.
package health

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Checker tracks component readiness and provides health endpoints.
type Checker struct {
	mu         sync.RWMutex
	components map[string]ComponentStatus
	startTime  time.Time
}

// ComponentStatus tracks whether a component is ready.
type ComponentStatus struct {
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// NewChecker creates a new Checker.
func NewChecker() *Checker {
	return &Checker{
		components: make(map[string]ComponentStatus),
		startTime:  time.Now(),
	}
}

// SetReady marks a component as ready.
func (c *Checker) SetReady(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.components[name] = ComponentStatus{Ready: true}
}

// SetNotReady marks a component as not ready with a message.
func (c *Checker) SetNotReady(name string, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.components[name] = ComponentStatus{Ready: false, Message: message}
}

// IsReady returns true if all components are ready.
func (c *Checker) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, cs := range c.components {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// LivenessHandler returns an HTTP handler for /healthz.
// Liveness always returns 200 if the process is running.
func (c *Checker) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "alive",
			"uptime": time.Since(c.startTime).String(),
		})
	}
}

// ReadinessHandler returns an HTTP handler for /readyz.
// Readiness returns 200 only when all components are ready.
func (c *Checker) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		components := make(map[string]ComponentStatus, len(c.components))
		for k, v := range c.components {
			components[k] = v
		}
		c.mu.RUnlock()

		ready := true
		for _, cs := range components {
			if !cs.Ready {
				ready = false
				break
			}
		}

		status := "ready"
		code := http.StatusOK
		if !ready {
			status = "not_ready"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":     status,
			"components": components,
		})
	}
}
