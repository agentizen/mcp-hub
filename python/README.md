# Python MCP backends

This directory holds the pinned references for Python MCP servers that
the dispatcher spawns on demand. Unlike the Node directory, most Python
MCPs are cloned from their upstream git repository at image build time
(rather than published to PyPI), so there is no local `pyproject.toml`
or `uv.lock` — each cloned project brings its own.

## Vendored projects

| Project | Upstream | Pinned commit | Handles |
|---|---|---|---|
| `google_workspace_mcp` | [taylorwilsdon/google_workspace_mcp](https://github.com/taylorwilsdon/google_workspace_mcp) | `93b4f4547bff4e655b338c711285f864bad6d479` (workspace-mcp 1.18.0) | `google-drive`, `gmail`, `google-calendar` |

The pinned commit is also referenced as the default value of the
`GWS_MCP_SHA` build arg in the root `Dockerfile` so a release rebuild
is fully reproducible.

## Adding a new Python MCP

1. **Pin an upstream commit** — open the upstream repo, pick a commit
   you trust, and record the 40-char SHA here.

2. **Add a new stage to the Dockerfile**

   ```dockerfile
   ARG MY_MCP_SHA=<sha>
   RUN git clone https://github.com/owner/my_mcp.git /opt/mcp-hub/python/my_mcp && \
       cd /opt/mcp-hub/python/my_mcp && \
       git checkout $MY_MCP_SHA && \
       uv sync --frozen
   ```

3. **Declare a subprocess + handle(s) in `config.yaml`**

   ```yaml
   subprocesses:
     - name: my-mcp
       type: python
       port: 9002
       cwd: /opt/mcp-hub/python/my_mcp
       command:
         - uv
         - run
         - main.py
         - --transport
         - streamable-http
       env:
         MY_MCP_PORT: "9002"     # match the port above

   handles:
     my-handle:
       subprocess: my-mcp
   ```

4. **Rebuild the image**

   ```bash
   make docker
   ```

## Runtime notes for `google_workspace_mcp`

- CLI flags: `--transport streamable-http`, `--tool-tier core|extended|complete`,
  `--tools gmail drive calendar docs sheets ...`, `--read-only`.
- Port comes from the environment: either `PORT` or `WORKSPACE_MCP_PORT`
  (default 8000). Set `WORKSPACE_MCP_PORT` in the subprocess `env` map
  to match the dispatcher-assigned internal port.
- For the hub's multi-tenant pass-through model, run with
  `MCP_ENABLE_OAUTH21=true` and `WORKSPACE_MCP_STATELESS_MODE=true` so
  every request validates its own Bearer token against Google — no
  refresh token is persisted to disk.
- `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` are
  inherited from the container environment — set them on the
  container at deployment time (e.g. via docker-compose `env_file`,
  Kubernetes secrets, or `docker run -e`).

## Why clone-in-Dockerfile rather than pip install?

The upstream `workspace-mcp` distributes on PyPI under that name but the
repo layout (service modules, tool registry, etc.) is not guaranteed to
match the PyPI wheel. Cloning the repo + `uv sync --frozen` gives us:

- Reproducible dependency resolution via the repo's own `uv.lock`
- Access to the full source for debugging
- Exact commit pin that won't drift between releases
