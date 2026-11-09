const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: null, photos: [], index: 0, info: false, busy: false };
const stream = $('#stream'), viewer = $('#viewer'), info = $('#info');
let zoom = $('#zoom');
const duration = () => matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 420;
// The public manifest is the sole source of gallery content. Never fall back to
// repository photos: doing so could reintroduce archived/unpublished images.
async function load() {
  $('#retry').hidden = true;
  $('#status').hidden = false;
  $('#status').textContent = 'Loading photographs…';
  $('#status').classList.add('loading');
  $('#skeleton').hidden = false;
  stream.setAttribute('aria-busy', 'true');
  $('#albums').setAttribute('aria-busy', 'true');
  try {
    const response = await fetch(document.body.dataset.manifest, { cache: 'no-cache', credentials: 'omit' });
    if (response.status === 404) state.catalog = { version: 1, photos: [], albums: [] };
    else {
      if (!response.ok) throw new Error('Manifest unavailable');
      state.catalog = await response.json();
    }
    if (state.catalog.version !== 1 || !Array.isArray(state.catalog.photos) || !Array.isArray(state.catalog.albums)) throw new Error('Unsupported manifest');
    render();
  } catch {
    state.catalog = null;
    stream.innerHTML = ''; $('#albums').innerHTML = '';
    $('#status').textContent = 'The photographs couldn’t be loaded. Please try again.';
    $('#retry').hidden = false;
  } finally {
    $('#skeleton').hidden = true;
    $('#status').classList.remove('loading');
    stream.setAttribute('aria-busy', 'false');
    $('#albums').setAttribute('aria-busy', 'false');
  }
}

function albumURL(slug) { return `${$('#nav-stream').getAttribute('href')}#album/${encodeURIComponent(slug)}`; }
function render() {
  if (!state.catalog) return;
  const route = location.hash.slice(1) || (document.body.dataset.album ? `album/${encodeURIComponent(document.body.dataset.album)}` : 'stream');
  const isAlbums = route === 'albums', isAlbum = route.startsWith('album/');
  const photos = state.catalog.photos.filter(p => !p.archived && !p.deleting);
  const byID = new Map(photos.map(p => [p.id, p]));
  const album = isAlbum ? state.catalog.albums.find(a => `album/${encodeURIComponent(a.slug)}` === route) : null;
  $('#nav-stream').toggleAttribute('aria-current', !isAlbums && !isAlbum);
  $('#nav-albums').toggleAttribute('aria-current', isAlbums || isAlbum);
  $('#back').hidden = !isAlbum;
  $('#title').textContent = isAlbums ? 'Albums' : isAlbum ? album?.title || 'Album unavailable' : 'Photostream';
  $('#description').hidden = !isAlbums && !isAlbum;
  $('#description').textContent = isAlbums ? 'A story, a place, a weekend.' : isAlbum ? album?.description || '' : '';
  document.title = `${$('#title').textContent} · Hugo’s Blog`;
  stream.hidden = isAlbums;
  $('#albums').hidden = !isAlbums;
  $('#status').hidden = true;
  $('#retry').hidden = true;
  if (isAlbums) {
    $('#albums').innerHTML = state.catalog.albums.map(a => {
      const members = a.photoIds.filter(id => byID.has(id));
      const cover = byID.get(a.coverId) || byID.get(members[0]);
      return `<a class="album" href="${escape(albumURL(a.slug))}"><div class="photo">${cover ? `<img src="${escape(cover.thumbnail)}" width="${cover.width}" height="${cover.height}" alt="${escape(cover.alt)}" loading="lazy">` : '<span class="album-placeholder" aria-hidden="true">◧</span>'}</div><h2>${escape(a.title)}</h2><p>${members.length} photograph${members.length === 1 ? '' : 's'}</p></a>`;
    }).join('');
    if (!state.catalog.albums.length) message('No albums published yet.');
    return;
  }
  // Album order is curated; the stream includes every published photo, including
  // standalone photos, in the manifest's newest-first order.
  state.photos = isAlbum ? (album?.photoIds || []).map(id => byID.get(id)).filter(Boolean) : photos;
  stream.setAttribute('aria-label', isAlbum ? `${album?.title || 'Album'} photographs` : 'Photostream');
  stream.innerHTML = state.photos.map((p, i) => `<button class="photo" aria-label="Open ${escape(p.alt)}" data-photo="${i}"><img src="${escape(p.thumbnail)}" width="${p.width}" height="${p.height}" alt="${escape(p.alt)}" loading="${i < 4 ? 'eager' : 'lazy'}"></button>`).join('');
  if (!state.photos.length) message(isAlbum ? album ? 'No photographs in this album yet.' : 'This album isn’t currently published.' : 'No photographs published yet. Check back soon.');
}
function message(text) { $('#status').textContent = text; $('#status').hidden = false; }

