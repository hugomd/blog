import { scryptSync, timingSafeEqual } from 'node:crypto';

const ID = /^[0-9a-f]{32}$/;
const LINK_KEY = /^[0-9a-f]{64}$/;
const HASH = /^scrypt\$16384\$8\$5\$([0-9a-f]{32})\$([0-9a-f]{64})$/;
const encoder = new TextEncoder();
const SESSION_SECONDS = 12 * 60 * 60;
const BODY_LIMIT = 4096;
const securityHeaders = {
  'Cache-Control': 'private, no-store, max-age=0',
  'CDN-Cache-Control': 'no-store',
  'Cloudflare-CDN-Cache-Control': 'no-store',
  Pragma: 'no-cache',
  'Referrer-Policy': 'no-referrer',
  'X-Robots-Tag': 'noindex, nofollow, noarchive',
  'X-Content-Type-Options': 'nosniff',
  Vary: 'Cookie, Origin',
};

class Failure extends Error {
  constructor(status) { super('Photo access unavailable'); this.status = status; }
}
const fail = status => { throw new Failure(status); };
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const hex = bytes => Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
const unhex = value => Uint8Array.from(value.match(/../g), byte => parseInt(byte, 16));
const cookieName = id => `__Secure-photo_album_${id}`;
const albumPath = id => `/photos/private/${id}/`;

function response(request, status, body = null, extra = {}) {
  return new Response(request.method === 'HEAD' ? null : body, {
    status, headers: { ...securityHeaders, ...extra },
  });
}
function json(request, value, extra = {}) {
  return response(request, 200, JSON.stringify(value), { 'Content-Type': 'application/json; charset=utf-8', ...extra });
}

async function readJSON(bucket, key) {
  const object = await bucket.get(key);
  if (!object) fail(503);
  return JSON.parse(await object.text());
}

// The public index is the ONLY publication pointer. Never use a URL, cookie, or
// old session to select a release. R2 get is deliberately unconditional.
async function currentAlbum(env, id) {
  const index = await readJSON(env.PUBLIC_PHOTOS, 'index.json');
  if (!record(index)) fail(503);
  if (index.protectedRelease === undefined) fail(404);
  if (typeof index.protectedRelease !== 'string' || !ID.test(index.protectedRelease)) fail(503);
  const release = await readJSON(env.PROTECTED_PHOTOS, `releases/${index.protectedRelease}.json`);
  if (!record(release) || release.version !== 1 || !Array.isArray(release.albums) || !Array.isArray(release.photos)) fail(503);
  const matches = release.albums.filter(album => record(album) && album.id === id);
  if (matches.length > 1) fail(503);
  const album = matches[0];
  if (!album || album.published !== true || !['password', 'link'].includes(album.access)) fail(404);
  if (typeof album.accessVersion !== 'string' || !ID.test(album.accessVersion)) fail(503);
  if (album.access === 'password' && (typeof album.passwordHash !== 'string' || !HASH.test(album.passwordHash))) fail(503);
  if (album.access === 'link' && (typeof album.linkHash !== 'string' || !LINK_KEY.test(album.linkHash))) fail(503);
  if (!Array.isArray(album.photoIds) || !album.photoIds.every(photoID => typeof photoID === 'string' && ID.test(photoID))
      || new Set(album.photoIds).size !== album.photoIds.length
      || !['title', 'slug', 'description', 'coverId'].every(key => typeof album[key] === 'string')
      || (album.coverId !== '' && !album.photoIds.includes(album.coverId))) fail(503);
  return { album, release };
}

function b64(bytes) {
  return btoa(String.fromCharCode(...bytes)).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
}
function unb64(value) {
  if (!/^[A-Za-z0-9_-]+$/.test(value)) fail(401);
  return Uint8Array.from(atob(value.replaceAll('-', '+').replaceAll('_', '/')), char => char.charCodeAt(0));
}
async function hmacKey(secret) {
  return crypto.subtle.importKey('raw', encoder.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign', 'verify']);
}
async function session(secret, album, now) {
  const payload = b64(encoder.encode(JSON.stringify({ album: album.id, version: album.accessVersion, exp: now + SESSION_SECONDS })));
  const signature = await crypto.subtle.sign('HMAC', await hmacKey(secret), encoder.encode(payload));
  return `${payload}.${b64(new Uint8Array(signature))}`;
}
async function authorized(request, secret, album, now) {
  // Reject duplicate cookies rather than making browser/path ordering security relevant.
  const cookies = (request.headers.get('Cookie') || '').split(';').map(value => value.trim()).filter(value => value.startsWith(`${cookieName(album.id)}=`));
  if (cookies.length !== 1) fail(401);
  const token = cookies[0].slice(cookieName(album.id).length + 1);
  if (token.length > 1024) fail(401);
  try {
    const parts = token.split('.');
    if (parts.length !== 2) fail(401);
    const signature = unb64(parts[1]);
    if (signature.length !== 32 || !await crypto.subtle.verify('HMAC', await hmacKey(secret), signature, encoder.encode(parts[0]))) fail(401);
    const claims = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(unb64(parts[0])));
    if (!record(claims) || claims.album !== album.id || claims.version !== album.accessVersion
        || !Number.isSafeInteger(claims.exp) || claims.exp <= now || claims.exp > now + SESSION_SECONDS) fail(401);
  } catch { fail(401); }
}
function sessionCookie(id, token, now, logout = false) {
  const age = logout ? 0 : SESSION_SECONDS;
  return `${cookieName(id)}=${token}; Path=${albumPath(id)}; HttpOnly; Secure; SameSite=Strict; Max-Age=${age}; Expires=${new Date((now + age) * 1000).toUTCString()}`;
}

