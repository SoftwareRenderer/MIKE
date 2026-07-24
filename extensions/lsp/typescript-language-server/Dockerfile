# --- Stage 1: Install dependencies ---
FROM node:22-alpine AS builder

# Install typescript-language-server and typescript globally
# Pin TypeScript to 5.x — version 7+ uses native plugins without traditional tsserver.js
RUN npm install -g typescript@5 typescript-language-server@5


# --- Stage 2: Minimal Runtime Stage ---
FROM node:22-alpine

# Copy certificates for HTTPS requests made by tsserver/bridge
RUN apk add -U ca-certificates && update-ca-certificates

# Copy globally installed packages from builder
COPY --from=builder /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=builder /usr/local/bin /usr/local/bin

# Copy sandbox-bridge
COPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge

RUN chmod +x /usr/local/bin/sandbox-bridge

# Set working directory
WORKDIR /workspace

# Expose bridge port
EXPOSE 9999

# Run sandbox-bridge
CMD ["/usr/local/bin/sandbox-bridge"]
