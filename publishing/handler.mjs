import { createRemoteJWKSet, jwtVerify } from 'jose';
import { normalizeDraft, validateDraft, serializePost } from './content.mjs';
import { escape, page, draftPage } from './ui.mjs';

const CLIENT = 'https://ia.net/writer';
const CALLBACK = 'https://ia.net/writer/indieauth/redirect';
const PREFIX = '/publishing';
const LIMIT = 256 * 1024;
const encoder = new TextEncoder();
const seconds = () => Math.floor(Date.now() / 1000);
const random = () => [...crypto.getRandomValues(new Uint8Array(32))].map(byte => byte.toString(16).padStart(2, '0')).join('');
const digest = async text => [...new Uint8Array(await crypto.subtle.digest('SHA-256', encoder.encode(text)))].map(byte => byte.toString(16).padStart(2, '0')).join('');
const b64 = bytes => btoa(Array.from(bytes, byte => String.fromCharCode(byte)).join(''));
const b64url = bytes => b64(bytes).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const fail = (status, message) => { throw Object.assign(new Error(message), { status }); };
const first = value => Array.isArray(value) ? value[0] : value;

function response(status, value, type = 'application/json', extra = {}) {
  return new Response(value === null ? null : type === 'application/json' ? JSON.stringify(value) : value, { status, headers: {
    'Content-Type': `${type}; charset=utf-8`, 'Cache-Control': 'private, no-store',
    'CDN-Cache-Control': 'no-store', 'Cloudflare-CDN-Cache-Control': 'no-store',
    'Referrer-Policy': 'same-origin', 'X-Robots-Tag': 'noindex, nofollow, noarchive',
    'X-Content-Type-Options': 'nosniff', 'X-Frame-Options': 'DENY',
    'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
    ...extra,
  } });
}
const redirect = location => response(303, null, 'text/plain', { Location: location });

async function readBody(request) {
  if (Number(request.headers.get('Content-Length')) > LIMIT) fail(413, 'Drafts must be smaller than 256 KiB.');
  const reader = request.body?.getReader();
  if (!reader) return '';
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > LIMIT) { await reader.cancel(); fail(413, 'Drafts must be smaller than 256 KiB.'); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const data = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { data.set(chunk, offset); offset += chunk.byteLength; }
  try { return new TextDecoder('utf-8', { fatal: true }).decode(data); } catch { fail(400, 'Use UTF-8 text.'); }
}

async function payload(request) {
  const raw = await readBody(request);
  const type = request.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  if (type === 'application/json') {
    try { const parsed = JSON.parse(raw); if (record(parsed)) return parsed; } catch { /* Report malformed input without echoing it. */ }
    fail(400, 'Expected a JSON object.');
  }
  if (type === 'application/x-www-form-urlencoded') {
    const values = {};
    for (const [key, value] of new URLSearchParams(raw)) (values[key.replace(/\[\]$/, '')] ??= []).push(value);
    return values;
  }
  fail(415, 'Send text as JSON or URL-encoded form data. Media uploads are not supported yet.');
}

async function owner(request, env) {
  if (!env.PUBLISHING_ACCESS_TEAM || !env.PUBLISHING_ACCESS_AUD || !env.PUBLISHING_OWNER_EMAIL) fail(503, 'Browser login is not configured.');
  if (!/^[a-z0-9-]+\.cloudflareaccess\.com$/.test(env.PUBLISHING_ACCESS_TEAM)) fail(503, 'Browser login is not configured.');
  const token = request.headers.get('Cf-Access-Jwt-Assertion');
  if (!token) fail(401, 'Sign in through Cloudflare Access to review your drafts.');
  try {
    const issuer = `https://${env.PUBLISHING_ACCESS_TEAM}`;
    const keys = createRemoteJWKSet(new URL(`${issuer}/cdn-cgi/access/certs`));
    const { payload: claims } = await jwtVerify(token, keys, { issuer, audience: env.PUBLISHING_ACCESS_AUD, algorithms: ['RS256'], requiredClaims: ['exp', 'sub', 'email'] });
    if (typeof claims.email !== 'string' || claims.email.toLowerCase() !== env.PUBLISHING_OWNER_EMAIL.toLowerCase()) fail(403, 'This writing desk belongs to another account.');
  } catch (error) {
    if (error.status === 403) throw error;
    fail(401, 'Your browser login could not be verified. Sign in again.');
  }
}