async function loginBody(request, access) {
  if (request.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/json') fail(415);
  const length = request.headers.get('Content-Length');
  if (length !== null && (!/^\d+$/.test(length) || Number(length) > BODY_LIMIT)) fail(413);
  const reader = request.body?.getReader();
  if (!reader) fail(400);
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > BODY_LIMIT) { await reader.cancel(); fail(413); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  let body;
  try { body = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)); } catch { fail(400); }
  const field = access === 'link' ? 'key' : 'password';
  if (!record(body) || typeof body[field] !== 'string') fail(400);
  if (access === 'link' && !LINK_KEY.test(body.key)) fail(400);
  return body[field]; // Exact UTF-8: deliberately no trim or Unicode normalization.
}

// Persistent fixed keys: expiry overwrites the old window instead of creating
// unlimited per-window objects. Conditional writes serialize concurrent attempts.
async function takeAttempt(bucket, key, limit, window) {
  for (let retry = 0; retry < 8; retry++) {
    const object = await bucket.get(key);
    let count = 0;
    if (object) {
      if (typeof object.etag !== 'string' || !object.etag) fail(503);
      const state = JSON.parse(await object.text());
      if (!record(state) || !Number.isSafeInteger(state.window) || state.window < 0 || !Number.isSafeInteger(state.count) || state.count < 0) fail(503);
      if (state.window > window) fail(503);
      if (state.window === window) count = state.count;
    }
    if (count >= limit) fail(429);
    const result = await bucket.put(key, JSON.stringify({ window, count: count + 1 }), {
      onlyIf: object ? { etagMatches: object.etag } : { etagDoesNotMatch: '*' },
      httpMetadata: { contentType: 'application/json', cacheControl: 'no-store' },
    });
    if (result) return;
  }
  fail(429); // Contention cannot turn into an unlimited login budget.
}
async function limitLogin(request, bucket, id, now) {
  const ip = request.headers.get('CF-Connecting-IP');
  if (!ip || ip.length > 64) fail(503);
  const digest = hex(new Uint8Array(await crypto.subtle.digest('SHA-256', encoder.encode(ip))));
  const window = Math.floor(now / 60);
  // Bound new per-IP objects even when an attacker rotates source addresses.
  await takeAttempt(bucket, `auth-attempts/${id}/global.json`, 30, window);
  await takeAttempt(bucket, `auth-attempts/${id}/ip-${digest}.json`, 5, window);
}
async function correctPassword(password, stored) {
  const [, salt, expected] = HASH.exec(stored);
  // OWASP's 16 MiB scrypt profile; native Workers implementation, not a polyfill.
  const derived = scryptSync(encoder.encode(password), unhex(salt), 32, { N: 16384, r: 8, p: 5, maxmem: 32 * 1024 * 1024 });
  return timingSafeEqual(derived, unhex(expected));
}

function catalog(album, release) {
  const photos = album.photoIds.map(id => {
    const matches = release.photos.filter(photo => record(photo) && photo.id === id);
    if (matches.length !== 1) fail(503);
    const source = matches[0];
    // Explicit whitelist: never spread the release record or trust its URLs.
    const result = { id };
    for (const key of ['alt', 'caption', 'uploadedAt', 'capturedDate']) {
      if (typeof source[key] !== 'string') fail(503);
      result[key] = source[key];
    }
    if (source.tags !== null && (!Array.isArray(source.tags) || !source.tags.every(tag => typeof tag === 'string'))) fail(503);
    result.tags = source.tags || [];
    for (const key of ['width', 'height']) {
      if (!Number.isSafeInteger(source[key]) || source[key] <= 0) fail(503);
      result[key] = source[key];
    }
    if (source.exif !== null && (!record(source.exif) || !Object.values(source.exif).every(value => typeof value === 'string'))) fail(503);
    result.exif = source.exif || {};
    result.thumbnail = `${albumPath(album.id)}media/${id}/640.jpg`;
    result.src = `${albumPath(album.id)}media/${id}/2560.jpg`;
    return result;
  });
  return { version: 1, photos, albums: [{ id: album.id, title: album.title, slug: album.slug, description: album.description,
    published: true, access: album.access, photoIds: [...album.photoIds], coverId: album.coverId }] };
}

