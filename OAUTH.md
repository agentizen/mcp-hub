# OAuth app creation guide

mcp-hub is a **pass-through proxy**: it never performs OAuth flows itself.
Your consumer application (the one that sends requests to the hub) owns
the OAuth handshake and the long-lived refresh token storage. The hub
only forwards whatever `Authorization: Bearer <token>` header the
consumer attaches to each request.

This document walks you through creating the OAuth apps at each vendor
so your consumer has a `client_id` / `client_secret` pair to use during
the authorization code flow.

---

## Redirect URI convention

Every provider requires you to register a **redirect URI** when you
create the OAuth app. The URI must point at the consumer application
that handles the `code` → token exchange, **not** at mcp-hub itself.
Use a stable pattern such as:

```
https://<consumer-host>/oauth/<provider>/callback
```

For example, if your consumer is deployed at `https://app.example.com`,
register:

```
https://app.example.com/oauth/google/callback
https://app.example.com/oauth/microsoft/callback
https://app.example.com/oauth/hubspot/callback
...
```

Some providers (notably Microsoft Entra and Google) require an **exact**
match including protocol and path. You can register multiple redirect
URIs on the same app for dev / staging / production.

---

## 1. Microsoft 365 — Entra ID multi-tenant app

Powers: `outlook`, `outlook-calendar`, `onedrive`, `sharepoint`,
`ms-teams`, `ms-excel`.

### Dashboard

- <https://entra.microsoft.com/> → **Applications → App registrations**.

### Steps

1. Click **New registration**.
2. **Name**: anything meaningful (e.g. `<consumer-name>-mcp-hub`).
3. **Supported account types**: *Accounts in any organizational
   directory and personal Microsoft accounts* (multi-tenant).
4. **Redirect URI**: add a `Web` redirect pointing at your consumer
   (see the convention above).
5. Click **Register**.
6. On the new app overview, copy the **Application (client) ID** — this
   is your `MS365_CLIENT_ID`.
7. Go to **Certificates & secrets → Client secrets → New client
   secret**. Copy the **Value** immediately (it is shown only once) —
   this is your `MS365_CLIENT_SECRET`.

### API permissions (delegated)

Go to **API permissions → Add a permission → Microsoft Graph →
Delegated permissions**, then add:

| Permission | Needed by |
|---|---|
| `offline_access` | Required for refresh tokens on every Microsoft OAuth app |
| `User.Read` | Identity / token validation |
| `Mail.Read`, `Mail.ReadWrite`, `Mail.Send` | `outlook` |
| `MailboxSettings.Read`, `MailboxSettings.ReadWrite` | `outlook` (mail rules) |
| `Calendars.Read`, `Calendars.ReadWrite` | `outlook-calendar` |
| `Files.Read.All`, `Files.ReadWrite.All` | `onedrive`, `ms-excel` |
| `Sites.Read.All`, `Sites.ReadWrite.All`, `Sites.Manage.All` | `sharepoint` |
| `Team.ReadBasic.All`, `TeamMember.ReadWrite.All` | `ms-teams` |
| `Channel.ReadBasic.All`, `ChannelMessage.Read.All`, `ChannelMessage.Send` | `ms-teams` (channel messages) |
| `Chat.Read`, `Chat.ReadWrite` | `ms-teams` (private chats) |
| `OnlineMeetings.Read`, `OnlineMeetings.ReadWrite` | `ms-teams` (meetings) |
| `Group.Read.All`, `Group.ReadWrite.All` | `ms-teams` (groups, conversations) |

Multi-tenant apps require **admin consent** in each tenant that
connects. After adding the permissions, click **Grant admin consent
for `<your tenant>`** in your dev tenant; downstream customers can
grant their own via the standard admin-consent URL.

### Scopes passed to softeria

The softeria server (`node_modules/.bin/ms-365-mcp-server --http :9000 --org-mode`)
requests the superset of the scopes above during its own OAuth flow. If
your consumer delegates credential storage to softeria (e.g. by running
with `--enable-auth-tools`), softeria will handle the flow itself. In
the pass-through model used by mcp-hub you skip that — your consumer
performs the flow and injects the Bearer token per request.

---

## 2. Google Workspace — Google Cloud Console OAuth client

Powers: `google-drive`, `gmail`, `google-calendar`.

### Dashboard

- <https://console.cloud.google.com/apis/credentials>

### Steps

