import test from 'node:test';
import assert from 'node:assert/strict';
import { createHmac, createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { onRequest } from './handler.mjs';

const A = 'a'.repeat(32), B = 'b'.repeat(32), P = 'c'.repeat(32), Q = 'd'.repeat(32), R = 'e'.repeat(32);
const VERSION = '1'.repeat(32), RELEASE = '2'.repeat(32), NEXT = '3'.repeat(32);
const ORIGIN = 'https://blog.example';
const SECRET = '0123456789abcdef0123456789abcdef';
const SALT = '000102030405060708090a0b0c0d0e0f';
// Frozen independently with Python hashlib.scrypt, not derived by this handler.
const DERIVED = '36afa0b630c8b697b9341304b38a410d3b43b196456e60e0bc94feff15bff486';
const UNICODE_DERIVED = 'fa3566e46bd7acc865dca9576355e8ee804badbb44407c21e5cbc40da1d3fb95';
const PASSWORD_HASH = `scrypt$16384$8$5$${SALT}$${DERIVED}`;
const LINK_TOKEN = '000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f';
// Frozen independently with printf '%s' <token> | sha256sum (UTF-8 hex text, not decoded bytes).
const LINK_HASH = '6c86c6aac5fb24bcf5d9939cb7d7d5645ce39418f449e03b262dd4fa14b4b92b';
const base = id => `/photos/private/${id}/`;
const name = id => `__Secure-photo_album_${id}`;

// Snapshot reads, atomic conditional writes, streams and monotonically changing
// ETags model R2's strong consistency. No ordinary unconditional auth writes pass.
class R2 {
  objects = new Map();
  reads = [];
  heads = [];
  ranges = [];
  writes = [];
  revision = 0;
  getFailure = null;
  putFailure = false;
  contention = false;
  seed(key, value) {
    const bytes = typeof value === 'string' ? value : JSON.stringify(value);
    this.objects.set(key, { bytes, etag: `r${++this.revision}` });
  }
  async get(key, options) {
    this.reads.push(key);
    if (key.endsWith('/video.mp4') && options) {
      assert.deepEqual(Object.keys(options), ['range']);
      assert.deepEqual(Object.keys(options.range), ['offset', 'length']);
      this.ranges.push(options.range);
    } else assert.equal(options, undefined, 'JPEG and publication reads remain unconditional');
    if (this.getFailure?.(key)) throw new Error('sensitive storage diagnostics');
    const snapshot = this.objects.get(key);
    await Promise.resolve();
    if (!snapshot) return null;
    const bytes = Buffer.from(snapshot.bytes);
    const slice = options?.range ? bytes.subarray(options.range.offset, options.range.offset + options.range.length) : bytes;
    return { etag: snapshot.etag, httpEtag: `"${snapshot.etag}"`, size: bytes.length,
      body: new Response(slice).body,
      text: async () => snapshot.bytes,
      httpMetadata: { cacheControl: 'public, max-age=99999', contentType: 'text/secret' } };
  }
  async head(key) {
    this.heads.push(key);
    if (this.getFailure?.(key)) throw new Error('sensitive storage diagnostics');
    const snapshot = this.objects.get(key);
    return snapshot ? { size: Buffer.byteLength(snapshot.bytes), etag: snapshot.etag } : null;
  }
  async put(key, bytes, options) {
    this.writes.push({ key, bytes, options });
    assert.ok(key.startsWith('auth-attempts/'));
    assert.ok(options.onlyIf, 'limiter writes must be conditional');
    if (this.putFailure) throw new Error('sensitive write diagnostics');
    if (this.contention) return null;
    const old = this.objects.get(key);
    if ('etagMatches' in options.onlyIf && old?.etag !== options.onlyIf.etagMatches) return null;
    if (options.onlyIf.etagDoesNotMatch === '*' && old) return null;
    this.seed(key, bytes);
    return { etag: this.objects.get(key).etag };
  }
}

function photo(id) {
  return { id, alt: `secret alt ${id}`, caption: 'secret caption', tags: ['secret tag'], uploadedAt: '2026-01-01T00:00:00Z',
    capturedDate: '2025-01-01', width: 1500, height: 1000, exif: { Camera: 'secret camera' },
    filename: 'must-not-return-original.jpg', adminToken: 'must-not-return-token',
    thumbnail: 'https://wrong.example/original', src: 'https://wrong.example/original' };
}
function album(id, photoIds) {
  return { id, title: `secret title ${id}`, slug: 'secret-slug', description: 'secret description', published: true,
    photoIds, coverId: photoIds[0] || '', access: 'password', passwordHash: PASSWORD_HASH, accessVersion: VERSION,
    adminToken: 'must-not-return-token' };
}
function fixture() {
  const publicBucket = new R2(), privateBucket = new R2();
  const release = { version: 1, photos: [photo(P), photo(Q), photo(R)], albums: [album(A, [Q, P]), album(B, [P, R])] };
  publicBucket.seed('index.json', { version: 1, protectedRelease: RELEASE, photos: [{ id: P }] });
  privateBucket.seed(`releases/${RELEASE}.json`, release);
  for (const id of [P, Q, R]) for (const size of [640, 1280, 2560]) privateBucket.seed(`photos/${id}/${size}.jpg`, `JPEG:${id}:${size}`);
  const assetRequests = [];
  const env = { PUBLIC_PHOTOS: publicBucket, PROTECTED_PHOTOS: privateBucket, ALBUM_SESSION_SECRET: SECRET,
    ASSETS: { async fetch(request) {
      assetRequests.push(request);
      return new Response('<!doctype html><html><head><title>Photographs</title><base href="/wrong/"></head><body><script src="gallery.js"></script></body></html>',
        { headers: { 'Content-Type': 'text/html', ETag: 'shell', 'Access-Control-Allow-Origin': '*', 'Cache-Control': 'public' } });
    } } };
  const call = (action = 'catalog', init = {}, id = A) => onRequest({ env, request: new Request(`${ORIGIN}${base(id)}${action}`, init) });
  const login = (password = 'password', init = {}, id = A) => call('login', {
    method: 'POST', body: JSON.stringify({ password }), ...init,
    headers: { Origin: ORIGIN, 'Content-Type': 'application/json', 'CF-Connecting-IP': '192.0.2.1', ...init.headers },
  }, id);
  const save = (key = RELEASE) => privateBucket.seed(`releases/${key}.json`, release);
  return { env, publicBucket, privateBucket, release, call, login, save, assetRequests };
}
function linkFixture() {
  const f = fixture();
  f.release.albums[0].access = 'link';
  f.release.albums[0].linkHash = LINK_HASH;
  delete f.release.albums[0].passwordHash;
  f.save();
  f.exchange = (key = LINK_TOKEN, init = {}, id = A) => f.call('login', {
    method: 'POST', body: JSON.stringify({ key }), ...init,
    headers: { Origin: ORIGIN, 'Content-Type': 'application/json', ...init.headers },
  }, id);
  return f;
}
function signedCookie(id = A, claims = {}, secret = SECRET) {
  const payload = Buffer.from(JSON.stringify({ album: id, version: VERSION, exp: Math.floor(Date.now() / 1000) + 3600, ...claims })).toString('base64url');
  return `${name(id)}=${payload}.${createHmac('sha256', secret).update(payload).digest('base64url')}`;
}
function privateHeaders(result, video = false) {
  assert.match(result.headers.get('Cache-Control'), /private.*no-store/);
  assert.equal(result.headers.get('CDN-Cache-Control'), 'no-store');
  assert.equal(result.headers.get('Cloudflare-CDN-Cache-Control'), 'no-store');
  assert.equal(result.headers.get('Referrer-Policy'), 'no-referrer');
  assert.equal(result.headers.get('X-Robots-Tag'), 'noindex, nofollow, noarchive');
  assert.equal(result.headers.get('X-Content-Type-Options'), 'nosniff');
  for (const header of ['ETag', 'Last-Modified', ...(video !== true ? ['Accept-Ranges', 'Content-Range'] : []), 'Access-Control-Allow-Origin', 'Access-Control-Allow-Credentials']) {
    assert.equal(result.headers.get(header), null, header);
  }
}
async function status(result, expected) {
  assert.equal(result.status, expected);
  privateHeaders(result);
  return result;
}

test('independent scrypt and HMAC fixed vectors; correct/wrong login and secure cookie', async () => {
  const f = fixture();
  const wrong = await status(await f.login('wrong'), 401);
  assert.equal(wrong.headers.get('Set-Cookie'), null);
  assert.equal(await wrong.text(), 'Authentication required.');
  const success = await status(await f.login(), 200);
  assert.deepEqual(await success.json(), { ok: true });
  const set = success.headers.get('Set-Cookie');
  for (const flag of ['HttpOnly', 'Secure', 'SameSite=Strict', 'Max-Age=43200', `Path=${base(A)}`, 'Expires=']) assert.ok(set.includes(flag));
  assert.ok(!set.includes('Domain='));
  const cookie = set.split(';')[0];
  const [payload, signature] = cookie.slice(name(A).length + 1).split('.');
  assert.equal(signature, createHmac('sha256', SECRET).update(payload).digest('base64url'));
  const claims = JSON.parse(Buffer.from(payload, 'base64url'));
  assert.equal(claims.album, A);
  assert.equal(claims.version, VERSION);
  assert.ok(claims.exp > Date.now() / 1000);
  await status(await f.call('catalog', { headers: { Cookie: cookie } }), 200);
  const fixed = signedCookie(A, { exp: 2000000000 }).slice(name(A).length + 1);
  assert.equal(fixed, 'eyJhbGJ1bSI6ImFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhIiwidmVyc2lvbiI6IjExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExIiwiZXhwIjoyMDAwMDAwMDAwfQ.xExCrtKn8LDir7fiZzwR6ErpSYaXz1ozbM22FpKf4zY');
});

test('handler verifies the independent fixed HMAC vector and rejects its signature mutation', async t => {
  t.mock.method(Date, 'now', () => 1999999999000);
  const f = fixture();
  const token = 'eyJhbGJ1bSI6ImFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhIiwidmVyc2lvbiI6IjExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExIiwiZXhwIjoyMDAwMDAwMDAwfQ.xExCrtKn8LDir7fiZzwR6ErpSYaXz1ozbM22FpKf4zY';
  await status(await f.call('catalog', { headers: { Cookie: `${name(A)}=${token}` } }), 200);
  await status(await f.call('catalog', { headers: { Cookie: `${name(A)}=${token.replace('.x', '.z')}` } }), 401);
});

test('password UTF-8 whitespace is exact, not trimmed or normalized', async () => {
  const f = fixture();
  f.release.albums[0].passwordHash = `scrypt$16384$8$5$${SALT}$${UNICODE_DERIVED}`;
  f.save();
  await status(await f.login('päss🔐'), 401);
  await status(await f.login('  pa\u0308ss🔐  '), 401);
  await status(await f.login('  päss🔐  '), 200);
});

test('unauthenticated shell is generic, uses base href and strips inherited caching/CORS', async () => {
  const f = fixture();
  const result = await status(await f.call(''), 200);
  const html = await result.text();
  assert.match(html, /<head><base href="\/photos\/">/);
  assert.match(html, /<title>Private album · Hugo’s Blog<\/title>/);
  assert.match(html, /<body data-private-access="password">/);
  assert.doesNotMatch(html, /secret|passwordHash|accessVersion|wrong/);
  assert.equal((html.match(/<base /g) || []).length, 1);
  assert.equal(f.assetRequests[0].url, `${ORIGIN}/photos/`);
  assert.equal([...f.assetRequests[0].headers].length, 0);
  const head = await status(await f.call('', { method: 'HEAD', headers: { 'If-None-Match': '*' } }), 200);
  assert.equal(await head.text(), '');
  await status(await f.call('', {}, 'f'.repeat(32)), 404);
});

test('catalog is curated, whitelisted and album scoped, including shared public photos', async () => {
  const f = fixture();
  const blurhash = 'LEHV6nWB2yk8pyo0adR*.7kCMdnj';
  f.release.photos[0].blurhash = blurhash;
  f.release.photos[0].location = 'New York, NY';
  f.release.photos[2].blurhash = { private: 'not visitor metadata' };
  f.save();
  const a = await (await status(await f.call('catalog', { headers: { Cookie: signedCookie() } }), 200)).json();
  assert.deepEqual(a.photos.map(photo => photo.id), [Q, P]);
  assert.equal(a.albums.length, 1);
  assert.deepEqual(Object.keys(a.albums[0]).sort(), ['access', 'coverId', 'description', 'id', 'photoIds', 'published', 'slug', 'title']);
  assert.equal(a.albums[0].access, 'password');
  assert.deepEqual(Object.keys(a.photos[0]).sort(), ['alt', 'caption', 'capturedDate', 'exif', 'height', 'id', 'src', 'tags', 'thumbnail', 'uploadedAt', 'width']);
  assert.equal(a.photos[1].thumbnail, `${base(A)}media/${P}/640.jpg`);
  assert.equal(a.photos[1].blurhash, blurhash);
  assert.equal(a.photos[1].location, 'New York, NY');
  const b = await (await status(await f.call('catalog', { headers: { Cookie: signedCookie(B) } }, B), 200)).json();
  assert.deepEqual(b.photos.map(photo => photo.id), [P, R]);
  assert.equal(b.photos[0].src, `${base(B)}media/${P}/2560.jpg`);
  assert.equal(b.photos[0].blurhash, blurhash);
  assert.equal(b.photos[0].location, 'New York, NY');
  assert.equal(b.photos[1].location, undefined);
  assert.equal(b.photos[1].blurhash, undefined);
  assert.doesNotMatch(JSON.stringify(a), /passwordHash|accessVersion|must-not-return|wrong\.example/);
  for (const id of [A, B]) {
    const image = await status(await f.call(`media/${P}/640.jpg`, { headers: { Cookie: signedCookie(id) } }, id), 200);
    assert.equal(await image.text(), `JPEG:${P}:640`);
  }
  const before = f.privateBucket.reads.length;
  await status(await f.call(`media/${R}/640.jpg`, { headers: { Cookie: signedCookie() } }), 404);
  assert.ok(!f.privateBucket.reads.slice(before).some(key => key.startsWith('photos/')));
});

test('link fixed SHA-256 vector exchanges for a session; shell/catalog never leak credentials', async () => {
  const f = linkFixture();
  assert.equal(createHash('sha256').update(LINK_TOKEN).digest('hex'), LINK_HASH);
  assert.ok(!JSON.stringify(f.release).includes(LINK_TOKEN), 'delivery snapshot has no plaintext key');
  const shell = await status(await f.call(''), 200);
  const html = await shell.text();
  assert.match(html, /<body data-private-access="link">/);
  assert.doesNotMatch(html, /secret|passwordHash|linkHash|accessVersion/);
  assert.ok(!html.includes(LINK_TOKEN) && !html.includes(LINK_HASH));
  const success = await status(await f.exchange(), 200);
  assert.deepEqual(await success.json(), { ok: true });
  const set = success.headers.get('Set-Cookie');
  for (const flag of ['HttpOnly', 'Secure', 'SameSite=Strict', 'Max-Age=43200', `Path=${base(A)}`]) assert.ok(set.includes(flag));
  const Cookie = set.split(';')[0];
  assert.ok(!set.includes(LINK_TOKEN) && !set.includes(LINK_HASH));
  const data = await (await status(await f.call('catalog', { headers: { Cookie } }), 200)).json();
  assert.deepEqual(data.albums, [{ id: A, title: `secret title ${A}`, slug: 'secret-slug', description: 'secret description',
    published: true, access: 'link', photoIds: [Q, P], coverId: Q }]);
  assert.deepEqual(data.photos.map(photo => photo.id), [Q, P]);
  assert.doesNotMatch(JSON.stringify(data), /passwordHash|linkHash|accessVersion|must-not-return|wrong\.example/);
  assert.ok(!JSON.stringify(data).includes(LINK_TOKEN) && !JSON.stringify(data).includes(LINK_HASH));
  assert.ok(!JSON.stringify(data).includes(B));
  for (const photo of data.photos) {
    assert.equal(photo.thumbnail, `${base(A)}media/${photo.id}/640.jpg`);
    assert.equal(photo.src, `${base(A)}media/${photo.id}/2560.jpg`);
    for (const size of [640, 1280, 2560]) {
      const image = await status(await f.call(`media/${photo.id}/${size}.jpg`, { headers: { Cookie } }), 200);
      assert.equal(image.headers.get('Content-Type'), 'image/jpeg');
      assert.equal(await image.text(), `JPEG:${photo.id}:${size}`);
    }
  }
  assert.equal(f.privateBucket.writes.length, 0);
});

test('link exchange validates keys and never needs IP, KDF or limiter storage writes', async () => {
  const f = linkFixture();
  f.privateBucket.put = undefined;
  f.privateBucket.putFailure = true;
  f.privateBucket.getFailure = key => key.startsWith('auth-attempts/');
  for (const key of ['f'.repeat(64), LINK_HASH]) {
    const result = await status(await f.exchange(key), 401);
    assert.equal(result.headers.get('Set-Cookie'), null);
    assert.equal(await result.text(), 'Authentication required.');
  }
  for (const key of [undefined, null, 1, '', 'a'.repeat(63), 'a'.repeat(65), LINK_TOKEN.toUpperCase(), ` ${LINK_TOKEN}`]) {
    await status(await f.exchange('ignored', { body: JSON.stringify({ key }) }), 400);
  }
  for (const [body, expected] of [['{}', 400], ['{"password":"password"}', 400], ['{', 400], ['null', 400], ['x'.repeat(4097), 413]]) {
    await status(await f.exchange('ignored', { body }), expected);
  }
  await status(await f.exchange(LINK_TOKEN, { headers: { 'Content-Type': 'text/plain' } }), 415);
  await status(await f.exchange(), 200);
  await status(await f.exchange(), 200);
  assert.ok(!f.privateBucket.reads.some(key => key.startsWith('auth-attempts/')));
  assert.equal(f.privateBucket.writes.length, 0);
});

test('link album IDs are not capabilities; session media stays member-only and album-scoped', async () => {
  const f = linkFixture();
  for (const id of [A.toUpperCase(), A.slice(1), `${A}a`, `%61${A.slice(1)}`, 'secret-slug', 'f'.repeat(32)]) {
    for (const action of ['', 'catalog', `media/${P}/640.jpg`]) await status(await f.call(action, {}, id), 404);
  }
  for (const path of [`media/${P}/original.jpg`, `media/${P}/641.jpg`, `media/%63${P.slice(1)}/640.jpg`,
    `releases/${RELEASE}.json`, 'index.json', 'catalog.json']) await status(await f.call(path), 404);
  const before = f.privateBucket.reads.length;
  await status(await f.call(`media/${R}/640.jpg`, { headers: { Cookie: signedCookie() } }), 404);
  assert.ok(!f.privateBucket.reads.slice(before).some(key => key.startsWith('photos/')));
  for (const Cookie of ['', signedCookie(B), `${name(A)}=invalid`, `${signedCookie()}; ${signedCookie()}`]) {
    for (const action of ['catalog', `media/${P}/640.jpg`]) await status(await f.call(action, { headers: { Cookie } }), 401);
  }
  f.release.albums[1].access = 'link';
  f.release.albums[1].linkHash = createHash('sha256').update('f'.repeat(64)).digest('hex');
  delete f.release.albums[1].passwordHash; f.save();
  await status(await f.exchange(LINK_TOKEN, {}, B), 401);
  await status(await f.exchange('f'.repeat(64)), 401);
  await status(await f.exchange('f'.repeat(64), {}, B), 200);
  await status(await f.call('catalog', {}, B), 401);
  await status(await f.call(`media/${P}/640.jpg`, { headers: { Cookie: signedCookie() } }, B), 401);
});

test('link requests retain cross-site, conditional, range and HEAD controls', async () => {
  const f = linkFixture();
  const conditions = { 'If-None-Match': '*', 'If-Modified-Since': 'Wed, 01 Jan 2099 00:00:00 GMT', Range: 'bytes=0-0', 'If-Range': 'anything' };
  for (const action of ['', 'catalog', `media/${P}/640.jpg`]) {
    for (const method of ['GET', 'HEAD']) {
      if (action !== '') await status(await f.call(action, { method, headers: conditions }), 401);
      const result = await status(await f.call(action, { method, headers: { ...conditions, Cookie: signedCookie() } }), 200);
      const body = await result.text();
      if (method === 'HEAD') assert.equal(body, '');
      else assert.ok(body.length > 1);
    }
    for (const headers of [{ Origin: 'https://evil.example' }, { 'Sec-Fetch-Site': 'cross-site' }]) {
      await status(await f.call(action, { headers }), 403);
    }
  }
});

test('shared links open from other sites, without allowing cross-site embedding or API reads', async () => {
  for (const create of [fixture, linkFixture]) {
    const f = create();
    const navigation = { 'Sec-Fetch-Site': 'cross-site', 'Sec-Fetch-Mode': 'navigate', 'Sec-Fetch-Dest': 'document' };
    const shell = await status(await f.call('', { headers: navigation }), 200);
    assert.doesNotMatch(await shell.text(), /secret title|secret alt/);
    for (const action of ['catalog', `media/${P}/640.jpg`]) {
      await status(await f.call(action, { headers: navigation }), 403);
    }
    for (const headers of [
      { ...navigation, 'Sec-Fetch-Dest': 'iframe' },
      { ...navigation, 'Sec-Fetch-Mode': 'cors' },
      { ...navigation, Origin: 'https://evil.example' },
    ]) await status(await f.call('', { headers }), 403);
    await status(await f.login('password', { headers: navigation }), 403);
    assert.equal(f.privateBucket.writes.length, 0);
  }
});

test('public/link/password transitions and unpublish follow only the current release pointer', async () => {
  const f = fixture();
  const passwordAlbum = structuredClone(f.release.albums[0]);
  f.release.albums.shift(); f.save(); // Public-only album has no protected projection.
  f.release.albums.unshift({ ...passwordAlbum, access: 'link', linkHash: LINK_HASH, accessVersion: NEXT });
  delete f.release.albums[0].passwordHash;
  f.save(NEXT);
  for (const action of ['', 'catalog', `media/${P}/640.jpg`]) {
    await status(await f.call(`${action}?protectedRelease=${NEXT}`), 404);
  }
  f.publicBucket.seed('index.json', { protectedRelease: NEXT });
  await status(await f.call(''), 200);
  for (const action of ['catalog', `media/${P}/640.jpg`]) await status(await f.call(action), 401);
  const exchange = await status(await f.login('ignored', { body: JSON.stringify({ key: LINK_TOKEN }) }), 200);
  const linkCookie = exchange.headers.get('Set-Cookie').split(';')[0];
  f.release.albums[0] = passwordAlbum;
  const passwordRelease = '4'.repeat(32);
  f.save(passwordRelease);
  for (const action of ['catalog', `media/${P}/640.jpg`]) await status(await f.call(action, { headers: { Cookie: linkCookie } }), 200);
  f.publicBucket.seed('index.json', { protectedRelease: passwordRelease });
  for (const action of ['catalog', `media/${P}/640.jpg`]) {
    await status(await f.call(action), 401);
    await status(await f.call(action, { headers: { Cookie: linkCookie } }), 401);
    await status(await f.call(`${action}?protectedRelease=${NEXT}&access=link`), 401);
  }
  const login = await status(await f.login(), 200);
  const Cookie = login.headers.get('Set-Cookie').split(';')[0];
  for (const action of ['catalog', `media/${P}/640.jpg`]) await status(await f.call(action, { headers: { Cookie } }), 200);
  f.release.albums[0].published = false;
  f.save(passwordRelease);
  for (const action of ['', 'catalog', `media/${P}/640.jpg`]) await status(await f.call(action, { headers: { Cookie } }), 404);
  f.publicBucket.seed('index.json', {});
  for (const action of ['', 'catalog', `media/${P}/640.jpg`]) await status(await f.call(`${action}?protectedRelease=${NEXT}`), 404);
});

test('link requires session secret, valid storage and metadata; unlisted/unknown access fails closed', async () => {
  const mutations = [
    f => { delete f.env.PUBLIC_PHOTOS; },
    f => { delete f.env.PROTECTED_PHOTOS; },
    f => { delete f.env.ALBUM_SESSION_SECRET; },
    f => { f.env.ALBUM_SESSION_SECRET = 'short'; },
    f => { f.publicBucket.getFailure = () => true; },
    f => { f.privateBucket.objects.delete(`releases/${RELEASE}.json`); },
    f => { delete f.release.albums[0].linkHash; f.save(); },
    f => { f.release.albums[0].linkHash = LINK_HASH.toUpperCase(); f.save(); },
    f => { f.release.albums[0].linkHash = 'a'.repeat(63); f.save(); },
    f => { delete f.release.albums[0].accessVersion; f.save(); },
    f => { f.release.albums[0].accessVersion = 'a'.repeat(64); f.save(); },
    f => { f.release.albums[0].photoIds.push(Q); f.save(); },
    f => { f.release.albums[0].coverId = R; f.save(); },
    f => { f.release.photos = []; f.save(); },
    f => { f.release.photos[0].location = { latitude: 1 }; f.save(); },
  ];
  for (const mutate of mutations) {
    const f = linkFixture(); mutate(f);
    const result = await status(await f.call('catalog', { headers: { Cookie: signedCookie() } }), 503);
    assert.equal(await result.text(), 'Request unavailable.');
  }
  for (const access of [undefined, null, 'public', 'unknown', 'unlisted', 'UNLISTED']) {
    const f = linkFixture();
    f.release.albums[0].access = access; f.save();
    for (const action of ['', 'catalog', `media/${P}/640.jpg`]) await status(await f.call(action), 404);
    await status(await f.login(), 404);
    assert.equal(f.privateBucket.writes.length, 0);
  }
});

test('link fragments are not server credentials and query keys never authorize or exchange', async () => {
  const f = linkFixture();
  for (const suffix of [`#key=${LINK_TOKEN}`, `?key=${LINK_TOKEN}`, `?key=${LINK_TOKEN}&access=link`]) {
    const shell = await status(await f.call(suffix), 200);
    assert.equal(shell.headers.get('Set-Cookie'), null);
    assert.ok(!(await shell.text()).includes(LINK_TOKEN));
    for (const action of ['catalog', `media/${P}/640.jpg`]) await status(await f.call(`${action}${suffix}`), 401);
    await status(await f.call(`login${suffix}`, { method: 'POST', headers: { Origin: ORIGIN, 'Content-Type': 'application/json' }, body: '{}' }), 400);
  }
  assert.equal(f.privateBucket.writes.length, 0);
});

test('real HTTP link navigation never sends the fragment key to the handler', async () => {
  const f = linkFixture(), received = [];
  // Bridge HTTP test transport to the HTTPS request Pages supplies in production.
  const server = createServer(async (request, reply) => {
    received.push(request.url);
    const result = await onRequest({ env: f.env, request: new Request(`${ORIGIN}${request.url}`) });
    reply.writeHead(result.status, Object.fromEntries(result.headers));
    reply.end(await result.text());
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    const origin = `http://127.0.0.1:${server.address().port}`;
    for (const [action, expected] of [['', 200], ['catalog', 401], [`media/${P}/640.jpg`, 401]]) {
      const result = await status(await fetch(`${origin}${base(A)}${action}#key=${LINK_TOKEN}`), expected);
      assert.ok(!(await result.text()).includes(LINK_TOKEN));
      assert.equal(received.at(-1), `${base(A)}${action}`);
    }
    assert.ok(received.every(path => !path.includes('#') && !path.includes(LINK_TOKEN)));
    assert.equal(f.privateBucket.writes.length, 0);
  } finally {
    server.closeAllConnections();
    await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  }
});

test('link draft pointer isolation, hash/version rotation and unpublish/republish revoke keys and cookies', async () => {
  const f = linkFixture();
  const login = await status(await f.exchange(), 200);
  const Cookie = login.headers.get('Set-Cookie').split(';')[0];
  const nextKey = 'f'.repeat(64);
  f.release.albums[0].linkHash = createHash('sha256').update(nextKey).digest('hex');
  f.release.albums[0].accessVersion = NEXT;
  f.save(NEXT); // Draft cannot change delivery until the public pointer commits.
  await status(await f.exchange(nextKey), 401);
  await status(await f.exchange(), 200);
  await status(await f.call(`catalog?protectedRelease=${NEXT}`, { headers: { Cookie } }), 200);
  f.publicBucket.seed('index.json', { protectedRelease: NEXT });
  await status(await f.exchange(), 401);
  for (const action of ['catalog', `media/${P}/640.jpg`]) {
    await status(await f.call(`${action}?protectedRelease=${RELEASE}`, { headers: { Cookie } }), 401);
  }
  const rotated = await status(await f.exchange(nextKey), 200);
  const newCookie = rotated.headers.get('Set-Cookie').split(';')[0];
  await status(await f.call('catalog', { headers: { Cookie: newCookie } }), 200);
  f.release.albums[0].published = false; f.save(NEXT);
  for (const action of ['', 'catalog', `media/${P}/640.jpg`]) await status(await f.call(action, { headers: { Cookie: newCookie } }), 404);
  await status(await f.exchange(nextKey), 404);
  f.publicBucket.seed('index.json', {});
  await status(await f.call(`catalog?protectedRelease=${NEXT}`, { headers: { Cookie: newCookie } }), 404);
  // Publisher rotates both fields when re-publishing; the handler follows that live version.
  f.release.albums[0].published = true;
  f.release.albums[0].accessVersion = '4'.repeat(32);
  f.release.albums[0].linkHash = LINK_HASH; f.save('4'.repeat(32));
  f.publicBucket.seed('index.json', { protectedRelease: '4'.repeat(32) });
  await status(await f.exchange(nextKey), 401);
  await status(await f.call('catalog', { headers: { Cookie: newCookie } }), 401);
  await status(await f.call('catalog', { headers: { Cookie } }), 401);
  await status(await f.exchange(), 200);
  assert.equal(f.privateBucket.writes.length, 0);
});

test('missing/forged/expired/overlong/duplicate/cross-album cookies fail closed', async () => {
  const f = fixture();
  const now = Math.floor(Date.now() / 1000);
  const cookies = ['', `${name(A)}=forged`, signedCookie(A, {}, 'wrong secret'), signedCookie(A, { exp: now - 1 }),
    signedCookie(A, { exp: now }), signedCookie(A, { exp: now + 999999 }), signedCookie(A, { exp: 'future' }),
    signedCookie(A, { version: NEXT }), signedCookie(B), signedCookie(A, { album: B }),
    `${name(A)}=${'x'.repeat(1100)}`, `${signedCookie()}; ${signedCookie()}`];
  for (const Cookie of cookies) for (const action of ['catalog', `media/${P}/640.jpg`]) {
    const result = await status(await f.call(action, { headers: { Cookie } }), 401);
    assert.doesNotMatch(await result.text(), /secret|camera|caption|JPEG|passwordHash/);
  }
  // Moving a genuine B token into A's cookie name must not change its claims.
  await status(await f.call('catalog', { headers: { Cookie: signedCookie(B).replace(name(B), name(A)) } }), 401);
  assert.ok(!f.privateBucket.reads.some(key => key.startsWith('photos/')));
});

test('current publication pointer enforces password rotation, removal and unpublish on every request', async () => {
  const f = fixture(), Cookie = signedCookie();
  await status(await f.call('catalog', { headers: { Cookie } }), 200);
  f.release.albums[0].accessVersion = NEXT;
  f.save(NEXT);
  f.publicBucket.seed('index.json', { protectedRelease: NEXT });
  for (const action of ['catalog', `media/${P}/640.jpg`]) {
    await status(await f.call(action, { headers: { Cookie } }), 401);
    await status(await f.call(`${action}?release=${RELEASE}&protectedRelease=${RELEASE}`, { headers: { Cookie } }), 401);
  }
  assert.equal(f.publicBucket.reads.filter(key => key === 'index.json').length, 5);
  f.release.albums[0].published = false;
  f.save(NEXT);
  await status(await f.call('catalog', { headers: { Cookie } }), 404);
  f.release.albums.shift(); f.save(NEXT);
  await status(await f.call(''), 404);
  await status(await f.login(), 404);
  f.publicBucket.seed('index.json', {});
  await status(await f.call(`media/${P}/640.jpg`, { headers: { Cookie } }), 404);
});

test('conditional/range/HEAD never bypass authorization or turn private content into 304/206', async () => {
  const f = fixture();
  const conditions = { 'If-None-Match': '*', 'If-Modified-Since': 'Wed, 01 Jan 2099 00:00:00 GMT', Range: 'bytes=0-0', 'If-Range': 'anything' };
  for (const method of ['GET', 'HEAD']) for (const action of ['catalog', `media/${P}/640.jpg`]) {
    const denied = await status(await f.call(action, { method, headers: conditions }), 401);
    const body = await denied.text();
    assert.equal(body, method === 'HEAD' ? '' : 'Authentication required.');
    const result = await status(await f.call(action, { method, headers: { ...conditions, Cookie: signedCookie() } }), 200);
    const authorizedBody = await result.text();
    if (method === 'HEAD') assert.equal(authorizedBody, '');
    else assert.ok(authorizedBody.length > 1);
  }
  for (const size of [640, 1280, 2560]) {
    const result = await status(await f.call(`media/${P}/${size}.jpg`, { headers: { Cookie: signedCookie() } }), 200);
    assert.equal(result.headers.get('Content-Type'), 'image/jpeg');
  }
});

test('strict routes and methods do not allow arbitrary bucket paths', async () => {
  const f = fixture();
  for (const path of [`media/${P}/original.jpg`, `media/${P}/641.jpg`, `media/${P}/0640.jpg`, `media/${P}/640.jpg/`,
    `media/${P.toUpperCase()}/640.jpg`, `media/%63${P.slice(1)}/640.jpg`, `media/${P}%2f640.jpg`,
    'catalog.json', 'index.json', `releases/${RELEASE}.json`, 'login/', '%63atalog', 'media/../../catalog.json']) {
    await status(await f.call(path, { headers: { Cookie: signedCookie() } }), 404);
  }
  for (const [action, method] of [['login', 'GET'], ['login', 'HEAD'], ['catalog', 'POST'], ['', 'OPTIONS'], ['logout', 'GET'], ['logout', 'DELETE'], ['catalog', 'PUT']]) {
    await status(await f.call(action, { method }), 405);
  }
  assert.equal(f.privateBucket.reads.length, 0);
});

test('same-Origin writes only; CORS and cross-site requests denied', async () => {
  const f = fixture();
  for (const Origin of ['', 'null', 'https://evil.example', `${ORIGIN}.evil.example`]) {
    await status(await f.login('password', { headers: { Origin } }), 403);
  }
  await status(await f.call('login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }), 403);
  await status(await f.call('logout', { method: 'POST' }), 403);
  await status(await f.call('catalog', { headers: { Origin: 'https://evil.example', Cookie: signedCookie() } }), 403);
  await status(await f.call(`media/${P}/640.jpg`, { headers: { 'Sec-Fetch-Site': 'cross-site', Cookie: signedCookie() } }), 403);
  assert.equal(f.privateBucket.writes.length, 0);
});

test('bounded JSON bodies including streamed unknown length, invalid UTF-8 and wrong types', async () => {
  const f = fixture();
  for (const [body, expected] of [['{', 400], ['null', 400], ['{"password":1}', 400], ['{}', 400], ['x'.repeat(4097), 413]]) {
    await status(await f.login('ignored', { body }), expected);
  }
  await status(await f.login('ignored', { headers: { 'Content-Type': 'text/plain' } }), 415);
  await status(await f.login('ignored', { headers: { 'Content-Length': '4097' } }), 413);
  const stream = new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(4097)); controller.close(); } });
  await status(await f.login('ignored', { body: stream, duplex: 'half' }), 413);
  await status(await f.login('ignored', { body: Uint8Array.from([0xff, 0xfe]) }), 400);
  assert.equal(f.privateBucket.writes.length, 0);
});

