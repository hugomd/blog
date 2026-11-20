import test from 'node:test';
import assert from 'node:assert/strict';
import { DatabaseSync } from 'node:sqlite';
import { readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { generateKeyPair, exportJWK, SignJWT } from 'jose';
import { parse } from 'yaml';
import { onRequest } from './handler.mjs';

const ORIGIN = 'https://blog.example';
const BASE = `${ORIGIN}/publishing`;
const CLIENT = 'https://ia.net/writer';
const CALLBACK = `${CLIENT}/indieauth/redirect`;
const TEAM = 'writing-test.cloudflareaccess.com';
const TOKEN = 'b'.repeat(64);
const BODY = '# Café ☕\n\nA **bold** thought.\n\n{{< sidenote "An aside" 1 >}}Keep $& unchanged.{{< /sidenote >}}\n\n```js\nconst x = 3;\n```\n';
const schema = await readFile(new URL('./schema.sql', import.meta.url), 'utf8');
const { publicKey, privateKey } = await generateKeyPair('RS256');
const jwk = { ...await exportJWK(publicKey), kid: 'fixture', alg: 'RS256', use: 'sig' };
const hash = text => createHash('sha256').update(text).digest('hex');
const now = () => Math.floor(Date.now() / 1000);
const jwt = (claims = {}) => new SignJWT({ email: 'owner@example.com', ...claims }).setProtectedHeader({ alg: 'RS256', kid: 'fixture' }).setSubject('owner-id').setIssuer(`https://${TEAM}`).setAudience('writing-audience').setExpirationTime(now() + 300).sign(privateKey);

// Execute the production SQL in SQLite, including RETURNING and conditional writes.
// This adapter mirrors only D1's documented prepared-statement surface.
function database(sqlite) {
  return { prepare(sql) {
    let args = [];
    return { bind(...values) { args = values; return this; },
      async first() { return sqlite.prepare(sql).get(...args) || null; },
      async all() { return { results: sqlite.prepare(sql).all(...args) }; },
      async run() { return { success: true, meta: sqlite.prepare(sql).run(...args) }; },
    };
  } };
}

async function fixture(t) {
  const sqlite = new DatabaseSync(':memory:');
  sqlite.exec(schema);
  t.after(() => sqlite.close());
  sqlite.prepare('INSERT INTO tokens VALUES (?, ?, ?, ?)').run(hash(TOKEN), CLIENT, now(), now() + 3600);
  const env = { PUBLISHING: database(sqlite), PUBLISHING_ORIGIN: ORIGIN, PUBLISHING_ACCESS_TEAM: TEAM, PUBLISHING_ACCESS_AUD: 'writing-audience', PUBLISHING_OWNER_EMAIL: 'owner@example.com', PUBLISHING_ALLOW_PUBLISH: 'true', PUBLISHING_GITHUB_TOKEN: 'fixture-not-a-real-token', CF_PAGES_BRANCH: 'develop' };
  const access = await jwt();
  const f = { env, sqlite, files: new Map(), commits: 0, failAfterWrite: false };
  t.mock.method(globalThis, 'fetch', async (input, init = {}) => {
    const url = new URL(input);
    if (url.origin === `https://${TEAM}` && url.pathname === '/cdn-cgi/access/certs') return Response.json({ keys: [jwk] });
    assert.equal(url.origin, 'https://api.github.com', 'Tests never contact a real external service');
    assert.equal(init.headers.Authorization, 'Bearer fixture-not-a-real-token');
    const path = url.pathname.split('/contents/')[1];
    if (init.method === 'PUT') {
      const data = JSON.parse(init.body);
      assert.equal(data.sha, undefined, 'Existing files cannot be overwritten');
      assert.equal(data.branch, 'develop');
      if (f.files.has(path)) return Response.json({}, { status: 422 });
      f.files.set(path, data.content); f.commits++;
      if (f.failAfterWrite) throw new Error('simulated connection lost after commit');
      return Response.json({}, { status: 201 });
    }
    assert.equal(url.searchParams.get('ref'), 'develop');
    if (f.readGitHub) return f.readGitHub(path);
    if (!f.files.has(path)) return Response.json({}, { status: 404 });
    return Response.json({ type: 'file', encoding: 'base64', content: f.files.get(path) });
  });
  f.call = (path = '/', { owner = false, native = false, headers = {}, body, ...options } = {}) => onRequest({ env, request: new Request(`${BASE}${path}`, { ...options, body, headers: { 'CF-Connecting-IP': '192.0.2.1', ...(owner ? { 'Cf-Access-Jwt-Assertion': access } : {}), ...(native ? { Authorization: `Bearer ${TOKEN}` } : {}), ...headers } }) });
  f.form = (path, values, options = {}) => f.call(path, { owner: true, method: 'POST', body: new URLSearchParams(values), headers: { Origin: ORIGIN }, ...options });
  f.capture = (content = BODY) => f.call('/micropub', { native: true, method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ type: ['h-entry'], properties: { name: ['From the train'], content: [content], 'post-status': ['draft'] } }) });
  f.row = id => sqlite.prepare('SELECT * FROM drafts WHERE id = ?').get(id);
  f.create = async (content = BODY) => {
    const received = await f.capture(content); assert.equal(received.status, 201);
    const path = new URL(received.headers.get('Location')).pathname.slice('/publishing'.length);
    return { path, id: path.split('/').at(-1), response: received };
  };
  f.fields = (id, extra = {}) => {
    const row = f.row(id); const draft = JSON.parse(row.content);
    return { ...draft, tags: draft.tags.join('\n'), toc: draft.toc ? 'true' : '', version: row.version, intent: 'save', ...extra };
  };
  return f;
}

