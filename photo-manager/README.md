# Photo studio

A small Go service for managing the blog’s photostream and optional albums.
The frontend is embedded in the binary: no Node build, database or persistent
data volume. Media **and all manifests** live in R2 in production; video
processing needs temporary disk space.

## Editing and publishing

- Upload multiple JPEGs/PNGs or MP4/MOV videos (or drop them on the library).
  Each starts as a private draft. HEIC/RAW files must first be exported as JPEGs.
  Studio sends 8 MiB chunks directly to private R2 using one-hour signed URLs,
  with three simultaneous transfers, byte progress, bounded retries and Cancel.
  Original bytes no longer pass through the Studio container. Keep the page
  open during transfer; once processing is accepted, it runs server-side.
  Reloading does not resume browser chunks. A server restart requires inspection
  and explicit retry, not automatic replay.
- The Go service corrects EXIF orientation and creates JPEG variants with a
  maximum long edge of 640, 1280 and 2560 pixels, without upscaling. PNG
  transparency is flattened onto white. Input is limited to 30 MiB, 40 MP and
  16,000 pixels on either axis; processing is serialized to bound memory usage.
  New uploads also store an optional standard `blurhash` string for image or
  video-poster placeholders. The 4×3-component hash is encoded from the oriented,
  flattened 640px JPEG, reduced to at most 32px on its long edge.
- Videos are limited to 512 MiB and ten minutes. FFmpeg accepts MP4/MOV
  (including HEVC), creates metadata-stripped H.264/AAC faststart MP4 playback
  and three JPEG posters. Output is bounded to 1920×1080 landscape or
  1080×1920 portrait at 30 fps without upscaling. Originals stay private.
  The blog and authenticated Studio previews use native video controls;
  protected videos require the same album session as protected images and
  support byte ranges for seeking.
- Re-encoded images contain **no embedded source metadata**. The manifest
  preserves only camera make/model, lens, ISO, shutter speed, aperture and
  focal length, plus an editable capture date. GPS, camera serial numbers,
  owner names, XMP and embedded previews are not published. Original files,
  including any original GPS data, remain private.
- Optionally add alt text, a caption and comma-separated tags. Photos can be
  published without alt text. Photos in any published album are automatically ready;
  the per-photo **Publish independently of albums** setting is for standalone
  publication. Remove a photo from every published album and clear its
  independent setting to return it to draft. A photo can appear in multiple
  albums without duplication in the photostream.
- Albums have titles, URL slugs, descriptions, covers and explicit photo order.
  Use the arrow buttons to reorder photos; remove an album without deleting
  its photos. The photostream orders all published photos by upload time,
  newest first, independent of album membership or later edits.
- Albums can be public, accessible with a private share link, or password protected.
  A photo can belong to multiple albums with different access modes;
  independent public publication or any published public album keeps it public.
  Studio labels that overlap **Also public**. Private-only photos are omitted
  from the public photostream. Save, then Publish applies access/password changes.
  Private share links contain a random key in `#key=...`, not a password.
  Anyone with the full link can view or forward it; the album URL alone cannot
  open the photos. **Regenerate link**, Save, then Publish revokes the old link
  and its viewing sessions. Ordinary saves keep the link unchanged.
  Both private modes require the separate [delivery gateway](../photo-access/README.md)
  and a third, non-public delivery bucket. Previously public URLs cannot be recalled.
- **Save photo / Save album** changes the private working catalog only.
  **Publish changes** confirms and publishes all ready photos/albums together.
  Published albums include their non-archived photos without a separate photo
  opt-in. Draft albums alone do not make their photos public. Archived/deleting
  photos are excluded and covers fall back.
- The publication preview compares the next public manifest with the live index:
  photos and albums are counted separately as added, updated, removed or unchanged,
  alongside live and next-release totals. Private-only edits do not require
  publication. Album order/cover changes count as album updates, not new photos.
