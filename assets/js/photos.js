const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: null, photos: [], index: 0, info: false, busy: false };
const stream = $('#stream'), viewer = $('#viewer'), info = $('#info');
const privatePath = location.pathname.match(/^\/photos\/private\/[a-f0-9]{32}\//)?.[0];
let zoom = $('#zoom');
let swipeStart;
let loadRequest;
const duration = () => matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 420;

function clearPrivate() {
  loadRequest?.abort(); loadRequest = null;
  viewer.close(); zoom.getAnimations().forEach(animation => animation.cancel());
  zoom.removeAttribute('src'); zoom.alt = ''; info.innerHTML = ''; $('#caption').textContent = ''; $('#count').textContent = '';
  document.body.style.overflow = '';
  state.catalog = null; state.photos = []; state.busy = false; state.info = false;
  stream.innerHTML = ''; $('#albums').innerHTML = '';
  $('#album-intro').hidden = true; $('#title').textContent = ''; $('#description').textContent = '';
  $('#lock-album').hidden = true;
  $('#skeleton').hidden = true;
  $('#status').classList.remove('loading');
  stream.setAttribute('aria-busy', 'false'); $('#albums').setAttribute('aria-busy', 'false');
  $('#main').setAttribute('aria-label', 'Private album'); document.title = 'Private album · Hugo’s Blog';
}

function showUnlock(message = '') {
  clearPrivate();
  $('#album-unlock').hidden = false;
  $('#unlock-error').textContent = message;
  $('#status').hidden = true; $('#retry').hidden = true;
}
// The public manifest is the sole source of gallery content. Never fall back to
// repository photos: doing so could reintroduce archived/unpublished images.
async function load() {
  if (privatePath) clearPrivate();
  loadRequest?.abort();
  const request = new AbortController(); loadRequest = request;
  $('#album-unlock').hidden = true;
  $('#retry').hidden = true;
  $('#status').hidden = false;
  $('#status').textContent = 'Loading photographs…';
  $('#status').classList.add('loading');
  $('#skeleton').hidden = false;
  layoutPhotos();
  stream.setAttribute('aria-busy', 'true');
  $('#albums').setAttribute('aria-busy', 'true');
  try {
    const response = await fetch(privatePath ? `${privatePath}catalog` : document.body.dataset.manifest, { cache: privatePath ? 'no-store' : 'no-cache', credentials: privatePath ? 'same-origin' : 'omit', signal: request.signal });
    if (request.signal.aborted) return;
    if (privatePath && response.status === 401) { showUnlock(); return; }
    if (privatePath && response.status === 404) { message('This album isn’t available.'); return; }
    let catalog;
    if (response.status === 404) catalog = { version: 1, photos: [], albums: [] };
    else {
      if (!response.ok) throw new Error('Manifest unavailable');
      catalog = await response.json();
    }
    if (request.signal.aborted) return;
    if (catalog.version !== 1 || !Array.isArray(catalog.photos) || !Array.isArray(catalog.albums)) throw new Error('Unsupported manifest');
    state.catalog = catalog;
    $('#lock-album').hidden = !privatePath;
    render();
  } catch {
    if (request.signal.aborted) return;
    state.catalog = null;
    stream.innerHTML = ''; $('#albums').innerHTML = '';
    $('#status').textContent = 'The photographs couldn’t be loaded. Please try again.';
    $('#retry').hidden = false;
  } finally {
    if (loadRequest === request) {
      $('#skeleton').hidden = true;
      $('#status').classList.remove('loading');
      stream.setAttribute('aria-busy', 'false');
      $('#albums').setAttribute('aria-busy', 'false');
    }
  }
}

function albumURL(slug) { return `${$('#nav-stream').getAttribute('href').split('#')[0]}#album/${encodeURIComponent(slug)}`; }
function render() {
  const route = privatePath ? `album/${encodeURIComponent(state.catalog?.albums[0]?.slug || '')}` : location.hash.slice(1) || (document.body.dataset.album ? `album/${encodeURIComponent(document.body.dataset.album)}` : 'stream');
  const isAlbums = route === 'albums', isAlbum = route.startsWith('album/');
  stream.classList.toggle('list', !isAlbums && !isAlbum);
  $('#skeleton').classList.toggle('list', !isAlbums && !isAlbum);
  layoutPhotos();
  if (!state.catalog) return;
  const photos = state.catalog.photos.filter(p => !p.archived && !p.deleting);
  const byID = new Map(photos.map(p => [p.id, p]));
  const album = isAlbum ? state.catalog.albums.find(a => `album/${encodeURIComponent(a.slug)}` === route) : null;
  const title = isAlbums ? 'Albums' : isAlbum ? album?.title || 'Album unavailable' : 'Photostream';
  $('#nav-stream').setAttribute('aria-current', !isAlbums && !isAlbum ? 'page' : 'false');
  $('#nav-albums').setAttribute('aria-current', !privatePath && (isAlbums || isAlbum) ? 'page' : 'false');
  $('#album-intro').hidden = !isAlbum;
  $('#title').textContent = title;
  $('#description').hidden = !isAlbum || !album?.description;
  $('#description').textContent = isAlbum ? album?.description || '' : '';
  $('#main').setAttribute('aria-label', title);
  document.title = `${title} · Hugo’s Blog`;
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
  stream.innerHTML = state.photos.map((p, i) => `<button class="photo" style="--ratio:${p.width / p.height}" aria-label="Open ${escape(p.alt)}" data-photo="${i}"><img src="${escape(p.thumbnail)}" ${isAlbum ? '' : `srcset="${escape(p.thumbnail)} 640w, ${escape(p.src)} 2560w" sizes="(max-width:700px) 100vw, (max-width:900px) 50vw, 33vw"`} width="${p.width}" height="${p.height}" alt="${escape(p.alt)}" loading="${i < (isAlbum ? 4 : 3) ? 'eager' : 'lazy'}"></button>`).join('');
  layoutPhotos();
  if (!state.photos.length) message(isAlbum ? album ? 'No photographs in this album yet.' : 'This album isn’t currently published.' : 'No photographs published yet. Check back soon.');
}

function layoutPhotos() {
  for (const grid of [stream, $('#skeleton')]) {
    if (grid.hidden) continue;
    const width = grid.firstElementChild?.getBoundingClientRect().width;
    const gap = parseFloat(getComputedStyle(grid).columnGap);
    for (const photo of grid.children) {
      const ratio = Number(getComputedStyle(photo).getPropertyValue('--ratio')) || 4 / 3;
      // Tiny row spans pack each column independently without reordering the DOM.
      photo.style.gridRowEnd = `span ${Math.ceil(width / ratio + gap)}`;
      const image = photo.querySelector('img[srcset]');
      if (image) image.sizes = `${Math.ceil(width)}px`;
    }
  }
}
function message(text) { $('#status').textContent = text; $('#status').hidden = false; }

function formatDate(value) {
  if (!value) return '';
  const date = new Date(`${value}T00:00:00Z`), day = date.getUTCDate();
  const suffix = day % 100 >= 11 && day % 100 <= 13 ? 'th' : { 1: 'st', 2: 'nd', 3: 'rd' }[day % 10] || 'th';
  return `${date.toLocaleString('en-US', { month: 'long', timeZone: 'UTC' })} ${day}${suffix}, ${date.getUTCFullYear()}`;
}

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
  const date = formatDate(p.capturedDate);
  $('#caption').innerHTML = [escape(p.caption), date ? `<em><time datetime="${escape(p.capturedDate)}">${date}</time></em>` : ''].filter(Boolean).join(' · ');
  $('#details').setAttribute('aria-pressed', state.info);
  info.hidden = !state.info;
  const fields = { ...p.exif, ...(date ? { 'Capture date': date } : {}), Dimensions: `${p.width} × ${p.height}` };
  info.innerHTML = `<dl>${Object.entries(fields).map(([key, value]) => `<div><dt>${escape(key)}</dt><dd>${key === 'Capture date' ? `<em>${escape(value)}</em>` : escape(value)}</dd></div>`).join('')}</dl>${p.tags?.length ? `<div class="tags" aria-label="Tags">${p.tags.map(tag => `<span class="tag">${escape(tag)}</span>`).join('')}</div>` : ''}`;
  $('#previous').disabled = $('#next').disabled = state.photos.length < 2;
  place();
}
async function open(index) {
  if (state.busy) return;
  state.busy = true; state.index = index;
  const thumb = stream.children[index].querySelector('img'), from = thumb.getBoundingClientRect();
  viewer.showModal(); viewer.focus({ preventScroll: true });
  document.body.style.overflow = 'hidden'; renderViewer(true);
  thumb.style.visibility = 'hidden';
  try { await animatePhoto(from, place()); }
  finally { state.busy = false; }
}
async function close(event) {
  if (state.busy || !viewer.open) return;
  state.busy = true;
  const button = stream.children[state.index], thumb = button.querySelector('img');
  const touch = event?.pointerType === 'touch' || (event?.detail > 0 && matchMedia('(pointer: coarse)').matches);
  // Navigation/resize can move the thumbnail: use its current position.
  try { await animatePhoto(thumb.getBoundingClientRect(), zoom.getBoundingClientRect(), true); }
  finally {
    viewer.close(); zoom.getAnimations().forEach(a => a.cancel());
    thumb.style.visibility = ''; document.body.style.overflow = '';
    // Closing a dialog can restore focus automatically, so touch needs an
    // explicit blur. Keyboard users still return to their selected thumbnail.
    if (touch) button.blur();
    else button.focus({ preventScroll: true });
    state.busy = false;
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
$('#album-login').onsubmit = async event => {
  event.preventDefault();
  if (!privatePath) return;
  const form = event.target, button = form.querySelector('button');
  button.disabled = true; $('#unlock-error').textContent = '';
  try {
    const response = await fetch(`${privatePath}login`, { method: 'POST', credentials: 'same-origin', cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: new FormData(form).get('password') }) });
    if (!response.ok) {
      const message = response.status === 401 ? 'Incorrect password.' : response.status === 429 ? 'Too many attempts. Please try again in a minute.' : response.status === 404 ? 'This album isn’t available.' : 'The album couldn’t be unlocked. Please try again.';
      showUnlock(message);
      return;
    }
    await load();
  } catch {
    showUnlock('The album couldn’t be unlocked. Please try again.');
  } finally { form.reset(); button.disabled = false; }
};
$('#lock-album').onclick = async () => {
  if (!privatePath) return;
  showUnlock();
  const button = $('#album-login button');
  button.disabled = true;
  try {
    const response = await fetch(`${privatePath}logout`, { method: 'POST', credentials: 'same-origin', cache: 'no-store' });
    if (!response.ok) throw new Error('Logout failed');
  } catch { $('#unlock-error').textContent = 'Couldn’t end the session. Retry locking before leaving a shared device.'; $('#lock-album').hidden = false; }
  finally { button.disabled = false; }
};
$('.skip').onclick = event => { event.preventDefault(); $('#main').focus(); };
viewer.addEventListener('cancel', event => { event.preventDefault(); close(); });
viewer.addEventListener('keydown', event => {
  if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); step(event.key === 'ArrowLeft' ? -1 : 1); }
});
viewer.addEventListener('touchstart', event => {
  swipeStart = innerWidth <= 700 && event.touches.length === 1 && (event.target === viewer || event.target === zoom) ? event.touches[0] : null;
}, { passive: true });
viewer.addEventListener('touchend', event => {
  if (!swipeStart) return;
  const dx = event.changedTouches[0].clientX - swipeStart.clientX, dy = event.changedTouches[0].clientY - swipeStart.clientY;
  swipeStart = null;
  if (Math.abs(dx) < 50 || Math.abs(dx) <= Math.abs(dy) * 1.5) return;
  // Suppress the synthesized tap so swiping never also closes the viewer.
  event.preventDefault();
  step(dx < 0 ? 1 : -1);
}, { passive: false });
viewer.addEventListener('touchcancel', () => { swipeStart = null; });
viewer.addEventListener('click', event => { if (event.target === viewer || event.target === zoom) close(event); });
addEventListener('resize', () => { layoutPhotos(); if (viewer.open) place(); });
addEventListener('hashchange', async () => {
  // Browser Back may arrive during the zoom. Keep its thumbnail in the DOM
  // until the transition and close have finished, then change collections.
  await Promise.allSettled(zoom.getAnimations().map(animation => animation.finished));
  if (viewer.open) await close();
  render();
});
addEventListener('pageshow', event => { if (privatePath && event.persisted) load(); });
render();
load();