1. Create (or pick) a Google Cloud project.
2. **APIs & Services → Library**, enable:
   - Google Drive API
   - Gmail API
   - Google Calendar API
   - Google Docs, Google Sheets, Google Slides APIs if you later expose
     those handles
3. **OAuth consent screen**:
   - User type: **External** (for multi-tenant SaaS).
   - App name, support email, developer contact.
   - Scopes: add the ones below; the console will mark some as
     *sensitive* / *restricted*.
   - Test users during development. Go through Google's verification
     process before shipping to production — expect 1-2 weeks review.
4. **Credentials → Create credentials → OAuth client ID**:
   - Application type: **Web application**.
   - Authorized redirect URIs: add your consumer's callback.
5. Copy the generated **Client ID** and **Client secret** — these are
   `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET`, consumed
   by `taylorwilsdon/google_workspace_mcp` at runtime.

### Scopes

| Scope | Handle |
|---|---|
| `https://www.googleapis.com/auth/drive` | `google-drive` |
| `https://www.googleapis.com/auth/gmail.modify` | `gmail` |
| `https://www.googleapis.com/auth/gmail.labels` | `gmail` (filters, labels) |
| `https://www.googleapis.com/auth/calendar` | `google-calendar` |
| `openid`, `email`, `profile` | Identity / userinfo validation |

### Runtime configuration

The Python subprocess reads its credentials from the container env —
pass them at deploy time (see `python/README.md`):

```bash
docker run \
  -e GOOGLE_OAUTH_CLIENT_ID=... \
  -e GOOGLE_OAUTH_CLIENT_SECRET=... \
  -p 8090:8090 \
  mcp-hub:local
```

---

## 3. ClickUp

