# Stage 1: Build the Go binary
FROM golang:alpine AS builder

WORKDIR /src

# Download dependencies first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/qbot ./cmd/qbot

# Stage 2: Minimal runtime image
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /bin/qbot /app/qbot

# Create directory for state and logs
RUN mkdir -p /app/data

# Default state file location in container
ENV STATE_FILE=/app/data/bot_state.json

ENTRYPOINT ["/app/qbot"]
