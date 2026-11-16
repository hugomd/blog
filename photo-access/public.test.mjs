import test from 'node:test';
import assert from 'node:assert/strict';
import { onRequest } from './public.mjs';

const A = 'a'.repeat(32), B = 'b'.repeat(32);
const image = id => `https://photos.example/photos/${id}/2560.jpg`;
const poster = id => `https://photos.example/photos/${id}/640.jpg`;
const shell = '<html><head><title>Photostream :: Hugo’s Blog</title><meta name=description content="Generic"><meta property="og:image" content="avatar.jpg"><meta name=twitter:card content=summary><link rel=canonical href=https://hugo.md/photos/><link rel=stylesheet href=../css/photos.css></head><body>Gallery</body></html>';
function fixture() {
  const catalog = { version: 1, photos: [
    { id: A, published: true, alt: 'A city & its people', caption: 'Evening light.', src: image(A), thumbnail: poster(A) },
    { id: B, published: true, kind: 'video', alt: 'City in motion', caption: '', src: `https://photos.example/photos/${B}/video.mp4`, thumbnail: poster(B) },
  ], albums: [] };
  const env = {
    PUBLIC_PHOTOS: { async get(key) { assert.equal(key, 'index.json'); return { text: async () => JSON.stringify(catalog) }; } },
    get PROTECTED_PHOTOS() { throw Error('Public previews must not read protected storage'); },
    ASSETS: { async fetch(request) {
      assert.equal(new URL(request.url).pathname, '/photos/');
      assert.deepEqual([...request.headers], [], 'No visitor cookies or credentials reach assets');
      return new Response(shell, { headers: { 'Content-Type': 'text/html', ETag: 'static-shell' } });
    } },
  };
  const call = (path = `/photos/${A}/`, options = {}) => onRequest({ request: new Request(`https://hugo.md${path}`, options), env,
    next: () => new Response('Static fallback') });
  return { catalog, env, call };
}

test('photostream previews the first four published items in manifest order, with posters for videos', async () => {
  const f = fixture();
  const C = 'c'.repeat(32), D = 'd'.repeat(32), E = 'e'.repeat(32);
  f.catalog.photos.push(...[C, D, E].map(id => ({ id, src: image(id), alt: id })));
  // Excluded entries must not consume preview slots, even at the front.
  for (const flag of ['published', 'archived', 'archiving', 'deleting', 'kind']) {
    f.catalog.photos.unshift({ id: 'f'.repeat(32), src: 'https://private.example/original.jpg',
      [flag]: flag === 'published' ? false : flag === 'kind' ? 'unknown' : true });
  }
  const response = await f.call('/photos/', { headers: { Cookie: 'private=session' } });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('Cache-Control'), 'no-store');
  const html = await response.text();
  assert.deepEqual([...html.matchAll(/property="og:image" content="([^"]+)"/g)].map(match => match[1]),
    [image(A), poster(B), image(C), image(D)]);
  assert.match(html, /property="og:image:alt" content="A city &amp; its people"/);
  assert.ok(html.includes(`name="twitter:image" content="${image(A)}"`));
  assert.equal((html.match(/name="twitter:card"/g) || []).length, 1);
  assert.match(html, /name="twitter:card" content="summary_large_image"/);
  assert.match(html, /<title>Photostream :: Hugo’s Blog<\/title>/);
  assert.match(html, /<meta name=description content="Generic">/);
  assert.match(html, /<link rel=canonical href=https:\/\/hugo.md\/photos\/>/);
  assert.match(html, /<body>Gallery<\/body>/);
  assert.doesNotMatch(html, /avatar|private.example|video.mp4/);
  assert.equal(await (await f.call('/photos/', { method: 'HEAD' })).text(), '');
  assert.equal((await f.call('/photos/', { method: 'POST' })).status, 405);
  f.catalog.photos = f.catalog.photos.filter(photo => photo.id !== A);
  const refreshed = await (await f.call('/photos/?photo=invalid')).text();
  assert.deepEqual([...refreshed.matchAll(/property="og:image" content="([^"]+)"/g)].map(match => match[1]),
    [poster(B), image(C), image(D), image(E)]);
  assert.ok(refreshed.includes(`name="twitter:image" content="${poster(B)}"`));
});