test('POST logout clears only album cookie even after removal or storage/config outage', async () => {
  for (const mode of ['normal', 'removed', 'storageFailure', 'noBindings']) {
    const f = fixture();
    if (mode === 'removed') { f.release.albums = []; f.save(); }
    if (mode === 'storageFailure') {
      f.privateBucket.getFailure = () => true;
      f.publicBucket.getFailure = () => true;
    }
    if (mode === 'noBindings') for (const key of Object.keys(f.env)) delete f.env[key];
    const result = await status(await f.call('logout', { method: 'POST', headers: { Origin: ORIGIN } }), 200);
    assert.deepEqual(await result.json(), { ok: true });
    const set = result.headers.get('Set-Cookie');
    assert.match(set, /Max-Age=0/);
    assert.ok(set.startsWith(`${name(A)}=;`));
    assert.ok(set.includes(`Path=${base(A)}`));
    assert.ok(!set.includes(name(B)));
    assert.equal(f.privateBucket.reads.length, 0);
    assert.equal(f.publicBucket.reads.length, 0);
  }
});

test('per-IP fixed window persists across requests and overwrites expired keys', async t => {
  let now = 1900000000000;
  t.mock.method(Date, 'now', () => now);
  const f = fixture();
  for (let i = 0; i < 5; i++) await status(await f.login('wrong'), 401);
  const limited = await status(await f.login(), 429);
  assert.equal(limited.headers.get('Retry-After'), '60');
  assert.equal(f.privateBucket.objects.size, 12); // release + 9 media + IP + global
  const keys = f.privateBucket.writes.map(write => write.key).join(' ');
  assert.ok(!keys.includes('192.0.2.1'));
  assert.ok(keys.includes(createHash('sha256').update('192.0.2.1').digest('hex')));
  now += 60000;
  await status(await f.login(), 200);
  assert.equal(f.privateBucket.objects.size, 12);
  assert.ok(f.privateBucket.writes.some(write => write.options.onlyIf.etagMatches));
  assert.ok(f.privateBucket.writes.some(write => write.options.onlyIf.etagDoesNotMatch === '*'));
  // Different album has its own limiter even for the same IP.
  await status(await f.login('wrong', {}, B), 401);
});