test('public discovery uses absolute canonical URLs and exposes no private data', async t => {
  const f = await fixture(t);
  const received = await f.call('/');
  const html = await received.text();
  assert.ok(html.includes(`<link rel="micropub" href="${BASE}/micropub">`));
  assert.ok(received.headers.get('Link').includes(`<${BASE}/token>`));
  assert.equal(html.includes(TOKEN), false);
  const head = await f.call('/', { method: 'HEAD' });
  assert.equal(head.status, 200); assert.equal(await head.text(), '');
  assert.match(received.headers.get('Cache-Control'), /no-store/);
  // no-referrer makes browser form POSTs send Origin: null, blocking owner saves.
  // Keep same-origin forms working without leaking draft URLs to other sites.
  assert.equal(received.headers.get('Referrer-Policy'), 'same-origin');
  assert.equal((await f.call('/micropub?q=config')).status, 401);
  assert.equal((await onRequest({ env: f.env, request: new Request('https://preview.example/publishing/') })).status, 404);
  delete f.env.PUBLISHING;
  assert.equal((await f.call('/')).status, 503);
});

test('draft pages require a verified Access signature, issuer, audience, lifetime and owner', async t => {
  const f = await fixture(t);
  const { path } = await f.create();
  assert.equal((await f.call(path)).status, 401);
  assert.equal((await f.call(path, { native: true })).status, 401);
  for (const claims of [{ email: 'intruder@example.com' }, { nbf: now() + 100 }]) {
    const result = await f.call(path, { headers: { 'Cf-Access-Jwt-Assertion': await jwt(claims) } });
    assert.ok([401, 403].includes(result.status));
    assert.equal((await result.text()).includes('Keep $&'), false);
  }
  for (const options of [{ iss: 'https://wrong.example' }, { aud: 'wrong' }, { exp: now() - 1 }]) {
    const token = await new SignJWT({ sub: 'owner', email: 'owner@example.com', iss: `https://${TEAM}`, aud: 'writing-audience', exp: now() + 300, ...options }).setProtectedHeader({ alg: 'RS256', kid: 'fixture' }).sign(privateKey);
    assert.equal((await f.call(path, { headers: { 'Cf-Access-Jwt-Assertion': token } })).status, 401);
  }
  const forged = `${(await jwt()).split('.').slice(0, 2).join('.')}.${'a'.repeat(342)}`;
  assert.equal((await f.call(path, { headers: { 'Cf-Access-Jwt-Assertion': forged } })).status, 401);
  const review = await f.call(path, { owner: true });
  assert.equal(review.status, 200);
  assert.equal(review.headers.get('Content-Security-Policy').split(';').map(value => value.trim()).find(value => value.startsWith('form-action ')), "form-action 'self'");
});

