import { isPublished } from './public.mjs';

const headers = { 'Cache-Control': 'no-store', 'CDN-Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'X-Robots-Tag': 'noindex' };
const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { ...headers, 'Content-Type': 'application/json' } });

// Anonymous, like-only counters for public photostream items. The browser
// remembers its own likes; a Cloudflare WAF rule rate-limits POSTs per IP.
export async function onRequest({ request, env }) {
  const url = new URL(request.url);
  const id = /^\/photos\/likes\/([0-9a-f]{32})$/.exec(url.pathname)?.[1];
  if (!id) return json(404, { error: 'Not found' });
  if (!['GET', 'POST'].includes(request.method)) return new Response(null, { status: 405, headers: { ...headers, Allow: 'GET, POST' } });
  try {
    if (request.method === 'GET') {
      const count = await env.REACTIONS.prepare('SELECT count FROM photo_likes WHERE photo_id = ?1').bind(id).first('count');
      return json(200, { count: count ?? 0 });
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
