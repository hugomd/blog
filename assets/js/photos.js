const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: null, photos: [], index: 0, info: false, busy: false };
const stream = $('#stream'), viewer = $('#viewer'), info = $('#info');
const privatePath = location.pathname.match(/^\/photos\/private\/[a-f0-9]{32}\//)?.[0];
const linkAccess = document.body.dataset.privateAccess === 'link';
let zoom = $('#zoom');
let swipe;
let loadRequest;
const duration = () => matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 420;

const isVideo = photo => photo.kind === 'video';
function videoDuration(value) {
  const seconds = Math.max(0, Math.floor(Number(value) || 0));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
function videoBadge(photo) {
  return isVideo(photo) ? `<span class="video-badge" aria-hidden="true">▶ ${videoDuration(photo.duration)}</span>` : '';
}
function releaseMedia() {
  if (zoom instanceof HTMLVideoElement) {
    zoom.pause(); zoom.removeAttribute('src'); zoom.removeAttribute('poster'); zoom.load();
  }
}

function clearPrivate() {
  loadRequest?.abort(); loadRequest = null;
  cancelSwipe();
  releaseMedia();
  viewer.close(); zoom.getAnimations().forEach(animation => animation.cancel());
  zoom.removeAttribute('src'); zoom.alt = ''; info.innerHTML = ''; $('#caption').textContent = ''; $('#count').textContent = '';
  document.body.style.overflow = '';
  state.catalog = null; state.photos = []; state.busy = false; state.info = false;
  stream.innerHTML = ''; $('#albums').innerHTML = '';
  $('#album-intro').hidden = true; $('#title').textContent = ''; $('#description').textContent = '';
  $('#skeleton').hidden = true;
  $('#status').classList.remove('loading');
  stream.setAttribute('aria-busy', 'false'); $('#albums').setAttribute('aria-busy', 'false');
  $('#main').setAttribute('aria-label', 'Private album'); document.title = 'Private album · Hugo’s Blog';
}

function showUnlock(message = '') {
  clearPrivate();
  $('#album-unlock').hidden = false;
  $('#album-login label').hidden = linkAccess;
  $('#album-password').hidden = linkAccess;
  $('#album-password').disabled = linkAccess;
  $('#album-login button').hidden = linkAccess;
  $('#unlock-error').textContent = message || (linkAccess ? 'Open the complete private share link to view this album.' : '');
  $('#status').hidden = true; $('#retry').hidden = true;
}

async function openShareLink() {
  const fragment = new URLSearchParams(location.hash.slice(1));
  if (!fragment.has('key')) { await load(); return; }
  // Fragments never reach HTTP logs. Remove the bearer credential before any
  // asynchronous work, and keep it out of storage and subsequent page URLs.
  history.replaceState(history.state, '', location.pathname + location.search);
  clearPrivate();
  if (!linkAccess || fragment.getAll('key').length !== 1 || !/^[a-f0-9]{64}$/.test(fragment.get('key'))) {
    showUnlock('This share link is invalid. Ask the owner for a new link.'); return;
  }
  try {
    const response = await fetch(`${privatePath}login`, { method: 'POST', credentials: 'same-origin', cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ key: fragment.get('key') }) });
    if (!response.ok) {
      showUnlock(response.status === 401 ? 'This share link is invalid or has been replaced. Ask the owner for a new link.' : response.status === 404 ? 'This album isn’t available.' : 'The album couldn’t be opened. Please reopen the original share link.');
      return;
    }
    await load();
  } catch { showUnlock('The album couldn’t be opened. Please reopen the original share link.'); }
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
  const photos = state.catalog.photos.filter(p => !p.archived && !p.deleting && (p.kind === undefined || p.kind === 'image' || isVideo(p)));
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
      return `<a class="album" href="${escape(albumURL(a.slug))}"><div class="photo">${cover ? `<img src="${escape(cover.thumbnail)}" width="${cover.width}" height="${cover.height}" alt="${escape(cover.alt)}" loading="lazy">${videoBadge(cover)}` : '<span class="album-placeholder" aria-hidden="true">◧</span>'}</div><h2>${escape(a.title)}</h2><p>${members.length} item${members.length === 1 ? '' : 's'}</p></a>`;
    }).join('');
    if (!state.catalog.albums.length) message('No albums published yet.');
    return;
  }
  // Album order is curated; the stream includes every published photo, including
  // standalone photos, in the manifest's newest-first order.
  state.photos = isAlbum ? (album?.photoIds || []).map(id => byID.get(id)).filter(Boolean) : photos;
  stream.setAttribute('aria-label', isAlbum ? `${album?.title || 'Album'} photographs` : 'Photostream');
  stream.innerHTML = state.photos.map((p, i) => `<button class="photo" style="--ratio:${p.width / p.height}" aria-label="${isVideo(p) ? 'Play video' : 'Open'} ${escape(p.alt)}${isVideo(p) ? `, ${videoDuration(p.duration)}` : ''}" data-photo="${i}"><img src="${escape(p.thumbnail)}" ${isAlbum || isVideo(p) ? '' : `srcset="${escape(p.thumbnail)} 640w, ${escape(p.src)} 2560w" sizes="(max-width:700px) 100vw, (max-width:900px) 50vw, 33vw"`} width="${p.width}" height="${p.height}" alt="${escape(p.alt)}" loading="${i < (isAlbum ? 4 : 3) ? 'eager' : 'lazy'}">${videoBadge(p)}</button>`).join('');
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

