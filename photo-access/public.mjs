const ID = /^[0-9a-f]{32}$/;
const escape = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const headers = { 'Cache-Control': 'no-store', 'CDN-Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' };

function previewImage(photo) {
  // Videos share their published JPEG poster, never an MP4 or an original.
  const image = new URL(photo.kind === 'video' ? photo.thumbnail : photo.src);
  if (image.protocol !== 'https:' || image.username || image.password || image.search || image.hash
      || image.pathname !== `/photos/${photo.id}/${photo.kind === 'video' ? '640' : '2560'}.jpg`) throw Error('Invalid public derivative');
  return image.href;
}

export const isPublished = photo => photo.published !== false && !photo.archived && !photo.archiving && !photo.deleting
  && [undefined, 'image', 'video'].includes(photo.kind);

function failure(request, status) {
  return new Response(request.method === 'HEAD' ? null : status === 404 ? 'This photograph isn’t available.' : 'The photographs couldn’t be loaded. Please try again.', {
    status, headers: { ...headers, 'Content-Type': 'text/plain; charset=utf-8', 'X-Robots-Tag': 'noindex' },
  });
}

export async function onRequest(context) {
  const { request, env } = context;
  const url = new URL(request.url);
  // The more-specific private Function owns these routes. Never fall through to
  // public metadata or static assets if this handler is called for one.
  if (url.pathname.startsWith('/photos/private/')) return failure(request, 404);
  const match = /^\/photos\/([0-9a-f]{32})\/?$/.exec(url.pathname);
  const legacy = ['/photos/', '/photos/2022/new-york/'].includes(url.pathname) && url.searchParams.get('photo');
  const collection = url.pathname === '/photos/' && !(legacy && ID.test(legacy));
  if (!collection && !match && !(legacy && ID.test(legacy))) return context.next();
  if (!['GET', 'HEAD'].includes(request.method)) return new Response(null, { status: 405, headers: { ...headers, Allow: 'GET, HEAD' } });
  const id = match?.[1] || legacy;
  if (!collection && (legacy || !url.pathname.endsWith('/'))) {
    const destination = new URL(`/photos/${id}/`, url);
    destination.search = url.search;
    destination.searchParams.delete('photo');
    if (url.pathname === '/photos/2022/new-york/') destination.hash = 'album/new-york-2022';
    return new Response(null, { status: 308, headers: { ...headers, Location: destination.href } });
  }
  try {
    // Only the current PUBLIC delivery catalog can select preview media. Do not
    // fetch visitor-supplied URLs, protected snapshots or the originals bucket.
    const object = await env.PUBLIC_PHOTOS.get('index.json');
    if (!object) return failure(request, 503);
    const catalog = JSON.parse(await object.text());
    if (catalog.version !== 1 || !Array.isArray(catalog.photos) || !Array.isArray(catalog.albums)) return failure(request, 503);
    const photos = catalog.photos.filter(isPublished);
    if (collection) {
      const previews = photos.slice(0, 4).map(photo => ({ image: previewImage(photo), alt: photo.alt || '' }));
      const shell = await env.ASSETS.fetch(new Request(new URL('/photos/', url)));
      if (shell.status !== 200 || !shell.headers.get('Content-Type')?.includes('text/html')) return failure(request, 503);
      let html = await shell.text();
      if (!/<head\b[^>]*>/i.test(html) || !/<\/head>/i.test(html)) return failure(request, 503);
      html = html.replace(/<meta\b[^>]*\b(?:name|property)=(?:"(?:og:image[^" ]*|twitter:image[^" ]*|twitter:card)"|'(?:og:image[^' ]*|twitter:image[^' ]*|twitter:card)'|(?:og:image[^\s>]*|twitter:image[^\s>]*|twitter:card))[^>]*>/gi, '')
        .replace(/<\/head>/i, () => previews.map(({ image, alt }) => `<meta property="og:image" content="${escape(image)}"><meta property="og:image:alt" content="${escape(alt)}">`).join('')
          + (previews.length ? `<meta name="twitter:card" content="summary_large_image"><meta name="twitter:image" content="${escape(previews[0].image)}"><meta name="twitter:image:alt" content="${escape(previews[0].alt)}">` : '<meta name="twitter:card" content="summary">') + '</head>');
      return new Response(request.method === 'HEAD' ? null : html, { headers: { ...headers, 'Content-Type': 'text/html; charset=utf-8' } });
    }
    const photo = photos.find(photo => photo.id === id);
    if (!photo) return failure(request, 404);
    const image = previewImage(photo);
    const shell = await env.ASSETS.fetch(new Request(new URL('/photos/', url)));
    if (shell.status !== 200 || !shell.headers.get('Content-Type')?.includes('text/html')) return failure(request, 503);
    let dateTitle = '';
    if (photo.capturedDate) {
      const date = new Date(`${photo.capturedDate}T00:00:00Z`), day = date.getUTCDate();
      const suffix = day % 100 >= 11 && day % 100 <= 13 ? 'th' : { 1: 'st', 2: 'nd', 3: 'rd' }[day % 10] || 'th';
      dateTitle = `${date.toLocaleString('en-US', { month: 'long', timeZone: 'UTC' })} ${day}${suffix}, ${date.getUTCFullYear()}`;
    }
    const title = `${photo.alt || dateTitle || (photo.kind === 'video' ? 'Video' : 'Photograph')} :: Hugo’s Blog`;
    const description = photo.caption || photo.alt || 'Photographs, videos and albums by Hugo.';
    const canonical = `https://hugo.md/photos/${id}/`;
    const metadata = {
      description, 'og:type': 'website', 'og:site_name': 'Hugo’s Blog', 'og:title': title, 'og:description': description,
      'og:url': canonical, 'og:image': image, 'og:image:alt': photo.alt || '',
      'twitter:card': 'summary_large_image', 'twitter:title': title, 'twitter:description': description,
      'twitter:image': image, 'twitter:image:alt': photo.alt || '',
    };
    let html = await shell.text();
    if (!/<head\b[^>]*>/i.test(html) || !/<\/head>/i.test(html)) return failure(request, 503);
    // The shell is our Hugo template (including its minified, unquoted attrs).
    // Replace its generic social block rather than leaving duplicate OG tags.
    html = html.replace(/<base\b[^>]*>|<title\b[^>]*>[\s\S]*?<\/title>/gi, '')
      .replace(/<meta\b[^>]*\b(?:name|property)=(?:"(?:description|og:[^"]*|twitter:[^"]*)"|'(?:description|og:[^']*|twitter:[^']*)'|(?:description|og:[^\s>]*|twitter:[^\s>]*))[^>]*>/gi, '')
      .replace(/<link\b[^>]*\brel=(?:"canonical"|'canonical'|canonical)[^>]*>/gi, '')
      .replace(/<head\b[^>]*>/i, '$&<base href="/photos/">')
      .replace(/<\/head>/i, () => `<title>${escape(title)}</title><link rel="canonical" href="${canonical}">${Object.entries(metadata).map(([name, value]) => `<meta ${name.startsWith('og:') ? 'property' : 'name'}="${name}" content="${escape(value)}">`).join('')}</head>`);
    return new Response(request.method === 'HEAD' ? null : html, { headers: { ...headers, 'Content-Type': 'text/html; charset=utf-8' } });
  } catch {
    return failure(request, 503);
  }
}