test('concurrent per-IP attempts cannot exceed five verifications', async () => {
  const f = fixture();
  const results = await Promise.all(Array.from({ length: 20 }, () => f.login('wrong')));
  assert.equal(results.filter(result => result.status === 401).length, 5);
  assert.equal(results.filter(result => result.status === 429).length, 15);
  results.forEach(privateHeaders);
  const global = JSON.parse(f.privateBucket.objects.get(`auth-attempts/${A}/global.json`).bytes);
  assert.ok(global.count >= 5 && global.count <= 20);
});

test('per-album global limiter bounds concurrent independent IPs to thirty', async t => {
  t.mock.method(Date, 'now', () => 1900000000000);
  const f = fixture();
  let verified = 0;
  for (let batch = 0; batch < 8; batch++) {
    const results = await Promise.all(Array.from({ length: 8 }, (_, i) => f.login('wrong', { headers: { 'CF-Connecting-IP': `2001:db8::${batch * 8 + i}` } })));
    for (const result of results) {
      assert.ok(result.status === 401 || result.status === 429);
      if (result.status === 401) verified++;
    }
  }
  assert.equal(verified, 30);
  const writes = f.privateBucket.writes.length;
  const keys = [...f.privateBucket.objects.keys()];
  await status(await f.login(), 429);
  assert.equal(f.privateBucket.writes.length, writes, 'exhausted global budget prevents per-IP writes');
  assert.deepEqual([...f.privateBucket.objects.keys()], keys);
  assert.equal(keys.filter(key => key.includes('/ip-')).length, 30);
  assert.equal(JSON.parse(f.privateBucket.objects.get(`auth-attempts/${A}/global.json`).bytes).count, 30);
});

