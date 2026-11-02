# Bake toolchain + LRP bubblewrap deps. AppArmor is intentionally not required —
# isolation is bubblewrap namespaces; Docker runners cannot load host AA policy.
FROM myoung34/github-runner:ubuntu-noble

USER root
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bubblewrap \
    build-essential \
    ca-certificates \
    curl \
    git \
    jq \
    python3 \
    python3-pip \
    python3-venv \
    unzip \
  && rm -rf /var/lib/apt/lists/*

ARG GO_VERSION=1.26.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -C /usr/local -xz \
  && ln -sf /usr/local/go/bin/go /usr/local/bin/go \
  && ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

ARG NODE_MAJOR=20
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
  && apt-get install -y --no-install-recommends nodejs \
  && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh

# The base entrypoint defaults RUN_AS_ROOT to true; explicitly select its
# non-root path before changing USER. Install tooling above as root.
ENV RUN_AS_ROOT=false
USER runner
WORKDIR /actions-runner
