# MagnetDrive

**Your files. Your server. Your workspace.**

A self-hosted file workspace with personal and shared spaces, file history, public links, and SFTP access. Built with Go and MariaDB, with a responsive web interface in English and Russian.

[Getting started](#getting-started) · [Configuration](magnetdrive.example.yaml) · [Administration & operations](docs/operations.md) · [License](#license)

## Features

- **Personal & shared spaces** — folders, name search, storage limits, and owner, editor, and viewer roles.
- **A familiar file browser** — drag and drop anywhere, multiple selection, bulk actions, right-click menus, and keyboard navigation.
- **Previews & streaming** — images, PDF, text, audio, and video, with seeking for supported media formats.
- **Version history** — preview, download, and restore earlier versions without losing later changes.
- **Public sharing** — expiring or permanent file links, direct URLs, downloads, and revocation.
- **Flexible sign-in** — local accounts and OpenID Connect, with profile updates on every provider login.
- **SFTP access** — connect with your account or a dedicated, revocable SFTP password; use the same files, permissions, quotas, and history.
- **File API** — scoped, revocable API keys; paginated lists/search, uploads/downloads, folders, bulk actions, versions, and sharing. [API reference and examples](API.md) · [OpenAPI specification](web/openapi.json).
- **Administration** — invite users, manage accounts and roles, and set personal and shared storage limits.
- **Made for everyday use** — responsive layouts, light and dark themes, smooth dialogs, reduced-motion support, and automatic language selection.

The web interface is embedded in the application. No frontend build pipeline or runtime CDN is required.

## Getting started

### 1. Prepare the server

Install **Go 1.27** and **MariaDB**. Create an empty `utf8mb4` database and a dedicated database user with normal data privileges plus `CREATE` and `ALTER`. MagnetDrive creates and upgrades its tables at startup.

### 2. Configure

Copy [magnetdrive.example.yaml](magnetdrive.example.yaml) to `magnetdrive.yaml` and set:

| Setting | Purpose |
| --- | --- |
| `database` | MariaDB connection credentials |
| `data_dir` | File storage directory; use an absolute path in production |
| `public_url` | Exact external URL, including `https://` in production |
| `listen` | HTTP listen address; defaults to `127.0.0.1:8090` |
| `registration` | Whether visitors may create local accounts |

Set `MAGNETDRIVE_CONFIG` to use another configuration path. Protect the configuration file: it contains credentials.

### 3. Build and start

```sh
go mod download
go build -o MagnetDrive .
./MagnetDrive
```

On Windows, run `MagnetDrive.exe`. Relative paths resolve from the process working directory.

With the example configuration, open [localhost:8090](http://localhost:8090). Temporarily enable `registration` to create your first local account, then disable it for a private installation.

### 4. Grant administrator access

Run this command for an existing account, using the same configuration:

```sh
./MagnetDrive admin grant you@example.com
```

Refresh the browser and open **Account → Administration**. From there, manage users, storage limits, and single-use invitations. Invitations can assign new users to a shared space and work with public registration disabled.

## Connect your tools

### File API

Create a key in **User settings → API access → Manage API keys**. Choose read-only or read/write access, an expiry, and optionally a single-space restriction. Send it in `Authorization: Bearer …` to `/api/v1`. Keys use your current account permissions and storage quotas; cookie sessions are not accepted on these routes. API keys cannot administer accounts, memberships, invitations, or other keys.

See [API.md](API.md) for endpoint contracts, pagination, curl examples, and revocation behavior. The application serves the machine-readable specification at `/openapi.json`.

### OpenID Connect

Set `oidc.issuer`, `oidc.client_id`, and `oidc.client_secret`. Register this callback with your provider:

```text
https://your-drive.example/auth/oidc/callback
```

The provider must supply a verified email address. First login matches an account by email or creates one; subsequent logins use the provider identity. Display name and avatar refresh on each login.

Use a trusted provider. Local registration does not verify email ownership; keep it disabled when relying on automatic email matching. See [OpenID setup](docs/operations.md#openid-connect) for details.

### SFTP

Enable `sftp.listen` in the configuration and expose that TCP port for remote access. SFTP uses its own SSH listener, independently of the HTTP reverse proxy.

```sh
sftp -P 2022 -oUser=you@example.com files.example.com
```

Use your local password or create a dedicated password in **Account → SFTP access**. The root contains `/self` and your accessible shared spaces. Verify the server fingerprint before connecting.

See [SFTP setup and client compatibility](docs/operations.md#sftp) for supported operations and configuration.

## Deployment & storage

Run behind an HTTPS reverse proxy with a matching `public_url`. Keep the original `Origin` header and configure `trusted_proxies` for the proxy's actual address; loopback proxies are trusted by default. Deployment examples are available for [systemd](deploy/magnetdrive.service) and [nginx](deploy/nginx.conf).

Back up **MariaDB and the complete data directory together**, including the SFTP host key. Every file version counts toward storage usage, and moving files to Trash does not free space. Permanent deletion does.

Storage uses the local filesystem. Media playback depends on browser codec support. Folder sharing links, bulk ZIP downloads, resumable uploads, S3 storage, and media transcoding are not included. Local accounts currently have no email verification, password recovery, or built-in MFA.

See the [operations guide](docs/operations.md) for permissions, invitations, account deletion, recovery, request limits, and reverse-proxy settings. Target-environment validation is still required; development verification has been limited to static checks.

## Built with

Go 1.27 · Gin · sqlx · MariaDB · go-yaml · embedded HTML, CSS & JavaScript

## License

[PolyForm Noncommercial 1.0.0](LICENSE). Commercial use requires a [separate license](LICENSE-COMMERCIAL.md).

Copyright shoker137. Third-party dependency licenses remain applicable.