test('limiter read/write failure, corrupted state, missing IP and bounded CAS contention fail closed', async () => {
  for (const mode of ['read', 'write', 'contention', 'corrupt', 'missingIP']) {
    const f = fixture();
    if (mode === 'read') f.privateBucket.getFailure = key => key.startsWith('auth-attempts/');
    if (mode === 'write') f.privateBucket.putFailure = true;
    if (mode === 'contention') f.privateBucket.contention = true;
    if (mode === 'corrupt') f.privateBucket.seed(`auth-attempts/${A}/global.json`, { window: 'bad', count: -1 });
    const result = await status(await f.login('password', mode === 'missingIP' ? { headers: { 'CF-Connecting-IP': '' } } : {}), mode === 'contention' ? 429 : 503);
    assert.equal(result.headers.get('Set-Cookie'), null);
    assert.doesNotMatch(await result.text(), /diagnostics|secret|passwordHash/);
    if (mode === 'contention') assert.equal(f.privateBucket.writes.length, 8);
  }
});

test('config, publication and storage failures return generic 503 without private bytes', async () => {
  const mutations = [
    f => { delete f.env.PUBLIC_PHOTOS; },
    f => { delete f.env.PROTECTED_PHOTOS; },
    f => { f.env.ALBUM_SESSION_SECRET = 'short'; },
    f => { f.publicBucket.seed('index.json', '{'); },
    f => { f.publicBucket.seed('index.json', { protectedRelease: '../catalog' }); },
    f => { f.privateBucket.objects.delete(`releases/${RELEASE}.json`); },
    f => { f.release.version = 2; f.save(); },
    f => { f.release.albums[0].passwordHash = PASSWORD_HASH.replace('16384', '1024'); f.save(); },
    f => { f.release.albums[0].accessVersion = 'invalid'; f.save(); },
    f => { f.release.photos = []; f.save(); },
    f => { f.release.albums.push(f.release.albums[0]); f.save(); },
    f => { f.privateBucket.getFailure = () => true; },
  ];
  for (const mutate of mutations) {
    const f = fixture(); mutate(f);
    const result = await status(await f.call('catalog', { headers: { Cookie: signedCookie() } }), 503);
    assert.equal(await result.text(), 'Request unavailable.');
  }
  const f = fixture();
  f.privateBucket.objects.delete(`photos/${P}/640.jpg`);
  await status(await f.call(`media/${P}/640.jpg`, { headers: { Cookie: signedCookie() } }), 503);
  delete f.env.ASSETS;
  await status(await f.call(''), 503);
});

