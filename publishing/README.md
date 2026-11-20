# iPhone publishing bridge

Write in iA Writer → **Publish / New Draft** → private browser review → **Publish to blog**.
The browser can edit the title, slug, date, description, tags, cover and Markdown before publication. Saving a draft writes only to D1. Publishing creates `content/post/<slug>.md` on `hugomd/blog`'s `develop` branch. The usual Cloudflare Pages build then publishes it. “Sent to GitHub” is deliberately not “live”.

This implementation is not deployed or provisioned merely by adding these files. Production setup below requires approval. Do not test against production with real GitHub writes until that is explicitly intended.

## Front matter requirement

**Posts need valid Hugo front matter, not just Markdown from iA Writer.** This is implemented rather than left as a follow-up:

- Generate YAML `title`, `date` (RFC3339 with timezone), `slug`, `description`, `tags`, `toc`, optional `cover`, and `draft: false` only in the file committed on explicit publication.
- Private draft capture does not create any public-repository file, even one with `draft: true`.
- Import supported YAML front matter if the source starts with `---`. Prefer its title over the app's filename/title, strip that front matter once, and preserve the remaining Markdown, Unicode and Hugo shortcodes. Strings are quoted for Hugo's YAML reader.
- Reject invalid, duplicate or unsupported YAML fields, tags and aliases instead of silently changing their meaning. Supported input keys are `title`, `date`, `slug`, `description`, `tags`, `toc`, `cover`, `draft`. Input `draft` never bypasses browser publication. Arbitrary Hugo parameters (for example `sequence`, `aliases` and translated filenames) need deliberate support before using them here.
- Default the date to capture time in UTC, generate a safe slug from the title, and offer both for review. Future-dated captures remain editable, but saving/publication requires correcting the date: scheduling is not implemented.
- Reject the reserved slug `index` on capture, editing and publication: Hugo treats `content/post/index.md` as a leaf bundle and could hide sibling posts. A title that generates this slug needs changing or an explicit different front matter slug.
- The generated file is visible on the review page. This is **source review**, not an exact rendered Hugo preview. Changing fields requires saving to regenerate the file preview; Publish saves current fields and publishes that version.

Example generated header:

```yaml
---
title: "From the window seat"
date: "2026-11-20T09:30:00+11:00"
slug: "from-the-window-seat"
description: "A quiet moment between places."
tags:
  - "travel"
toc: false
draft: false
---
```

## Production setup — not automatic

Keep dashboard-managed Pages bindings: this repository does not introduce a Wrangler configuration that might replace the existing photo bindings.

1. Create **two new D1 databases**, suggested names `blog-publishing` and `blog-publishing-preview`, in Oceania. Apply `publishing/schema.sql` to each. For an existing publishing database, reapply this idempotent schema **before deploying this handler**: it adds `auth_admissions` without altering existing requests, codes, tokens or drafts. Bind each as `PUBLISHING` in its corresponding Pages environment. Never reuse `REACTIONS` or bind preview to the production publishing database. Do not enable read replication: authorization and retry reconciliation must read current state from the primary.
2. Create a Cloudflare Access self-hosted application protecting **`hugo.md/publishing/admin*`**, including the exact `/publishing/admin` path. Allow only the owner's verified email. Use GitHub as the identity provider, or Access's email one-time PIN. This is the browser login, including the one-time app approval. The function also verifies the JWT signature, issuer, audience, expiry, not-before and exact owner email; a forged header or a native app token cannot read drafts.
3. Leave `/publishing/`, `/publishing/auth`, `/publishing/token`, `/publishing/metadata` and `/publishing/micropub` **outside Access**. Native iA Writer requests cannot complete an interactive Access login. Only the connection page/discovery is public; Micropub requires a hashed, revocable bearer token with create-only permission.
4. Set production Pages variables:
   - `PUBLISHING_ORIGIN=https://hugo.md` — exact origin, no path, query or fragment. Other hostnames (including `pages.dev` and `hu.md`) fail closed rather than bypassing Access or mixing identities.
   - `PUBLISHING_ACCESS_TEAM=<team>.cloudflareaccess.com` — hostname, not URL.
   - `PUBLISHING_ACCESS_AUD=<the Access application's audience tag>`.
   - `PUBLISHING_OWNER_EMAIL=<the verified email allowed by Access>`.
   - `PUBLISHING_GITHUB_REPOSITORY=hugomd/blog`.
   - `PUBLISHING_GITHUB_BRANCH=develop`.
   - `PUBLISHING_ALLOW_PUBLISH=true` **only when ready for live commits**.
