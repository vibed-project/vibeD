package builder

import "strings"

// DetectLanguage inspects the file map and returns the best-guess language.
// Returns "static", "nodejs", "python", or "go".
func DetectLanguage(files map[string]string) string {
	for name := range files {
		lower := strings.ToLower(name)
		switch {
		case lower == "go.mod":
			return "go"
		case lower == "package.json":
			return "nodejs"
		case lower == "requirements.txt" || lower == "main.py" || lower == "app.py":
			return "python"
		}
	}
	// Check for HTML files (static site)
	for name := range files {
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".html") {
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

func dockerfileGo() string {
	return `FROM golang:1.23-alpine AS build
WORKDIR /app
COPY go.* ./
RUN go mod download 2>/dev/null || true
COPY . .
RUN CGO_ENABLED=0 go build -o server .

FROM alpine:3.20
WORKDIR /app
COPY --from=build /app/server .
EXPOSE 8080
CMD ["./server"]
`
}
