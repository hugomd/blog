# Hugo's Blog

This is the repository that houses my blog and personal website. It runs [Hugo](https://gohugo.io/) (I know, I know, it has a very cool name).

It uses the [TBD](https://github.com/hugomd/blog/tree/develop/themes/simple) theme (of my own making) and is hosted on [Cloudflare Pages](https://pages.cloudflare.com/).

## Running locally

The toolchain is pinned with [Hermit](https://cashapp.github.io/hermit/).
Run `./bin/hermit install` to install it, then activate it with
`source bin/activate-hermit` before running:

```
hugo server
```

### Amp orbs

`.agents/setup` bootstraps Hermit and installs the versions pinned in `bin/`:
Hugo Extended 0.111.3 and Go 1.27.3. Hugo is kept at this version
for compatibility with the site's legacy templates; the gallery needs Extended's
Sass support. Setup then builds a pinned `openring` revision with Hermit's Go
compiler into its standard `.hermit/go/bin` directory. Go verifies module
checksums; generated binaries are ignored by Git.

Setup also adds a repository-scoped login-shell hook so Amp commands and
supervised services receive the complete Hermit environment automatically.
Themes are vendored; there are no Node packages or databases to install.
Repeated setup runs reuse Hermit's package cache and the Go build cache.
Setup downloads the [photo manager](photo-manager/README.md)'s Go modules and
warms its build cache without needing R2 credentials or starting the service.
`.agents/resume` only checks that the tools are available; it does not reinstall
anything or start servers.

Setup deliberately does not run `deploy.sh`: that script refreshes the checked-in
webring from live feeds. Full site builds also fetch remote tweet and Fediverse
data. The sponsorship list is maintained directly in `content/about.md` and
does not need a GitHub token.
Unavailable remote data can still break a build independently of the toolchain;
for example, the tweet shortcode assumes the API supplies a creation date.

#### Gallery previews

A working Hugo server does not guarantee a working `/photos/` preview. The
gallery fetches `https://photos.hugo.md/index.json` in the visitor's browser;
orb setup does not provision that endpoint, publish photos or configure CORS.
If the portal shows “The photographs couldn’t be loaded”, check the manifest
request in the browser's Network panel and inspect the endpoint without following
redirects:

```sh
curl -sS --max-time 15 -D - -o /dev/null https://photos.hugo.md/index.json
```

The endpoint must return HTTP 200 and a version-1 JSON catalog with `photos` and
`albums` arrays. On 15 November 2026 it returned HTTP 303 to
`https://hugo.md/about.json`, which is not a gallery catalog. If that redirect is
still present, correct the public photo domain's routing/custom-domain setup to
serve the published R2 `index.json`; reinstalling Hugo or rerunning orb setup
will not fix it. Shared infrastructure changes need separate approval.

Even with a valid endpoint, a portal has a different origin from `hugo.md`.
Public-bucket GET/HEAD CORS must allow the exact preview origin for both the
manifest and media. Inspect `Access-Control-Allow-Origin` on a request with that
`Origin` header; a successful command-line fetch alone does not verify CORS.
Do not enable public access or CORS on either private bucket to fix a preview.

For isolated browser checks while the public endpoint is unavailable, use a
disposable local preview server that rewrites only the preview HTML's
`data-manifest` to a same-origin version-1 fixture catalog, and serves its
explicitly chosen public test images/posters and MP4s. Leave the production
manifest URL unchanged and label captures as fixture-based. Never add an
automatic fallback to repository photos, drafts or private originals.

### Static Assets
Hosted assets are stored in [`/content`](./content), and get served on `/`, e.g. `/about.json`.

### Photo manager

The standalone [Go photo manager](photo-manager/README.md) uploads and processes
photos, manages optional albums, and publishes an R2-backed manifest. Its
deployment configuration belongs in `hugomd/jegigrat-k3s`; it is independent of
the Hugo build.

The public `/photos/` page fetches `https://photos.hugo.md/index.json` at runtime:
the photostream includes every published photo (including photos without an
album), and Albums preserves each album's curated order. Album links use
`/photos/#album/<slug>`; the existing New York URL opens the `new-york-2022`
album from the same manifest. Photos remain on R2, with no build-time download
or blog redeployment needed after publishing. Only the public manifest is read;
drafts never fall back to repository images. A missing manifest shows an empty
gallery, and other request failures offer a retry.

The image RSS feed is at `https://photos.hugo.md/index.xml`, with autodiscovery
in the gallery HTML. Studio generates it in Go and stores it in the public R2
bucket when publishing, archiving or deleting photos; no blog rebuild or
request-time feed generation is needed. Items follow photostream order and
include the image, caption and stable viewer link as their GUID; dates use upload
time, not capture time. Videos and non-public photos are excluded. Empty catalogs
produce an empty feed. The existing blog RSS feed is unchanged.

Each image or video has a stable viewer link using its manifest ID, for example
`/photos/<id>/#album/new-york-2022` (or `/photos/<id>/` for the
photostream). Thumbnails are ordinary links that support copying and opening in
a new tab. Opening, paging, swiping and closing the viewer update browser
history; direct links, reloads and Back/Forward restore the selected item within
that collection. Missing, removed or out-of-album IDs show an unavailable response
instead of selecting another item. Existing `?photo=<id>` links redirect to the
new paths; the legacy New York page retains its album order.

The gallery HTML includes a description, a semantic heading and Open Graph /
Twitter metadata. Public photo paths return image-specific metadata in the
initial HTML, including the published derivative (or a video's JPEG poster),
title, caption and a canonical `/photos/<id>/` URL. Album fragments are viewer
context, not separate canonical pages. `/photos/` includes the first four
published items in manifest (photostream) order as Open Graph images, using JPEG
posters for videos and the first item for Twitter's large-image card. Empty
streams have no preview image; the avatar is never used for a stream preview.
The Pages Function reads the current `index.json` from the `PUBLIC_PHOTOS` R2
binding at request time; it never reads protected storage or originals. That
binding is required in production and preview environments, even without private
albums. Missing configuration/catalogs return 503; removed/unpublished IDs return
404 with noindex. Responses are not cached, so later requests reflect publication
changes (social platforms can retain their own earlier previews). No build-time
manifest fetch or redeployment is needed when photos change.

The public R2 bucket needs GET/HEAD CORS access for `https://hugo.md` and
`https://hu.md`. Private-bucket access is never enabled. The public page uses
rounded, edge-to-edge photographs on black, a thumbnail-origin zoom viewer,
keyboard navigation, reduced-motion support, and optional camera information
and tags. It has no frontend dependencies or build step.

#### Likes

The viewer shows a heart and like count for public photostream items (not
private albums). Likes are anonymous and like-only: the browser remembers its own
likes in `localStorage`, and `functions/photos/likes/` stores one counter per
item in D1. The gallery reads every count once per page load (`GET
/photos/likes`), so paging through photos makes no requests; a like is `POST
/photos/likes/<id>`. Only same-origin POSTs for IDs in the current public
`index.json` are counted. The button stays hidden when the counts can't be
loaded, including under plain `hugo server`.

Cloudflare configuration (already set up):

1. D1 database `blog-reactions` (Oceania), created with
   `photo-access/likes.sql`.
2. Bound as `REACTIONS` on the `blog` Pages project for production and preview.
3. A WAF rate-limiting rule on the `hugo.md` zone, using the free plan's single
   rule: `starts_with(http.request.uri.path, "/photos/likes/")`, 20 requests per
   10 seconds per IP, blocking for 10 seconds. It matches likes only: the bulk
   read at `/photos/likes` has no trailing slash. There is no rate limiting in
   code. Requests to `blog-cti.pages.dev` bypass zone WAF rules.

Private share-link and password-protected albums use a narrowly routed Pages
Function and a separate, non-public delivery bucket. Private links contain a
revocable random key in the URL fragment, not an encoded password;
password-protected albums require a separately shared password. See the
[protected album configuration and security contract](photo-access/README.md)
before deploying or enabling them in Studio.
Private viewer links use `/photos/private/<album-id>/<photo-id>/` and keep
generic, noindex, no-store metadata. Old `?photo=<id>` links migrate after unlocking.
Neither kind of viewer link grants access: the
recipient still needs a valid album session, the password, or the original
complete share link. Navigating photos never copies the share key into a URL.
