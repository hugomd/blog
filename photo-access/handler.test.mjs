import test from 'node:test';
import assert from 'node:assert/strict';
import { createHmac, createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
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
const base = id => `/photos/private/${id}/`;
const name = id => `__Secure-photo_album_${id}`;

// Snapshot reads, atomic conditional writes, streams and monotonically changing
// ETags model R2's strong consistency. No ordinary unconditional auth writes pass.
class R2 {
  objects = new Map();
  reads = [];
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
    assert.equal(options, undefined, 'never forward request conditions or ranges to R2');
    if (this.getFailure?.(key)) throw new Error('sensitive storage diagnostics');
    const snapshot = this.objects.get(key);
    await Promise.resolve();
    if (!snapshot) return null;
    return { etag: snapshot.etag, httpEtag: `"${snapshot.etag}"`,
      body: new Response(snapshot.bytes).body,
      text: async () => snapshot.bytes,
      httpMetadata: { cacheControl: 'public, max-age=99999', contentType: 'text/secret' } };
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
function signedCookie(id = A, claims = {}, secret = SECRET) {
  const payload = Buffer.from(JSON.stringify({ album: id, version: VERSION, exp: Math.floor(Date.now() / 1000) + 3600, ...claims })).toString('base64url');
  return `${name(id)}=${payload}.${createHmac('sha256', secret).update(payload).digest('base64url')}`;
}
function privateHeaders(result) {
  assert.match(result.headers.get('Cache-Control'), /private.*no-store/);
  assert.equal(result.headers.get('CDN-Cache-Control'), 'no-store');
  assert.equal(result.headers.get('Cloudflare-CDN-Cache-Control'), 'no-store');
  assert.equal(result.headers.get('Referrer-Policy'), 'no-referrer');
  assert.equal(result.headers.get('X-Robots-Tag'), 'noindex, nofollow, noarchive');
  assert.equal(result.headers.get('X-Content-Type-Options'), 'nosniff');
  for (const header of ['ETag', 'Last-Modified', 'Accept-Ranges', 'Content-Range', 'Access-Control-Allow-Origin', 'Access-Control-Allow-Credentials']) {
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
  const a = await (await status(await f.call('catalog', { headers: { Cookie: signedCookie() } }), 200)).json();
  assert.deepEqual(a.photos.map(photo => photo.id), [Q, P]);
  assert.equal(a.albums.length, 1);
  assert.deepEqual(Object.keys(a.albums[0]).sort(), ['coverId', 'description', 'id', 'photoIds', 'published', 'slug', 'title']);
  assert.deepEqual(Object.keys(a.photos[0]).sort(), ['alt', 'caption', 'capturedDate', 'exif', 'height', 'id', 'src', 'tags', 'thumbnail', 'uploadedAt', 'width']);
  assert.equal(a.photos[1].thumbnail, `${base(A)}media/${P}/640.jpg`);
  const b = await (await status(await f.call('catalog', { headers: { Cookie: signedCookie(B) } }, B), 200)).json();
  assert.deepEqual(b.photos.map(photo => photo.id), [P, R]);
  assert.equal(b.photos[0].src, `${base(B)}media/${P}/2560.jpg`);
  assert.doesNotMatch(JSON.stringify(a), /passwordHash|accessVersion|must-not-return|wrong\.example/);
  for (const id of [A, B]) {
    const image = await status(await f.call(`media/${P}/640.jpg`, { headers: { Cookie: signedCookie(id) } }, id), 200);
    assert.equal(await image.text(), `JPEG:${P}:640`);
  }
  const before = f.privateBucket.reads.length;
  await status(await f.call(`media/${R}/640.jpg`, { headers: { Cookie: signedCookie() } }), 404);
  assert.ok(!f.privateBucket.reads.slice(before).some(key => key.startsWith('photos/')));
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
