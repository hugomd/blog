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

The public R2 bucket needs GET/HEAD CORS access for `https://hugo.md` and
`https://hu.md`. Private-bucket access is never enabled. The public page uses
rounded, edge-to-edge photographs on black, a thumbnail-origin zoom viewer,
keyboard navigation, reduced-motion support, and optional camera information
and tags. It has no frontend dependencies or build step.

Private share-link and password-protected albums use a narrowly routed Pages
Function and a separate, non-public delivery bucket. Private links contain a
revocable random key in the URL fragment, not an encoded password;
password-protected albums require a separately shared password. See the
[protected album configuration and security contract](photo-access/README.md)
before deploying or enabling them in Studio.
