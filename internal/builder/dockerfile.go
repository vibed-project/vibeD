package builder

import "strings"

// DetectLanguage inspects the file map and returns the best-guess language.
// Returns "static", "nodejs", "python", "go", or "rust".
func DetectLanguage(files map[string]string) string {
	hasGoFile := false
	for name := range files {
		lower := strings.ToLower(name)
		switch {
		case lower == "go.mod":
			return "go" // explicit module file wins immediately
		case lower == "cargo.toml":
			return "rust"
		case lower == "package.json":
			return "nodejs"
		case lower == "requirements.txt" || lower == "main.py" || lower == "app.py":
			return "python"
		case strings.HasSuffix(lower, ".go"):
			hasGoFile = true
		}
	}
	// Any .go source file without a go.mod is still a Go app —
	// the Dockerfile handles module init + tidy automatically.
	if hasGoFile {
		return "go"
	}
	// Check for HTML files (static site)
	for name := range files {
		if strings.HasSuffix(strings.ToLower(name), ".html") {
			return "static"
		}
	}
	return "static"
}

// GenerateDockerfile returns a Dockerfile for the given language.
// If language is empty or "auto", it auto-detects from the file map.
func GenerateDockerfile(language string, files map[string]string) string {
	if language == "" || language == "auto" {
		language = DetectLanguage(files)
	}

	switch language {
	case "nodejs":
		return dockerfileNodeJS(files)
	case "python":
		return dockerfilePython(files)
	case "go":
		return dockerfileGo()
	case "rust":
		return dockerfileRust()
	default:
		return dockerfileStatic()
	}
}

func dockerfileStatic() string {
	return `FROM nginx:alpine
RUN sed -i 's/listen\s*80;/listen 8080;/g' /etc/nginx/conf.d/default.conf
COPY . /usr/share/nginx/html
EXPOSE 8080
`
}

func dockerfileNodeJS(files map[string]string) string {
	entrypoint := "index.js"
	for _, candidate := range []string{"index.js", "server.js", "app.js", "main.js"} {
		if _, ok := files[candidate]; ok {
			entrypoint = candidate
			break
		}
	}
	return `FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci --production 2>/dev/null || npm install --production
COPY . .
RUN npm run build 2>/dev/null || true

FROM node:22-alpine
WORKDIR /app
COPY --from=build /app .
EXPOSE 8080
CMD ["node", "` + entrypoint + `"]
`
}

func dockerfilePython(files map[string]string) string {
	// Find the Python entry point: app.py, main.py, or first .py file
	entrypoint := "app.py"
	for _, candidate := range []string{"app.py", "main.py", "server.py", "run.py"} {
		if _, ok := files[candidate]; ok {
			entrypoint = candidate
			break
		}
	}
	return `FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt* ./
RUN pip install --no-cache-dir -r requirements.txt 2>/dev/null || true
COPY . .
EXPOSE 8080
CMD ["python", "` + entrypoint + `"]
`
}

func dockerfileRust() string {
	return `FROM rust:1.77-alpine AS build
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY . .
# If Cargo.toml is missing, init a minimal binary crate so any .rs source compiles.
RUN if [ ! -f Cargo.toml ]; then \
      cargo init --name server .; \
    fi && \
    cargo build --release
# Find the compiled binary regardless of crate name.
RUN cp $(find target/release -maxdepth 1 -type f -perm /111 | grep -v '\.d$' | head -1) /app/server-bin

FROM alpine:3.20
WORKDIR /app
COPY --from=build /app/server-bin ./server
EXPOSE 8080
CMD ["./server"]
`
}

func dockerfileGo() string {
	return `FROM golang:1.23-alpine AS build
WORKDIR /app
COPY . .
# If go.mod is missing, init a module and tidy to resolve all imports.
# This lets any Go app deploy without requiring go.mod / go.sum in the source.
RUN if [ ! -f go.mod ]; then \
      module=$(basename $(pwd)); \
      go mod init app/${module}; \
    fi && \
    go mod tidy && \
    go mod download
RUN CGO_ENABLED=0 go build -o server .

FROM alpine:3.20
WORKDIR /app
COPY --from=build /app/server .
EXPOSE 8080
CMD ["./server"]
`
}
