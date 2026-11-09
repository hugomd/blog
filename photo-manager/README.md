# Photo studio

A small Go service for managing the blog’s photostream and optional albums.
The frontend is embedded in the binary: no Node build, database or persistent
volume. Images **and all manifests** live in R2 in production.

## Editing and publishing

- Upload multiple JPEGs/PNGs (or drop them on the library). Each starts as a
  private draft. HEIC/RAW files must first be exported as JPEGs.
- The Go service corrects EXIF orientation and creates JPEG variants with a
  maximum long edge of 640, 1280 and 2560 pixels, without upscaling. PNG
  transparency is flattened onto white. Input is limited to 30 MiB, 40 MP and
  16,000 pixels on either axis; processing is serialized to bound memory usage.
- Re-encoded images contain **no embedded source metadata**. The manifest
  preserves only camera make/model, lens, ISO, shutter speed, aperture and
  focal length, plus an editable capture date. GPS, camera serial numbers,
  owner names, XMP and embedded previews are not published. Original files,
  including any original GPS data, remain private.
- Add alt text, a caption and comma-separated tags. Alt text is required to
  mark a photo ready for publication. Albums are optional, and a photo can
  appear in multiple albums without duplication in the photostream.
- Albums have titles, URL slugs, descriptions, covers and explicit photo order.
  Use the arrow buttons to reorder photos; remove an album without deleting
  its photos. The photostream orders all published photos by upload time,
  newest first, independent of album membership or later edits.
- **Save photo / Save album** changes the private working catalog only.
  **Publish changes** confirms and publishes all ready photos/albums together.
  Public albums omit draft photos and choose a public fallback cover if needed.
- **Delete photo** is separate from removing album membership. Use Delete in
  an album row or the photo editor, then confirm the named photo. It removes
  the private original and all resized images from both buckets, removes the
  photo from every album, and selects replacement covers where necessary.
  The current public index is updated immediately without publishing unrelated
  drafts; no separate Publish is needed. Deletion cannot be undone here.

This service publishes a data contract, not the blog's public gallery pages.
The existing Hugo gallery and ignored photostream prototype are unchanged.
Connecting the public photostream and album pages to the R2 index is a separate
integration step.

## Storage and failure behavior

Private bucket (never enable public access):

- `originals/<id>`: untouched originals, including original metadata.
- `photos/<id>/{640,1280,2560}.jpg`: private preview/ready-to-publish images.
- `catalog.json`: versioned-schema working catalog, including drafts and albums.

Public bucket:

- `photos/<id>/{640,1280,2560}.jpg`: approved web images only.
- `releases/<id>.json`: immutable historical public snapshots.
- `index.json`: the currently published snapshot, containing `version`,
  `revision`, `publishedAt`, `photos` and `albums`. Photos contain `id`, `alt`,
  `caption`, `tags`, `capturedDate`, `uploadedAt`, dimensions, allowlisted `exif`,
  `thumbnail` and `src`. Albums reference the same IDs through ordered
  `photoIds` and `coverId`. Standalone photos are in `photos` too.

Images and a historical release are written before the final conditional write
of `index.json`. Failure before that final write leaves the previous live index
intact, although some unreferenced public files may have uploaded. A lost
connection can leave a write's outcome unknown: reload to check, rather than
blindly retrying an upload. Failed private uploads can leave unreferenced
originals/derivatives; there is no automatic garbage collector.

Catalog updates use ETag compare-and-swap, preventing stale tabs from silently
overwriting each other. Keep **one writer deployment, one replica, Recreate**;
do not run other catalog writers or scale replicas. Serialization and the
publish sequence are intentionally single-process. ETags are not a distributed
transaction across different keys.

The index has a 30-second public cache lifetime; immutable images/releases have
a one-year cache lifetime. Do not put an overriding “cache everything forever”
rule on the index. Configure public-bucket CORS to allow `GET`/`HEAD` from the
blog origin when its frontend starts fetching the manifest cross-origin.

**Unpublishing is not erasure.** It removes a photo/album from the next index,
not historical releases, cached copies or existing public image URLs.
**Delete photo** additionally deletes stored image files, but is not a promise
of privacy erasure: historical release metadata, CDN/browser copies (potentially
cached for a year), independent backups and external copies remain. Purge the
exact public image/index URLs and remove relevant historical releases separately
when privacy erasure is needed. Maintain an independent private-bucket backup.

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
From this directory:

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
| `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY` | Dedicated R2 S3 credentials, read/write only the two required buckets |
| `R2_PRIVATE_BUCKET` | Private original/catalog bucket |
| `R2_PUBLIC_BUCKET` | Separate public web-image/manifest bucket |
| `PUBLIC_MEDIA_URL` | HTTPS public bucket custom domain, optionally with a URL prefix |
| `ADMIN_PASSWORD` / `ADMIN_PASSWORD_FILE` | At least 16 characters; only needed in password mode |
| `PORT` | HTTP listen port, default 8080 |

All data APIs trust `X-Auth-Request-Email` in `AUTH_MODE=proxy`. **Do not expose
that mode directly.** It requires the Traefik ForwardAuth gate to overwrite the
header and a NetworkPolicy preventing ordinary pods from bypassing the proxy.
Host-network/node-local processes and cluster administrators remain trusted,
as with other internal Jegigrat services. Keep `/healthz` unauthenticated for
kubelet probes; don't create an unauthenticated route for the other APIs.

Production routing, NetworkPolicy, Secret references and shared Gateway TLS
changes belong in `hugomd/jegigrat-k3s/config/photo-manager/`, not this repository.
The proposed editor hostname is `photos.internal.ii.md`; confirm it and prepare
DNS, R2 buckets/domain, sealed credentials, an immutable image digest and the
shared certificate before deploying. No resources are provisioned by this app.

## Container and image publication

```sh copy Copy Container Build Commands
docker build -t blog-photo-manager:test photo-manager
```

Run that command from the repository root. The multi-stage build runs the Go
tests, embeds the frontend and produces a static, nonroot binary in a distroless
image. It supports a read-only root filesystem and needs no writable volume.

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