- **Republish** rebuilds an existing publication, filling missing BlurHashes
  without re-uploading media. It requires no pending public or protected changes;
  publish or undo those saved edits first. The server enforces this too.
- Publication runs in the background with image-copy progress. You can browse,
  reload or close the page; edits, uploads, archive and delete are locked until
  it finishes. A pod replacement resumes the saved job. Failures expose a retry
  action; reload first if another writer changed the catalog or public index.
- Photo/album saves, album/photo deletions and archive/restore run as server-side
  background changes. Studio gets a `202` acknowledgement after the job status is
  stored, then polls `/api/operation`; you can browse, reload or leave the page.
  Conflicting writes and publication are blocked until completion. Success is
  shown only after storage finishes. A server restart marks an interrupted change
  failed rather than replaying a possibly committed edit; reload to inspect the
  actual state before retrying. Archive/deletion intent markers remain available
  for cleanup retries. Request bodies/passwords are not persisted in job status.
  Upload completion uses the same background worker; creating upload plans and
  draft albums remains synchronous. API clients without
  `Prefer: respond-async` retain the synchronous mutation response contract.
- **Archive photo** keeps originals, derivatives, metadata and private album
  memberships/order, but removes the photo from the current public index and
  every stored historical release, including album references and covers.
  This takes effect immediately, without publishing other drafts. Find archived
  photos using **Archived** in the sidebar; **Restore photo** returns one to
  the active library without changing the live index. Restored photos in a
  published album become ready for the next publication; otherwise they are
  drafts. Private album editors retain and
  label archived entries; public albums exclude them and use a fallback cover.
  Archiving is not privacy erasure: public image URLs and cached copies remain.
- **Delete photo** is separate from removing album membership. Use Delete in
  an album row or the photo editor, then confirm the named photo. It removes
  the private original and all resized images from both buckets, removes the
  photo from every album, and selects replacement covers where necessary.
  The current public index is updated immediately without publishing unrelated
  drafts; no separate Publish is needed. Deletion cannot be undone here.

This service publishes a data contract, not the blog's public gallery pages.
The Hugo `/photos/` page reads the public R2 index at runtime for its photostream
and albums. Publishing here updates the public page without rebuilding the blog.
Only published photos are shown; private drafts have no repository fallback.

## Storage and failure behavior

Private bucket (never enable public access):

- `originals/<id>`: untouched originals, including original metadata.
- `photos/<id>/{640,1280,2560}.jpg`: private preview/ready-to-publish images.
- `photos/<id>/video.mp4`: optimized playback for videos only.
- `uploads/<id>.json`: private multipart-upload plan/recovery metadata.
- `catalog.json`: versioned-schema working catalog, including drafts and albums.
- `publication.json`: latest job identity, catalog ETag/revision and outcome.
  Intermediate progress is in memory; a restarted job recounts its copies.

Public bucket:

- `photos/<id>/{640,1280,2560}.jpg`: approved web images only.
- `photos/<id>/video.mp4`: approved optimized video playback, never originals.
- `releases/<id>.json`: historical public snapshots, redacted when archiving.
- `index.json`: the currently published snapshot, containing `version`,
  `revision`, `publishedAt`, `photos` and `albums`. Photos contain `id`, `alt`,
  `caption`, `tags`, `capturedDate`, `uploadedAt`, dimensions, allowlisted `exif`,
  `thumbnail`, `src` and optional `blurhash`. Albums reference the same IDs through
  ordered `photoIds` and `coverId`. Standalone photos are in `photos` too.
  Videos also contain `kind: "video"` and duration in seconds; their `src` is
  the MP4 and `thumbnail` is the poster. Missing `kind` still means image.