test('IndieAuth binds approval, callback and PKCE; codes are one-use and tokens are hashed', async t => {
  const f = await fixture(t);
  const verifier = 'independent-verifier-'.repeat(3);
  const params = new URLSearchParams({ response_type: 'code', me: `${BASE}/`, client_id: CLIENT, redirect_uri: CALLBACK, state: 'opaque+state&roundtrip', scope: 'create draft', code_challenge: createHash('sha256').update(verifier).digest('base64url'), code_challenge_method: 'S256' });
  const pending = await f.call(`/auth?${params}`); assert.equal(pending.status, 303);
  const approval = new URL(pending.headers.get('Location'));
  const id = approval.searchParams.get('request');
  assert.equal((await f.call(`/admin/authorize?request=${id}`)).status, 401);
  const consent = await f.call(`/admin/authorize?request=${id}`, { owner: true });
  assert.equal(consent.status, 200);
  // Browsers enforce the original form's policy across the callback redirect,
  // not just the policy on the POST response.
  assert.equal(consent.headers.get('Content-Security-Policy').split(';').map(value => value.trim()).find(value => value.startsWith('form-action ')), `form-action 'self' ${CALLBACK}`);
  assert.equal((await f.form('/admin/authorize', { request: id }, { headers: { Origin: 'https://evil.example' } })).status, 403);
  const allowed = await f.form('/admin/authorize', { request: id }); assert.equal(allowed.status, 303);
  assert.equal((await f.form('/admin/authorize', { request: id })).status, 400);
  const callback = new URL(allowed.headers.get('Location'));
  assert.equal(callback.origin + callback.pathname, CALLBACK);
  assert.equal(callback.searchParams.get('state'), params.get('state'));
  assert.equal(callback.searchParams.get('iss'), `${BASE}/`);
  const exchange = { grant_type: 'authorization_code', code: callback.searchParams.get('code'), client_id: CLIENT, redirect_uri: CALLBACK, code_verifier: verifier };
  const redeem = values => f.call('/token', { method: 'POST', body: new URLSearchParams(values) });
  assert.equal((await redeem({ ...exchange, redirect_uri: 'https://evil.example' })).status, 400);
  assert.equal((await redeem({ ...exchange, code_verifier: 'wrong' })).status, 400);
  const result = await redeem(exchange); assert.equal(result.status, 200);
  const credentials = await result.json();
  assert.equal(credentials.me, `${BASE}/`); assert.equal(credentials.scope, 'create');
  assert.match(credentials.access_token, /^[a-f0-9]{64}$/);
  assert.equal(f.sqlite.prepare('SELECT hash FROM tokens WHERE hash = ?').get(credentials.access_token), undefined);
  assert.ok(f.sqlite.prepare('SELECT hash FROM tokens WHERE hash = ?').get(hash(credentials.access_token)));
  assert.equal((await redeem(exchange)).status, 400);
  assert.equal((await f.call('/micropub?q=config', { headers: { Authorization: `Bearer ${credentials.access_token}` } })).status, 200);
  params.set('redirect_uri', 'https://evil.example/');
  assert.equal((await f.call(`/auth?${params}`)).status, 400);
});

test('legacy iA authorization works without PKCE, while expired connections and revoked tokens fail', async t => {
  const f = await fixture(t);
  const params = new URLSearchParams({ response_type: 'code', client_id: CLIENT, redirect_uri: CALLBACK, state: 'legacy', scope: 'create' });
  const pending = await f.call(`/auth?${params}`);
  const id = new URL(pending.headers.get('Location')).searchParams.get('request');
  const allowed = await f.form('/admin/authorize', { request: id });
  const code = new URL(allowed.headers.get('Location')).searchParams.get('code');
  assert.equal((await f.call('/token', { method: 'POST', body: new URLSearchParams({ code, grant_type: 'authorization_code', client_id: CLIENT, redirect_uri: CALLBACK }) })).status, 200);
  const expired = await f.call(`/auth?${params}`);
  f.sqlite.exec('UPDATE auth_requests SET expires_at = 0');
  assert.equal((await f.call(new URL(expired.headers.get('Location')).pathname.slice('/publishing'.length) + new URL(expired.headers.get('Location')).search, { owner: true })).status, 400);
  assert.equal((await f.form(`/admin/tokens/${hash(TOKEN)}/revoke`, {})).status, 303);
  assert.equal((await f.capture()).status, 401);
});