Powers: `clickup` (remote handle → <https://mcp.clickup.com/mcp>).

### Dashboard

- Workspace **Settings → Integrations → ClickUp API** or
  <https://app.clickup.com/settings/apps>.

### Steps

1. Click **Create an App**.
2. **App name**, **Redirect URL** (your consumer callback).
3. Save and copy the **Client ID** and **Client Secret**.

### Scopes

ClickUp OAuth uses a single all-or-nothing scope. The token grants
access to every workspace the user authorizes, filtered by the user's
own permissions inside ClickUp.

### Notes

- The token response includes a `team_id` field that your consumer
  must capture and send back to the MCP via a vendor-specific header
  if it wants to scope calls to a single team.
- For multi-team workspaces, re-run the authorization to collect
  multiple tokens.

---

## 4. HubSpot

Powers: `hubspot` (remote → <https://mcp.hubspot.com>).

### Dashboard

- <https://app.hubspot.com/developer/> → pick a developer account →
  **Apps → Create app**.

### Steps

1. **App Info**: name, description, logo.
2. **Auth → Install URL**: list the scopes you need (see below).
3. **Redirect URL**: your consumer callback.
4. Copy **Client ID** and **Client Secret** from the Auth tab.
5. Install the app on your test HubSpot account to generate a
   refresh token; production customers do their own install.

### Scopes

At minimum:

```
crm.objects.contacts.read
crm.objects.contacts.write
crm.objects.companies.read
crm.objects.companies.write
crm.objects.deals.read
crm.objects.deals.write
crm.schemas.contacts.read
crm.schemas.companies.read
crm.schemas.deals.read
```

Add `marketing.*` or `content` scopes for marketing handles.

### Notes

- The token response includes `hub_id` and `hub_domain` — your
  consumer should persist both alongside the refresh token to
  disambiguate multi-portal customers.

---

## 5. Atlassian (Jira & Confluence)

Powers: `atlassian` (remote → <https://mcp.atlassian.com/v1/mcp>).

### Dashboard

- <https://developer.atlassian.com/console/myapps/> → **Create →
  OAuth 2.0 (3LO) integration**.

### Steps

1. **Create app**, choose **OAuth 2.0 (3LO)**.
2. **Permissions → Add APIs**: Jira, Confluence, User identity.
3. **Authorization → OAuth 2.0 (3LO) → Callback URL**: your consumer
   callback.
4. Copy **Client ID** and **Secret** from **Settings**.

### Scopes

```
read:jira-user
read:jira-work
write:jira-work
read:confluence-content.all
write:confluence-content
read:me
offline_access
```

### Notes

- Atlassian tokens are scoped to a single **cloud id** (one workspace).
  Capture `cloud_id` from the `/oauth/token/accessible-resources`
  endpoint after the token exchange and persist it alongside the
  refresh token.

---

## 6. Linear

Powers: `linear` (remote → <https://mcp.linear.app/mcp>).

### Dashboard

- <https://linear.app/settings/api/applications> → **New OAuth2
  application**.

### Steps

1. **Name**, **Developer URL**, **Callback URLs**: add your consumer
   callback.
2. Copy **Client ID** and **Client Secret**.

### Scopes

```
read
write
offline_access
```

Linear's OAuth is simple — a single `read` / `write` pair covers all
API surface exposed by the MCP.

---

## 7. Notion

Powers: `notion` (remote → <https://mcp.notion.com/mcp>).

### Dashboard

- <https://www.notion.so/my-integrations> → **New integration →
  Public integration**.

### Steps

1. **Integration name** and associated workspace.
2. Select **Public integration** (not internal) if you want customers
   to install it themselves.
3. **OAuth domain & URIs**:
   - Redirect URIs: your consumer callback.
   - Company name / homepage / privacy & terms.
4. Copy the **OAuth client ID** and **OAuth client secret**.

### Capabilities

Notion uses "capabilities" instead of scopes, configured on the
integration:

- Read content
- Update content
- Insert content
- Read user information including email addresses

### Notes

- Notion installs are per-page / per-database — customers explicitly
  pick which pages to share with the integration at install time. The
  MCP cannot access anything the user did not share.

---

## 8. Asana

Powers: `asana` (remote → <https://mcp.asana.com/v2/mcp>).

### Dashboard

- <https://app.asana.com/0/my-apps> → **Manage Developer Apps →
  Create new app**.

### Steps

1. Provide app name, redirect URLs (your consumer callback), and icon.
2. Under **OAuth 2.0**, copy the **Client ID** and **Client Secret**.

### Scopes

Asana OAuth uses a single default scope that grants access to
everything the authorizing user can see. Fine-grained scoping is
applied by Asana itself based on the user's workspace membership.

---

## 9. Monday.com

Powers: `monday` (remote → <https://mcp.monday.com/mcp>).

### Dashboard

- <https://monday.com/developers/apps> → **Create app**.

### Steps

1. **App name** and description.
2. **OAuth & Permissions → Add scopes** (see below).
3. **Redirect URLs**: your consumer callback.
4. Publish the app to the App Marketplace when you are ready for
   customer installs.
5. Copy **Client ID** and **Client Secret** from **OAuth &
   Permissions**.

### Scopes

```
me:read
boards:read
boards:write
workspaces:read
updates:read
updates:write
notifications:write
```

---

## API-key-only MCPs (no OAuth needed)

Two of the remotes in `config.yaml` do **not** use OAuth and instead
accept an API key via an `Authorization: Bearer` header or a
vendor-specific header:

- **Context7** (`context7`) — sign up at <https://context7.com>, mint
  an API key, and have your consumer attach it per request.
- **Exa** (`exa`) — sign up at <https://exa.ai>, generate an API key,
  same pattern.

No app registration is required for these; there is no redirect URI
to configure.

---

## Summary table

| MCP | Auth model | Where to create | Client ID / Secret env on consumer |
|---|---|---|---|
| Microsoft 365 | OAuth 2.0 (Entra multi-tenant) | entra.microsoft.com | `MS365_CLIENT_ID` / `MS365_CLIENT_SECRET` |
| Google Workspace | OAuth 2.0 (Google Cloud) | console.cloud.google.com | `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET` |
| ClickUp | OAuth 2.0 | app.clickup.com/settings/apps | consumer-defined |
| HubSpot | OAuth 2.0 | app.hubspot.com/developer | consumer-defined |
| Atlassian | OAuth 2.0 (3LO) | developer.atlassian.com | consumer-defined |
| Linear | OAuth 2.0 | linear.app/settings/api | consumer-defined |
| Notion | OAuth 2.0 (Public) | notion.so/my-integrations | consumer-defined |
| Asana | OAuth 2.0 | app.asana.com/0/my-apps | consumer-defined |
| Monday.com | OAuth 2.0 | monday.com/developers/apps | consumer-defined |
| Context7 | API key | context7.com | — |
| Exa | API key | exa.ai | — |

The `MS365_*` and `GOOGLE_OAUTH_*` env vars are the **only** OAuth
credentials passed directly to the hub, because they are consumed by
the Node and Python subprocess backends vendored inside the image. All
other OAuth credentials stay entirely in the consumer application —
mcp-hub never sees them.