function target(p = state.photos[state.index]) {
  const narrow = innerWidth <= 540;
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
  return zoom.animate(closing ? frames.reverse() : frames, { duration: duration(), easing: 'cubic-bezier(.22,.75,.2,1)', fill: closing ? 'forwards' : 'none' }).finished.catch(error => {
    // Revocation can clear the viewer while its opening transition is running.
    if (error.name !== 'AbortError') throw error;
  });
}
function renderViewer(changePhoto = false) {
  cancelSwipe();
  const p = state.photos[state.index];
  if (changePhoto) {
    releaseMedia();
    $('#media-error').hidden = true;
    // Fresh pixels immediately, even when a previous full image is still loading.
    const display = isVideo(p) ? document.createElement('video') : new Image();
    display.id = 'zoom'; display.className = 'zoom';
    if (isVideo(p)) {
      display.controls = true; display.playsInline = true; display.preload = 'metadata';
      display.poster = p.thumbnail; display.setAttribute('aria-label', p.alt || 'Video');
      display.setAttribute('aria-describedby', 'caption media-error');
      display.addEventListener('error', async () => {
        if (zoom !== display || !viewer.open || !display.hasAttribute('src')) return;
        if (privatePath) {
          try {
            const result = await fetch(`${privatePath}catalog`, { credentials: 'same-origin', cache: 'no-store' });
            if (zoom !== display || !viewer.open) return;
            if (result.status === 401) { showUnlock('Your viewing session has ended.'); return; }
            if (result.status === 404) { clearPrivate(); message('This album isn’t available.'); return; }
            if (result.ok) {
              const current = await result.json();
              if (zoom !== display || !viewer.open) return;
              if (!current.photos?.some(item => item.id === p.id && isVideo(item))) {
                clearPrivate(); message('This video isn’t available.'); return;
              }
            }
          } catch { /* Keep a useful media error if the connection is offline. */ }
        }
        if (zoom === display && viewer.open) {
          display.pause(); display.removeAttribute('src'); display.controls = false; display.load();
          $('#media-error').hidden = false;
        }
      });
      display.src = p.src;
    } else { display.alt = p.alt; display.src = p.thumbnail; }
    zoom.replaceWith(display); zoom = display;
    if (!isVideo(p)) {
      const full = new Image(); full.src = p.src;
      full.decode().then(() => { if (zoom === display && viewer.open) display.src = full.src; }).catch(() => {});
    }
  }
  $('#count').textContent = `${state.index + 1} / ${state.photos.length}`;
  const date = formatDate(p.capturedDate);
  $('#caption').innerHTML = [escape(p.caption), date ? `<em><time datetime="${escape(p.capturedDate)}">${date}</time></em>` : ''].filter(Boolean).join(' · ');
  $('#details').setAttribute('aria-pressed', state.info);
  info.hidden = !state.info;
  const fields = { ...p.exif, ...(date ? { 'Capture date': date } : {}), Dimensions: `${p.width} × ${p.height}`, ...(isVideo(p) ? { Duration: videoDuration(p.duration) } : {}) };
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
  cancelSwipe();
  state.busy = true;
  if (zoom instanceof HTMLVideoElement) zoom.pause();
  const button = stream.children[state.index], thumb = button.querySelector('img');
  const touch = event?.pointerType === 'touch' || (event?.detail > 0 && matchMedia('(pointer: coarse)').matches);
  // Navigation/resize can move the thumbnail: use its current position.
  try { await animatePhoto(thumb.getBoundingClientRect(), zoom.getBoundingClientRect(), true); }
  finally {
    releaseMedia();
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
  cancelSwipe();
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
  if (!privatePath || linkAccess) return;
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
$('.skip').onclick = event => { event.preventDefault(); $('#main').focus(); };
viewer.addEventListener('cancel', event => { event.preventDefault(); close(); });
viewer.addEventListener('keydown', event => {
  if (event.target instanceof HTMLVideoElement) return;
  if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); step(event.key === 'ArrowLeft' ? -1 : 1); }
});

