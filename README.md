# Magnet Drive

An independent, self-hosted file workspace using Go 1.27, Gin, sqlx, MariaDB, and go-yaml. The embedded plain HTML/CSS/JavaScript interface follows MagnetServer's sidebar, quiet surfaces, blue accents, shared application mark, and light/dark/system themes. No frontend bundler or runtime CDN is required.

## Included

- Local accounts with bcrypt passwords and database-backed, expiring sessions.
- SFTP access using account email and local password or a revocable, dedicated SFTP password (including OpenID-only accounts). Its virtual root lists personal and shared spaces; transfers share web permissions, quotas, metadata, and version history.
- OpenID Connect authorization code flow with PKCE, browser-bound one-use state, nonce, signature, issuer, and audience verification. A verified provider email matches an existing account or creates a new account and personal space. The provider subject is retained for subsequent logins. Name and avatar refresh after each successful OpenID login.
- Personal spaces and independently managed group spaces. Group membership has owner, editor, and viewer roles. Owners manage members by existing account email; editors organize and share files; viewers preview and download.
- Nested folders, name search across the selected space, recent files, sorting, checkbox/range selection, double-click/Enter to open, a parent-directory button, and a keyboard-accessible right-click context menu. Bulk move, trash, and restore operations are atomic.
- Streaming uploads with progress/cancel, whole-window drag and drop, explicit replacement uploads, storage quotas, immutable versions, version downloads/previews, and rollback without destroying subsequent history. Upload completion updates file content and storage counters without rebuilding the page shell or headings.
- Native image/audio/video, bounded plain-text, and PDF previews. HTTP range requests allow seeking in streamed audio and video. Actual playback depends on browser codec support; no transcoding is included. Unsupported and active document formats download as attachments.
- Read-only public file links with expiration or Forever, revocation, and hashed tokens. Choose a preview page or direct file URL; a force-download variant is also available. All formats of one token share its expiry/revocation. Links follow the current version. Trash makes the linked file unavailable; restoring it re-enables valid links.
- English and Russian interface translations. System language is the default; a saved account preference can explicitly select English or Russian. Dates and file-size formatting follow the selected language. Filenames, user/space names, URLs, form values, and file contents stay unchanged.
- User settings for display name, HTTPS avatar URL, stored language and system/light/dark theme, local password changes, and SFTP credential management. Password changes revoke other browser sessions; OpenID refreshes name/avatar at the next provider login.
- Administrator access to every space in the web app and SFTP, plus user creation/deletion, administrator-role management, and per-personal/per-shared-space storage limits.
- Trash and permanent deletion, including all file versions and public links. Folder trash includes descendants. Restore a folder first, then restore its children from Trash individually: independently trashed descendants are never silently restored.
- Responsive navigation, keyboard focus states, accessible native dialogs, backdrop/Escape dismissal, scale/fade entry and exit, page transitions, and reduced-motion support.

## Configuration

1. Install Go 1.27 and provision an empty MariaDB database using `utf8mb4` and a dedicated database account. The application creates tables and applies idempotent schema upgrades at startup, requiring CREATE/ALTER and normal data privileges in its database. It does not create the database or database user.
2. Copy `magnetdrive.example.yaml` to `magnetdrive.yaml`. Set the database keys, an absolute `data_dir`, and the exact externally visible `public_url`. Alternatively set `MAGNETDRIVE_CONFIG` to the configuration path. Keep the configuration readable only by the service account.
3. Obtain dependencies with `go mod download`. Build/run the module through your normal deployment process. The module root is the executable entry point. Configuration and storage are resolved relative to the process working directory unless absolute paths are supplied.
4. Set `registration: true` if local self-registration is wanted, create your account through the sign-in page, then disable registration for a private installation. Shared-space members must sign in or register before the owner can add them.

The sample listens on loopback port 8090. HTTP is suitable for local development. Use an HTTPS reverse proxy and an HTTPS `public_url` in production; this enables Secure cookies and HSTS. Forward the original Origin header unchanged. Browser mutations require an exact matching Origin. No cross-origin API access is enabled.