Images and a historical release are written before the final conditional write
of `index.json`. Failure before that final write leaves the previous live index
intact, although some unreferenced public files may have uploaded. A lost
connection can leave a write's outcome unknown: reload to check, rather than
blindly retrying an upload. Failed private uploads can leave unreferenced
originals/derivatives; there is no automatic garbage collector.
Signed upload plans expire after one hour but expiration is not garbage
collection. Configure an R2 incomplete-multipart lifecycle rule (R2 defaults to
seven days); abandoned plan records and already-completed, unimported originals
still need scoped operator cleanup. Explicit Cancel removes unfinished uploads;
completion retries finish post-commit cleanup without deleting library media.

Publishing uses native R2 `CopyObject`, retaining the private source, rather than
downloading JPEGs into Go and uploading them again. Credentials need source read
and destination write access. At most four derivative copies run concurrently;
photos already in the current index are not recopied. Only re-encoded derivatives
are copied, never originals. An ordinary background publication also fills missing
legacy BlurHashes from private `640.jpg` previews, reusing hashes from the prior
live public/protected release when available. These best-effort reads are bounded;
missing or invalid previews omit the hash so clients can use neutral placeholders.
Backfill only enriches the authorized release projections, never exposes private-only
photos publicly, and does not rewrite the working catalog or change its ETag/revision.
Derived hash-only differences do not count as pending publication changes. No
re-upload or separate migration is required; existing live manifests remain unchanged
until the next publication. Studio uses these hashes for loading placeholders in
library, album, chooser and editor previews, including video posters. Its catalog
response fills missing draft hashes from already-loaded live public/protected
manifests without saving draft changes or performing extra storage reads. Missing
or invalid hashes keep a neutral placeholder; alt text remains available to
assistive technology without flashing while images load or fail.
Library thumbnails use 640px previews; photo details
keep 1280px previews. Authenticated image/poster and video preview endpoints
check current library membership, then issue `307` redirects to 15-minute signed
GET/HEAD URLs for private R2 derivatives. Image and video bytes, including video
seek ranges, travel directly from R2 to the browser, not through Studio. The
redirect and media responses are `no-store`; revisiting a Studio preview endpoint
gets a fresh URL, so lazy-loaded images do not depend on catalog-age expiry.
Local filesystem development retains authenticated same-origin streaming.

Preview URLs are temporary bearer capabilities: anyone holding one can read that
derivative until expiry, even outside Tailscale or after Studio logout. Removing
catalog membership prevents new URLs, but does not revoke an already-issued URL
before expiry unless its object is deleted. Originals never receive preview URLs;
public and protected visitor-gallery delivery/authentication is unchanged.

`POST /api/publish` validates `If-Match`, persists a job, and returns **202** with
`publication`. Authenticated `GET /api/publication` exposes `idle`, `running`,
`succeeded` or `failed`, the revision, and completed/total derivative counts.
Repeated submissions for the same active catalog return that job. Mutations
return **423** while it runs instead of queuing behind storage requests. The UI
polls once per second and keeps browsing available. Jobs have a 30-minute limit;
process shutdown leaves the durable running marker for startup recovery. Recovery
requires the same catalog ETag/revision; it never publishes a changed catalog.
A lost final-index response can make the outcome uncertain: reload and inspect
the live revision before retrying. Do not roll back to a synchronous publisher
while a background job is pending.

Catalog updates use ETag compare-and-swap, preventing stale tabs from silently
overwriting each other. Keep **one writer deployment, one replica, Recreate**;
do not run other catalog writers or scale replicas. Serialization and the
publish sequence are intentionally single-process. ETags are not a distributed
transaction across different keys.

The index and new releases have a 30-second public cache lifetime; immutable
images have a one-year cache lifetime. Older releases may already be cached for
a year. Rewriting them does not invalidate those copies. Do not put an overriding “cache everything forever”
rule on the index. Configure public-bucket CORS to allow `GET`/`HEAD` from the
blog origin when its frontend starts fetching the manifest cross-origin.