function target() {
  const p = state.photos[state.index], narrow = innerWidth <= 540;
  const area = { x: narrow ? 12 : 64, y: 84, w: innerWidth - (narrow ? 24 : 128) - (state.info && !narrow ? 300 : 0), h: innerHeight - 160 - (state.info && narrow ? innerHeight * .38 : 0) };
  const scale = Math.min(area.w / p.width, area.h / p.height);
  const width = p.width * scale, height = p.height * scale;
  return { left: area.x + (area.w - width) / 2, top: area.y + (area.h - height) / 2, width, height };
}
function place() {
  const rect = target();
  for (const [key, value] of Object.entries(rect)) zoom.style[key] = `${value}px`;
  return rect;
}
function animatePhoto(thumbnail, full, closing = false) {
  const transform = `translate(${thumbnail.left + thumbnail.width / 2 - full.left - full.width / 2}px,${thumbnail.top + thumbnail.height / 2 - full.top - full.height / 2}px) scale(${thumbnail.width / full.width},${thumbnail.height / full.height})`;
  const frames = [{ transform }, { transform: 'none' }];
  // Scale/travel from the actual thumbnail bounds in both directions; no fade.
  return zoom.animate(closing ? frames.reverse() : frames, { duration: duration(), easing: 'cubic-bezier(.22,.75,.2,1)', fill: closing ? 'forwards' : 'none' }).finished;
}
function renderViewer(changePhoto = false) {
  const p = state.photos[state.index];
  if (changePhoto) {
    // Fresh pixels immediately, even when a previous full image is still loading.
    const display = new Image();
    display.id = 'zoom'; display.className = 'zoom'; display.alt = p.alt; display.src = p.thumbnail;
    zoom.replaceWith(display); zoom = display;
    const full = new Image(); full.src = p.src;
    full.decode().then(() => { if (zoom === display && viewer.open) display.src = full.src; }).catch(() => {});
  }
  $('#count').textContent = `${state.index + 1} / ${state.photos.length}`;
  $('#caption').textContent = [p.caption, p.capturedDate].filter(Boolean).join(' · ');
  $('#details').setAttribute('aria-pressed', state.info);
  info.hidden = !state.info;
  const fields = { ...p.exif, ...(p.capturedDate ? { 'Capture date': p.capturedDate } : {}), Dimensions: `${p.width} × ${p.height}` };
  info.innerHTML = `<h2>Behind the photo</h2><dl>${Object.entries(fields).map(([key, value]) => `<div><dt>${escape(key)}</dt><dd>${escape(value)}</dd></div>`).join('')}</dl>${p.tags?.length ? `<div class="tags" aria-label="Tags">${p.tags.map(tag => `<span class="tag">${escape(tag)}</span>`).join('')}</div>` : ''}`;
  $('#previous').disabled = $('#next').disabled = state.photos.length < 2;
  place();
}
async function open(index) {
  if (state.busy) return;
  state.busy = true; state.index = index;
  const thumb = stream.children[index].querySelector('img'), from = thumb.getBoundingClientRect();
  viewer.showModal(); document.body.style.overflow = 'hidden'; renderViewer(true);
  thumb.style.visibility = 'hidden';
  try { await animatePhoto(from, place()); }
  finally { state.busy = false; $('#close').focus(); }
}
async function close() {
  if (state.busy || !viewer.open) return;
  state.busy = true;
  const button = stream.children[state.index], thumb = button.querySelector('img');
  // Navigation/resize can move the thumbnail: use its current position.
  try { await animatePhoto(thumb.getBoundingClientRect(), zoom.getBoundingClientRect(), true); }
  finally {
    viewer.close(); zoom.getAnimations().forEach(a => a.cancel());
    thumb.style.visibility = ''; document.body.style.overflow = '';
    button.focus({ preventScroll: true }); state.busy = false;
  }
}
function step(delta) {
  if (state.busy) return;
  stream.children[state.index].querySelector('img').style.visibility = '';
  state.index = (state.index + delta + state.photos.length) % state.photos.length;
  stream.children[state.index].scrollIntoView({ block: 'nearest', behavior: 'instant' });
  stream.children[state.index].querySelector('img').style.visibility = 'hidden';
  renderViewer(true);
}
stream.addEventListener('click', event => {
  const button = event.target.closest('[data-photo]');
  if (button) open(Number(button.dataset.photo));
});
$('#close').onclick = close;
$('#previous').onclick = () => step(-1);
$('#next').onclick = () => step(1);
$('#details').onclick = () => { if (!state.busy) { state.info = !state.info; renderViewer(); } };
$('#retry').onclick = load;
$('.skip').onclick = event => { event.preventDefault(); $('#main').focus(); };
viewer.addEventListener('cancel', event => { event.preventDefault(); close(); });
viewer.addEventListener('keydown', event => {
  if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); step(event.key === 'ArrowLeft' ? -1 : 1); }
});
viewer.addEventListener('click', event => { if (event.target === viewer || event.target === zoom) close(); });
addEventListener('resize', () => { if (viewer.open) place(); });
addEventListener('hashchange', async () => {
  // Browser Back may arrive during the zoom. Keep its thumbnail in the DOM
  // until the transition and close have finished, then change collections.
  await Promise.allSettled(zoom.getAnimations().map(animation => animation.finished));
  if (viewer.open) await close();
  render();
});
load();
