# ---------------------------------------------------------
# Stage 1: Build
# ---------------------------------------------------------
FROM golang:1.24-alpine AS builder

# Enable Go modules and speed up builds
ENV CGO_ENABLED=0 \
    GO111MODULE=on

# Install build tools (optional, but useful)
RUN apk add --no-cache git

WORKDIR /app

# Copy go.mod and go.sum first (for better caching)
COPY shepherd/go.mod shepherd/go.sum ./
RUN go mod download

# Copy source code
COPY shepherd/ .

# Build the binary (static)
RUN go build -o /shepherd ./main.go

# ---------------------------------------------------------
# Stage 2: Runtime (minimal)
# ---------------------------------------------------------
FROM alpine:3.20

# Add CA certificates so HTTPS calls (to kube API, Locust, etc.) work
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /shepherd /usr/local/bin/shepherd

# Non-root user for better security
RUN adduser -D -u 10001 shepherd
USER shepherd

# Default working dir
WORKDIR /app

# Entrypoint
ENTRYPOINT ["/usr/local/bin/shepherd"]
