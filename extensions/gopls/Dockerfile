# --- Stage 1: Build Stage ---
FROM golang:1-alpine AS builder

# Install build dependencies required by git/Go
RUN apk add -U git ca-certificates

# Install gopls
RUN go install golang.org/x/tools/gopls@latest


# --- Stage 2: Minimal Runtime Stage ---
FROM alpine:latest

# gopls requires the go toolchain for module resolution, diagnostics, etc.
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Copy certificates for HTTPS requests made by gopls/bridge
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy gopls and sandbox-bridge
COPY --from=builder /go/bin/gopls /usr/local/bin/gopls
COPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge

RUN chmod +x /usr/local/bin/sandbox-bridge /usr/local/bin/gopls

# Set working directory
WORKDIR /workspace

# Expose bridge port
EXPOSE 9999

# Run sandbox-bridge
CMD ["/usr/local/bin/sandbox-bridge"]