Use the examples in `deploy/` as deployment inputs; adapt paths, database settings, TLS certificates, request limits, and timeouts to your environment. No services or database changes were executed during implementation.

### Administrator setup and user settings

Create or sign in to the account that should become an administrator, then run the deployed executable from its configured working directory:

```text
./MagnetDrive admin grant you@example.com
```

On Windows use `MagnetDrive.exe admin grant you@example.com`. The command reads the same `magnetdrive.yaml` or `MAGNETDRIVE_CONFIG`, applies required schema upgrades, grants the role to the existing active account, and exits without starting HTTP or SFTP listeners. It does not create the account. Refresh the browser to show **Account → Administration**. This command was implemented but not executed against a database during development.

Administrators see every user's personal space and all shared spaces, regardless of membership, and can grant/remove administrator roles, create local users, delete users, and adjust limits from **Users & storage**. Limits are entered in GiB. Reducing a limit below usage preserves files and blocks new storage until usage falls below the limit; 0 allows no additional bytes. Owners/editors remain subject to the configured quota, including administrators. Self-deletion is refused, and serialized role changes prevent removal of the final administrator.

Choose **Administration → Invite a user** to create a single-use registration link. Optionally bind it to an email and assign the new user to one shared space as a Viewer or Editor. Without a space assignment, the account receives only its own personal space. Personal spaces cannot be assigned through invitations, and invitations never grant administrator privileges. The recipient chooses their display name and password; account creation, personal space, optional membership, invitation consumption, and the initial session commit atomically. Invitations work when public registration is disabled. Existing accounts must be added through the space's Members screen; invitation acceptance never takes over or merges an existing account.

Invitations expire after 3 days by default; the UI offers 1 hour, 1 day, 3 days, or 7 days. Admins can view/revoke pending invitations in the same dialog. URLs are shown only when created; only token hashes are stored. There may be up to 1,000 active pending invitations. Losing the issuing administrator role or deleting that account revokes its invitations. Expired records are cleaned up hourly. A bound email is fixed during signup; an unbound link lets its bearer choose an email. Share these links privately with the intended recipient. Acceptance signs the current browser into the newly created account.

Deleting an account revokes its sessions, SFTP credentials, provider identities, memberships, and public links. The retained audit record is anonymized. Owned spaces transfer to the acting administrator; the personal space becomes an archived shared space. This preserves file history and shared data. Delete its files through the normal Trash/permanent-deletion flow if they should also be erased. A later registration with the former email creates a fresh account without access to these old spaces.

**Account → User settings** edits profile and appearance, changes/sets a local password, and opens SFTP access management. Existing local passwords require the current password to change them. OpenID-only accounts may set an initial local password from their authenticated session. Password changes revoke other browser sessions and invalidate local-password SFTP authentication; separately issued SFTP passwords remain valid until revoked. System, light, and dark modes have distinct icons. Email remains the login/account-matching identity and is not edited here.

Choose **Language → System language / English / Русский** and save settings. The preference is stored with the account and restored on login on any device; a browser copy also keeps the sign-in/public pages consistent. System mode selects Russian when the browser's primary preferred language starts with `ru`, and English otherwise. Existing and new accounts default to System. Language switching updates navigation, dialogs, labels, accessibility attributes, feedback, and dates without translating user content or changing API values. Open form drafts remain in memory when the page is translated. A browser system-language change in automatic mode is applied when dialogs/uploads are idle. The Russian catalog is bundled locally; there is no translation service or CDN dependency.

Forever links store a NULL expiry. Existing expiring links keep their expiry during upgrade. Direct links serve the same protected-by-token file content without a preview page and support GET/HEAD and range requests; active/unsupported document formats continue to use attachment disposition. URLs can only be copied when a link is first created because only the token hash is retained.

### OpenID Connect