test('anonymous admission limits one IP atomically without starving another and expires cleanly', async t => {
  const f = await fixture(t);
  // Simulate the pre-admission schema with an existing token and draft.
  const { id } = await f.create();
  const existing = { ...f.row(id) };
  f.sqlite.exec('DROP TABLE auth_admissions');
  f.sqlite.exec(schema);
  f.sqlite.exec(schema);
  assert.deepEqual({ ...f.row(id) }, existing);
  assert.equal((await f.call('/token', { native: true })).status, 200);
  const params = new URLSearchParams({ response_type: 'code', client_id: CLIENT, redirect_uri: CALLBACK, state: 'pending', scope: 'create' });
  const connect = headers => f.call(`/auth?${params}`, { headers });
  const attempts = await Promise.all(Array.from({ length: 8 }, () => connect()));
  assert.equal(attempts.filter(result => result.status === 303).length, 5);
  assert.equal(attempts.filter(result => result.status === 429).length, 3);
  assert.equal((await connect({ 'CF-Connecting-IP': '192.0.2.2' })).status, 303);
  assert.equal((await connect({ 'CF-Connecting-IP': '', 'X-Forwarded-For': '192.0.2.3' })).status, 503);
  assert.equal((await connect({ 'X-Forwarded-For': '192.0.2.4' })).status, 429);
  const buckets = f.sqlite.prepare('SELECT * FROM auth_admissions').all();
  assert.equal(buckets.length, 2);
  for (const bucket of buckets) assert.match(bucket.client_hash, /^[a-f0-9]{64}$/);
  f.sqlite.exec('UPDATE auth_admissions SET expires_at = 0; UPDATE auth_requests SET expires_at = 0');
  assert.equal((await connect()).status, 303);
  assert.equal(f.sqlite.prepare('SELECT count(*) AS n FROM auth_admissions').get().n, 1);
  assert.equal(f.sqlite.prepare('SELECT count(*) AS n FROM auth_requests').get().n, 1);
  // Reapplying the schema to an already-populated database is the migration.
  f.sqlite.exec(schema);
  assert.equal((await connect()).status, 303);
});

test('anonymous admission retains the global pending-request backstop', async t => {
  const f = await fixture(t);
  const params = new URLSearchParams({ response_type: 'code', client_id: CLIENT, redirect_uri: CALLBACK, state: 'pending', scope: 'create' });
  for (let i = 0; i < 100; i++) {
    assert.equal((await f.call(`/auth?${params}`, { headers: { 'CF-Connecting-IP': `192.0.2.${i + 1}` } })).status, 303);
  }
  assert.equal((await f.call(`/auth?${params}`, { headers: { 'CF-Connecting-IP': '198.51.100.1' } })).status, 429);
  assert.equal(f.sqlite.prepare('SELECT count(*) AS n FROM auth_requests').get().n, 100);
});

test('capture is private, preserves Markdown, and identical retries reopen rather than duplicate', async t => {
  const f = await fixture(t);
  const { path, id } = await f.create();
  assert.equal(JSON.parse(f.row(id).content).body, BODY);
  const repeat = await f.capture();
  assert.equal(new URL(repeat.headers.get('Location')).pathname, `/publishing${path}`);
  await f.create(`${BODY}\nRevision.`);
  assert.equal(f.sqlite.prepare('SELECT count(*) AS n FROM drafts').get().n, 2);
  assert.equal(f.commits, 0);
  const malicious = await f.create('</textarea><script>alert(1)</script>');
  const html = await (await f.call(malicious.path, { owner: true })).text();
  assert.ok(html.includes('&lt;/textarea&gt;&lt;script&gt;'));
  assert.equal(html.includes('<script>'), false);
});

test('reserved bundle slugs cannot enter through capture, browser edits or legacy stored drafts', async t => {
  const f = await fixture(t);
  assert.equal((await f.capture('---\nslug: index\n---\nA bundle must not be created.')).status, 400);
  assert.equal(f.sqlite.prepare('SELECT count(*) AS n FROM drafts').get().n, 0);
  const { id, path } = await f.create();
  assert.equal((await f.form(path, f.fields(id, { slug: 'index', intent: 'publish' }))).status, 400);
  assert.equal(f.row(id).version, 1);
  const legacy = { ...JSON.parse(f.row(id).content), slug: 'index' };
  f.sqlite.prepare('UPDATE drafts SET content = ? WHERE id = ?').run(JSON.stringify(legacy), id);
  assert.equal((await f.form(`${path}/publish`, { version: 1 })).status, 400);
  assert.equal(f.row(id).status, 'draft');
  assert.equal(f.commits, 0);
});

