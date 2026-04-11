# ── Stage 1: Go dispatcher build ─────────────────────────────────────
FROM golang:1.25-alpine AS go-builder
WORKDIR /build
COPY dispatcher/go.mod dispatcher/go.sum ./
RUN go mod download
COPY dispatcher/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /dispatcher .

# ── Stage 2: Node backend install ────────────────────────────────────
# Use the Debian-based Node image so the resulting node_modules is
# binary-compatible with the Debian slim runtime stage below (glibc,
# not musl).
FROM node:22-bookworm-slim AS node-builder
WORKDIR /opt/mcp-hub/node
COPY node/package.json node/package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force

# ── Stage 3: Python backend clone + uv sync ──────────────────────────
FROM python:3.11-slim AS python-builder
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    pip install --no-cache-dir uv==0.5.11

WORKDIR /opt/mcp-hub/python
# Pinned commit — bump via python/README.md then rebuild. Using
# --python python3.11 forces uv to reuse the system Python interpreter
# so the resulting .venv is portable to the runtime image (otherwise
# uv downloads its own managed Python under /root/.local/share which
# is NOT copied across stages).
ARG GWS_MCP_SHA=93b4f4547bff4e655b338c711285f864bad6d479
RUN git clone https://github.com/taylorwilsdon/google_workspace_mcp.git google_workspace_mcp && \
    cd google_workspace_mcp && \
    git -c advice.detachedHead=false checkout "$GWS_MCP_SHA" && \
    uv sync --frozen --no-dev --python python3.11 && \
    rm -rf .git

# ── Stage 4: Runtime ─────────────────────────────────────────────────
FROM python:3.11-slim

# Install Node.js 22, curl (HEALTHCHECK), ca-certificates, and uv.
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates gnupg && \
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    apt-get purge -y --auto-remove gnupg && \
    rm -rf /var/lib/apt/lists/* && \
    pip install --no-cache-dir uv==0.5.11

COPY --from=go-builder     /dispatcher                              /usr/local/bin/dispatcher
COPY --from=node-builder   /opt/mcp-hub/node                        /opt/mcp-hub/node
COPY --from=python-builder /opt/mcp-hub/python/google_workspace_mcp /opt/mcp-hub/python/google_workspace_mcp
# The operator's own config.yaml is baked into the image at build time.
# If config.yaml is missing, the build fails — you must copy
# config.example.yaml → config.yaml and customize it first.
COPY config.yaml                                                    /etc/mcp-hub/config.yaml

RUN useradd --system --uid 1000 --home /opt/mcp-hub --shell /usr/sbin/nologin mcphub && \
    chown -R mcphub:mcphub /opt/mcp-hub /etc/mcp-hub
USER mcphub

EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD curl -fsS http://localhost:8090/health || exit 1

ENTRYPOINT ["/usr/local/bin/dispatcher"]
CMD ["--config", "/etc/mcp-hub/config.yaml"]