async function nativeToken(request, data, db) {
  const token = request.headers.get('Authorization')?.match(/^Bearer ([a-f0-9]{64})$/i)?.[1] || first(data.access_token);
  if (typeof token !== 'string' || !/^[a-f0-9]{64}$/.test(token)) fail(401, 'Connect your writing app again.');
  const stored = await db.prepare('SELECT * FROM tokens WHERE hash = ? AND expires_at > ?').bind(await digest(token), seconds()).first();
  if (!stored) fail(401, 'The app token expired or was revoked. Connect again.');
  return stored;
}

function publishEnabled(env) {
  return env.PUBLISHING_ALLOW_PUBLISH === 'true' && Boolean(env.PUBLISHING_GITHUB_TOKEN)
    && (!env.CF_PAGES_BRANCH || env.CF_PAGES_BRANCH === (env.PUBLISHING_GITHUB_BRANCH || 'develop'));
}

async function publish(row, env, db) {
  if (!publishEnabled(env)) fail(503, 'GitHub publishing is disabled in this environment.');
  if (row.status === 'published') return;
  const draft = validateDraft(JSON.parse(row.content));
  const file = serializePost(draft);
  if (row.status === 'draft') {
    const locked = await db.prepare("UPDATE drafts SET status = 'publishing' WHERE id = ? AND version = ? AND status = 'draft' RETURNING id").bind(row.id, row.version).first();
    if (!locked) fail(409, 'This draft changed in another tab. Reload before publishing.');
  }
  const repository = env.PUBLISHING_GITHUB_REPOSITORY || 'hugomd/blog';
  const branch = env.PUBLISHING_GITHUB_BRANCH || 'develop';
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository)) fail(503, 'Invalid GitHub repository configuration.');
  const path = `content/post/${draft.slug}.md`;
  const endpoint = `https://api.github.com/repos/${repository}/contents/${path}`;
  const headers = { Authorization: `Bearer ${env.PUBLISHING_GITHUB_TOKEN}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'hugomd-blog-publishing' };
  const expected = b64(encoder.encode(file));
  async function existing() {
    const result = await fetch(`${endpoint}?ref=${encodeURIComponent(branch)}`, { headers, redirect: 'manual', signal: AbortSignal.timeout(15000) });
    if (result.status === 404) return false;
    if (!result.ok) fail(502, 'GitHub could not confirm this post. Your draft is saved; retry publication.');
    const data = await result.json();
    if (data.type !== 'file' || data.encoding !== 'base64' || data.content?.replace(/\s/g, '') !== expected) {
      const unlocked = await db.prepare("UPDATE drafts SET status = 'draft' WHERE id = ? AND version = ? AND status = 'publishing' RETURNING id").bind(row.id, row.version).first();
      if (!unlocked) fail(409, 'This draft changed in another tab. Reload to check its publication status.');
      fail(409, 'That filename already exists on GitHub with different content. Choose a different slug; no file was overwritten.');
    }
    return true;
  }
  if (!await existing()) {
    const created = await fetch(endpoint, { method: 'PUT', headers: { ...headers, 'Content-Type': 'application/json' }, redirect: 'manual', signal: AbortSignal.timeout(15000),
      body: JSON.stringify({ message: `Publish: ${draft.title}`, content: expected, branch }) });
    if (created.status !== 201) {
      // A racing retry may already have created this exact file. Never send a sha
      // to the Contents API: this bridge cannot overwrite an existing post.
      if (![409, 422].includes(created.status) || !await existing()) fail(502, 'GitHub did not confirm publication. Your draft is frozen safely; retry to check for the commit.');
    }
  }
  const githubURL = `https://github.com/${repository}/blob/${encodeURIComponent(branch)}/${path}`;
  const completed = await db.prepare("UPDATE drafts SET status = 'published', github_url = ?, updated_at = ? WHERE id = ? AND version = ? AND status = 'publishing' RETURNING id").bind(githubURL, new Date().toISOString(), row.id, row.version).first();
  if (!completed) fail(409, 'This draft changed in another tab. Reload to check its publication status.');
}