test('bad formats and oversized or unsupported post requests fail without writes', async t => {
  const f = await fixture(t);
  for (const properties of [{ content: [{ html: '<b>HTML</b>' }] }, { content: ['okay'], photo: ['image'] }, { content: ['okay'], 'post-status': ['published'] }, {}]) {
    const result = await f.call('/micropub', { native: true, method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ properties }) });
    assert.ok([400, 415].includes(result.status));
  }
  const form = await f.call('/micropub', { native: true, method: 'POST', body: new URLSearchParams({ h: 'entry', content: 'A & plus + café', category: 'travel' }) });
  assert.equal(form.status, 201);
  assert.equal((await f.call('/micropub', { native: true, method: 'POST', body: new URLSearchParams({ content: 'x'.repeat(270000) }) })).status, 413);
  assert.equal(f.commits, 0);
});

test('browser saves metadata, rejects CSRF and stale edits, and leaves GitHub untouched', async t => {
  const f = await fixture(t); const { id, path } = await f.create();
  const edits = f.fields(id, { description: 'A train of thought', tags: 'travel\nwriting', slug: 'train-of-thought' });
  assert.equal((await f.form(path, edits, { headers: {} })).status, 403);
  const saved = await f.form(path, edits);
  assert.equal(saved.status, 303);
  const confirmation = new URL(saved.headers.get('Location'));
  const reviewed = await f.call(confirmation.pathname.slice('/publishing'.length) + confirmation.search, { owner: true });
  assert.match(await reviewed.text(), /Draft saved\. Still private/);
  assert.equal(JSON.parse(f.row(id).content).description, edits.description);
  assert.deepEqual(JSON.parse(f.row(id).content).tags, ['travel', 'writing']);
  assert.equal((await f.form(path, { ...edits, title: 'Stale edit' })).status, 409);
  assert.equal(JSON.parse(f.row(id).content).title, 'From the train');
  assert.equal(f.commits, 0);
});

test('invalid edits remain in the form, future captures stay editable, and browser newline conversion does not rewrite unchanged Markdown', async t => {
  const f = await fixture(t);
  const { id, path } = await f.create('\n' + BODY);
  const invalid = await f.form(path, f.fields(id, { date: 'not-a-date', body: 'Unsaved words must survive.' }));
  assert.equal(invalid.status, 400);
  assert.ok((await invalid.text()).includes('Unsaved words must survive.'));
  assert.equal(JSON.parse(f.row(id).content).body, '\n' + BODY);
  const saved = await f.form(path, f.fields(id, { body: ('\n' + BODY).replaceAll('\n', '\r\n') }));
  assert.equal(saved.status, 303); assert.equal(JSON.parse(f.row(id).content).body, '\n' + BODY);
  const future = await f.create('---\ndate: 2999-01-01T00:00:00Z\n---\nA future private draft.');
  const review = await f.call(future.path, { owner: true });
  assert.equal(review.status, 200); assert.ok((await review.text()).includes('scheduling is not implemented'));
  assert.equal((await f.form(future.path, f.fields(future.id, { date: '2026-01-01T00:00:00Z' }))).status, 303);
  assert.equal(f.commits, 0);
});

test('explicit publish writes correct Hugo front matter and exact Markdown only once', async t => {
  const f = await fixture(t); const { id, path } = await f.create();
  const result = await f.form(path, f.fields(id, { intent: 'publish', slug: 'train-thoughts', description: 'A "quote": yes', tags: 'travel\ncoffee', toc: 'true' }));
  assert.equal(result.status, 303); assert.equal(f.row(id).status, 'published');
  const text = Buffer.from(f.files.get('content/post/train-thoughts.md'), 'base64').toString('utf8');
  const [, yaml, body] = text.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);
  const meta = parse(yaml);
  assert.equal(meta.title, 'From the train'); assert.equal(meta.slug, 'train-thoughts'); assert.equal(meta.draft, false);
  assert.equal(meta.description, 'A "quote": yes'); assert.deepEqual(meta.tags, ['travel', 'coffee']); assert.equal(meta.toc, true);
  assert.equal(body, BODY);
  assert.equal((await f.form(`${path}/publish`, { version: f.row(id).version })).status, 303);
  assert.equal(f.commits, 1);
  const html = await (await f.call(path, { owner: true })).text();
  assert.ok(html.includes('after a successful build'));
  assert.ok(html.includes('https://github.com/hugomd/blog/blob/develop/content/post/train-thoughts.md'));
});