**Unpublishing is not erasure.** It removes a photo/album from the next index,
not historical releases, cached copies or existing public image URLs.
**Delete photo** additionally deletes stored image files, but is not a promise
of privacy erasure: historical release metadata, CDN/browser copies (potentially
cached for a year), independent backups and external copies remain. Purge the
exact public image/index URLs and remove relevant historical releases separately
when privacy erasure is needed. Maintain an independent private-bucket backup.

Archive requires object listing permission on the public bucket. It persists
`archived` and `archiving` before redacting manifests with conditional ETag
writes. If any listing/read/write fails, reload and use **Retry archive** in
Archived. Pending photos cannot be edited or restored; publishing excludes them.
Files are never deleted by archive, and the private working catalog deliberately
retains their metadata. Historical redaction is not an atomic multi-object
transaction; a failure can leave some older manifests unchanged until retry.
Do not roll back to an app version that does not understand archived photos.

Deletion persists a `deleting` marker before changing the live index or removing
files. Interrupted cleanup leaves the photo visible as **Deletion incomplete**;
reload and use **Retry deletion** to finish, including after a restart. Such
photos cannot be edited or republished. Files already removed are safe to retry;
the photo leaves the working catalog only after all object deletions succeed.
No R2 multi-object transaction or automatic background retry is assumed.
Rollback must not blindly restore a historical manifest referencing deleted
photos; those files no longer exist. Coordinate rollback with the sole writer
and condition the index write on its ETag.

## Local development

From the repository root, `./bin/hermit install` installs the pinned Go toolchain.
`.agents/setup` also downloads modules and warms the build cache for fresh orbs.
Install FFmpeg/FFprobe with H.264, HEVC and AAC support before running the
service or tests (Debian/Ubuntu: the `ffmpeg` package). They are included in the
container image and standard Amp orbs, not installed by Hermit. From this directory:

```sh copy Copy Local Development Commands
../bin/go test -race ./...
umask 077
openssl rand -hex 24 > /tmp/photo-manager-password
APP_ENV=development PHOTO_STORAGE=local \
  ADMIN_PASSWORD_FILE=/tmp/photo-manager-password \
  ../bin/go run .
```

Read the generated password privately to sign in; never commit it. Local mode
uses ignored `.local-data/{private,public}`, and refuses to start in production.
The UI labels the sandbox clearly. It uses the same upload/catalog/publishing
code as R2 but is **not** an R2 acceptance test. Password sessions expire after
12 hours and are cleared by a restart; production should use Pocket ID instead.
In Amp, start long-lived previews with `amp orb service start` and use a portal.

## Production configuration

Default port: `8080`. `GET /healthz` is an unauthenticated process liveness check,
not an R2 credentials/readiness check. Private previews and all data APIs require
authentication. Mutations additionally require an exact matching `Origin`.

| Variable | Purpose |
| --- | --- |
| `APP_ORIGIN` | Exact HTTPS editor origin, no trailing slash/path/query |
| `AUTH_MODE` | `proxy` for Jegigrat; `password` is the default |
| `ALLOWED_EMAIL` | Required exact Pocket ID email in proxy mode |
| `R2_ENDPOINT` | HTTPS account S3 endpoint, e.g. `https://ACCOUNT.r2.cloudflarestorage.com` |
| `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY` | Dedicated R2 S3 credentials, scoped to required buckets |
| `R2_PRIVATE_BUCKET` | Private original/catalog bucket |
| `R2_PUBLIC_BUCKET` | Separate public web-image/manifest bucket |
| `R2_PROTECTED_BUCKET` | Optional third, non-public derivative/snapshot bucket; enables protected album controls |
| `PUBLIC_GALLERY_URL` | Share-link base, default `https://hugo.md/photos/` |
| `PUBLIC_MEDIA_URL` | HTTPS public bucket custom domain, optionally with a URL prefix |
| `ADMIN_PASSWORD` / `ADMIN_PASSWORD_FILE` | At least 16 characters; only needed in password mode |
| `PORT` | HTTP listen port, default 8080 |