Set `oidc.issuer`, `oidc.client_id`, `oidc.client_secret`, and the optional display `label`. Register this exact redirect URI at the provider:

```text
https://your-drive.example/auth/oidc/callback
```

The issuer must expose discovery and signing keys and supply `openid`, `profile`, and `email` scopes with `email_verified: true`. Unverified/missing email is rejected. Account matching by email occurs only on initial provider-subject binding; later logins use the established subject binding. Name and avatar update on every successful login, including clearing a removed avatar. Avatars must use HTTPS.

Only configure an identity provider you trust to attest email ownership. Local self-registration does not verify email ownership: for an installation combining local accounts with automatic OpenID email matching, keep local registration disabled and use trusted account provisioning. This version does not include SMTP verification, password recovery, or personal MFA. OpenID provisioning remains available when local registration is disabled.

### SFTP

Configure the listener in YAML and restart the application:

```yaml
sftp:
  listen: ":2022"
  host_key: ""
```

An empty listener disables SFTP; this is the default for existing configuration files without an `sftp` section. The example configuration binds to loopback `127.0.0.1:2022`; use an appropriate external interface for remote clients and allow that TCP port through your firewall. SFTP is a separate SSH listener and does not use the HTTP reverse proxy. The default host key is generated once at `data_dir/sftp_host_ed25519`; preserve this private key through restarts and backups. An explicit `host_key` path can point to an existing unencrypted SSH private key readable by the service account. Its parent directory must already exist.

In the web sidebar, open **Account → SFTP access** to see the connection host, port, username, and SHA-256 host-key fingerprint. Verify the fingerprint with the server administrator before trusting a new server. If the server uses NAT or port mapping, use the externally configured host and port. Local accounts can use their normal password. Every signed-in user, including OpenID-only users, can create up to 20 named, dedicated SFTP passwords. These are shown once, stored only as token digests, and can be revoked independently. Revocation and space permissions are rechecked during read/write chunks and before publishing uploads.

For example, connect from a terminal client using:

```text
sftp -P 2022 -oUser=you@example.com files.example.com
```

The virtual root is read-only and contains:

```text
/
  self/                       # Your personal space
  shared-12 - Design team/    # A shared space available to you
  shared-19 - Family/
```

IDs make shared-space names unambiguous, even if two spaces have the same display name. Normal users are limited to currently available memberships. Administrators additionally see other personal spaces as `personal-<id> - <owner>` and can access every space. Physical storage paths are never exposed. Viewers can list/read; editors, owners, and administrators can upload, create folders, rename/move within the same space, and delete files or empty folders. Delete moves an item to web Trash. Use the web app to restore, permanently delete, or manage versions/public links/members.

Writes are staged privately and published as one immutable file version when the client closes the handle successfully. Until then, the web app and other connections continue to see the previous version. Interrupted or failed transfers discard staged content. Random-access writes and append are supported within the configured upload-size limit. Conflicting changes to an existing file reject the later upload instead of overwriting a concurrently committed version. All completed versions count against the same space quota. Web and SFTP share the four concurrent writable-upload slots.

Supported operations are directory listing, stat, reads, writes, append, mkdir, file removal, empty-directory removal, and rename/move to an unused name within the same space. Space roots cannot be modified. Cross-space renames, replacing a destination through rename, symlinks/hardlinks, chmod/chown, timestamp preservation, remote truncation through SETSTAT, shell/exec, SSH forwarding, and public-key login are not supported. Disable remote timestamp/permission preservation and temporary-file rename-overwrite in clients that enable those options; upload directly to an existing filename to create a new version. Closing a partial transfer explicitly can commit the bytes received, as required by ordinary SFTP close semantics; a transport disconnect aborts an unclosed handle.

The listener allows at most 64 SSH connections, three authentication attempts per connection, a 20-second handshake timeout, and 15 minutes without inbound SSH activity. It shares the web sign-in limiter. The host-key fingerprint is logged at startup. The HTTP router explicitly uses `gin.Default()` in debug mode, including Gin's request logger and recovery middleware.

