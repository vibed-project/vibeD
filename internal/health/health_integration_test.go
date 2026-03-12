//go:build integration

package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vibed-project/vibeD/internal/health"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthz_Returns200(t *testing.T) {
	checker := health.NewChecker()
	handler := checker.LivenessHandler()

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "alive", body["status"])
	assert.Contains(t, body, "uptime")
}

func TestReadyz_AllReady(t *testing.T) {
	checker := health.NewChecker()
	checker.SetReady("kubernetes")
	checker.SetReady("storage")
	checker.SetReady("store")

	handler := checker.ReadinessHandler()

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "ready", body["status"])
}

func TestReadyz_NotReady(t *testing.T) {
	checker := health.NewChecker()
	checker.SetReady("kubernetes")
	checker.SetNotReady("storage", "disk full")

	handler := checker.ReadinessHandler()

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "not_ready", body["status"])
}

func TestReadyz_ComponentDetails(t *testing.T) {
	checker := health.NewChecker()
	checker.SetReady("kubernetes")
	checker.SetNotReady("storage", "connection timeout")

	handler := checker.ReadinessHandler()

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var body map[string]interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)

	components, ok := body["components"].(map[string]interface{})
	require.True(t, ok, "response should contain components map")

	// Check kubernetes component
	k8s, ok := components["kubernetes"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, k8s["ready"])

	// Check storage component
	stor, ok := components["storage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, stor["ready"])
	assert.Equal(t, "connection timeout", stor["message"])
}

func TestReadyz_NoComponents(t *testing.T) {
	checker := health.NewChecker()
	// No components set — should be ready (vacuously true)

	handler := checker.ReadinessHandler()

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestIsReady(t *testing.T) {
	checker := health.NewChecker()
	assert.True(t, checker.IsReady(), "empty checker should be ready")

	checker.SetReady("component-a")
	assert.True(t, checker.IsReady())

	checker.SetNotReady("component-b", "initializing")
	assert.False(t, checker.IsReady())

	checker.SetReady("component-b")
	assert.True(t, checker.IsReady())
}
