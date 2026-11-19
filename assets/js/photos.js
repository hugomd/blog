const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: null, photos: [], index: 0, info: false, infoProgress: 0, busy: false, zoomed: false };
const stream = $('#stream'), viewer = $('#viewer'), info = $('#info');
const privatePath = location.pathname.match(/^\/photos\/private\/[a-f0-9]{32}\//)?.[0];
const publicPath = new URL($('#nav-stream').href).pathname;
const collectionPath = privatePath || (document.body.dataset.album ? location.pathname : publicPath);
const linkAccess = document.body.dataset.privateAccess === 'link';
let zoom = $('#zoom');
let swipe;
let pan;
let offset = { x: 0, y: 0 };
let loadRequest;
let infoFrame;
let navigation = Promise.resolve();
let preloadedImages = new Map();
let preloadTimer;
const duration = milliseconds => matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : milliseconds;
// A valid transparent image leaves the BlurHash background visible without alt-text flashes.
const emptyImage = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';

const isVideo = photo => photo.kind === 'video';
function videoDuration(value) {
  const seconds = Math.max(0, Math.floor(Number(value) || 0));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
function videoBadge(photo) {
  return isVideo(photo) ? `<span class="video-badge" aria-hidden="true">▶ ${videoDuration(photo.duration)}</span>` : '';
}
function blurhashBackground(photo) {
  const hash = photo.blurhash;
  const alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~';
  if (typeof hash !== 'string' || hash.length < 6 || hash.length > 166 || [...hash].some(c => !alphabet.includes(c))) return 'none';
  const base83 = value => [...value].reduce((result, c) => result * 83 + alphabet.indexOf(c), 0);
  const size = base83(hash[0]), nx = size % 9 + 1, ny = Math.floor(size / 9) + 1;
  if (size > 80 || hash.length !== 4 + 2 * nx * ny || !(photo.width > 0 && photo.height > 0)) return 'none';
  const linear = value => value / 255 <= .04045 ? value / 255 / 12.92 : ((value / 255 + .055) / 1.055) ** 2.4;
  const srgb = value => Math.round(255 * (value <= .0031308 ? 12.92 * value : 1.055 * value ** (1 / 2.4) - .055));
  const maximum = (base83(hash[1]) + 1) / 166;
  const dc = base83(hash.slice(2, 6));
  if (dc > 0xffffff) return 'none';
  const colors = [[linear(dc >> 16), linear(dc >> 8 & 255), linear(dc & 255)]];
  for (let i = 1; i < nx * ny; i++) {
    const value = base83(hash.slice(4 + i * 2, 6 + i * 2));
    if (value >= 19 ** 3) return 'none';
    colors.push([Math.floor(value / 361), Math.floor(value / 19) % 19, value % 19].map(n => {
      const v = (n - 9) / 9;
      return Math.sign(v) * v * v * maximum;
    }));
  }
  const canvas = document.createElement('canvas');
  canvas.width = Math.max(1, Math.round(32 * Math.min(1, photo.width / photo.height)));
  canvas.height = Math.max(1, Math.round(32 * Math.min(1, photo.height / photo.width)));
  const context = canvas.getContext('2d');
  if (!context) return 'none';
  const pixels = context.createImageData(canvas.width, canvas.height);
  for (let y = 0; y < canvas.height; y++) {
    for (let x = 0; x < canvas.width; x++) {
      const rgb = [0, 0, 0];
      for (let j = 0; j < ny; j++) {
        for (let i = 0; i < nx; i++) {
          const basis = Math.cos(Math.PI * x * i / canvas.width) * Math.cos(Math.PI * y * j / canvas.height);
          for (let c = 0; c < 3; c++) rgb[c] += colors[i + j * nx][c] * basis;
        }
      }
      const offset = 4 * (x + y * canvas.width);
      for (let c = 0; c < 3; c++) pixels.data[offset + c] = srgb(Math.max(0, Math.min(1, rgb[c])));
      pixels.data[offset + 3] = 255;
    }
  }
  context.putImageData(pixels, 0, 0);
  return `url(${canvas.toDataURL()})`;
}
// Decoding costs about a millisecond per tile, so decode each tile once, when it
// nears the viewport or the viewer needs it, rather than for the whole collection.
function placeholder(index) {
  const tile = stream.children[index];
  if (!tile.style.backgroundImage) {
    tile.style.backgroundImage = blurhashBackground(state.photos[index]);
    placeholders.unobserve(tile);
  }
  return tile.style.backgroundImage;
}
const placeholders = new IntersectionObserver(entries => {
  for (const { target, isIntersecting } of entries) {
    // Entries can arrive after a re-render or a switch to the albums view.
    if (isIntersecting && target.isConnected && !stream.hidden) placeholder(Number(target.dataset.photo));
  }
}, { rootMargin: '100% 0px' });
function revealImages(container) {
  for (const image of container.querySelectorAll('img')) {
    if (image.complete && image.naturalWidth) image.classList.add('loaded');
    else image.addEventListener('load', () => image.classList.add('loaded'), { once: true });
  }
}
function releaseMedia() {
  if (zoom instanceof HTMLVideoElement) {
    zoom.pause(); zoom.removeAttribute('src'); zoom.removeAttribute('poster'); zoom.load();
  }
}

function clearPrivate() {
  loadRequest?.abort(); loadRequest = null;
  clearTimeout(preloadTimer); preloadedImages.clear();
  cancelAnimationFrame(infoFrame); state.infoProgress = 0;
  cancelSwipe();
  setZoom(false);
  releaseMedia();
  viewer.close(); zoom.getAnimations().forEach(animation => animation.cancel());
  zoom.removeAttribute('src'); zoom.alt = ''; $('#caption').textContent = ''; $('#count').textContent = '';
  for (const id of ['camera-value', 'lens-value', 'info-fields', 'info-tags', 'video-summary']) $(`#${id}`).textContent = '';
  info.hidden = true; $('#photo-date').textContent = 'Photo info';
  $('#details').setAttribute('aria-expanded', 'false'); $('#details').removeAttribute('title');
  $('#details').setAttribute('aria-label', 'Photo information');
  zoom.style.backgroundImage = '';
  document.body.style.overflow = '';
  state.catalog = null; state.photos = []; state.busy = false; state.info = false;
  stream.innerHTML = ''; $('#albums').innerHTML = '';
  $('#album-intro').hidden = true; $('#title').textContent = ''; $('#description').textContent = '';
  $('#skeleton').hidden = true;
  $('#status').classList.remove('loading');
  stream.setAttribute('aria-busy', 'false'); $('#albums').setAttribute('aria-busy', 'false');
  $('#main').setAttribute('aria-label', 'Private album'); document.title = 'Private album :: Hugo’s Blog';
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
  stream.setAttribute('aria-busy', 'true');
  $('#albums').setAttribute('aria-busy', 'true');
  try {
    const response = await fetch(privatePath ? `${privatePath}catalog` : document.body.dataset.manifest, { cache: privatePath ? 'no-store' : 'no-cache', credentials: 'same-origin', signal: request.signal });
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
    syncLocation();
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

function albumURL(slug) { return `${publicPath}#album/${encodeURIComponent(slug)}`; }
function collectionRoute() {
  return privatePath ? `album/${encodeURIComponent(state.catalog?.albums[0]?.slug || '')}` : location.hash.slice(1) || (document.body.dataset.album ? `album/${encodeURIComponent(document.body.dataset.album)}` : 'stream');
}
function photoURL(id) {
  const url = new URL(location.href);
  url.searchParams.delete('photo');
  url.pathname = id ? `${privatePath || publicPath}${encodeURIComponent(id)}/` : collectionPath;
  if (!privatePath && document.body.dataset.album && !url.hash) url.hash = `album/${encodeURIComponent(document.body.dataset.album)}`;
  return url.pathname + url.search + url.hash;
}
function writePhotoURL(id) {
  const url = photoURL(id);
  if (url !== location.pathname + location.search + location.hash) history.pushState(null, '', url);
}
function render() {
  clearTimeout(preloadTimer); preloadedImages.clear();
  const route = collectionRoute();
  const isAlbums = route === 'albums', isAlbum = route.startsWith('album/');
  stream.classList.toggle('list', !isAlbums && !isAlbum);
  $('#skeleton').classList.toggle('list', !isAlbums && !isAlbum);
  $('#count').hidden = !isAlbum;
  if (!state.catalog) return;
  state.route = route;
  const photos = state.catalog.photos.filter(p => p.published !== false && !p.archived && !p.archiving && !p.deleting && (p.kind === undefined || p.kind === 'image' || isVideo(p)));
  const byID = new Map(photos.map(p => [p.id, p]));
  const album = isAlbum ? state.catalog.albums.find(a => `album/${encodeURIComponent(a.slug)}` === route) : null;
  const title = isAlbums ? 'Albums' : isAlbum ? album?.title || 'Album unavailable' : 'Photostream';
  $('#nav-stream').setAttribute('aria-current', !isAlbums && !isAlbum ? 'page' : 'false');
  $('#nav-albums').setAttribute('aria-current', !privatePath && (isAlbums || isAlbum) ? 'page' : 'false');
  $('#album-intro').hidden = false;
  $('#album-intro').classList.toggle('sr-only', !isAlbum);
  $('#title').textContent = title;
  $('#description').hidden = !isAlbum || !album?.description;
  $('#description').textContent = isAlbum ? album?.description || '' : '';
  $('#main').setAttribute('aria-label', title);
  state.title = `${title} :: Hugo’s Blog`;
  document.title = state.title;
  stream.hidden = isAlbums;
  $('#albums').hidden = !isAlbums;
  $('#status').hidden = true;
  $('#retry').hidden = true;
  placeholders.disconnect();
  if (isAlbums) {
    state.photos = [];
    $('#albums').innerHTML = state.catalog.albums.map(a => {
      const members = a.photoIds.filter(id => byID.has(id));
      const cover = byID.get(a.coverId) || byID.get(members[0]);
      return `<a class="album" href="${escape(albumURL(a.slug))}"><div class="photo" style="background-image:${cover ? blurhashBackground(cover) : 'none'}">${cover ? `<img src="${escape(cover.thumbnail)}" width="${cover.width}" height="${cover.height}" alt="${escape(cover.alt)}" loading="lazy">${videoBadge(cover)}` : '<span class="album-placeholder" aria-hidden="true">◧</span>'}</div><h2>${escape(a.title)}</h2><p>${members.length} item${members.length === 1 ? '' : 's'}</p></a>`;
    }).join('');
    revealImages($('#albums'));
    if (!state.catalog.albums.length) message('No albums published yet.');
    return;
  }
  // Album order is curated; the stream includes every published photo, including
  // standalone photos, in the manifest's newest-first order.
  state.photos = isAlbum ? (album?.photoIds || []).map(id => byID.get(id)).filter(Boolean) : photos;
  stream.setAttribute('aria-label', isAlbum ? `${album?.title || 'Album'} photographs` : 'Photostream');
  stream.innerHTML = state.photos.map((p, i) => `<a class="photo" href="${escape(photoURL(p.id))}" style="--ratio:${p.width / p.height}" aria-label="${isVideo(p) ? 'Play video' : 'Open'} ${escape(p.alt)}${isVideo(p) ? `, ${videoDuration(p.duration)}` : ''}" data-photo="${i}"><img src="${escape(p.thumbnail)}" ${isAlbum || isVideo(p) ? '' : `srcset="${escape(p.thumbnail)} 640w, ${escape(p.thumbnail.replace(/\/640\.jpg$/, '/1280.jpg'))} 1280w, ${escape(p.src)} 2560w" sizes="(max-width:700px) 100vw, (max-width:900px) 50vw, 33vw"`} width="${p.width}" height="${p.height}" alt="${escape(p.alt)}" ${i < (isAlbum ? 4 : 3) ? 'loading="eager" fetchpriority="high"' : 'loading="lazy"'}>${videoBadge(p)}</a>`).join('');
  for (const tile of stream.children) placeholders.observe(tile);
  revealImages(stream);
  layoutPhotos();
  if (!state.photos.length) message(isAlbum ? album ? 'No photographs in this album yet.' : 'This album isn’t currently published.' : 'No photographs published yet. Check back soon.');
}

// The loading skeleton uses CSS columns instead, so it never shifts before this runs.
function layoutPhotos() {
  if (stream.hidden) return;
  const width = stream.firstElementChild?.getBoundingClientRect().width;
  const gap = parseFloat(getComputedStyle(stream).columnGap);
  for (const photo of stream.children) {
    const ratio = Number(getComputedStyle(photo).getPropertyValue('--ratio')) || 4 / 3;
    // Tiny row spans pack each column independently without reordering the DOM.
    photo.style.gridRowEnd = `span ${Math.ceil(width / ratio + gap)}`;
    const image = photo.querySelector('img[srcset]');
    if (image) image.sizes = `${Math.ceil(width)}px`;
  }
}
function message(text) { $('#status').textContent = text; $('#status').hidden = false; }

function renderInfo(animate = false) {
  cancelAnimationFrame(infoFrame);
  const surface = $('.photo-info'), button = $('#details');
  const video = isVideo(state.photos[state.index]);
  const empty = !info.querySelector('.camera-info > :not([hidden]), #info-fields > *, #info-tags > *');
  if ((video || empty) && surface.contains(document.activeElement)) viewer.focus({ preventScroll: true });
  surface.hidden = !video && empty;
  button.hidden = video || surface.hidden; $('#video-summary').hidden = !video;
  if (video || surface.hidden) { state.info = false; state.infoProgress = 0; }
  button.setAttribute('aria-expanded', state.info);
  info.inert = !state.info;
  info.setAttribute('aria-hidden', !state.info);
  if (video || surface.hidden) {
    info.hidden = true;
    Object.assign(surface.style, { height: '44px', transform: 'translateX(-50%)', borderRadius: '22px' });
    return;
  }
  info.hidden = false;
  const width = surface.offsetWidth, height = 44 + info.offsetHeight;
  surface.style.height = `${height}px`;
  const start = state.infoProgress, end = state.info ? 1 : 0;
  const milliseconds = animate ? duration(200 * Math.abs(end - start)) : 0;
  const started = performance.now();
  function draw(now) {
    const elapsed = milliseconds ? Math.min(1, (now - started) / milliseconds) : 1;
    const progress = start + (end - start) * elapsed;
    state.infoProgress = progress;
    const eased = 1 - (1 - progress) ** 3;
    const x = (160 + (width - 160) * eased) / width;
    const y = (44 + (height - 44) * eased) / height;
    const radius = 22 + 2 * eased;
    surface.style.transform = `translateX(-50%) scale(${x},${y})`;
    surface.style.borderRadius = `${radius / x}px / ${radius / y}px`;
    // Grow one glass surface without stretching or moving the date or metadata.
    button.style.width = `${width * x}px`;
    button.style.left = `${width * (1 - x) / 2}px`;
    button.style.transform = `translateY(${22 / y - 22}px) scale(${1 / x},${1 / y})`;
    info.style.transform = `translateY(${44 / y - 44}px) scale(${1 / x},${1 / y})`;
    info.style.opacity = Math.max(0, (progress - .35) / .65);
    $('.date-chevron').style.transform = `rotate(${180 * eased}deg)`;
    info.hidden = progress === 0;
    if (elapsed < 1) infoFrame = requestAnimationFrame(draw);
  }
  draw(started);
}

function formatDate(value, compact = false) {
  if (!value) return '';
  const date = new Date(`${value}T00:00:00Z`), day = date.getUTCDate();
  const suffix = day % 100 >= 11 && day % 100 <= 13 ? 'th' : { 1: 'st', 2: 'nd', 3: 'rd' }[day % 10] || 'th';
  return `${date.toLocaleString('en-US', { month: compact ? 'short' : 'long', timeZone: 'UTC' })} ${day}${suffix}${compact && date.getUTCFullYear() === new Date().getFullYear() ? '' : `, ${date.getUTCFullYear()}`}`;
}

function target(p = state.photos[state.index]) {
  const scale = Math.min(innerWidth / p.width, innerHeight / p.height);
  const width = p.width * scale, height = p.height * scale;
  return { left: (innerWidth - width) / 2, top: (innerHeight - height) / 2, width, height };
}
function place() {
  const rect = target();
  for (const [key, value] of Object.entries(rect)) zoom.style[key] = `${value}px`;
  if (state.zoomed) {
    offset.x = Math.max(-rect.width * .1, Math.min(rect.width * .1, offset.x));
    offset.y = Math.max(-rect.height * .1, Math.min(rect.height * .1, offset.y));
    zoom.style.transform = `translate(${offset.x}px,${offset.y}px) scale(1.2)`;
  }
  return rect;
}
function cancelPan() {
  const gesture = pan;
  pan = null;
  viewer.classList.remove('panning');
  if (gesture && viewer.hasPointerCapture(gesture.id)) viewer.releasePointerCapture(gesture.id);
}
function setZoom(value) {
  cancelSwipe(); cancelPan();
  state.zoomed = value;
  offset = { x: 0, y: 0 };
  zoom.style.transform = '';
  viewer.classList.toggle('zoomed', value);
  const button = $('#zoom-toggle');
  button.setAttribute('aria-pressed', value);
  button.setAttribute('aria-label', value ? 'Zoom out' : 'Zoom in 20%');
  button.title = value ? 'Zoom out' : 'Zoom in 20%';
  button.querySelector('.zoom-in-icon').hidden = value;
  button.querySelector('.zoom-out-icon').hidden = !value;
  if (viewer.open && state.photos.length) place();
}
function animatePhoto(thumbnail, full, closing = false) {
  const transform = `translate(${thumbnail.left + thumbnail.width / 2 - full.left - full.width / 2}px,${thumbnail.top + thumbnail.height / 2 - full.top - full.height / 2}px) scale(${thumbnail.width / full.width},${thumbnail.height / full.height})`;
  const frames = [{ transform }, { transform: 'none' }];
  // Scale/travel from the actual thumbnail bounds in both directions; no fade.
  // Only opening overshoots: closing should land precisely on the thumbnail.
  return zoom.animate(closing ? frames.reverse() : frames, { duration: duration(closing ? 280 : 340), easing: closing ? 'cubic-bezier(.16,1,.3,1)' : 'cubic-bezier(.22,1.28,.36,1)', fill: closing ? 'forwards' : 'none' }).finished.catch(error => {
    // Revocation can clear the viewer while its opening transition is running.
    if (error.name !== 'AbortError') throw error;
  });
}
function preloadPhoto(photo) {
  const image = preloadedImages.get(photo.src) || new Image();
  image.fetchPriority = 'low';
  image.src = photo.src;
  image.decode().catch(() => {});
  return image;
}
function preloadAdjacentPhotos() {
  const images = new Map();
  for (const delta of [-1, 1]) {
    const index = (state.index + delta + state.photos.length) % state.photos.length;
    const photo = state.photos[index];
    if (index === state.index || isVideo(photo) || images.has(photo.src)) continue;
    images.set(photo.src, preloadPhoto(photo));
  }
  preloadedImages = images;
}
function renderViewer(changePhoto = false) {
  cancelSwipe();
  const p = state.photos[state.index];
  document.title = `${p.alt || formatDate(p.capturedDate) || (isVideo(p) ? 'Video' : 'Photograph')} · ${state.title}`;
  if (changePhoto) {
    releaseMedia();
    $('#media-error').hidden = true;
    // Fresh pixels immediately, even when a previous full image is still loading.
    const display = isVideo(p) ? document.createElement('video') : new Image();
    display.id = 'zoom'; display.className = 'zoom';
    display.style.backgroundImage = placeholder(state.index);
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
    } else {
      display.alt = p.alt; display.src = emptyImage; display.draggable = false;
      const thumbnail = new Image(); thumbnail.src = p.thumbnail;
      thumbnail.decode().then(() => {
        if (zoom === display && viewer.open && display.src === emptyImage) display.src = thumbnail.src;
      }).catch(() => {});
    }
    zoom.replaceWith(display); zoom = display;
    setZoom(false);
    if (!isVideo(p)) {
      const full = preloadedImages.get(p.src) || new Image(); full.src = p.src;
      full.decode().then(() => { if (zoom === display && viewer.open) display.src = full.src; }).catch(() => {});
    }
    preloadAdjacentPhotos();
    cancelHold(); renderLike(); loadLikes();
  }
  $('#count').textContent = `${state.index + 1} / ${state.photos.length}`;
  const date = formatDate(p.capturedDate);
  $('#caption').textContent = p.caption || '';
  $('#photo-date').innerHTML = date ? `<time datetime="${escape(p.capturedDate)}">${escape(formatDate(p.capturedDate, true))}</time>` : 'Photo info';
  $('#details').setAttribute('aria-label', date ? `Photo information for ${date}` : 'Photo information');
  $('#details').title = date || 'Photo information';
  $('#video-summary').innerHTML = isVideo(p) ? `${date ? $('#photo-date').innerHTML : 'Video'} · ${videoDuration(p.duration)}` : '';
  $('#zoom-toggle').hidden = isVideo(p);
  viewer.classList.toggle('playing-video', isVideo(p));
  for (const [key, id] of [['Camera', 'camera'], ['Lens', 'lens']]) {
    $(`#info-${id}`).hidden = !p.exif?.[key];
    $(`#${id}-value`).textContent = p.exif?.[key] || '';
  }
  const fields = { ...p.exif, ...(p.location ? { Location: p.location } : {}) };
  $('#info-fields').innerHTML = Object.entries(fields).filter(([key, value]) => value && !['Camera', 'Lens', 'Dimensions'].includes(key)).map(([key, value]) => `<div><dt>${escape(key)}</dt><dd>${escape(value)}</dd></div>`).join('');
  $('#info-tags').innerHTML = (p.tags || []).map(tag => `<span class="tag">${escape(tag)}</span>`).join('');
  $('#previous').disabled = $('#next').disabled = state.photos.length < 2;
  renderInfo(!changePhoto);
  place();
}
// Public items only: anonymous, like-only counts. This browser remembers what it
// liked; the button stays hidden until the counts load (and without a backend).
// All counts arrive in one request, so paging through photos fetches nothing.
let likes = null, likesRequest = null;
// Reserve the toolbar space up front so the photo info never moves after a fetch.
viewer.classList.toggle('liking', !privatePath);
function likedPhotos() {
  try { return new Set(JSON.parse(localStorage.getItem('liked-photos')) || []); } catch { return new Set(); }
}
function rememberLike(id, liked) {
  const photos = likedPhotos();
  if (liked) photos.add(id); else photos.delete(id);
  try { localStorage.setItem('liked-photos', JSON.stringify([...photos])); } catch { /* Private browsing. */ }
}
function renderLike() {
  const id = state.photos[state.index]?.id, count = likes?.get(id) ?? 0, button = $('#like');
  const visible = !privatePath && viewer.open && likes !== null;
  button.hidden = !visible;
  if (!visible) return;
  const liked = likedPhotos().has(id);
  const label = `${liked ? 'Liked' : 'Like'}${count ? ` (${count.toLocaleString()} ${count === 1 ? 'like' : 'likes'})` : ''}`;
  button.setAttribute('aria-pressed', liked); button.setAttribute('aria-label', label);
  button.title = liked ? label : `${label}: press and hold`;
  button.querySelector('.like-icon').hidden = liked; button.querySelector('.liked-icon').hidden = !liked;
  $('#like-count').textContent = count ? count.toLocaleString() : '';
}
function loadLikes() {
  if (privatePath || likes || likesRequest) return;
  likesRequest = (async () => {
    try {
      const response = await fetch(`${publicPath}likes`, { cache: 'no-store' });
      const { counts } = response.ok ? await response.json() : {};
      if (counts && typeof counts === 'object') likes = new Map(Object.entries(counts));
    } catch { /* Leave the button hidden; opening another photo retries. */ }
    likesRequest = null;
    renderLike();
  })();
}
loadLikes();
// Press and hold to like: the heart grows while held, the like lands, the heart
// springs back and a ring of tiny hearts bursts out. Letting go early shrinks
// it back unliked.
const heart = $('#like .like-heart');
$('#like').addEventListener('pointerenter', () => {
  const tilt = 3 + Math.floor(Math.random() * 8);
  heart.style.setProperty('--like-tilt', `${Math.random() < .5 ? -tilt : tilt}deg`);
});
let hold, pressed = false;
const canLike = id => likes !== null && Boolean(id) && !likedPhotos().has(id);
function burstHearts() {
  if (!duration(1)) return;
  const { left, top, width, height } = heart.getBoundingClientRect();
  const colors = ['#ff5a76', '#fff', '#ff8fa3', '#ffd166'], count = 10;
  for (let i = 0; i < count; i++) {
    const mini = $('#like .liked-icon svg').cloneNode(true);
    mini.classList.add('burst-heart'); mini.style.fill = colors[i % colors.length];
    mini.style.left = `${left + width / 2}px`; mini.style.top = `${top + height / 2}px`;
    viewer.append(mini);
    const angle = (i + (Math.random() - .5) * .4) * Math.PI * 2 / count, reach = 38 + Math.random() * 22, scale = .6 + Math.random() * .6;
    const at = (k, s) => `translate(${Math.cos(angle) * reach * k}px,${Math.sin(angle) * reach * k}px) scale(${s})`;
    // Easing per segment keeps each heart solid for most of its flight.
    mini.animate([
      { transform: 'translate(0,0) scale(.3)', opacity: 0, easing: 'ease-out' },
      { transform: at(.35, scale), opacity: 1, offset: .15, easing: 'ease-out' },
      { transform: at(.85, scale), opacity: 1, offset: .65, easing: 'ease-in' },
      { transform: at(1, scale * .6), opacity: 0 },
    ], { duration: 800 + Math.random() * 250 }).finished.then(() => mini.remove(), () => mini.remove());
  }
}
function settleHeart(grow, liked) {
  const from = getComputedStyle(heart).transform;
  heart.animate(liked ? [{ transform: from }, { transform: 'scale(.8)', offset: .6 }, { transform: 'none' }] : [{ transform: from }, { transform: 'none' }],
    { duration: duration(liked ? 380 : 180), easing: 'ease-out' });
  grow?.cancel();
  if (liked) burstHearts();
}
function cancelHold() {
  if (!hold) return;
  const { grow } = hold; hold = null;
  settleHeart(grow, false);
}
$('#like').addEventListener('pointerdown', event => {
  // Touch screens like on a plain tap, through the click handler below.
  if (event.pointerType === 'touch') { pressed = false; return; }
  const id = state.photos[state.index]?.id;
  pressed = true;
  if (event.button !== 0 || hold || !canLike(id)) return;
  $('#like').setPointerCapture(event.pointerId);
  // The hold always takes 600ms; reduced motion only drops the growth.
  const grow = heart.animate([{ transform: 'none' }, { transform: `scale(${duration(1) ? 1.8 : 1})` }],
    { duration: 600, easing: 'cubic-bezier(.4,0,.8,.6)', fill: 'forwards' });
  hold = { id, grow };
  grow.finished.then(() => {
    if (hold?.grow !== grow) return;
    hold = null; like(id); settleHeart(grow, true);
  }, () => {});
});
for (const type of ['pointerup', 'pointercancel', 'lostpointercapture']) $('#like').addEventListener(type, cancelHold);
// A press released off the button never clicks; keys start afresh.
$('#like').addEventListener('keydown', () => { pressed = false; });
// Long presses would otherwise open a context menu on touch screens.
$('#like').addEventListener('contextmenu', event => event.preventDefault());
$('#like').addEventListener('click', () => {
  // Taps, keyboards and screen readers click without a mouse or pen press,
  // so those clicks like at once.
  const id = state.photos[state.index]?.id, held = pressed;
  pressed = false;
  if (held || !canLike(id)) return;
  like(id);
  heart.animate([{ transform: 'none' }, { transform: 'scale(1.8)' }, { transform: 'none' }], { duration: duration(400), easing: 'ease-out' });
  burstHearts();
});
async function like(id) {
  if (!canLike(id)) return;
  rememberLike(id, true); likes.set(id, (likes.get(id) ?? 0) + 1); renderLike();
  try {
    const response = await fetch(`${publicPath}likes/${id}`, { method: 'POST', cache: 'no-store' });
    const { count } = response.ok ? await response.json() : {};
    if (!Number.isInteger(count)) throw new Error('Like failed');
    likes.set(id, count);
  } catch {
    rememberLike(id, false); likes.set(id, (likes.get(id) ?? 1) - 1);
  }
  renderLike();
}
async function open(index, updateURL = true) {
  if (state.busy) return;
  clearTimeout(preloadTimer);
  state.busy = true; state.index = index; state.info = false;
  $('#status').hidden = true;
  const thumb = stream.children[index].querySelector('img'), from = thumb.getBoundingClientRect();
  viewer.showModal(); viewer.focus({ preventScroll: true });
  document.body.style.overflow = 'hidden'; renderViewer(true);
  if (updateURL) writePhotoURL(state.photos[index].id);
  thumb.style.visibility = 'hidden';
  try { await animatePhoto(from, place()); }
  finally { state.busy = false; }
}
async function close(event, updateURL = true) {
  if (state.busy || !viewer.open) return;
  cancelSwipe();
  setZoom(false);
  state.info = false; renderInfo();
  state.busy = true;
  if (updateURL) writePhotoURL(null);
  if (zoom instanceof HTMLVideoElement) zoom.pause();
  const button = stream.children[state.index], thumb = button.querySelector('img');
  const touch = event?.pointerType === 'touch' || (event?.detail > 0 && matchMedia('(pointer: coarse)').matches);
  // Reveal the stream behind the shrinking photo, not only after dialog.close().
  viewer.classList.add('closing');
  // Navigation/resize can move the thumbnail: use its current position.
  try { await animatePhoto(thumb.getBoundingClientRect(), zoom.getBoundingClientRect(), true); }
  finally {
    preloadedImages.clear();
    releaseMedia();
    viewer.close(); zoom.getAnimations().forEach(a => a.cancel());
    viewer.classList.remove('closing');
    thumb.style.visibility = ''; document.body.style.overflow = '';
    document.title = state.title;
    // Closing a dialog can restore focus automatically, so touch needs an
    // explicit blur. Keyboard users still return to their selected thumbnail.
    if (touch) button.blur();
    else button.focus({ preventScroll: true });
    state.busy = false;
  }
}
function step(delta, updateURL = true) {
  if (state.busy) return;
  cancelSwipe();
  stream.children[state.index].querySelector('img').style.visibility = '';
  state.index = (state.index + delta + state.photos.length) % state.photos.length;
  stream.children[state.index].scrollIntoView({ block: 'nearest', behavior: 'instant' });
  stream.children[state.index].querySelector('img').style.visibility = 'hidden';
  renderViewer(true);
  if (updateURL) writePhotoURL(state.photos[state.index].id);
}
function schedulePhotoPreload(event) {
  const link = event.target.closest('[data-photo]');
  if (!link || link.contains(event.relatedTarget) || viewer.open || navigator.connection?.saveData) return;
  if (event.type === 'pointerover') {
    if (event.pointerType !== 'mouse' || !matchMedia('(hover: hover) and (pointer: fine)').matches) return;
  } else if (!link.matches(':focus-visible')) return;
  clearTimeout(preloadTimer);
  const photo = state.photos[Number(link.dataset.photo)];
  if (isVideo(photo)) return;
  preloadTimer = setTimeout(() => {
    preloadedImages = new Map([[photo.src, preloadPhoto(photo)]]);
  }, 100);
}
stream.addEventListener('pointerover', schedulePhotoPreload);
stream.addEventListener('focusin', schedulePhotoPreload);
for (const type of ['pointerout', 'focusout']) {
  stream.addEventListener(type, event => {
    const link = event.target.closest('[data-photo]');
    if (link && !link.contains(event.relatedTarget)) clearTimeout(preloadTimer);
  });
}
stream.addEventListener('click', event => {
  const link = event.target.closest('[data-photo]');
  // Keep copy-link, new-tab and modified-click behavior native.
  if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  open(Number(link.dataset.photo));
});
$('#close').onclick = close;
$('#previous').onclick = () => step(-1);
$('#next').onclick = () => step(1);
$('#zoom-toggle').onclick = () => { if (!state.busy) setZoom(!state.zoomed); };
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
  if (event.key === 'Escape' && state.info && !state.busy) {
    event.preventDefault(); state.info = false; renderViewer(); $('#details').focus(); return;
  }
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
    preview.style.backgroundImage = placeholder((state.index + delta + state.photos.length) % state.photos.length);
    preview.src = emptyImage;
    const thumbnail = new Image(); thumbnail.src = p.thumbnail;
    thumbnail.decode().then(() => { if (swipe?.preview === preview) preview.src = thumbnail.src; }).catch(() => {});
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
  state.busy = true; gesture.commit = commit;
  const end = commit ? -gesture.delta * innerWidth : 0;
  const options = { duration: duration(210), easing: 'cubic-bezier(.16,1,.3,1)', fill: 'forwards' };
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
  // A new touch completes a settling swipe at once, so quick repeated swipes are not dropped.
  if (swipe?.animations) { const { commit, delta } = swipe; cancelSwipe(); if (commit) step(delta); }
  if (event.touches.length !== 1) { cancelSwipe(); return; }
  if (state.busy || state.zoomed || !viewer.open || innerWidth > 700 || state.photos.length < 2 || (visualViewport?.scale || 1) > 1
      || !(event.target === viewer || event.target === zoom)) return;
  const touch = event.touches[0];
  // Leave the native playback controls free to handle seeking and other gestures.
  if (event.target === zoom && zoom instanceof HTMLVideoElement && touch.clientY >= zoom.getBoundingClientRect().bottom - 64) return;
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
viewer.addEventListener('pointerdown', event => {
  if (!state.zoomed || state.busy || event.target !== zoom || event.button !== 0) return;
  if (!event.isPrimary) { cancelPan(); return; }
  event.preventDefault();
  pan = { id: event.pointerId, x: event.clientX, y: event.clientY, startX: offset.x, startY: offset.y };
  viewer.setPointerCapture(event.pointerId);
  viewer.classList.add('panning');
});
viewer.addEventListener('pointermove', event => {
  if (!pan || pan.id !== event.pointerId) return;
  offset = { x: pan.startX + event.clientX - pan.x, y: pan.startY + event.clientY - pan.y };
  place();
});
for (const type of ['pointerup', 'pointercancel', 'lostpointercapture']) {
  viewer.addEventListener(type, event => { if (pan?.id === event.pointerId) cancelPan(); });
}
viewer.addEventListener('click', event => {
  if (!state.zoomed && (event.target === viewer || (event.target === zoom && !(zoom instanceof HTMLVideoElement)))) close(event);
});
addEventListener('resize', () => {
  cancelSwipe(); cancelPan(); layoutPhotos();
  if (state.zoomed && innerWidth <= 700) setZoom(false);
  if (viewer.open) {
    if (!viewer.contains(document.activeElement)) viewer.focus({ preventScroll: true });
    place(); renderInfo();
  }
});
function syncLocation() {
  // Serialize history changes, including Back during an opening/closing zoom.
  // Read the latest URL after each transition so rapid Back/Forward cannot
  // restore a stale item or detach a thumbnail while it is still animating.
  navigation = navigation.then(async () => {
    if (privatePath && new URLSearchParams(location.hash.slice(1)).has('key')) { await openShareLink(); return; }
    cancelSwipe();
    await Promise.allSettled(zoom.getAnimations().map(animation => animation.finished));
    if (!state.catalog) return;
    if (state.route !== collectionRoute()) {
      if (viewer.open) await close(null, false);
      render();
    }
    const selection = location.pathname.slice((privatePath || '/photos/').length);
    const id = /^([a-f0-9]{32})\/$/.exec(selection)?.[1] || new URLSearchParams(location.search).get('photo');
    const index = id ? state.photos.findIndex(photo => photo.id === id) : -1;
    if (index >= 0 && new URLSearchParams(location.search).has('photo')) history.replaceState(history.state, '', photoURL(id));
    if (state.photos.length) $('#status').hidden = true;
    if (index < 0) {
      if (viewer.open) await close(null, false);
      if (id) message('This item isn’t available in this collection.');
    } else if (viewer.open) {
      if (index !== state.index) step(index - state.index, false);
    } else {
      stream.children[index].scrollIntoView({ block: 'nearest', behavior: 'instant' });
      await open(index, false);
    }
  });
}
addEventListener('popstate', syncLocation);
addEventListener('hashchange', syncLocation);
addEventListener('pageshow', event => { if (privatePath && event.persisted) load(); });
render();
if (privatePath) openShareLink();
else load();