### Storage and recovery

Object files have random immutable keys and restrictive permissions. User filenames never become storage paths. Each file version consumes quota; rolling back changes the current pointer without duplicating object bytes. Trash continues to consume quota. Permanent deletion releases database quota and unlinks objects after committing metadata deletion.

Back up MariaDB and the complete data directory as one consistent snapshot. Stop writes during backup or use a coordinated snapshot procedure. Restore both together. Storage is local filesystem storage, so multiple replicas must share the same filesystem and database. There is no S3 backend, transcoding pipeline, malware scanner, or background indexing service.

A crash during upload or immediately after permanent deletion can leave an unreferenced object. Do not delete storage objects based only on age. Reconcile candidate keys against `versions.blob_key` during a maintenance window with uploads stopped. Committed file versions are never deliberately removed before their database records are deleted.

Space quotas include every version. Each upload is bounded by `max_upload_bytes`; four uploads may be in flight at once, and temporary uploads consume disk before their database quota check. Reserve sufficient filesystem headroom. Requests have a one-hour read deadline. Folder operations are serialized per space to prevent conflicting moves, membership updates, and quota races. Tree ancestry is limited to 256 levels for move and delete operations.

## Operational boundaries

File lists return the first 1,000 matching entries; search can narrow results. Bulk requests accept up to 200 selected entries. Search uses MariaDB name matching within the selected space, with its configured collation. Public links apply to individual files. Folder public links, ZIP/bulk downloads, resumable chunk uploads, full-text content search, and independent reusable groups spanning multiple spaces are not included. Each shared space is its own user group.

The built-in sign-in limiter permits 20 attempts per client IP per minute and four concurrent password computations. HTTP uses Gin's client-IP resolution; SFTP uses the direct connection peer. Sessions expire after seven days; expired sessions, login states, and links are cleaned up hourly. Authorization checks apply to every private API request. Data is protected by filesystem/database access controls; encryption at rest depends on the host storage setup.

HTTP uses the same scraper-ban middleware as MagnetServer. Three sensitive-path probes from an IP within five minutes trigger a 24-hour ban. Probes include `.git`, `.aws`, `.env` variants, service-account files, cloud/AI credential paths, actuator secrets, PHP information, and WordPress login paths. Before the third probe, normal routing still determines the response. Once banned, every HTTP request from that IP receives an empty 403 response with `Cache-Control: no-store`; existing SFTP connections are unaffected. Ban events are logged. State is synchronized, kept in memory, expires automatically, and is bounded to 20,000 IP records; restarting clears it, and a flood may evict the oldest records. Ordinary missing routes, failed sign-ins, and file names supplied through API data do not count as scraper probes.

For reverse proxies, `trusted_proxies` accepts the proxy's IP addresses or CIDR networks. Omitted configuration defaults to `127.0.0.0/8` and `::1/128`, matching MagnetServer, so local Caddy works with its forwarded client IP header. For a remote/containerized proxy, set its actual source IP/CIDR. The proxy must set or sanitize forwarded headers itself. Use `trusted_proxies: []` for direct connections with no trusted proxy; never trust all public IPs. This prevents clients from choosing their own ban/rate-limit identity and prevents all proxied visitors from being treated as the proxy IP. Incorrect settings can cause one scanner to ban every visitor sharing the proxy address.

## Verification

Only static checks are permitted for this project. Go formatting, `go vet -tests=false ./...`, JavaScript syntax checks for all browser scripts, and a static SQL argument/placeholder audit were performed. No builds, unit tests, application startup, database migrations, provider logins, media playback, or browser checks were run. Deployment readiness still needs validation in the target environment.

## License

[PolyForm Noncommercial 1.0.0](LICENSE), with a separate [commercial licensing path](LICENSE-COMMERCIAL.md). Copyright shoker137. Third-party dependency licenses remain applicable. The application mark is reused from the local Magnet reference project.