export async function onRequest({ request, env }) {
  let base = PREFIX;
  let admin = false;
  try {
    const configured = new URL(env.PUBLISHING_ORIGIN);
    if (configured.protocol !== 'https:' || configured.pathname !== '/' || configured.search || configured.hash || configured.username || configured.password) fail(503, 'Publishing origin is not configured.');
    const url = new URL(request.url);
    if (url.origin !== configured.origin) fail(404, 'Publishing is not available on this hostname.');
    base = `${configured.origin}${PREFIX}`;
    const identity = `${base}/`;
    const path = url.pathname.slice(PREFIX.length) || '/';
    if (!url.pathname.startsWith(`${PREFIX}/`) && url.pathname !== PREFIX) fail(404, 'Not found.');
    admin = path === '/admin' || path.startsWith('/admin/');
    const db = env.PUBLISHING;
    if (!db) fail(503, 'The publishing database is not configured.');
    const html = (title, body, status = 200) => response(status, page(title, body, base), 'text/html', {
      Link: `<${base}/micropub>; rel="micropub", <${base}/auth>; rel="authorization_endpoint", <${base}/token>; rel="token_endpoint", <${base}/metadata>; rel="indieauth-metadata"`,
    });
    if (request.method === 'HEAD') {
      const result = await onRequest({ request: new Request(request.url, { headers: request.headers }), env });
      return new Response(null, { status: result.status, headers: result.headers });
    }
    if (admin) {
      await owner(request, env);
      if (request.method === 'POST' && request.headers.get('Origin') !== configured.origin) fail(403, 'Submit changes from this writing desk, not another website.');
    }
    if (path === '/metadata' && request.method === 'GET') return response(200, {
      issuer: identity, authorization_endpoint: `${base}/auth`, token_endpoint: `${base}/token`,
      response_types_supported: ['code'], grant_types_supported: ['authorization_code'],
      code_challenge_methods_supported: ['S256'], token_endpoint_auth_methods_supported: ['none'],
      scopes_supported: ['create'], authorization_response_iss_parameter_supported: true,
    });
    if (path === '/' && request.method === 'GET') return html('Connect iA Writer', `<p class="eyebrow">Hugo’s Blog · Publishing</p><h1>A small desk for your words.</h1><p>Write in iA Writer, send a draft, and review it here before publishing.</p><h2>Connect once</h2><p>In iA Writer, add a Micropub account with this website URL:</p><pre>${escape(identity)}</pre><p><a class="button" href="${base}/admin/">Open your writing desk</a></p><p class="muted">Text and Markdown only for now. Hugo shortcodes are kept as written. Draft content is never exposed by this public connection page.</p>`);
    if (path === '/auth' && request.method === 'GET') {
      const params = url.searchParams;
      if (params.get('client_id') !== CLIENT || params.get('redirect_uri') !== CALLBACK || params.get('response_type') !== 'code'
        || !params.get('state') || params.get('state').length > 2048 || !params.get('scope')?.split(' ').includes('create')
        || (params.has('me') && params.get('me') !== identity)) fail(400, 'Start the connection from iA Writer using this publishing website URL.');
      const challenge = params.get('code_challenge') || '';
      const method = params.get('code_challenge_method') || '';
      if ((challenge && (method !== 'S256' || !/^[\w-]{43}$/.test(challenge))) || (!challenge && method)) fail(400, 'Unsupported PKCE challenge.');
      // Cloudflare supplies this header at the edge. Never fall back to a
      // caller-controlled forwarded address or store the raw IP in D1.
      const ip = request.headers.get('CF-Connecting-IP');
      if (!ip || ip.length > 45 || !/^[a-f0-9:.]+$/i.test(ip)) fail(503, 'Connection admission requires a Cloudflare client address.');
      const now = seconds();
      await db.prepare('DELETE FROM auth_admissions WHERE expires_at <= ?').bind(now).run();
      const admitted = await db.prepare(`INSERT INTO auth_admissions (client_hash, attempts, expires_at) VALUES (?, 1, ?)
        ON CONFLICT(client_hash) DO UPDATE SET
          attempts = CASE WHEN expires_at <= ? THEN 1 ELSE attempts + 1 END,
          expires_at = CASE WHEN expires_at <= ? THEN excluded.expires_at ELSE expires_at END
        WHERE expires_at <= ? OR attempts < 5 RETURNING client_hash`)
        .bind(await digest(`${CLIENT}\n${ip}`), now + 300, now, now, now).first();
      if (!admitted) fail(429, 'Too many connection attempts from this address. Try again in five minutes.');
      const id = random();
      await db.prepare('DELETE FROM auth_requests WHERE expires_at <= ?').bind(now).run();
      const inserted = await db.prepare('INSERT INTO auth_requests (id, state, client_id, redirect_uri, challenge, challenge_method, expires_at) SELECT ?, ?, ?, ?, ?, ?, ? WHERE (SELECT count(*) FROM auth_requests) < 100 RETURNING id')
        .bind(await digest(id), params.get('state'), CLIENT, CALLBACK, challenge, method, now + 300).first();
      if (!inserted) fail(429, 'Too many pending connections. Try again in five minutes.');
      return redirect(`${base}/admin/authorize?request=${id}`);
    }
    if (path === '/token' && request.method === 'POST') {
      const data = await payload(request);
      const code = first(data.code);
      if (code) {
        if (typeof code !== 'string' || !/^[a-f0-9]{64}$/.test(code) || first(data.grant_type) !== 'authorization_code') fail(400, 'Invalid authorization grant.');
        const hash = await digest(code);
        const grant = await db.prepare('SELECT * FROM auth_codes WHERE hash = ? AND expires_at > ?').bind(hash, seconds()).first();
        if (!grant || first(data.client_id) !== grant.client_id || first(data.redirect_uri) !== grant.redirect_uri) fail(400, 'Invalid authorization grant.');
        const verifier = first(data.code_verifier);
        if (grant.challenge && (typeof verifier !== 'string' || !/^[\w.~-]{43,128}$/.test(verifier) || b64url(new Uint8Array(await crypto.subtle.digest('SHA-256', encoder.encode(verifier)))) !== grant.challenge)) fail(400, 'Invalid PKCE verifier.');
        const consumed = await db.prepare('DELETE FROM auth_codes WHERE hash = ? AND expires_at > ? RETURNING hash').bind(hash, seconds()).first();
        if (!consumed) fail(400, 'This authorization code was already used or expired.');
        const token = random();
        const lifetime = 90 * 24 * 60 * 60;
        await db.prepare('DELETE FROM tokens WHERE expires_at <= ?').bind(seconds()).run();
        await db.prepare('INSERT INTO tokens (hash, client_id, created_at, expires_at) VALUES (?, ?, ?, ?)').bind(await digest(token), grant.client_id, seconds(), seconds() + lifetime).run();
        return response(200, { access_token: token, token_type: 'Bearer', me: identity, scope: 'create', expires_in: lifetime });
      }
      const stored = await nativeToken(request, data, db);
      return response(200, { me: identity, client_id: stored.client_id, scope: 'create', expires_in: stored.expires_at - seconds() });
    }
    if (path === '/token' && request.method === 'GET') {
      const stored = await nativeToken(request, {}, db);
      return response(200, { me: identity, client_id: stored.client_id, scope: 'create', expires_in: stored.expires_at - seconds() });
    }
    if (path === '/micropub' && ['GET', 'POST'].includes(request.method)) {
      const data = request.method === 'POST' ? await payload(request) : {};
      await nativeToken(request, data, db);
      const query = url.searchParams.get('q') || first(data.q);
      if (query === 'config') return response(200, { 'syndicate-to': [], 'post-types': [{ type: 'article', name: 'Private Markdown draft', properties: ['name', 'content', 'category'] }] });
      if (query === 'syndicate-to') return response(200, { 'syndicate-to': [] });
      if (request.method !== 'POST' || query || data.action) fail(400, 'This endpoint accepts new drafts only, not updates or deletes.');
      const props = data.properties || data;
      if (!record(props)) fail(400, 'Expected post properties.');
      const allowed = ['name', 'content', 'category', 'post-status', 'h', 'type', 'access_token'];
      if (Object.keys(props).some(key => !allowed.includes(key))) fail(400, 'Unsupported post property. Send a text-only draft.');
      if (first(props['post-status']) && first(props['post-status']) !== 'draft') fail(400, 'Posts must be submitted as drafts for browser review.');
      let content = first(props.content);
      if (record(content)) {
        if (typeof content.value !== 'string' || content.html !== undefined) fail(415, 'Send Markdown, not an HTML-only post.');
        content = content.value;
      }
      const id = random();
      const source = { title: first(props.name) || '', content, categories: props.category === undefined ? [] : Array.isArray(props.category) ? props.category : [props.category] };
      const draft = normalizeDraft(source, new Date().toISOString(), id.slice(0, 12));
      const hash = await digest(JSON.stringify(source));
      const now = new Date().toISOString();
      await db.prepare('INSERT INTO drafts (id, source_hash, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(source_hash) DO NOTHING').bind(id, hash, JSON.stringify(draft), now, now).run();
      const saved = await db.prepare('SELECT id FROM drafts WHERE source_hash = ?').bind(hash).first();
      return response(201, null, 'application/json', { Location: `${base}/admin/drafts/${saved.id}` });
    }
    if (path === '/admin/authorize' && ['GET', 'POST'].includes(request.method)) {
      const data = request.method === 'POST' ? await payload(request) : {};
      const id = request.method === 'GET' ? url.searchParams.get('request') : first(data.request);
      if (!/^[a-f0-9]{64}$/.test(id || '')) fail(400, 'Invalid connection request. Start again in iA Writer.');
      const hash = await digest(id);
      const pending = await db.prepare('SELECT * FROM auth_requests WHERE id = ? AND expires_at > ?').bind(hash, seconds()).first();
      if (!pending) fail(400, 'This connection expired. Start again in iA Writer.');
      if (request.method === 'GET') return html('Connect iA Writer', `<p class="eyebrow">App connection</p><h1>Let iA Writer send drafts.</h1><p>It can create private drafts here. It cannot read drafts or publish to GitHub.</p><p>App: <code>${CLIENT}</code><br>Return address: <code>${CALLBACK}</code></p><form method="post" action="${base}/admin/authorize"><input type="hidden" name="request" value="${id}"><button class="primary">Connect and return to iA Writer</button></form>`);
      const consumed = await db.prepare('DELETE FROM auth_requests WHERE id = ? AND expires_at > ? RETURNING id').bind(hash, seconds()).first();
      if (!consumed) fail(400, 'This connection was already approved or expired.');
      const code = random();
      await db.prepare('DELETE FROM auth_codes WHERE expires_at <= ?').bind(seconds()).run();
      await db.prepare('INSERT INTO auth_codes (hash, client_id, redirect_uri, challenge, challenge_method, expires_at) VALUES (?, ?, ?, ?, ?, ?)').bind(await digest(code), pending.client_id, pending.redirect_uri, pending.challenge, pending.challenge_method, seconds() + 60).run();
      const callback = new URL(pending.redirect_uri);
      callback.searchParams.set('code', code); callback.searchParams.set('state', pending.state); callback.searchParams.set('iss', identity);
      return response(303, null, 'text/plain', { Location: callback.href, 'Content-Security-Policy': "default-src 'none'; frame-ancestors 'none'" });
    }
    if (['/admin', '/admin/'].includes(path) && request.method === 'GET') {
      const { results: drafts } = await db.prepare('SELECT * FROM drafts ORDER BY updated_at DESC LIMIT 100').all();
      const { results: tokens } = await db.prepare('SELECT * FROM tokens WHERE expires_at > ? ORDER BY created_at DESC').bind(seconds()).all();
      return html('Your drafts', `<p class="eyebrow">Hugo’s Blog · Writing</p><h1>Your words, at your pace.</h1><p>Send a draft from iA Writer, then finish the details here.</p><h2>Recent drafts</h2>${drafts.length ? drafts.map(row => `<a class="draft" href="${base}/admin/drafts/${row.id}"><strong>${escape(JSON.parse(row.content).title)}</strong><small>${escape(row.status)} · ${escape(row.updated_at.slice(0, 16).replace('T', ' '))} UTC</small></a>`).join('') : '<p class="notice">No drafts yet. In iA Writer, publish a new draft to your connected account. It will appear here without touching GitHub.</p>'}<h2>Connected apps</h2>${tokens.length ? tokens.map(token => `<div class="token"><span>iA Writer<br><small>Connected ${new Date(token.created_at * 1000).toISOString().slice(0, 10)} · expires ${new Date(token.expires_at * 1000).toISOString().slice(0, 10)}</small></span><form method="post" action="${base}/admin/tokens/${token.hash}/revoke"><button>Disconnect</button></form></div>`).join('') : '<p class="muted">No connected apps. <a href="'+base+'/">Connection instructions</a></p>'}`);
    }
    const revoke = path.match(/^\/admin\/tokens\/([a-f0-9]{64})\/revoke$/);
    if (revoke && request.method === 'POST') {
      await db.prepare('DELETE FROM tokens WHERE hash = ?').bind(revoke[1]).run();
      return redirect(`${base}/admin/`);
    }
    const match = path.match(/^\/admin\/drafts\/([a-f0-9]{64})(\/publish)?$/);
    if (match && ['GET', 'POST'].includes(request.method)) {
      let row = await db.prepare('SELECT * FROM drafts WHERE id = ?').bind(match[1]).first();
      if (!row) fail(404, 'Draft not found.');
      if (request.method === 'GET' && !match[2]) {
        let postFile, postError;
        try { postFile = serializePost(JSON.parse(row.content)); } catch (error) {
          if (error.status !== 400) throw error;
          postError = `Correct the post details before publication: ${error.message}`;
        }
        return html('Review draft', draftPage({ ...row, postFile, postError, saved: url.searchParams.get('saved') === '1' }, base, publishEnabled(env)));
      }
      if (request.method === 'POST') {
        const data = await payload(request);
        const version = Number(first(data.version));
        if (!Number.isSafeInteger(version) || version !== row.version) fail(409, 'This draft changed in another tab. Reload before saving.');
        if (match[2]) {
          await publish(row, env, db);
        } else {
          if (row.status !== 'draft') fail(409, 'This draft is frozen or already published. Reload to check its status.');
          const intent = first(data.intent);
          if (!['save', 'publish'].includes(intent)) fail(400, 'Choose Save draft or Publish to blog.');
          if (intent === 'publish' && !publishEnabled(env)) fail(503, 'GitHub publishing is disabled in this environment.');
          const previous = JSON.parse(row.content);
          const body = first(data.body);
          const input = { title: first(data.title), slug: first(data.slug), date: first(data.date), description: first(data.description) || '', body: typeof body === 'string' && body.replaceAll('\r\n', '\n') === previous.body.replaceAll('\r\n', '\n') ? previous.body : body, tags: (first(data.tags) || '').split(/\r?\n/).map(tag => tag.trim()).filter(Boolean), cover: first(data.cover) || '', toc: first(data.toc) === 'true' };
          let draft;
          try { draft = validateDraft(input); } catch (error) {
            if (error.status !== 400) throw error;
            return html('Correct draft details', draftPage({ ...row, content: JSON.stringify(input), error: error.message, postError: 'Save valid details to regenerate the post file.' }, base, publishEnabled(env)), 400);
          }
          const saved = await db.prepare("UPDATE drafts SET content = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND status = 'draft' RETURNING *").bind(JSON.stringify(draft), new Date().toISOString(), row.id, version).first();
          if (!saved) fail(409, 'This draft changed in another tab. Reload before saving.');
          row = saved;
          if (intent === 'publish') await publish(row, env, db);
          else return redirect(`${base}/admin/drafts/${row.id}?saved=1`);
        }
        return redirect(`${base}/admin/drafts/${row.id}`);
      }
    }
    fail(404, 'Not found.');
  } catch (error) {
    const status = Number.isInteger(error.status) ? error.status : 503;
    const message = error.status ? error.message : 'The writing desk could not finish this request. If you were publishing, reopen the draft and retry to check GitHub before making any changes.';
    if (!error.status) console.error(JSON.stringify({ component: 'publishing', status, message: 'Request failed; credentials and draft content omitted.' }));
    if (admin) return response(status, page('Could not finish', `<p class="eyebrow">Writing desk</p><h1>Let’s keep your words safe.</h1><div class="notice error" role="alert">${escape(message)}</div><p><a href="${base}/admin/">Return to your drafts</a></p>`, base), 'text/html');
    return response(status, { error: status === 401 ? 'unauthorized' : 'invalid_request', error_description: message }, 'application/json', status === 401 ? { 'WWW-Authenticate': 'Bearer' } : {});
  }
}
