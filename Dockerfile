# Stage 1: Build stage
FROM golang:1.25.0-alpine AS builder

# Install build dependencies including CGO support
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with CGO enabled
RUN CGO_ENABLED=1 go build -o /tmp/nipper ./cmd/nipper

# Stage 2: Final stage (alpine:2)
FROM alpine:3

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /tmp/nipper /usr/local/bin/nipper

# Set entrypoint
ENTRYPOINT []
