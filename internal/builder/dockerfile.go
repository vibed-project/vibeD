package builder

import "github.com/vibed-project/vibeD/internal/appspec"

// GenerateDockerfile returns a Dockerfile for the given language.
// If language is empty or "auto", it auto-detects from the file map.
func GenerateDockerfile(language string, files map[string]string) string {
	if language == "" || language == "auto" {
		language = appspec.DetectLanguage(files)
	}

	switch language {
	case appspec.LangNodeJS:
		return dockerfileNodeJS(files)
	case appspec.LangPython:
		return dockerfilePython(files)
	case appspec.LangGo:
		return dockerfileGo()
	case appspec.LangRust:
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
	entrypoint := appspec.Entrypoint(appspec.LangNodeJS, files)
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
	entrypoint := appspec.Entrypoint(appspec.LangPython, files)
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
