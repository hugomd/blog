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

Studio writes three JPEG derivatives per delivery audience (plus an MP4 for each
video), then a protected `releases/<random ID>.json` snapshot. The final conditional public `index.json`
write commits both audiences using an opaque `protectedRelease` pointer.
The public pointer reveals that protected content exists, but no album IDs or
protected metadata. Uncommitted snapshots cannot be selected by a visitor.

The gateway rereads the current pointer/snapshot on every protected request,
including every video seek. It allows only a current published album's members
and the 640/1280/2560 JPEG variants, plus `video.mp4` for records explicitly marked
`kind: "video"`. It never serves bucket paths, originals, releases or credentials.
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
access changes invalidate prior sessions **when published**. There is no manual
lock control in the gallery. Protected responses are `private, no-store`, noindex,
no-referrer, without cache validators or CORS. JPEGs still ignore ranges and
conditional requests, returning the full image with 200 after authentication.
MP4 GET/HEAD support a single `bytes=start-end`, `bytes=start-` or `bytes=-suffix`
range with 206, `Content-Range`, exact `Content-Length` and `Accept-Ranges: bytes`.
Malformed, multipart and unsatisfiable ranges return 416 (`bytes */<size>`), only
after authentication and membership checks. Client cache validators and `If-Range`
are not forwarded or allowed to skip those checks. Full MP4 responses preserve the
R2 stream's known length; JPEGs deliberately retain their length-stripping stream.
Downloaded screenshots and external copies cannot be revoked.
Links can open the generic album page from other sites through a top-level
navigation. Cross-site embedded pages, catalog/media reads and auth requests
remain denied; the page fetches its catalog on the blog origin.

### Video catalog contract

The version-1 delivery catalog retains the `photos` array for images and videos.
Missing `kind` means image; explicit `image` is also accepted. Video records add
only `kind: "video"` and a positive finite `duration` in seconds to the existing
visitor metadata whitelist. `width`/`height` describe encoded video dimensions.
The poster is the 640 JPEG and `src` is the MP4. Protected URLs are always rebuilt
as `/photos/private/<album ID>/media/<photo ID>/{640.jpg,video.mp4}`; source URLs,
originals and admin metadata are never trusted or passed through. Unknown catalog
kinds fail closed. Public catalogs keep their existing public-domain URLs.

Each video has `photos/<32 lowercase hex ID>/{640,1280,2560}.jpg` poster derivatives
and `photos/<ID>/video.mp4`. The gallery labels video posters with a play badge and
duration, and opens a native-controls video with a poster and no autoplay. Closing,
navigating or clearing a revoked private session pauses and releases the source.
Native seeking, keyboard controls and touch gestures on the video are not gallery
navigation shortcuts; Escape still closes the viewer. A video failure displays an
accessible error and rechecks private catalog authorization before retaining it.
Already downloaded/buffered bytes cannot be revoked; requests already authorized
may finish. There is no background polling or promise of immediate remote erasure.

R2 implementation follows the [Workers API reference](https://developers.cloudflare.com/r2/api/workers/workers-api-reference/):
`head(key)` returns the complete object size without reading its body; ranged
`get(key, { range: { offset, length } })` returns a bounded stream while `size`
remains the full object size. No request headers are forwarded as R2 conditions.

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