test('Pages adapter exports the handler and routing invokes only private paths', async () => {
  const adapter = await import('../functions/photos/private/[[path]].js');
  assert.equal(adapter.onRequest, onRequest);
  const routes = JSON.parse(await readFile(new URL('../static/_routes.json', import.meta.url), 'utf8'));
  assert.deepEqual(routes, { version: 1, include: ['/photos/private/*'], exclude: [] });
});

function videoFixture() {
  const f = fixture();
  Object.assign(f.release.photos[0], { kind: 'video', duration: 12.75, width: 1280, height: 720 });
  f.save();
  f.bytes = '0123456789abcdefghijklmnopqrstuvwxyz';
  f.privateBucket.seed(`photos/${P}/video.mp4`, f.bytes);
  return f;
}

test('mixed catalog adds only kind/duration, rewrites MP4 and preserves image/poster URLs', async () => {
  const f = videoFixture();
  const data = await (await f.call('catalog', { headers: { Cookie: signedCookie() } })).json();
  const p = data.photos.find(p => p.id === P), image = data.photos.find(p => p.id === Q);
  assert.equal(p.kind, 'video'); assert.equal(p.duration, 12.75);
  assert.equal(p.width, 1280); assert.equal(p.height, 720);
  assert.equal(p.thumbnail, `${base(A)}media/${P}/640.jpg`);
  assert.equal(p.src, `${base(A)}media/${P}/video.mp4`);
  assert.deepEqual(Object.keys(p).sort(), [...Object.keys(image), 'kind', 'duration'].sort());
  assert.equal(image.src, `${base(A)}media/${Q}/2560.jpg`);
  assert.ok(!('kind' in image));
  assert.doesNotMatch(JSON.stringify(data), /must-not-return|wrong\.example/);
  for (const size of [640, 1280, 2560]) {
    const poster = await status(await f.call(`media/${P}/${size}.jpg`, { headers: { Cookie: signedCookie(), Range: 'bytes=0-0' } }), 200);
    assert.equal(await poster.text(), `JPEG:${P}:${size}`);
  }
});

