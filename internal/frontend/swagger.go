package frontend

import (
	_ "embed"
	"net/http"
)

//go:embed swagger.html
var swaggerHTML []byte

//go:embed openapi.yaml
var openapiSpec []byte

// swaggerUIHandler serves the Swagger UI page and the OpenAPI spec.
func swaggerUIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve the raw OpenAPI spec
		if r.URL.Path == "/openapi.yaml" {
			w.Header().Set("Content-Type", "application/yaml")
			w.Write(openapiSpec)
			return
		}

		// Serve the Swagger UI HTML for all other paths (including "/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(swaggerHTML)
	})
}
