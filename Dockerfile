# syntax=docker/dockerfile:1

# --- stage 1: frontend (Vite) ---
FROM node:22-slim AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# --- stage 2: go build (static, CGO-free) ---
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./frontend/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /pingway ./cmd/pingway

# --- final: distroless static (nonroot) + tzdata + ca-certificates ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /pingway /pingway
ENV GOMEMLIMIT=80MiB
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["/pingway", "-healthcheck"]
ENTRYPOINT ["/pingway"]