test('MP4 full and single range GET/HEAD return actual slices and exact headers', async () => {
  const f = videoFixture(), length = f.bytes.length;
  const cases = [[null, 200, 0, length - 1], ['bytes=0-0', 206, 0, 0], ['bytes=5-12', 206, 5, 12],
    ['bytes=30-', 206, 30, length - 1], ['bytes=-5', 206, length - 5, length - 1],
    ['bytes=-999', 206, 0, length - 1], ['bytes=10-999', 206, 10, length - 1]];
  for (const method of ['GET', 'HEAD']) for (const [range, expected, start, end] of cases) {
    const headers = { Cookie: signedCookie(), 'If-None-Match': '*', 'If-Modified-Since': 'Wed, 01 Jan 2099 00:00:00 GMT', 'If-Range': 'stale' };
    if (range) headers.Range = range;
    const before = f.privateBucket.reads.filter(key => key.endsWith('.mp4')).length;
    const result = await f.call(`media/${P}/video.mp4`, { method, headers });
    assert.equal(result.status, expected); privateHeaders(result, true);
    assert.equal(result.headers.get('Content-Type'), 'video/mp4');
    assert.equal(result.headers.get('Accept-Ranges'), 'bytes');
    assert.equal(result.headers.get('Content-Length'), String(end - start + 1));
    assert.equal(result.headers.get('Content-Range'), range ? `bytes ${start}-${end}/${length}` : null);
    assert.equal(await result.text(), method === 'HEAD' ? '' : f.bytes.slice(start, end + 1));
    if (method === 'HEAD') assert.equal(f.privateBucket.reads.filter(key => key.endsWith('.mp4')).length, before);
    if (method === 'GET' && range) assert.deepEqual(f.privateBucket.ranges.at(-1), { offset: start, length: end - start + 1 });
  }
});