Direct browser delivery uses CORS on the **private** R2 bucket, restricted to
the exact Studio origin, allowing `PUT`, `GET`, `HEAD` and the `Content-Type` and
`Range` request headers.
For example (replace the origin if your deployment differs):

```json
[{"AllowedOrigins":["https://photos.internal.ii.md"],"AllowedMethods":["PUT","GET","HEAD"],"AllowedHeaders":["Content-Type","Range"],"MaxAgeSeconds":3600}]
```

This does not make the bucket public. Browser requests use signed query
credentials, never Studio cookies; no exposed ETag header is needed because the
server lists authoritative parts before completion. The Studio CSP permits
upload connections and video sources only to itself and the configured R2 endpoint origin. Signed URLs
are bearer capabilities: do not log or share them. R2 credentials also require
multipart initiation, part listing, completion and abort permissions. Without
CORS, browser uploads fail even if server-to-R2 access works. Local filesystem
development instead uses authenticated same-origin chunk endpoints; that fallback
is never registered for R2-backed deployments. The legacy image-only
`POST /api/photos` remains available for older clients, but Studio no longer uses it.

All data APIs trust `X-Auth-Request-Email` in `AUTH_MODE=proxy`. **Do not expose
that mode directly.** It requires the Traefik ForwardAuth gate to overwrite the
header and a NetworkPolicy preventing ordinary pods from bypassing the proxy.
Host-network/node-local processes and cluster administrators remain trusted,
as with other internal Jegigrat services. Keep `/healthz` unauthenticated for
kubelet probes; don't create an unauthenticated route for the other APIs.

Production routing, NetworkPolicy, Secret references and shared Gateway TLS
changes belong in `hugomd/jegigrat-k3s/config/photo-manager/`, not this repository.
Protected delivery adds Pages bindings, a session secret and runtime prerequisites;
follow the [protected album deployment notes](../photo-access/README.md) before
enabling it. The working catalog upgrades permanently to version 2 when first
password-protecting an album, and version 3 when first using a private share link;
older Studio versions must not be restored afterward. Private share links need no
password: anyone with the secret share URL can view or forward them. Both private
access modes stay out of the public gallery; a photo also published independently
or through a public album remains public.
The first saved video permanently upgrades the working catalog to version 4;
do not roll back to an image without video/schema-4 support. Public and protected
release versions remain 1 for compatible image-only consumers.
The proposed editor hostname is `photos.internal.ii.md`; confirm it and prepare
DNS, R2 buckets/domain, sealed credentials, an immutable image digest and the
shared certificate before deploying. No resources are provisioned by this app.

## Container and image publication

```sh copy Copy Container Build Commands
docker build -t blog-photo-manager:test photo-manager
```

Run that command from the repository root. The multi-stage build runs the Go
tests, embeds the frontend and produces a static, nonroot binary in Debian
bookworm slim with FFmpeg/FFprobe. A read-only root filesystem requires a
writable `/tmp` volume owned/writable by UID 65532 (mode 1777 also works).
Allow space for a 512 MiB original plus playback, posters and transcode scratch;
use a size-limited disk-backed `emptyDir`, not a RAM-backed volume under a small
memory limit. Processing is serialized, but cluster CPU/memory/ephemeral-storage
limits still need validation with representative videos before rollout.

The `Photo manager` GitHub workflow tests/builds on relevant pull requests and
pushes to `develop`. Image **publication is manual**: run `workflow_dispatch`
with `publish=true`. It pushes the tested linux/amd64 image to
`ghcr.io/hugomd/blog-photo-manager:sha-<commit>` and records the immutable digest
in the run summary. Pin that digest in the infrastructure repo. Make the package
public or supply a sealed GHCR pull Secret. The workflow never deploys to k3s.

Before a real deployment, validate R2 permissions/private-public separation,
restart persistence, trusted-proxy bypass prevention, admin/non-admin access,
TLS and upload memory limits against the actual cluster. Unit/HTTP/S3-fixture
tests do not prove those live environment contracts.
