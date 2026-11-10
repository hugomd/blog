# Protected album delivery

The Pages Function at `/photos/private/<album ID>/` serves private share-link and
password-protected albums on the blog origin. Studio remains a separate,
authenticated admin app; no visitor request reaches its admin APIs.

Studio offers three access modes:

- **Public:** listed in the public gallery, with public image URLs.
- **Private share link:** omitted from the public gallery; anyone with the complete
  link can view and forward it without entering a password.
- **Password protected:** omitted from the public gallery; both the share URL
  and the password are needed.

Private share links have the form `/photos/private/<album ID>/#key=<token>`.
The key is a cryptographically random 256-bit secret, separate from the album ID
and unrelated to any password. The browser removes the fragment with
`history.replaceState` before exchanging it in a same-origin JSON POST for a
viewing session. It is never sent in the HTTP request URL or Referrer, or saved
in local/session storage. The blog serves the photos; Tailscale is only needed
to manage them in Studio.

Treat the complete share link as a bearer credential. It can still be copied
from a message, clipboard, browser extension or device; fragment handling is not
a promise of secrecy from the browser or server-side request-body inspection.
In Studio, choose **Regenerate link**, Save, then Publish to revoke both the old
link and its viewing sessions without changing the album address. Draft saves
alone do not change live access. Unpublishing or changing access also revokes
sessions when published. Republishing a link album restores its saved link with
new sessions; regenerate it to replace the key permanently. Noindex is an
indexing hint, not access control.

Photos can belong to several albums. Independent public publication or membership
in any published public album makes a photo public, even if it also belongs to a
non-public album. Private share-link and password-protected titles, descriptions, share
URLs, membership and order are not included in the public index. Previously
public URLs and downloads cannot be made private retroactively.

## Required configuration before enabling production

These are deployment prerequisites, not resources created by this repository:

- Create a third R2 bucket exclusively for protected **delivery**. Disable both
  its public development URL and all public custom domains. Never bind the
  private working/original bucket to Pages.
- Studio: set `R2_PROTECTED_BUCKET` to that bucket and extend its scoped S3
  credential to read/write/list/delete it and copy from the private working
  bucket. Set `PUBLIC_GALLERY_URL=https://hugo.md/photos/` for share links.
- Pages production **and preview** environments: bind `PUBLIC_PHOTOS` to the
  public bucket and `PROTECTED_PHOTOS` to the protected delivery bucket. Pages
  provides `ASSETS` automatically. Use separate test buckets for previews;
  never let untrusted preview code read production protected data.
- Add the `ALBUM_SESSION_SECRET` Pages secret: at least 32 random bytes. Keep it
  out of Git, logs and client JavaScript. Rotating it invalidates all sessions.
  Both private access modes require this session secret.
- Enable the `nodejs_compat` Pages compatibility flag with compatibility date
  `2026-11-10` or later. The native `node:crypto` scrypt implementation is
  required; there is no JavaScript fallback or reduced-cost password mode.
- Allow sufficient Pages Functions CPU for a full scrypt verification. The
  free-plan 10 ms CPU budget is not suitable; use a paid Workers plan and verify
  login within the configured budget before enabling real albums. R2 operations
  and Functions requests incur usage charges.
- Deploy both the blog Function/assets and the compatible Studio image before
  publishing protected albums. Verify missing configuration fails closed, then
  use disposable test photos to test actual R2 copies, login, media access,
  password rotation and unpublication before sharing real albums.

`static/_routes.json` invokes Functions only for `/photos/private/*`; ordinary
blog pages, gallery pages and hashed assets remain static. Do not bypass this
route with a CDN cache rule or forward it to the public bucket. With no bindings
or a storage failure, protected routes fail closed with 503; public pages continue
to work. Never deploy the protected bucket with public access or CORS enabled.

## Publication and access

Studio writes three JPEG derivatives per delivery audience, then a protected
`releases/<random ID>.json` snapshot. The final conditional public `index.json`
write commits both audiences using an opaque `protectedRelease` pointer.
The public pointer reveals that protected content exists, but no album IDs or
protected metadata. Uncommitted snapshots cannot be selected by a visitor.

The gateway rereads the current pointer/snapshot on every protected request.
It allows only a current published album's members and the 640/1280/2560 JPEG
variants. It never serves bucket paths, originals, releases or credentials.
Both private modes require the album's current session for catalog/media reads.
The album ID alone grants no access. Link exchanges verify SHA-256 of the exact
UTF-8 lowercase hex64 token using a constant-time comparison. Only its hash and
session version enter the protected delivery snapshot; the plaintext key stays
in the private working catalog and authenticated Studio responses so the owner
can copy it again. Neither key nor hash appears in public output or visitor
catalogs. High-entropy link verification needs no password KDF or limiter writes.
Password hashes use salted scrypt N=16384, r=8, p=5, 32-byte output (OWASP's
16 MiB profile). Passwords are 12–128 Unicode characters, exact UTF-8 with no
trimming or normalization. Share the password separately from the album URL.

Sessions are signed, album/version-scoped, HttpOnly, Secure, SameSite=Strict
cookies lasting 12 hours. Link regeneration, password replacements, unpublication/republishing and
access changes invalidate prior sessions **when published**. Lock clears the
browser cookie and visible photos; it does not individually revoke a copied
bearer token before expiry. Protected responses are `private, no-store`, noindex,
no-referrer, without cache validators, ranges or CORS. Downloaded screenshots
and external copies cannot be revoked.
Links can open the generic album page from other sites through a top-level
navigation. Cross-site embedded pages, catalog/media reads and auth requests
remain denied; the page fetches its catalog on the blog origin.

Persistent conditional writes in the protected bucket enforce five login
attempts per IP/album/minute and 30 per album/minute, before password verification.
Fixed-window boundaries permit bursts; someone can exhaust an album's budget
and temporarily deny new logins. Records live at
`auth-attempts/<album ID>/{ip-<SHA256 of IP>,global}.json`; IP hashes are
pseudonymous, not anonymized. Configure an R2 lifecycle policy for that prefix
only (e.g. one-day expiry) to bound retention/object growth. Do not expire
`photos/` or `releases/`. Limiter outages/contention fail closed.

Archive redacts protected snapshots as well as public history. Delete removes
derivatives from all three buckets and protected snapshot references. Cleanup
retains existing retry markers; failures need a reload/retry. It is not an atomic
multi-object transaction. A request already authorized before a change may finish.

The private working catalog permanently upgrades to schema version 2 on first
password-protected save and version 3 on first private share-link save, even if those modes
are later removed. Do not roll back to older Studio images: they deliberately
reject modes they cannot safely publish. Password-only Studio releases do not
understand schema 3. Back up the working bucket and keep gateway and publisher
versions compatible.

## Local checks

Run `node --test photo-access/*.test.mjs` and
`./bin/go -C photo-manager test -race ./...` from the repository root.
Build Hugo normally, then run Wrangler Pages locally with `--r2 PUBLIC_PHOTOS=test-public`,
`--r2 PROTECTED_PHOTOS=test-protected`, a test-only `ALBUM_SESSION_SECRET`, `--local-protocol https`,
`--compatibility-date 2026-11-10` and `--compatibility-flags nodejs_compat`.
Use an isolated `--persist-to` directory, seeded with local Studio publication
output. Do not use remote bindings, production buckets or real passwords.
Local workerd and fixture tests are not production acceptance tests.