test('MP4 unsatisfiable, malformed and multipart ranges fail 416 only after authentication', async () => {
  const f = videoFixture();
  for (const Range of ['bytes=36-', 'bytes=99-100', 'bytes=-0', 'bytes=9-2', 'bytes=-', 'bytes=0-1,4-5',
    'items=0-1', 'bytes=wat', 'bytes=9007199254740992-', 'bytes=0-9007199254740992']) {
    for (const method of ['GET', 'HEAD']) {
      await status(await f.call(`media/${P}/video.mp4`, { method, headers: { Range } }), 401);
      const result = await f.call(`media/${P}/video.mp4`, { method, headers: { Cookie: signedCookie(), Range } });
      assert.equal(result.status, 416); privateHeaders(result, true);
      assert.equal(result.headers.get('Content-Range'), `bytes */${f.bytes.length}`);
      assert.equal(result.headers.get('Content-Length'), '0');
      assert.equal(await result.text(), '');
    }
  }
  assert.equal(f.privateBucket.reads.filter(key => key.endsWith('.mp4')).length, 0);
});

test('MP4 checks session, membership, exact kind, same origin and current pointer before storage on every seek', async () => {
  for (const method of ['GET', 'HEAD']) {
    const f = videoFixture();
    const conditions = { Range: 'bytes=0-0', 'If-None-Match': '*', 'If-Range': 'anything' };
    for (const Cookie of ['', signedCookie(B), signedCookie(A, { version: NEXT })]) {
      await status(await f.call(`media/${P}/video.mp4`, { method, headers: { ...conditions, Cookie } }), 401);
    }
    await status(await f.call(`media/${R}/video.mp4`, { method, headers: { ...conditions, Cookie: signedCookie() } }), 404);
    for (const kind of [undefined, 'image', 'unknown', null]) {
      f.release.photos[0].kind = kind; f.save();
      await status(await f.call(`media/${P}/video.mp4`, { method, headers: { ...conditions, Cookie: signedCookie() } }), 404);
    }
    for (const headers of [{ Origin: 'https://evil.example' }, { 'Sec-Fetch-Site': 'cross-site' }]) {
      await status(await f.call(`media/${P}/video.mp4`, { method, headers: { ...conditions, ...headers, Cookie: signedCookie() } }), 403);
    }
    assert.equal(f.privateBucket.heads.length, 0);
    assert.ok(!f.privateBucket.reads.some(key => key.endsWith('.mp4')));
    f.release.photos[0].kind = 'video'; f.save();
    assert.equal((await f.call(`media/${P}/video.mp4`, { method, headers: { ...conditions, Cookie: signedCookie() } })).status, 206);
    f.release.albums[0].accessVersion = NEXT; f.save(NEXT);
    f.publicBucket.seed('index.json', { protectedRelease: NEXT });
    const before = f.privateBucket.heads.length;
    await status(await f.call(`media/${P}/video.mp4?protectedRelease=${RELEASE}`, { method, headers: { ...conditions, Cookie: signedCookie() } }), 401);
    f.release.albums[0].published = false; f.save(NEXT);
    await status(await f.call(`media/${P}/video.mp4`, { method, headers: { ...conditions, Cookie: signedCookie(A, { version: NEXT }) } }), 404);
    assert.equal(f.privateBucket.heads.length, before);
  }
});

test('video metadata and missing MP4/storage failures fail closed; unknown catalog kinds rejected', async () => {
  for (const changes of [{ kind: 'unknown' }, { kind: null }, { duration: 0 }, { duration: -1 }, { duration: '12' }, { duration: null }]) {
    const f = videoFixture(); Object.assign(f.release.photos[0], changes); f.save();
    await status(await f.call('catalog', { headers: { Cookie: signedCookie() } }), 503);
  }
  for (const mutate of [f => f.privateBucket.objects.delete(`photos/${P}/video.mp4`),
    f => { f.privateBucket.getFailure = key => key.endsWith('.mp4'); },
    f => { delete f.env.PROTECTED_PHOTOS.head; f.env.PROTECTED_PHOTOS.head = undefined; }]) {
    const f = videoFixture(); mutate(f);
    await status(await f.call(`media/${P}/video.mp4`, { headers: { Cookie: signedCookie() } }), 503);
  }
});