function cancelSwipe() {
  if (!swipe) return;
  swipe.animations?.forEach(animation => animation.cancel());
  if (swipe.animations) state.busy = false;
  swipe.preview?.remove();
  zoom.style.transform = '';
  viewer.classList.remove('swiping');
  swipe = null;
}
function dragSwipe(dx) {
  const delta = dx < 0 ? 1 : -1;
  if (swipe.delta !== delta) {
    swipe.preview?.remove();
    const p = state.photos[(state.index + delta + state.photos.length) % state.photos.length];
    const preview = new Image();
    preview.className = 'zoom swipe-preview'; preview.alt = ''; preview.setAttribute('aria-hidden', 'true');
    preview.src = p.thumbnail;
    for (const [key, value] of Object.entries(target(p))) preview.style[key] = `${value}px`;
    viewer.append(preview); swipe.preview = preview; swipe.delta = delta;
  }
  swipe.dx = dx;
  zoom.style.transform = `translateX(${dx}px)`;
  swipe.preview.style.transform = `translateX(${dx + swipe.delta * innerWidth}px)`;
}
async function settleSwipe(commit) {
  const gesture = swipe;
  if (gesture?.animations) return;
  if (!gesture?.active) { cancelSwipe(); return; }
  state.busy = true;
  const end = commit ? -gesture.delta * innerWidth : 0;
  const options = { duration: duration() ? 240 : 0, easing: 'cubic-bezier(.22,.75,.2,1)', fill: 'forwards' };
  gesture.animations = [
    zoom.animate([{ transform: zoom.style.transform }, { transform: `translateX(${end}px)` }], options),
    gesture.preview.animate([{ transform: gesture.preview.style.transform }, { transform: `translateX(${end + gesture.delta * innerWidth}px)` }], options),
  ];
  await Promise.allSettled(gesture.animations.map(animation => animation.finished));
  // Resize, navigation or private-album revocation can cancel a settling swipe.
  if (swipe !== gesture) return;
  cancelSwipe();
  if (commit) step(gesture.delta);
}
viewer.addEventListener('touchstart', event => {
  if (event.touches.length !== 1) { cancelSwipe(); return; }
  if (state.busy || !viewer.open || innerWidth > 700 || state.photos.length < 2 || (visualViewport?.scale || 1) > 1
      || !(event.target === viewer || (event.target === zoom && !(zoom instanceof HTMLVideoElement)))) return;
  const touch = event.touches[0];
  swipe = { id: touch.identifier, x: touch.clientX, y: touch.clientY, dx: 0, lastX: touch.clientX, time: event.timeStamp, velocity: 0 };
}, { passive: true });
viewer.addEventListener('touchmove', event => {
  if (!swipe || swipe.animations) return;
  const touch = Array.from(event.touches).find(touch => touch.identifier === swipe.id);
  if (!touch || event.touches.length !== 1 || (visualViewport?.scale || 1) > 1) { cancelSwipe(); return; }
  const dx = touch.clientX - swipe.x, dy = touch.clientY - swipe.y;
  if (!swipe.active) {
    if (Math.max(Math.abs(dx), Math.abs(dy)) < 8) return;
    if (Math.abs(dx) <= Math.abs(dy) * 1.5) { cancelSwipe(); return; }
    swipe.active = true; viewer.classList.add('swiping');
  }
  event.preventDefault();
  const elapsed = event.timeStamp - swipe.time;
  if (elapsed > 0) swipe.velocity = (touch.clientX - swipe.lastX) / elapsed;
  swipe.lastX = touch.clientX; swipe.time = event.timeStamp;
  dragSwipe(dx);
}, { passive: false });
viewer.addEventListener('touchend', event => {
  if (!swipe || swipe.animations) return;
  if (!swipe.active) { cancelSwipe(); return; }
  // Even a short horizontal drag snaps back without synthesizing a close tap.
  event.preventDefault();
  const flick = event.timeStamp - swipe.time < 100 && Math.abs(swipe.velocity) > .5 && swipe.velocity * swipe.dx > 0;
  settleSwipe(Math.abs(swipe.dx) >= Math.min(100, innerWidth * .2) || (Math.abs(swipe.dx) >= 20 && flick));
}, { passive: false });
viewer.addEventListener('touchcancel', () => { settleSwipe(false); });
viewer.addEventListener('click', event => { if (event.target === viewer || (event.target === zoom && !(zoom instanceof HTMLVideoElement))) close(event); });
addEventListener('resize', () => { cancelSwipe(); layoutPhotos(); if (viewer.open) place(); });
addEventListener('hashchange', async () => {
  if (privatePath && new URLSearchParams(location.hash.slice(1)).has('key')) { await openShareLink(); return; }
  cancelSwipe();
  // Browser Back may arrive during the zoom. Keep its thumbnail in the DOM
  // until the transition and close have finished, then change collections.
  await Promise.allSettled(zoom.getAnimations().map(animation => animation.finished));
  if (viewer.open) await close();
  render();
});
addEventListener('pageshow', event => { if (privatePath && event.persisted) load(); });
render();
if (privatePath) openShareLink();
else load();