export async function onRequest(context) {
  const { request, env } = context;
  try {
    const url = new URL(request.url);
    // Do not decode path components: encoded slashes/dots/IDs are not aliases.
    const route = /^\/photos\/private\/([0-9a-f]{32})\/(|login|logout|catalog|media\/([0-9a-f]{32})\/(640|1280|2560)\.jpg)$/.exec(url.pathname);
    if (!route) fail(404);
    if (url.protocol !== 'https:') fail(403);
    const [, id, action, photoID, size] = route;
    const read = request.method === 'GET' || request.method === 'HEAD';
    const origin = request.headers.get('Origin');
    // Shared links must open from email/other sites. Only a top-level shell
    // navigation is exempt; embedded pages and all data/auth routes stay closed.
    const navigation = read && action === '' && request.headers.get('Sec-Fetch-Mode') === 'navigate'
      && request.headers.get('Sec-Fetch-Dest') === 'document';
    if ((origin !== null && origin !== url.origin) || (request.headers.get('Sec-Fetch-Site') === 'cross-site' && !navigation)) fail(403);
    if (!(read && (action === '' || action === 'catalog' || photoID))
        && !(request.method === 'POST' && (action === 'login' || action === 'logout'))) fail(405);
    if (!read && origin !== url.origin) fail(403);
    const now = Math.floor(Date.now() / 1000);
    // Cookie removal must work after unpublication or a storage/config outage.
    // Same-origin validation above is sufficient; no session or R2 is needed.
    if (action === 'logout') {
      return json(request, { ok: true }, { 'Set-Cookie': sessionCookie(id, '', now, true) });
    }
    if (!env.PUBLIC_PHOTOS?.get || !env.PROTECTED_PHOTOS?.get) fail(503);
    const { album, release } = await currentAlbum(env, id);
    if (typeof env.ALBUM_SESSION_SECRET !== 'string' || encoder.encode(env.ALBUM_SESSION_SECRET).length < 32
        || (album.access === 'password' && !env.PROTECTED_PHOTOS?.put)) fail(503);
    if (action === '') {
      if (!env.ASSETS?.fetch) fail(503);
      const shell = await env.ASSETS.fetch(new Request(new URL('/photos/', url), { method: 'GET' }));
      if (shell.status !== 200 || !shell.headers.get('Content-Type')?.includes('text/html')) fail(503);
      let html = await shell.text();
      if (!/<head\b[^>]*>/i.test(html) || !/<body\b[^>]*>/i.test(html)) fail(503);
      html = html.replace(/<base\b[^>]*>/gi, '').replace(/<title\b[^>]*>[\s\S]*?<\/title>/gi, '')
        .replace(/<head\b[^>]*>/i, '$&<base href="/photos/"><title>Private album · Hugo’s Blog</title>')
        .replace(/<body\b[^>]*>/i, tag => tag.replace(/\sdata-private-access(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+))?/gi, '')
          .replace(/>$/, ` data-private-access="${album.access}">`));
      return response(request, 200, html, { 'Content-Type': 'text/html; charset=utf-8' });
    }
    if (action === 'login') {
      const credential = await loginBody(request, album.access);
      if (album.access === 'link') {
        const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', encoder.encode(credential)));
        if (!timingSafeEqual(digest, unhex(album.linkHash))) fail(401);
      } else {
        await limitLogin(request, env.PROTECTED_PHOTOS, id, now);
        if (!await correctPassword(credential, album.passwordHash)) fail(401);
      }
      return json(request, { ok: true }, { 'Set-Cookie': sessionCookie(id, await session(env.ALBUM_SESSION_SECRET, album, now), now) });
    }
    await authorized(request, env.ALBUM_SESSION_SECRET, album, now);
    if (action === 'catalog') return json(request, catalog(album, release));
    if (!album.photoIds.includes(photoID)) fail(404);
    const object = await env.PROTECTED_PHOTOS.get(`photos/${photoID}/${size}.jpg`);
    if (!object) fail(503); // A committed release with missing bytes is unavailable.
    if (request.method === 'HEAD') await object.body?.cancel();
    return response(request, 200, object.body, { 'Content-Type': 'image/jpeg' });
  } catch (error) {
    const status = error instanceof Failure ? error.status : 503;
    return response(request, status, status === 401 ? 'Authentication required.' : status === 404 ? 'Not found.' : 'Request unavailable.', {
      'Content-Type': 'text/plain; charset=utf-8', ...(status === 429 ? { 'Retry-After': '60' } : {}),
    });
  }
}
