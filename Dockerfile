# syntax=docker/dockerfile:1

# 1. Frontend: build the React app into web/dist.
FROM node:26-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Backend: a static Go binary with the frontend embedded.
FROM golang:1.27-alpine AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=0 GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
COPY --from=web /src/web/dist web/dist
RUN go build -trimpath -ldflags="-s -w" -o /argo-center ./cmd/argo-center

# 3. Runtime: no shell, no package manager. Runs as a non-root user (OpenShift assigns its own UID).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /argo-center /argo-center
EXPOSE 8080
ENTRYPOINT ["/argo-center"]
CMD ["-config", "/etc/argo-center/config.yaml"]