5. Store `PUBLISHING_GITHUB_TOKEN` as an **encrypted Pages secret**. Use a fine-grained GitHub token limited to this repository with Contents read/write; no Actions, administration or other repository permissions. GitHub cannot scope Contents permission to just `content/post/`, so the handler fixes that path and never supplies an overwrite SHA. Do not reuse an agent's GitHub credentials or put a token in source, logs, a public portal or this README.
6. Set the Pages Functions compatibility date to `2026-11-20` or newer and retain `nodejs_compat` required by existing photo functions. The build must install the root npm lockfile (`npm ci`) before bundling Functions. Enable supported Functions logging in Cloudflare; application error logs intentionally omit credentials, authorization parameters and draft content.
7. Preview needs its **own origin, Access application/AUD and database**. Set `PUBLISHING_ALLOW_PUBLISH=false` and omit the GitHub token. The handler also refuses publication on a Pages branch that differs from the configured GitHub branch. Do not copy production secrets into previews.
8. Deploy the code and smoke-test discovery, signed-out draft denial, app connection, draft capture, metadata editing and intentional publication. Protect the admin route with Access **before** enabling the live bridge. Verify that the committed post builds successfully before calling it live.

The relevant Pages Function adapter is `functions/publishing/[[path]].js`. `static/_routes.json` routes only photos and publishing through Functions; ordinary blog pages/assets remain static.

## Connect from iA Writer

Once deployed, use **`https://hugo.md/publishing/`** as the Micropub website URL, not the root blog URL and not `/micropub` itself. Approve in your browser, then return to the app. The actual iOS callback observed in the experiment was `https://ia.net/writer/indieauth/redirect`; the server accepts only that registered iA Writer client/callback pair, rather than arbitrary redirect URLs.

All discovery links in both HTML and HTTP headers are **absolute HTTPS URLs**. Relative HTML links caused iA Writer to report “Unhandled URL” even after successfully exchanging its authorization code. The disposable experiment succeeded after making those links absolute.

App tokens last 90 days and are stored only as SHA-256 hashes. Disconnect an app from the writing desk to revoke its token immediately, or reconnect after expiry. Authorization requests expire in five minutes; one-use authorization codes expire in one minute. S256 PKCE is checked when supplied; the observed legacy iA Writer flow does not supply PKCE. Browser mutations require both a valid owner JWT and a same-origin POST.

Anonymous connection admission allows five valid `/auth` attempts per registered client/IP in a five-minute window starting with the first attempt, plus the global 100-pending-request backstop. Admission increments atomically in D1, even if the global limit subsequently rejects the request; approval does not reset the window. Only Cloudflare's edge-provided `CF-Connecting-IP` is used, never `X-Forwarded-For`; a missing address fails closed. Deploy behind Cloudflare Pages, without an untrusted Worker rewriting that header. D1 stores a SHA-256 digest of the client/address, not raw IPs; these are pseudonymous, not anonymous (IPs can be guessed). Expired buckets are deleted opportunistically on subsequent valid connection attempts, not by a scheduled retention job. Shared NAT users share the limit; distributed IPs/IPv6 address rotation can still exhaust the global backstop, so this is not a substitute for edge abuse controls.

## Publication and retry behavior

- An identical repeated Micropub submission reopens its existing draft. Changed text creates a **new** draft; it does not guess which earlier draft or published post to overwrite. Re-sending the original after browser edits reopens those edits rather than replacing them.
- Browser saves use versions to prevent stale tabs from overwriting newer changes. Validation errors retain the entered text. Unchanged Markdown retains its original line endings despite browser form newline normalization.
- Publish freezes a version before contacting GitHub. Collision recovery and completion update only that frozen version and return a conflict if it has changed; an older retry cannot unlock or mark a newer version published. Existing files with different content are never overwritten: change the slug and try again. An exact matching file is treated as an already-completed publication, avoiding a duplicate commit.
- If a network error or database update failure leaves the GitHub result unknown, the draft stays frozen. **Check and retry publication** reconciles the same deterministic file/content before attempting creation. Do not manually unlock or edit a frozen draft without checking GitHub first.
- Publication currently creates new English posts, not updates to existing posts. It does not upload images, embed files, convert HTML to Markdown, schedule posts, or generate a full Hugo preview. Existing image URLs/covers can be referenced. Only text JSON and URL-encoded captures are accepted; unsupported media requests fail explicitly.
- Drafts remain in D1 until deliberately removed by an operator; the desk shows the 100 most recently changed drafts. Database backups and Access policies are part of deployment operations. At most 100 anonymous authorization requests can be pending; expired requests/codes/tokens are cleaned up as new connections are processed.

## Verification

Use Node 24+:

```sh
npm ci
npm test
npx wrangler pages functions build --outdir .amp/in/orb-backup/publishing-build --compatibility-date 2026-11-20 --compatibility-flags nodejs_compat
```

The tests use real SQLite for production SQL, signed RSA/JWKS fixtures for Access, and a simulated GitHub Contents API. They cover draft isolation, replay/expiry, revocation, front matter/shortcode fidelity, reserved bundle filenames, stale edits, CSRF, escaping, collision rejection, lost-response recovery, interleaved stale publishing retries, per-IP admission/expiry, the global backstop and preview isolation. None require production credentials or writes. Cloudflare's actual client-address header, Access policy/identity-provider and GitHub permissions still need the production smoke test.