test('small and empty photostreams never fall back to an avatar or unpublished media', async () => {
  const f = fixture();
  const html = await (await f.call('/photos/')).text();
  assert.deepEqual([...html.matchAll(/property="og:image" content="([^"]+)"/g)].map(match => match[1]), [image(A), poster(B)]);
  f.catalog.photos = [];
  const empty = await (await f.call('/photos/')).text();
  assert.doesNotMatch(empty, /avatar|og:image|twitter:image/);
  assert.match(empty, /name="twitter:card" content="summary"/);
  f.catalog.photos = [{ id: A, src: `https://photos.example/originals/${A}.jpg` }];
  assert.equal((await f.call('/photos/')).status, 503);
  f.env.PUBLIC_PHOTOS.get = async () => null;
  assert.equal((await f.call('/photos/')).status, 503);
  f.env.PUBLIC_PHOTOS.get = async () => ({ text: async () => '{"version":2,"photos":[],"albums":[]}' });
  assert.equal((await f.call('/photos/')).status, 503);
});

test('photo paths return unique image-specific metadata and a canonical URL without JavaScript', async () => {
  const f = fixture();
  const response = await f.call(`/photos/${A}/?utm_source=share`, { headers: { Cookie: 'private=session' } });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('Cache-Control'), 'no-store');
  assert.equal(response.headers.get('ETag'), null);
  const html = await response.text();
  assert.match(html, /<head><base href="\/photos\/">/);
  assert.match(html, /<title>A city &amp; its people :: Hugo’s Blog<\/title>/);
  for (const name of ['og:image', 'twitter:image']) assert.ok(html.includes(`="${name}" content="${image(A)}"`));
  assert.match(html, /name="twitter:card" content="summary_large_image"/);
  assert.match(html, /name="description" content="Evening light\."/);
  assert.ok(html.includes(`property="og:url" content="https://hugo.md/photos/${A}/"`));
  assert.ok(html.includes(`rel="canonical" href="https://hugo.md/photos/${A}/"`));
  assert.equal((html.match(/property="og:image"/g) || []).length, 1);
  assert.equal((html.match(/<title>/g) || []).length, 1);
  assert.doesNotMatch(html, /avatar.jpg|utm_source|static-shell/);
  assert.equal(await (await f.call(`/photos/${A}/`, { method: 'HEAD' })).text(), '');
  f.catalog.photos[0].alt = `<script>alert("x")</script> $& $'`;
  f.catalog.photos[0].caption = `Caption "quoted" <b>not markup</b> $& $'`;
  const escaped = await (await f.call()).text();
  assert.doesNotMatch(escaped, /<script>|<b>/);
  assert.ok(escaped.includes('&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt; $&amp; $&#39;'));
  assert.ok(escaped.includes('Caption &quot;quoted&quot; &lt;b&gt;not markup&lt;/b&gt; $&amp; $&#39;'));
});

test('untitled photos and videos use their capture date in page and social titles', async () => {
  const f = fixture();
  for (const [index, capturedDate, expected] of [[0, '2024-11-22', 'November 22nd, 2024'], [1, '2023-01-11', 'January 11th, 2023']]) {
    const photo = f.catalog.photos[index];
    photo.alt = '';
    photo.capturedDate = capturedDate;
    const html = await (await f.call(`/photos/${photo.id}/`)).text();
    assert.ok(html.includes(`<title>${expected} :: Hugo’s Blog</title>`));
    for (const name of ['og:title', 'twitter:title']) assert.ok(html.includes(`="${name}" content="${expected} :: Hugo’s Blog"`));
    delete photo.capturedDate;
    assert.ok((await (await f.call(`/photos/${photo.id}/`)).text()).includes(`<title>${index ? 'Video' : 'Photograph'} :: Hugo’s Blog</title>`));
  }
});

test('videos preview the public JPEG poster, not the MP4', async () => {
  const f = fixture();
  const response = await f.call(`/photos/${B}/`);
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.ok(html.includes(`property="og:image" content="${poster(B)}"`));
  assert.ok(html.includes(`name="twitter:image" content="${poster(B)}"`));
  assert.match(html, /property="og:description" content="City in motion"/);
  assert.doesNotMatch(html, /video.mp4/);
});

test('unpublished, removed, invalid and protected media cannot acquire public previews or cached metadata', async () => {
  const f = fixture();
  for (const flag of ['archived', 'archiving', 'deleting', 'published', 'kind']) {
    const original = structuredClone(f.catalog.photos[0]);
    f.catalog.photos[0][flag] = flag === 'published' ? false : flag === 'kind' ? 'unknown' : true;
    const response = await f.call();
    assert.equal(response.status, 404, flag);
    assert.equal(response.headers.get('X-Robots-Tag'), 'noindex');
    assert.doesNotMatch(await response.text(), /A city|photos.example/);
    f.catalog.photos[0] = original;
  }
  for (const src of [`https://photos.example/originals/${A}.jpg`, `https://photos.example/photos/${B}/2560.jpg`, `http://photos.example/photos/${A}/2560.jpg`]) {
    f.catalog.photos[0].src = src;
    assert.equal((await f.call()).status, 503);
  }
  f.catalog.photos = [];
  assert.equal((await f.call()).status, 404, 'Removed photo is not cached from the preceding request');
  f.env.PUBLIC_PHOTOS.get = () => { throw Error('Storage unavailable'); };
  assert.equal((await f.call()).status, 503);
  for (const path of [`/photos/private/${A}/`, `/photos/private/${A}/${B}/`, `/photos/private/${A}/media/${B}/640.jpg`]) {
    assert.equal((await f.call(path)).status, 404, 'Never fall back to public assets for protected routes');
  }
});

test('legacy links redirect; public previews require bindings while other collections remain static', async () => {
  const f = fixture();
  for (const [path, expected] of [
    [`/photos/?photo=${A}&utm_source=test`, `/photos/${A}/?utm_source=test`],
    [`/photos/2022/new-york/?photo=${B}`, `/photos/${B}/#album/new-york-2022`],
    [`/photos/${B}`, `/photos/${B}/`],
  ]) {
    const response = await f.call(path);
    assert.equal(response.status, 308);
    assert.equal(response.headers.get('Location'), `https://hugo.md${expected}`);
  }
  f.env.PUBLIC_PHOTOS = undefined;
  assert.equal((await f.call()).status, 503);
  for (const path of ['/photos/', '/photos/?photo=invalid']) assert.equal((await f.call(path)).status, 503);
  assert.equal(await (await f.call('/photos/2022/new-york/')).text(), 'Static fallback');
  const response = await f.call(`/photos/${A}/`, { method: 'POST' });
  assert.equal(response.status, 405);
  assert.equal(response.headers.get('Allow'), 'GET, HEAD');
});
