# Build stage
FROM golang:1.25-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /app

# Install git for fetching dependencies
RUN apk add --no-cache git

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w \
  -X github.com/keanucz/AdobeConnectDL/internal/version.Version=${VERSION} \
  -X github.com/keanucz/AdobeConnectDL/internal/version.Commit=${COMMIT} \
  -X github.com/keanucz/AdobeConnectDL/internal/version.Date=${BUILD_DATE}" \
  -o /adobeconnectdl .

# Runtime stage
FROM alpine:3

# Install gpac (MP4Box) for subtitle embedding and ca-certificates for HTTPS
RUN apk add --no-cache gpac ca-certificates

# Create non-root user
RUN adduser -D -u 1000 appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /adobeconnectdl /usr/local/bin/adobeconnectdl

# Create output directory
RUN mkdir -p /output && chown appuser:appuser /output

USER appuser

WORKDIR /output

ENTRYPOINT ["adobeconnectdl"]
CMD ["--help"]