test('existing different content is not overwritten and the slug remains editable', async t => {
  const f = await fixture(t); const { id, path } = await f.create();
  const original = Buffer.from('An existing post').toString('base64');
  f.files.set('content/post/from-the-train.md', original);
  assert.equal((await f.form(path, f.fields(id, { intent: 'publish' }))).status, 409);
  assert.equal(f.row(id).status, 'draft'); assert.equal(f.commits, 0);
  assert.equal(f.files.get('content/post/from-the-train.md'), original);
  assert.equal((await f.form(path, f.fields(id, { slug: 'a-new-slug', intent: 'publish' }))).status, 303);
  assert.equal(f.commits, 1);
});

test('a lost GitHub response freezes the draft and retry reconciles the existing file', async t => {
  const f = await fixture(t); const { id, path } = await f.create();
  f.failAfterWrite = true;
  const lost = await f.form(path, f.fields(id, { intent: 'publish' }));
  assert.equal(lost.status, 503); assert.equal(f.row(id).status, 'publishing'); assert.equal(f.commits, 1);
  assert.equal((await f.form(path, f.fields(id, { body: 'Cannot change frozen text' }))).status, 409);
  const recovery = await f.form(`${path}/publish`, { version: f.row(id).version });
  assert.equal(recovery.status, 303); assert.equal(f.row(id).status, 'published'); assert.equal(f.commits, 1);
});

test('stale publication retries cannot unlock or complete a newer frozen version', async t => {
  for (const staleResult of ['collision', 'matching']) await t.test(staleResult, async t => {
    const f = await fixture(t); const { id, path } = await f.create();
    // Freeze by the public handler, retaining its real serialized GitHub content.
    f.failAfterWrite = true;
    assert.equal((await f.form(path, f.fields(id, { intent: 'publish' }))).status, 503);
    f.failAfterWrite = false;
    const originalVersion = f.row(id).version;
    const originalFile = f.files.get('content/post/from-the-train.md');
    f.files.clear();
    const reads = [];
    let started;
    f.readGitHub = () => new Promise(resolve => { reads.push(resolve); started?.(); });
    const start = async action => {
      const ready = new Promise(resolve => { started = resolve; });
      const result = action();
      await ready;
      return { result };
    };
    const a = await start(() => f.form(`${path}/publish`, { version: originalVersion }));
    const b = await start(() => f.form(`${path}/publish`, { version: originalVersion }));
    const collision = () => Response.json({ type: 'file', encoding: 'base64', content: Buffer.from('Different existing post').toString('base64') });
    reads[0](collision());
    assert.equal((await a.result).status, 409);
    assert.equal(f.row(id).status, 'draft');
    const c = await start(() => f.form(path, f.fields(id, { intent: 'publish', slug: 'new-slug', body: 'New private version' })));
    const frozen = { ...f.row(id) };
    assert.equal(frozen.status, 'publishing');
    assert.equal(frozen.version, originalVersion + 1);
    reads[1](staleResult === 'collision' ? collision() : Response.json({ type: 'file', encoding: 'base64', content: originalFile }));
    const stale = await b.result;
    assert.equal(stale.status, 409);
    assert.match(await stale.text(), /changed in another tab/);
    assert.deepEqual({ ...f.row(id) }, frozen);
    assert.equal((await f.form(path, f.fields(id, { body: 'Must stay private' }))).status, 409);
    reads[2](Response.json({}, { status: 404 }));
    assert.equal((await c.result).status, 303);
    assert.equal(f.row(id).status, 'published');
    assert.equal(f.row(id).version, frozen.version);
    assert.match(f.row(id).github_url, /new-slug\.md$/);
    assert.match(Buffer.from(f.files.get('content/post/new-slug.md'), 'base64').toString(), /New private version/);
  });
});

test('previews and unconfigured environments cannot publish even with a browser session', async t => {
  const f = await fixture(t); const { id, path } = await f.create();
  f.env.CF_PAGES_BRANCH = 'feature-branch';
  assert.equal((await f.form(path, f.fields(id, { intent: 'publish' }))).status, 503);
  f.env.CF_PAGES_BRANCH = 'develop'; delete f.env.PUBLISHING_GITHUB_TOKEN;
  assert.equal((await f.form(`${path}/publish`, { version: 1 })).status, 503);
  assert.equal(f.commits, 0); assert.equal(f.row(id).status, 'draft');
});
