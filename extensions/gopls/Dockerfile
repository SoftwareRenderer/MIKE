# gopls Extension - Go Language Server
# Runs gopls inside a gVisor sandbox with the sandbox-bridge for LSP forwarding.

FROM ubuntu:22.04

# Avoid interactive prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install Go and build essentials
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    git \
    && rm -rf /var/lib/apt/lists/*

# Install Go
ENV GO_VERSION=1.22.0
ENV GOPATH=/root/go
ENV PATH=$PATH:/usr/local/go/bin:$GOPATH/bin

RUN curl -fsSL https://dl.google.com/go/go${GO_VERSION}.linux-amd64.tar.gz \
    | tar -C /usr/local -xzf -

# Install gopls
RUN go install golang.org/x/tools/gopls@latest

# Install sandbox-bridge (built from host, copied at build time)
# The bridge binary is placed in /usr/local/bin/sandbox-bridge
COPY sandbox-bridge /usr/local/bin/sandbox-bridge
RUN chmod +x /usr/local/bin/sandbox-bridge

# Working directory
WORKDIR /workspace

# Expose bridge port
EXPOSE 9999

# Run sandbox-bridge which handles LSP forwarding
CMD ["/usr/local/bin/sandbox-bridge"]
