# Node.js MCP backends

This directory vendors Node.js MCP servers that the dispatcher spawns on
demand. Each dependency listed in `package.json` corresponds to (at
least one) `subprocesses:` entry in `config.yaml`.

## Adding a new Node MCP

1. **Install the package**

   ```bash
   cd node
   npm install <scoped/package>@<exact-version>
   ```

   Pin to an exact version; MCP servers evolve quickly and a floating
   dependency can break the hub on rebuild.

2. **Declare it in `config.yaml`**

   ```yaml
   subprocesses:
     - name: my-mcp
       type: node
       port: 9002                       # unique internal port
       cwd: /opt/mcp-hub/node
       command:
         - node_modules/.bin/<binary>
         - --http
         - ":9002"
   ```

3. **Add handle(s) that reference it**

   ```yaml
   handles:
     my-handle:
       subprocess: my-mcp
       tools: [...]                     # optional allow-list
   ```

4. **Rebuild the image**

   ```bash
   make docker
   ```

## Vendored packages

| Package | Version | Handles |
|---|---|---|
| `@softeria/ms-365-mcp-server` | `0.75.0` | `outlook`, `outlook-calendar`, `onedrive`, `sharepoint`, `ms-teams`, `ms-excel` |

The softeria server listens on `--http :PORT` with Streamable HTTP at
`/mcp`. It validates the incoming `Authorization: Bearer` header against
Microsoft Graph on every request, so credential handling stays with the
consumer.

## Runtime notes

- Subprocesses are spawned lazily by the dispatcher on first request
  and reaped after 30 minutes of idle.
- Stdout/stderr are discarded — use the dispatcher's slog output to
  correlate per-request failures via the `handle` field.
- Auth tools (login/logout) must **not** be exposed in HTTP mode
  because they assume a stdio client; softeria disables them by
  default when `--http` is set.
