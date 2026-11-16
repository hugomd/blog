import { isPublished } from './public.mjs';

const headers = { 'Cache-Control': 'no-store', 'CDN-Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'X-Robots-Tag': 'noindex' };
const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { ...headers, 'Content-Type': 'application/json' } });

// Anonymous, like-only counters for public photostream items. The browser
// remembers its own likes; a Cloudflare WAF rule rate-limits this path per IP.
// GET /photos/likes returns every count in one request, so browsing photos
// never touches the limit; POST /photos/likes/<id> adds a like.
export async function onRequest({ request, env }) {
  const url = new URL(request.url);
  const match = /^\/photos\/likes(?:\/([0-9a-f]{32}))?$/.exec(url.pathname);
  if (!match) return json(404, { error: 'Not found' });
  const id = match[1], allow = id ? 'POST' : 'GET';
  if (request.method !== allow) return new Response(null, { status: 405, headers: { ...headers, Allow: allow } });
  try {
    if (!id) {
      const { results } = await env.REACTIONS.prepare('SELECT photo_id, count FROM photo_likes').all();
      return json(200, { counts: Object.fromEntries(results.map(row => [row.photo_id, row.count])) });
    }
    // Browsers send Origin on every fetch POST, so this stops other sites
    // from casting likes through their visitors.
    if (request.headers.get('Origin') !== url.origin) return json(403, { error: 'Forbidden' });
    // Only items in the current public catalog can be liked, so arbitrary IDs
    // never create rows. Protected albums are in other buckets and never match.
    const object = await env.PUBLIC_PHOTOS.get('index.json');
    const catalog = object && JSON.parse(await object.text());
    if (catalog?.version !== 1 || !Array.isArray(catalog.photos)) return json(503, { error: 'Unavailable' });
    if (!catalog.photos.some(photo => photo.id === id && isPublished(photo))) return json(404, { error: 'Not found' });
    const count = await env.REACTIONS.prepare(
      'INSERT INTO photo_likes (photo_id, count) VALUES (?1, 1) ON CONFLICT(photo_id) DO UPDATE SET count = count + 1 RETURNING count',
    ).bind(id).first('count');
    return json(200, { count });
  } catch {
    return json(503, { error: 'Unavailable' });
  }
}
