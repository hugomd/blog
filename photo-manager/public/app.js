const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: { photos: [], albums: [] }, etag: 'new' };
let busy = false, dirty = false, conflicted = false, filter = 'all', search = '';
let publicationTimer;
let operationTimer;
let transfer = null;
// Pending photostream order while reordering; null otherwise.
let streamOrder = null, streamCustom = false;
const operationStarted = Symbol('operation started');
const editor = $('#editor');
const deletion = $('#delete-confirmation');
const image = (id, size = 640) => `/api/photos/${encodeURIComponent(id)}/image?size=${size}`;
const publishing = () => state.publication?.state === 'running';
const operating = () => state.operation?.state === 'running';
const changing = () => operating() || !!transfer;
const duration = seconds => Number.isFinite(seconds) ? `${Math.floor(seconds / 60)}:${String(Math.floor(seconds % 60)).padStart(2, '0')}` : '';
const videoBadge = photo => photo.kind === 'video' ? `<span class="video-badge">Video ${duration(photo.duration)}</span>` : '';
const thumbnail = (photo, size = 640) => `<span class="media-image"><img data-preview="${photo.id}" src="${image(photo.id, size)}" width="${photo.width}" height="${photo.height}" alt="${escape(photo.alt)}" ${size === 640 ? 'loading="lazy"' : ''}></span>`;
const preview = photo => photo.kind === 'video' ? `<video data-preview="${photo.id}" width="${photo.width}" height="${photo.height}" controls preload="metadata" playsinline poster="${image(photo.id, 1280)}" src="/api/photos/${encodeURIComponent(photo.id)}/video" aria-label="${escape(photo.alt || photo.filename)}"></video>` : thumbnail(photo, 1280);
const cameraFields = ['Make', 'Camera', 'Lens', 'ISO', 'Shutter', 'Aperture', 'Focal length'];
let indexedCatalog, photosById, albumsByPhoto;
function indexCatalog() {
  if (indexedCatalog === state.catalog) return;
  photosById = new Map(state.catalog.photos.map(photo => [photo.id, photo]));
  albumsByPhoto = new Map();
  for (const album of state.catalog.albums) {
    for (const id of album.photoIds) {
      if (!albumsByPhoto.has(id)) albumsByPhoto.set(id, []);
      albumsByPhoto.get(id).push(album);
    }
  }
  indexedCatalog = state.catalog;
}
const photoById = id => { indexCatalog(); return photosById.get(id); };
const photoAlbums = id => { indexCatalog(); return albumsByPhoto.get(id) || []; };
const photoReady = (photo, albums) => !photo.archived && !photo.archiving && !photo.deleting && (photo.published || (albums ? albums.some(album => album.published && album.photoIds.includes(photo.id)) : photoAlbums(photo.id).some(album => album.published)));
const privateAlbum = album => album.access === 'password' || album.access === 'link';
const photoPublic = (photo, albums = state.catalog.albums) => photoReady(photo, albums.filter(album => !privateAlbum(album)));
const accessLabel = album => album.access === 'link' ? 'Private share link' : album.access === 'password' ? 'Password protected' : 'Public';
const badge = published => `<span class="badge ${published ? '' : 'draft'}">${published ? 'Ready to publish' : 'Draft'}</span>`;

const blurhashCache = new Map();
// Same standard Base83/DCT decoding as the independently bundled public gallery.
function blurhashBackground(photo) {
  const hash = photo.blurhash;
  const alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~';
  if (typeof hash !== 'string' || hash.length < 6 || hash.length > 166 || [...hash].some(c => !alphabet.includes(c))) return 'none';
  const base83 = value => [...value].reduce((result, c) => result * 83 + alphabet.indexOf(c), 0);
  const size = base83(hash[0]), nx = size % 9 + 1, ny = Math.floor(size / 9) + 1;
  if (size > 80 || hash.length !== 4 + 2 * nx * ny || !(photo.width > 0 && photo.height > 0)) return 'none';
  const width = Math.max(1, Math.round(32 * Math.min(1, photo.width / photo.height)));
  const height = Math.max(1, Math.round(32 * Math.min(1, photo.height / photo.width)));
  const key = `${hash}:${width}:${height}`;
  if (blurhashCache.has(key)) {
    const background = blurhashCache.get(key);
    blurhashCache.delete(key); blurhashCache.set(key, background);
    return background;
  }
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
  canvas.width = width;
  canvas.height = height;
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
  const background = `url(${canvas.toDataURL()})`;
  if (blurhashCache.size >= 512) blurhashCache.delete(blurhashCache.keys().next().value);
  blurhashCache.set(key, background);
  return background;
}

const pendingPreviews = new Set();
const preparedPreviews = new WeakSet();
const previewObserver = new IntersectionObserver(entries => {
  for (const { target: media, isIntersecting } of entries) {
    if (!isIntersecting) continue;
    if (media.isConnected && !(media instanceof HTMLImageElement && media.naturalWidth)) {
      const surface = media instanceof HTMLImageElement ? media.parentElement : media;
      surface.style.backgroundImage = blurhashBackground(photoById(media.dataset.preview));
    }
    previewObserver.unobserve(media); pendingPreviews.delete(media);
  }
}, { rootMargin: '200px' });
function revealPreviews(container) {
  for (const media of pendingPreviews) {
    if (!media.isConnected) { previewObserver.unobserve(media); pendingPreviews.delete(media); }
  }
  for (const media of container.querySelectorAll('[data-preview]')) {
    if (preparedPreviews.has(media)) continue;
    preparedPreviews.add(media);
    const photo = photoById(media.dataset.preview);
    const surface = media instanceof HTMLImageElement ? media.parentElement : media;
    surface.style.setProperty('--aspect', photo.width / photo.height);
    pendingPreviews.add(media); previewObserver.observe(media);
    if (media instanceof HTMLImageElement) {
      const reveal = () => {
        if (!media.naturalWidth) return;
        media.classList.add('loaded');
        previewObserver.unobserve(media); pendingPreviews.delete(media);
      };
      media.addEventListener('load', reveal, { once: true });
      reveal();
    }
  }
}

// Append pages instead of replacing existing media or mounting an entire library.
function renderPages(container, items, itemHTML) {
  container.innerHTML = '<div class="grid"></div><div class="result-controls"><span class="hint" role="status"></span><button type="button" data-browse>Show more</button></div>';
  const grid = container.firstElementChild, controls = container.lastElementChild;
  const more = controls.querySelector('button');
  let shown = 0;
  function appendPage() {
    const previous = shown;
    const page = items.slice(shown, shown + 60);
    grid.insertAdjacentHTML('beforeend', page.map(itemHTML).join(''));
    shown += page.length;
    controls.querySelector('span').textContent = `Showing ${shown} of ${items.length}`;
    const moveFocus = document.activeElement === more;
    more.hidden = shown === items.length;
    revealPreviews(grid); refreshButtons();
    if (moveFocus) {
      const first = grid.children[previous];
      (first.matches('button,a') ? first : first.querySelector('input,button,a'))?.focus();
    }
  }
  more.onclick = appendPage;
  appendPage();
}

async function api(path, { method = 'GET', body, headers = {} } = {}) {
  const backgroundChange = ['PUT', 'PATCH', 'DELETE'].includes(method) || (method === 'POST' && (/^\/photos\/[^/]+\/(archive|restore)$/.test(path) || ['/uploads/process', '/photostream', '/photos/archive'].includes(path)));
  const response = await fetch(`/api${path}`, {
    method, headers: { ...(method === 'GET' ? {} : { 'If-Match': state.etag }), ...(backgroundChange ? { Prefer: 'respond-async' } : {}), ...(body && !(body instanceof File) ? { 'Content-Type': 'application/json' } : {}), ...headers },
    body: body instanceof File ? body : body ? JSON.stringify(body) : undefined,
  });
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 409) conflicted = true;
    if (response.status === 401 && path !== '/login') {
      clearTimeout(publicationTimer); clearTimeout(operationTimer); state.publication = null; state.operation = null;
      deletion.close(); editor.close(); $('#studio').hidden = true; $('#login').hidden = false;
      $('#publication-progress').hidden = true; refreshButtons();
      $('#operation-progress').hidden = true;
    }
    throw new Error(result.error || 'The request failed. Reload before retrying.');
  }
  if (response.status === 202 && result.operation) {
    state.operation = result.operation;
    dirty = false; deletion.close(); editor.close();
    renderOperation(); refreshButtons();
    watchOperation();
    throw operationStarted; // Do not run a caller's synchronous success message.
  }
  return result;
}

function showError(error) {
  $('#error span').textContent = error.message; $('#error').hidden = false;
  if ($('#studio').hidden) $('#login-error').textContent = error.message;
  if (editor.open || deletion.open) {
    $('#dialog-error')?.remove();
    const notice = document.createElement('p');
    notice.id = 'dialog-error'; notice.className = 'error'; notice.setAttribute('role', 'alert'); notice.textContent = error.message;
    if (conflicted) {
      const reload = document.createElement('button'); reload.textContent = 'Reload library'; reload.dataset.reload = '';
      reload.onclick = () => $('#reload').click(); notice.append(reload);
    }
    $(deletion.open ? '#delete-body' : '#editor-body').prepend(notice); notice.scrollIntoView({ block: 'nearest' });
  }
}
function status(message) { $('#status').textContent = message; }
function refreshButtons() {
  document.querySelectorAll('button').forEach(button => { button.disabled = busy || ((publishing() || changing()) && !button.matches('.photo-card,[data-browse],#close-editor,#reload,#cancel-delete,#logout,#cancel-upload')) || button.hasAttribute('data-boundary') || (conflicted && !button.matches('#reload,#close-editor,#cancel-delete,#cancel-upload,[data-reload]')); });
  document.querySelectorAll('#album-form input,#album-form textarea,#album-form select,#editor-body input,#editor-body textarea,#editor-body select').forEach(input => { input.disabled = publishing() || changing(); });
  $('#publish').disabled = busy || publishing() || changing() || conflicted || (!state.publicationChanges?.changed && state.publication?.state !== 'failed');
  $('#republish').disabled = busy || publishing() || changing() || conflicted || state.publicationChanges?.changed || state.publishedRevision == null;
  $('#republish').title = state.publicationChanges?.changed ? 'Publish or undo saved publication changes before republishing.' : 'Rebuild published manifests and fill missing BlurHashes without re-uploading media.';
  $('#cancel-upload').disabled = busy || !transfer || transfer.processing || transfer.controller.signal.aborted;
}
function renderOperation() {
  $('#operation-progress').hidden = !operating();
  if (!operating()) return;
  const job = state.operation;
  const uploads = job.path.endsWith('/process') || job.path.endsWith('/complete');
  $('#operation-progress span').textContent = `${uploads ? `Processing uploaded files${job.total ? ` · ${job.completed || 0} / ${job.total} finished` : ''}` : job.method === 'DELETE' ? 'Deleting' : job.path.endsWith('/archive') ? 'Archiving' : job.path.endsWith('/restore') ? 'Restoring' : 'Saving'} in the background. You can browse or leave this page.`;
  const progress = $('#operation-progress progress');
  if (job.total) { progress.max = job.total; progress.value = job.completed || 0; }
  else progress.removeAttribute('value');
  status('Change accepted. Waiting for storage to finish…');
}
async function watchOperation() {
  clearTimeout(operationTimer);
  try {
    const { operation: job } = await api('/operation');
    if (job.state !== 'running') {
      Object.assign(state, await api('/catalog')); render();
      if (job.state === 'failed') {
        status('Change failed. Library reloaded; check the current state before retrying.'); showError(new Error(job.error));
      } else status('Change complete. Publish separately to apply saved catalog changes to the website.');
    } else state.operation = job;
    renderOperation(); refreshButtons();
  } catch (error) {
    showError(error);
  }
  if (operating() && !$('#studio').hidden) operationTimer = setTimeout(watchOperation, 1000);
}
function renderPublication() {
  const job = state.publication;
  $('#publish').textContent = publishing() ? 'Publishing…' : job?.state === 'failed' ? 'Retry publication ↗' : 'Publish changes ↗';
  $('#publish-status').textContent = publishing() ? 'Publication in progress' : job?.state === 'failed' ? 'Publication failed' : state.publicationChanges?.changed ? 'Unpublished changes' : 'No changes to publish';
  $('#publication-progress').hidden = !publishing();
  if (!publishing()) return;
  $('#publication-progress span').textContent = job.total ? `Copying media files · ${job.completed} / ${job.total}. You can leave this page.` : 'Preparing publication. You can leave this page.';
  const progress = $('#publication-progress progress');
  if (job.total) { progress.max = job.total; progress.value = job.completed; }
  else progress.removeAttribute('value');
}
async function watchPublication() {
  clearTimeout(publicationTimer);
  try {
    Object.assign(state, await api('/publication'));
    const job = state.publication;
    if (job.state === 'succeeded') { Object.assign(state, await api('/catalog')); render(); status('Publication complete.'); }
    else if (job.state === 'failed') { status('Publication failed. Reload and retry.'); showError(new Error(job.error)); }
    renderPublication(); refreshButtons();
  } catch (error) { showError(error); }
  if (publishing() && !$('#studio').hidden) publicationTimer = setTimeout(watchPublication, 1000);
}
async function work(task) {
  if (busy) return;
  busy = true; refreshButtons(); $('#error').hidden = true; $('#dialog-error')?.remove();
  try { await task(); } catch (error) { if (error !== operationStarted) showError(error); }
  finally { busy = false; refreshButtons(); }
}

function render() {
  $('#photo-count').textContent = state.catalog.photos.filter(photo => !photo.archived).length;
  $('#photostream-count').textContent = state.catalog.photos.filter(photo => photoPublic(photo)).length;
  $('#archived-count').textContent = state.catalog.photos.filter(photo => photo.archived).length;
  $('#album-count').textContent = state.catalog.albums.length;
  $('#sandbox').hidden = !state.development;
  $('#logout').hidden = state.authMode === 'proxy';
  renderPublication();
  renderOperation();
  const route = location.hash.slice(1) || 'photos';
  $('#nav-photos').toggleAttribute('aria-current', route === 'photos');
  $('#nav-photostream').toggleAttribute('aria-current', route === 'photostream');
  $('#nav-albums').toggleAttribute('aria-current', route.startsWith('album'));
  $('#nav-archived').toggleAttribute('aria-current', route === 'archived');
  if (route.startsWith('album/')) renderAlbum(route.slice(6));
  else if (route === 'albums') renderAlbums();
  else if (route === 'photostream') renderPhotostream();
  else if (route === 'archived') renderArchived();
  else renderPhotos();
  refreshButtons();
}

function renderPhotos() {
  const archivable = [...state.catalog.photos].reverse().filter(photo => !photo.archived && !photo.archiving && !photo.deleting);
  $('#main').innerHTML = `<div class="page-heading"><div><h1>All photos & videos</h1></div><div class="library-actions"><button id="choose-archive" ${archivable.length ? '' : 'disabled data-boundary'}>Archive…</button><button class="primary" data-upload>＋ Upload files</button></div></div>
    <div class="toolbar"><label class="search"><span class="hint">Search your library</span><input id="search" type="search" placeholder="Names, captions, tags…" value="${escape(search)}"></label><label><span class="hint">Show</span><select id="filter"><option value="all">All photos & videos</option><option value="draft">Drafts</option><option value="ready">Ready to publish</option><option value="standalone">Not in an album</option></select></label></div>
    <div id="photo-results"></div>`;
  function renderResults() {
    const query = search.toLowerCase();
    const photos = [...state.catalog.photos].reverse().filter(photo => {
      if (photo.archived) return false;
      if (filter === 'draft' && photoReady(photo)) return false;
      if (filter === 'ready' && !photoReady(photo)) return false;
      if (filter === 'standalone' && photoAlbums(photo.id).length) return false;
      return `${photo.filename} ${photo.alt} ${photo.caption} ${photo.tags.join(' ')}`.toLowerCase().includes(query);
    });
    if (photos.length) renderPages($('#photo-results'), photos, photo => {
      const count = photoAlbums(photo.id).length;
      return `<button class="photo-card" data-photo="${photo.id}" aria-label="${photo.deleting ? 'Retry deletion of' : 'Edit'} ${escape(photo.alt || photo.filename)}"><div class="photo-frame">${photo.deleting ? '<div class="placeholder">×</div>' : thumbnail(photo)}${videoBadge(photo)}</div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong>${photo.deleting ? '<span class="badge draft">Deletion incomplete</span>' : badge(photoReady(photo))}</div><div class="card-subtitle">${count ? `${count} album${count === 1 ? '' : 's'}` : photo.kind === 'video' ? 'Standalone video' : 'Standalone photo'}${photo.capturedDate ? ` · ${escape(photo.capturedDate)}` : ''}</div></button>`;
    });
    else $('#photo-results').innerHTML = `<div class="empty"><h2>${state.catalog.photos.length ? 'No matching photos or videos.' : 'No photos or videos yet.'}</h2><p>${state.catalog.photos.length ? 'Try another filter or search.' : 'Drop JPEGs, PNGs, MP4s or MOVs here, or choose files. Photos: up to 30 MiB; videos: up to 512 MiB. Every file starts as a private draft.'}</p><button data-upload>Choose files →</button></div>`;
    refreshButtons();
  }
  renderResults();
  $('#filter').value = filter;
  $('#filter').onchange = event => { filter = event.target.value; renderResults(); };
  $('#search').oninput = event => { search = event.target.value; renderResults(); };
  $('#choose-archive').onclick = () => archiveFiles(archivable, photo => photoPublic(photo) ? '<small class="hint">In the photostream</small>' : '');
}

function archiveFiles(candidates, note) {
  chooseFiles({
    title: 'Archive photos & videos',
    intro: '<p>Files, metadata and album memberships are kept, and you can restore them from Archived.</p><p class="hint access-warning">Archiving takes effect immediately: selected files leave the live public index and every historical release, without publishing other changes. Cached copies and direct media URLs may remain accessible.</p>',
    candidates, note,
    verb: 'Archive', hint: 'This applies immediately.', danger: true,
    submit: async photoIds => {
      Object.assign(state, await api('/photos/archive', { method: 'POST', body: { photoIds } }));
      return 'Archived. Files kept; public manifests updated.';
    },
  });
}

function renderPhotostream() {
  const candidates = [...state.catalog.photos].reverse().filter(photo => !photo.archived && !photo.archiving && !photo.deleting && !photoPublic(photo));
  const saved = photostreamPhotos();
  if (streamOrder) {
    // Keep a pending order across re-renders: drop files that left, and put new ones first like the publisher.
    const ids = new Set(saved.map(photo => photo.id));
    streamOrder = [...saved.map(photo => photo.id).filter(id => !streamOrder.includes(id)), ...streamOrder.filter(id => ids.has(id))];
  }
  const photos = streamOrder ? streamOrder.map(photoById) : saved;
  const source = photo => {
    const albums = state.catalog.albums.filter(album => album.published && !privateAlbum(album) && album.photoIds.includes(photo.id)).map(album => album.title);
    return [photo.published ? 'Published independently' : '', ...albums.map(title => `Via ${title}`)].filter(Boolean).join(' · ');
  };
  const card = (photo, i) => streamOrder
    ? `<button type="button" class="photo-card" data-order-id="${photo.id}" aria-roledescription="sortable photo" aria-label="${escape(photo.caption || photo.filename)}, position ${i + 1} of ${photos.length}"><div class="photo-frame">${thumbnail(photo)}${videoBadge(photo)}<span class="order-badge">${i + 1}</span></div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong></div><div class="card-subtitle">${escape(source(photo))}</div></button>`
    : `<button class="photo-card" data-photo="${photo.id}" aria-label="Edit ${escape(photo.alt || photo.filename)}"><div class="photo-frame">${thumbnail(photo)}${videoBadge(photo)}</div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong></div><div class="card-subtitle">${escape(source(photo))}${photo.capturedDate ? ` · ${escape(photo.capturedDate)}` : ''}</div></button>`;
  $('#main').innerHTML = streamOrder
    ? `<div class="page-heading"><div><h1>Photostream</h1><p>Drag photos into website order; on touch screens, press and hold to pick one up. With a keyboard, press Space on a photo, move it with the arrow keys, then press Space again. Save, then publish.</p></div></div>
      <div class="grid reorder-grid" id="stream-grid">${photos.map(card).join('')}</div>
      <div class="reorder-bar" role="group" aria-label="Reorder photostream"><span id="reorder-state">${dirty ? 'Unsaved order' : 'Drag to reorder'}</span><select id="stream-sort" aria-label="Sort photostream"><option value="">Sort…</option><option value="upload">Newest upload first</option><option value="-1">Newest captured first</option><option value="1">Oldest captured first</option></select><button type="button" id="discard-order">Discard</button><button type="button" class="primary" id="save-order">Save order</button></div>`
    : `<div class="page-heading"><div><h1>Photostream</h1><p>Public photos and videos in website order. Includes files published independently or through public albums; new additions appear first. Publish changes to update the website.</p></div><div class="library-actions"><button id="reorder-photostream" ${photos.length > 1 ? '' : 'disabled data-boundary'}>Reorder</button><button id="choose-stream-archive" ${photos.length ? '' : 'disabled data-boundary'}>Archive…</button><button id="choose-photostream" class="primary" ${candidates.length ? '' : 'disabled data-boundary'}>＋ Add to photostream</button></div></div>
      ${photos.length ? `<div class="grid">${photos.map(card).join('')}</div>` : '<div class="empty"><h2>The photostream is empty.</h2><p>Add photos or videos from your library, or publish a public album.</p></div>'}`;
  revealPreviews($('#main'));
  if (streamOrder) return reorderPhotostream();
  $('#reorder-photostream').onclick = () => {
    streamOrder = saved.map(photo => photo.id); streamCustom = Boolean(state.catalog.photostreamOrder?.length);
    renderPhotostream(); refreshButtons();
  };
  $('#choose-stream-archive').onclick = () => archiveFiles(photos, photo => `<small class="hint">${escape(source(photo))}</small>`);
  $('#choose-photostream').onclick = () => chooseFiles({
    title: 'Add to the public photostream',
    intro: '<p>Select multiple photos or videos to publish independently of albums. Items already ready for the public photostream are omitted.</p><p class="hint access-warning">This makes selected files public, including files in private share-link or password-protected albums. Saving stages the change; Publish changes makes it live.</p>',
    candidates,
    note: photo => state.catalog.albums.some(album => privateAlbum(album) && album.photoIds.includes(photo.id)) ? '<small class="hint access-warning">Private album · becomes public</small>' : '',
    verb: 'Add', hint: 'Publish separately when ready.',
    submit: async photoIds => {
      Object.assign(state, await api('/photostream', { method: 'POST', body: { photoIds } }));
      return 'Selection saved for the public photostream. Publish changes when ready.';
    },
  });
}

// Mirrors the publisher: photos missing from the saved order lead, newest upload first.
function photostreamPhotos() {
  const rank = new Map((state.catalog.photostreamOrder || []).map((id, i) => [id, i + 1]));
  return state.catalog.photos.filter(photo => photoPublic(photo)).sort((a, b) => {
    const left = rank.get(a.id) || 0, right = rank.get(b.id) || 0;
    if (left !== right) return !left || !right ? (left ? 1 : -1) : left - right;
    return b.uploadedAt.localeCompare(a.uploadedAt);
  });
}

let photostreamDragging = false;
// Stop touch scrolling only once a press-and-hold has picked a photo up.
document.addEventListener('touchmove', event => { if (photostreamDragging) event.preventDefault(); }, { passive: false });

function reorderPhotostream() {
  const grid = $('#stream-grid'), cards = () => [...grid.children];
  const name = card => photoById(card.dataset.orderId)?.caption || photoById(card.dataset.orderId)?.filename;
  const position = card => cards().indexOf(card) + 1;
  const motion = !matchMedia('(prefers-reduced-motion: reduce)').matches;
  // FLIP: move cards in the DOM, then animate each from its old position.
  const animateMove = mutate => {
    const before = new Map(cards().map(card => [card, card.getBoundingClientRect()]));
    mutate();
    cards().forEach((card, i) => {
      card.querySelector('.order-badge').textContent = i + 1;
      card.setAttribute('aria-label', `${name(card)}, position ${i + 1} of ${grid.children.length}`);
      const from = before.get(card), to = card.getBoundingClientRect();
      if (motion && (from.left !== to.left || from.top !== to.top)) card.animate([{ transform: `translate(${from.left - to.left}px, ${from.top - to.top}px)` }, { transform: 'none' }], { duration: 180, easing: 'cubic-bezier(.2,.7,.3,1)' });
    });
  };
  const commit = () => {
    const order = cards().map(card => card.dataset.orderId);
    if (order.join() === streamOrder.join()) return;
    streamOrder = order; streamCustom = true; dirty = true;
    $('#reorder-state').textContent = 'Unsaved order';
  };
  const restore = order => animateMove(() => order.forEach(id => grid.append(grid.querySelector(`[data-order-id="${id}"]`))));

  grid.ondragstart = event => event.preventDefault();
  grid.oncontextmenu = event => { if (event.target.closest('[data-order-id]')) event.preventDefault(); };
  grid.onpointerdown = event => {
    const card = event.target.closest('[data-order-id]');
    if (!card || event.button !== 0 || busy || publishing() || changing()) return;
    const start = { x: event.clientX, y: event.clientY }, original = [...streamOrder];
    let point = start, ghost = null, frame = 0;
    const timer = event.pointerType === 'touch' ? setTimeout(begin, 280) : 0;
    function begin() {
      photostreamDragging = true;
      const rect = card.getBoundingClientRect();
      ghost = card.cloneNode(true);
      ghost.classList.add('drag-ghost');
      Object.assign(ghost.style, { left: `${rect.left}px`, top: `${rect.top}px`, width: `${rect.width}px` });
      document.body.append(ghost); document.body.classList.add('photostream-dragging');
      card.classList.add('dragging');
      navigator.vibrate?.(10);
      follow();
      frame = requestAnimationFrame(scroll);
    }
    function follow() {
      ghost.style.transform = `translate(${point.x - start.x}px, ${point.y - start.y}px) scale(1.04)`;
      const target = document.elementFromPoint(point.x, point.y)?.closest('[data-order-id]');
      if (!target || target === card || !grid.contains(target) || target.getAnimations().length) return;
      const rect = target.getBoundingClientRect();
      const reference = point.x > rect.left + rect.width / 2 ? target.nextElementSibling : target;
      if (reference !== card && reference !== card.nextElementSibling) animateMove(() => grid.insertBefore(card, reference));
    }
    // Scroll near the viewport edges so a photo can travel the whole stream.
    function scroll() {
      const edge = 80, speed = point.y < edge ? point.y - edge : point.y > innerHeight - edge ? point.y - innerHeight + edge : 0;
      if (speed) { scrollBy(0, speed / 3); follow(); }
      frame = requestAnimationFrame(scroll);
    }
    function move(next) {
      point = { x: next.clientX, y: next.clientY };
      if (!ghost) {
        const distance = Math.hypot(point.x - start.x, point.y - start.y);
        if (event.pointerType === 'touch') { if (distance > 10) end(false); return; } // The person is scrolling.
        if (distance < 5) return;
        begin();
      }
      next.preventDefault();
      follow();
    }
    function end(drop) {
      clearTimeout(timer); cancelAnimationFrame(frame);
      removeEventListener('pointermove', move); removeEventListener('pointerup', up); removeEventListener('pointercancel', cancel);
      if (!ghost) return;
      photostreamDragging = false;
      ghost.remove(); document.body.classList.remove('photostream-dragging'); card.classList.remove('dragging');
      if (drop) commit(); else restore(original);
    }
    const up = () => end(true), cancel = () => end(false);
    addEventListener('pointermove', move); addEventListener('pointerup', up); addEventListener('pointercancel', cancel);
  };

  // Keyboard: Space or Enter picks up and drops, arrows move, Escape cancels.
  let held = null, heldOrder = null;
  grid.onkeydown = event => {
    const card = event.target.closest('[data-order-id]');
    if (!card) return;
    const columns = getComputedStyle(grid).gridTemplateColumns.split(' ').length;
    const step = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -columns, ArrowDown: columns }[event.key];
    if (event.key === ' ' || event.key === 'Enter') {
      event.preventDefault();
      if (held) { held.classList.remove('held'); held = null; commit(); status(`Dropped ${name(card)} at position ${position(card)}. Save order to keep it.`); }
      else { held = card; heldOrder = [...streamOrder]; card.classList.add('held'); status(`Picked up ${name(card)}, position ${position(card)} of ${grid.children.length}. Use the arrow keys to move it, Space to drop, Escape to cancel.`); }
    } else if (held && step) {
      event.preventDefault();
      const all = cards(), index = Math.max(0, Math.min(all.length - 1, all.indexOf(held) + step));
      animateMove(() => grid.insertBefore(held, index > all.indexOf(held) ? all[index].nextElementSibling : all[index]));
      held.focus(); held.scrollIntoView({ block: 'nearest' });
      status(`${name(held)}, position ${position(held)} of ${all.length}.`);
    } else if (held && event.key === 'Escape') {
      event.preventDefault(); event.stopPropagation();
      held.classList.remove('held'); restore(heldOrder); held.focus(); held = null;
      status('Move cancelled.');
    }
  };
  grid.onfocusout = event => { if (held && !grid.contains(event.relatedTarget)) { held.classList.remove('held'); held = null; commit(); } };

  $('#stream-sort').onchange = event => {
    const by = event.target.value;
    if (!by) return;
    const photos = streamOrder.map(photoById);
    if (by === 'upload') photos.sort((a, b) => b.uploadedAt.localeCompare(a.uploadedAt));
    else photos.sort((a, b) => !a.capturedDate ? (b.capturedDate ? 1 : 0) : !b.capturedDate ? -1 : Number(by) * a.capturedDate.localeCompare(b.capturedDate));
    restore(photos.map(photo => photo.id));
    // Upload order saves as no manual order, so files re-added later keep their upload position.
    streamOrder = photos.map(photo => photo.id); streamCustom = by !== 'upload'; dirty = true;
    $('#reorder-state').textContent = 'Unsaved order'; event.target.value = '';
    status(`Sorted ${by === 'upload' ? 'by upload, newest' : `by capture date, ${by === '1' ? 'oldest' : 'newest'}`} first; undated files go last. Save order, then Publish changes.`);
  };
  $('#discard-order').onclick = () => { streamOrder = null; dirty = false; renderPhotostream(); refreshButtons(); };
  $('#save-order').onclick = () => work(async () => {
    const photoIds = streamCustom ? streamOrder : [];
    try { Object.assign(state, await api('/photostream/order', { method: 'PUT', body: { photoIds } })); }
    catch (error) { if (error !== operationStarted) throw error; }
    // Show the saved order now; the catalog reloads when the background change finishes.
    state.catalog.photostreamOrder = photoIds.length ? photoIds : undefined;
    streamOrder = null; dirty = false; renderPhotostream();
  });
}

// Multi-select dialog for library actions that apply to several files at once.
function chooseFiles({ title, intro, candidates, note = () => '', verb, hint, danger = false, submit }) {
  openDialog(`<form id="choose-form" class="simple-dialog"><h2 id="dialog-title">${title}</h2>${intro}<div class="grid">${candidates.map(photo => `<label class="choice">${thumbnail(photo)}<input type="checkbox" name="photo" value="${photo.id}"><span>${escape(photo.caption || photo.filename)}${photo.kind === 'video' ? ` · Video ${duration(photo.duration)}` : ''}</span>${note(photo)}</label>`).join('')}</div><div class="editor-footer"><span class="hint" id="choose-count" role="status"></span><button class="${danger ? 'danger' : 'primary'}" id="choose-submit">${verb} selected files</button></div></form>`);
  const form = $('#choose-form');
  const updateSelection = () => {
    const count = new FormData(form).getAll('photo').length;
    $('#choose-count').textContent = `${count} file${count === 1 ? '' : 's'} selected. ${hint}`;
    $('#choose-submit').textContent = count ? `${verb} ${count} file${count === 1 ? '' : 's'}` : `${verb} selected files`;
    $('#choose-submit').toggleAttribute('data-boundary', !count);
    refreshButtons();
  };
  form.onchange = updateSelection;
  form.onsubmit = event => {
    event.preventDefault();
    const photoIds = new FormData(form).getAll('photo');
    if (!photoIds.length) return;
    work(async () => {
      const message = await submit(photoIds);
      dirty = false; editor.close(); render(); status(message);
    });
  };
  updateSelection();
}

function renderArchived() {
  const photos = [...state.catalog.photos].reverse().filter(photo => photo.archived);
  $('#main').innerHTML = `<div class="page-heading"><div><h1>Archived</h1><p>Files and album memberships are kept. Restored photos follow their albums’ publication status.</p></div></div>
    ${photos.length ? '<div id="archived-results"></div>' : '<div class="empty"><h2>No archived photos or videos.</h2><p>Archive a photo or video from its details to remove it from public manifests without deleting its files.</p></div>'}`;
  if (photos.length) renderPages($('#archived-results'), photos, photo => `<button class="photo-card" data-photo="${photo.id}" aria-label="View archived ${escape(photo.alt || photo.filename)}"><div class="photo-frame">${photo.deleting ? '<div class="placeholder">×</div>' : thumbnail(photo)}${videoBadge(photo)}</div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong><span class="badge draft">${photo.deleting ? 'Deletion incomplete' : photo.archiving ? 'Archive incomplete' : 'Archived'}</span></div></button>`);
}

function renderAlbums() {
  $('#main').innerHTML = `<div class="page-heading"><div><h1>Albums</h1></div><button class="primary" id="create-album">＋ New album</button></div>
  ${state.catalog.albums.length ? `<div class="grid album-grid">${state.catalog.albums.map(album => {
    let cover;
    for (const id of [album.coverId, ...album.photoIds]) {
      const photo = photoById(id);
      if (photo && !photo.deleting && !photo.archived) { cover = photo; break; }
    }
    return `<a class="photo-card" href="#album/${album.id}"><div class="photo-frame">${cover ? `${thumbnail(cover)}${videoBadge(cover)}` : '<div class="placeholder" aria-hidden="true">◧</div>'}</div><div class="card-line"><strong>${escape(album.title)}</strong>${badge(album.published)}</div><div class="card-subtitle">${accessLabel(album)} · ${album.photoIds.length} photos/videos${privateAlbum(album) ? '' : ` · /${escape(album.slug)}/`}</div></a>`;
  }).join('')}</div>` : '<div class="empty"><h2>No albums yet.</h2></div>'}`;
  revealPreviews($('#main'));
  $('#create-album').onclick = () => {
    openDialog(`<form id="create-form" class="simple-dialog"><h2 id="dialog-title">Create an album</h2><label>Album title<input name="title" placeholder="New York, September 2022" maxlength="150" required autofocus></label><p class="muted">Choose photos, a cover, and a URL after creating the album.</p><div class="editor-footer"><button class="primary">Create draft album →</button></div></form>`);
    $('#create-form').onsubmit = event => {
      event.preventDefault();
      work(async () => {
        const result = await api('/albums', { method: 'POST', body: { title: new FormData(event.target).get('title') } });
        Object.assign(state, result); dirty = false; editor.close(); location.hash = `album/${state.catalog.albums.at(-1).id}`; render(); status('Draft album created.');
      });
    };
  };
}

function renderAlbum(id) {
  const album = state.catalog.albums.find(album => album.id === id);
  if (!album) { location.hash = 'albums'; return; }
  const shareURL = privateAlbum(album) && state.galleryURL && (album.access !== 'link' || album.linkToken) ? `${state.galleryURL.replace(/\/?$/, '/')}private/${encodeURIComponent(album.id)}/${album.access === 'link' ? `#key=${encodeURIComponent(album.linkToken)}` : ''}` : '';
  const passwordSet = album.access === 'password' && album.passwordSet;
  $('#main').innerHTML = `<a class="back" href="#albums">← All albums</a><div class="page-heading"><div><h1>${escape(album.title)}</h1><p>Save edits, then publish to update the website.</p></div><div id="album-labels" class="album-labels">${badge(album.published)}<span class="badge">${accessLabel(album)}</span></div></div>
    <form id="album-form"><div class="album-form"><label>Title<input name="title" value="${escape(album.title)}" maxlength="150" required></label><label>URL slug<input name="slug" value="${escape(album.slug)}" pattern="[a-z0-9]+(-[a-z0-9]+)*" maxlength="100" required><small>Used for the album page’s address.</small></label><label class="wide">Description<textarea name="description" maxlength="4000">${escape(album.description)}</textarea></label><label class="check wide"><input name="published" type="checkbox" ${album.published ? 'checked' : ''}>Include this album and its photos in the next publication</label></div>
    <div class="album-form access-settings"><label>Access<select name="access" aria-describedby="album-access-help"><option value="public">Public</option><option value="link" ${!state.privateAlbumsEnabled && album.access !== 'link' ? 'disabled' : ''}>Private share link</option><option value="password" ${!state.privateAlbumsEnabled && album.access !== 'password' ? 'disabled' : ''}>Password protected</option></select></label><label id="album-password-label">${passwordSet ? 'Change password' : 'New password'}<input name="password" type="password" autocomplete="new-password" placeholder="${passwordSet ? 'Leave empty to keep current password' : '12–128 characters'}"><small>12–128 characters. Share the password separately.</small></label></div>
    <p id="album-access-help" class="hint"></p>
    ${!state.privateAlbumsEnabled ? '<p class="hint access-warning">Private share-link and password-protected albums are unavailable: protected storage is not configured. Public albums still work; nonpublic access cannot be saved.</p>' : ''}
    <p class="hint">Access changes, link regeneration, and password replacements apply after Save, then Publish; old viewing sessions end. Changing access cannot recall previously public URLs or downloads.</p>
    <div id="album-share-settings" ${!privateAlbum(album) ? 'hidden' : ''}>
    ${shareURL ? `<div class="album-share"><label>Saved ${accessLabel(album).toLowerCase()} URL<input id="album-share-link" value="${escape(shareURL)}" readonly></label><button type="button" id="copy-album-link">Copy link</button><span id="copy-link-status" class="hint" role="status"></span></div>` : ''}
    ${album.access === 'link' && album.linkToken ? '<button type="button" id="regenerate-album-link">Regenerate link</button>' : ''}
    <p id="album-share-warning" class="hint access-warning" role="status"></p></div>
    <div class="section-heading album-files-heading"><h2>Photos & videos <span class="muted">${album.photoIds.length}</span></h2><div class="album-file-actions"><div class="album-date-sort" role="group" aria-label="Sort by capture date"><button type="button" data-sort-date="1" aria-describedby="album-order-help">Oldest first</button><button type="button" data-sort-date="-1" aria-describedby="album-order-help">Newest first</button></div><button type="button" id="choose-photos">＋ Choose files</button></div></div>
    <p class="hint" id="album-order-help">Order is top to bottom. Date sorting uses capture dates; undated files go last. − removes album membership only. Archived photos retain their place here but never appear in public manifests. Delete permanently removes files.</p><ol class="ordered-list" id="ordered-list"></ol>
    <div class="editor-footer"><button type="button" class="danger" id="delete-album">Remove album</button><button class="primary" type="submit">Save album</button></div></form>`;
  let order = [...album.photoIds], cover = album.coverId, regenerateLink = false;
  $('#album-form select[name=access]').value = album.access || 'public';
  function renderOrder() {
    const selected = { ...album, access: $('#album-form select[name=access]').value, published: $('#album-form input[name=published]').checked, photoIds: order };
    const albums = state.catalog.albums.map(item => item.id === album.id ? selected : item);
    $('#album-labels').innerHTML = `${badge(selected.published)}<span class="badge">${accessLabel(selected)}</span>`;
    $('#album-access-help').textContent = selected.access === 'link' ? 'Not listed or searchable in the public gallery. Anyone with the full link can view and forward it. No entered password is required. Save first to create the key; Publish to activate it. Saving this access removes any existing password.' : selected.access === 'password' ? 'Not listed or searchable in the public gallery. Viewers need the share link and password.' : 'Listed in the public gallery. Anyone can view this album.';
    $('#album-password-label').hidden = selected.access !== 'password';
    $('#album-form input[name=password]').required = selected.access === 'password' && !passwordSet;
    if (selected.access !== 'password') $('#album-form input[name=password]').setCustomValidity('');
    const shareBlocked = selected.access !== album.access || (selected.access === 'link' && regenerateLink);
    $('#album-share-settings').hidden = !privateAlbum(selected);
    if ($('#album-share-link')) $('#album-share-link').value = shareBlocked ? '' : shareURL;
    if ($('#copy-album-link')) $('#copy-album-link').toggleAttribute('data-boundary', shareBlocked || !state.privateAlbumsEnabled);
    if ($('#regenerate-album-link')) {
      $('#regenerate-album-link').hidden = selected.access !== 'link';
      $('#regenerate-album-link').toggleAttribute('data-boundary', regenerateLink || !state.privateAlbumsEnabled);
    }
    $('#album-share-warning').textContent = selected.access === 'link' && regenerateLink ? 'Link regeneration staged, not saved. Save, then Publish to revoke the previous link AND its viewing sessions. Copying the old link is disabled. Save returns the new link; it works only after Publish.' : selected.access !== album.access ? 'Access change not saved. Save, then Publish before sharing the new access settings.' : !shareURL ? 'Save to create the link. A configured gallery URL is required to copy it; Publish before sharing.' : !album.published ? 'Saved link only. Include this album in the next publication, Save, then Publish before sharing.' : state.publicationChanges?.changed ? 'Saved link; unpublished changes remain. Publish before sharing changed access settings or a regenerated link. The previously published access remains active until then.' : 'Saved access settings have no pending publication changes. Anyone with the full private link can forward it; password-protected albums also require the password.';
    $('#ordered-list').innerHTML = order.map((id, i) => {
      const photo = photoById(id);
      const visibility = photoPublic(photo, albums) ? privateAlbum(selected) ? 'Also public' : 'Public' : [...new Set(albums.filter(item => item.published && item.photoIds.includes(id)).map(accessLabel))].join(' · ');
      const detailsLabel = `${photo.archived ? 'View' : 'Edit'} details`;
      return `<li>${photo.deleting ? '' : `<button type="button" class="album-photo-preview" data-photo="${id}" aria-label="${detailsLabel} for ${escape(photo.caption || photo.filename)}">${thumbnail(photo)}</button>`}<div class="order-title"><p>${escape(photo.caption || photo.filename)}${photo.kind === 'video' ? ` · Video ${duration(photo.duration)}` : ''}</p><small>${escape(photo.capturedDate || 'No capture date')} · ${photo.deleting ? 'Deletion incomplete · retry to finish' : photo.archiving ? 'Archive incomplete · retry in Archived' : photo.archived ? 'Archived · not in public manifests' : photoReady(photo, albums) ? `Ready to publish · ${visibility}` : `Draft · ${accessLabel(selected)}`}</small>${photo.deleting ? '' : `<button type="button" class="album-edit-details" data-photo="${id}" aria-label="${detailsLabel} for ${escape(photo.caption || photo.filename)}">${detailsLabel}</button>`}</div><div class="order-buttons"><button type="button" data-move="${i}" data-direction="-1" aria-label="Move item ${i + 1} up" ${i === 0 ? 'disabled data-boundary' : ''}>↑</button><button type="button" data-move="${i}" data-direction="1" aria-label="Move item ${i + 1} down" ${i === order.length - 1 ? 'disabled data-boundary' : ''}>↓</button><button type="button" data-remove="${id}" aria-label="Remove item ${i + 1} from album" title="Remove from album; keep in library">−</button>${!photo.deleting && !photo.archived ? `<button type="button" data-archive-photo="${id}" data-action="archive" aria-label="Archive ${escape(photo.filename)}">Archive</button>` : ''}<button type="button" class="danger" data-delete-photo="${id}" aria-label="Delete ${escape(photo.filename)} permanently">${photo.deleting ? 'Retry delete' : 'Delete'}</button></div><label class="check cover-label"><input type="radio" name="cover" value="${id}" ${cover === id || (!cover && i === 0) ? 'checked' : ''}>Cover</label></li>`;
    }).join('') || '<p class="muted">No photos or videos selected yet.</p>';
    revealPreviews($('#ordered-list'));
    $('#ordered-list').onclick = event => {
      const move = event.target.closest('[data-move]'), remove = event.target.closest('[data-remove]');
      if (move) { const i = Number(move.dataset.move), target = i + Number(move.dataset.direction); if (target < 0 || target >= order.length) return; [order[i], order[target]] = [order[target], order[i]]; dirty = true; renderOrder(); }
      if (remove) { order = order.filter(id => id !== remove.dataset.remove); if (!order.includes(cover)) cover = ''; dirty = true; renderOrder(); }
    };
    $('#ordered-list').onchange = event => { cover = event.target.value; dirty = true; };
    document.querySelectorAll('[data-sort-date]').forEach(button => button.toggleAttribute('data-boundary', order.length < 2));
    refreshButtons();
  }
  renderOrder();
  document.querySelectorAll('[data-sort-date]').forEach(button => {
    button.onclick = () => {
      const dates = new Map(state.catalog.photos.map(photo => [photo.id, photo.capturedDate]));
      if (!cover) cover = order[0] || '';
      order.sort((a, b) => {
        const left = dates.get(a), right = dates.get(b);
        if (!left) return right ? 1 : 0;
        if (!right) return -1;
        return Number(button.dataset.sortDate) * left.localeCompare(right);
      });
      dirty = true; renderOrder();
      status(`Album sorted by capture date, ${button.dataset.sortDate === '1' ? 'oldest' : 'newest'} first. Save album, then Publish changes to update the website.`);
    };
  });
  if ($('#regenerate-album-link')) $('#regenerate-album-link').onclick = () => {
    regenerateLink = true; dirty = true;
    if ($('#copy-link-status')) $('#copy-link-status').textContent = '';
    renderOrder();
  };
  if (shareURL) $('#copy-album-link').onclick = async () => {
    if ($('#copy-album-link').disabled || $('#album-form select[name=access]').value !== album.access || regenerateLink) return;
    try { await navigator.clipboard.writeText(shareURL); $('#copy-link-status').textContent = 'Link copied'; }
    catch { $('#album-share-link').select(); $('#copy-link-status').textContent = 'Select and copy the link manually.'; }
  };
  $('#album-form').oninput = event => {
    if (event.target.id === 'album-share-link') return;
    dirty = true;
    if (event.target.name === 'password') event.target.setCustomValidity('');
    if (['published', 'access'].includes(event.target.name)) renderOrder();
  };
  $('#album-form select[name=access]').onchange = () => { dirty = true; renderOrder(); };
  $('#album-form').onsubmit = event => {
    event.preventDefault();
    const form = new FormData(event.target);
    const access = form.get('access'), password = form.get('password');
    if (privateAlbum({ access }) && !state.privateAlbumsEnabled) { showError(new Error('Private share-link and password-protected albums are unavailable. Configure protected storage before saving nonpublic access.')); return; }
    if (access === 'password' && (password || !passwordSet) && ([...password].length < 12 || [...password].length > 128)) {
      const input = $('#album-form input[name=password]');
      input.setCustomValidity('Use 12–128 characters.'); input.reportValidity(); return;
    }
    work(async () => {
      const result = await api(`/albums/${id}`, { method: 'PUT', body: { title: form.get('title'), slug: form.get('slug'), description: form.get('description'), published: form.has('published'), access, ...(access === 'password' && password ? { password } : {}), ...(access === 'link' && regenerateLink ? { regenerateLink: true } : {}), photoIds: order, coverId: form.get('cover') || '' } });
      Object.assign(state, result); dirty = false; render(); status('Album saved. Publish changes to apply access and website updates.');
    });
  };
  $('#choose-photos').onclick = () => {
    const photos = [...state.catalog.photos].reverse().filter(photo => !photo.deleting);
    const chosen = new Set(order.filter(id => !photoById(id).deleting));
    openDialog('<form id="choose-form" class="simple-dialog"><h2 id="dialog-title">Choose photos & videos</h2><p class="muted">Photos and videos can belong to more than one album.</p><div id="choose-results"></div><div class="editor-footer"><span class="hint">Changes are applied when you save the album.</span><button class="primary">Use selected files</button></div></form>', false);
    if (photos.length) renderPages($('#choose-results'), photos, photo => `<label class="choice">${thumbnail(photo)}<input type="checkbox" name="photo" value="${photo.id}" ${chosen.has(photo.id) ? 'checked' : ''}><span>${escape(photo.caption || photo.filename)}${photo.kind === 'video' ? ` · Video ${duration(photo.duration)}` : ''}</span></label>`);
    else $('#choose-results').innerHTML = '<p class="muted">Upload files or finish pending deletions first.</p>';
    $('#choose-form').onchange = event => {
      if (event.target.name !== 'photo') return;
      if (event.target.checked) chosen.add(event.target.value);
      else chosen.delete(event.target.value);
    };
    $('#choose-form').onsubmit = event => {
      event.preventDefault();
      const previous = new Set(order);
      order = [...order.filter(id => chosen.has(id)), ...photos.filter(photo => chosen.has(photo.id) && !previous.has(photo.id)).map(photo => photo.id)];
      if (!order.includes(cover)) cover = '';
      dirty = true; editor.close(); renderOrder();
    };
  };
  $('#delete-album').onclick = () => {
    if (!confirm(`Remove “${album.title}”? Its photos stay in the library, but only photos ready through another album or their own publication setting remain in the next photostream. Publish afterward to update the website.`)) return;
    work(async () => { Object.assign(state, await api(`/albums/${id}`, { method: 'DELETE' })); dirty = false; location.hash = 'albums'; render(); status('Album removed from the working catalog. Photos retained.'); });
  };
}

function openDialog(html, trackEdits = true) {
  $('#editor-body').innerHTML = html;
  revealPreviews($('#editor-body'));
  $('#editor-body').oninput = trackEdits ? () => { dirty = true; } : null;
  editor.showModal();
  editor.querySelector('[autofocus]')?.focus();
  $('#editor-body').scrollTop = 0;
  editor.scrollTop = 0;
  refreshButtons();
}
function closeEditor() {
  const albumChooser = location.hash.startsWith('#album/') && !$('#photo-form');
  if (busy || (!albumChooser && dirty && !confirm('Discard unsaved edits?'))) return;
  // An album's unsaved edits remain in its page when dismissing the chooser.
  if (!albumChooser) dirty = false;
  editor.close();
}

function editPhoto(id) {
  if (location.hash.startsWith('#album/') && dirty) {
    showError(new Error('Save album before editing photo details so your unsaved album changes are kept.'));
    $('#error').scrollIntoView({ block: 'nearest' });
    return;
  }
  const photo = photoById(id);
  const noun = photo.kind === 'video' ? 'video' : 'photo';
  if (photo.deleting) { confirmDeletePhoto(id); return; }
  if (photo.archived) {
    openDialog(`<div class="simple-dialog"><h2 id="dialog-title">${escape(photo.caption || photo.filename)}</h2><div class="archived-preview">${preview(photo)}</div><p>Files, metadata, and album memberships are retained. Restoring makes this ${noun} ready if it belongs to a published album; publish separately to update the website.</p>${photo.archiving ? '<p class="error">Manifest cleanup was interrupted. Retry archive before restoring.</p>' : ''}<div class="editor-footer"><button class="danger" data-delete-photo="${id}">Delete permanently</button><button class="primary" data-archive-photo="${id}" data-action="${photo.archiving ? 'archive' : 'restore'}">${photo.archiving ? 'Retry archive' : `Restore ${noun}`}</button></div></div>`, false);
    return;
  }
  openDialog(`<div class="photo-editor"><div class="editor-image">${preview(photo)}</div><form id="photo-form" class="editor-fields"><h2 id="dialog-title">${photo.kind === 'video' ? 'Video' : 'Photo'} details</h2><p class="muted">${escape(photo.filename)} · ${photo.width} × ${photo.height}${photo.kind === 'video' ? ` · ${duration(photo.duration)}` : ''}</p>
    <label>Alt text<input name="alt" value="${escape(photo.alt)}" maxlength="1000" autofocus><small>Optional. Describe what’s in the ${noun}.</small></label><label>Caption<textarea name="caption" maxlength="4000">${escape(photo.caption)}</textarea></label><label>Tags<input name="tags" value="${escape(photo.tags.join(', '))}" placeholder="Travel, New York, Street"><small>Separate tags with commas.</small></label><label>Capture date<input type="date" name="capturedDate" value="${escape(photo.capturedDate)}"></label>
    <label>Location · optional<input name="location" value="${escape(photo.location)}" maxlength="300" placeholder="New York, NY"><small>Only a location you enter is shared with this ${noun}'s viewers. GPS is never extracted.</small></label>
    <fieldset><legend>Camera metadata</legend><dl class="exif">${cameraFields.map((key, i) => `<div><dt><label for="camera-${i}">${key}</label></dt><dd><input id="camera-${i}" name="exif-${key}" value="${escape(photo.exif?.[key])}" maxlength="300"></dd></div>`).join('')}</dl><p class="hint">Edit imported values or add missing details. Leave a field blank to hide it. Save, then publish to update the gallery. Original files are unchanged; GPS is never extracted.</p></fieldset>
    <fieldset><legend>Albums · optional</legend>${state.catalog.albums.map(album => `<label class="check"><input type="checkbox" name="album" value="${album.id}" ${album.photoIds.includes(id) ? 'checked' : ''}>${escape(album.title)} · ${accessLabel(album)}</label>`).join('') || '<span class="hint">No albums yet.</span>'}</fieldset>
    <label class="check"><input type="checkbox" name="published" ${photo.published ? 'checked' : ''}>Publish publicly, independently of albums</label><p class="hint">Photos and videos in published albums are automatically ready. Public albums or independent publication make this ${noun} public even if it also belongs to private share-link or password-protected albums. To make it a draft, remove it from every published album and uncheck this setting. Previously public media URLs and old releases remain accessible.</p>
    <button type="button" data-archive-photo="${id}" data-action="archive">Archive ${noun}</button><p class="hint">Keep files; remove this ${noun} from all public manifests, including historical releases. Cached copies and direct media URLs may remain accessible.</p><div class="editor-footer"><button type="button" class="danger" data-delete-photo="${id}">Delete ${noun}</button><button class="primary" type="submit">Save ${noun}</button></div></form></div>`);
  $('#photo-form').onsubmit = event => {
    event.preventDefault();
    const form = new FormData(event.target);
    work(async () => {
      Object.assign(state, await api(`/photos/${id}`, { method: 'PATCH', body: { alt: form.get('alt'), caption: form.get('caption'), tags: form.get('tags').split(',').map(tag => tag.trim()).filter(Boolean), capturedDate: form.get('capturedDate'), location: form.get('location'), exif: Object.fromEntries(cameraFields.map(key => [key, form.get(`exif-${key}`)])), published: form.has('published'), albumIds: form.getAll('album') } }));
      dirty = false; editor.close(); render(); status('Photo saved. Publish changes when you’re ready.');
    });
  };
}

function confirmDeletePhoto(id) {
  const photo = photoById(id);
  const noun = photo.kind === 'video' ? 'video' : 'photo';
  $('#delete-body').innerHTML = `<h2 id="delete-title">${photo.deleting ? `Finish deleting this ${noun}?` : `Delete this ${noun}?`}</h2><p><strong>${escape(photo.filename)}</strong>${photo.caption ? ` · ${escape(photo.caption)}` : ''}</p><p id="delete-description">This deletes the original and generated files from storage and removes the ${noun} from your library, every album, and the current public manifest. This cannot be undone.</p><p class="muted">Other drafts won’t be published. Previously cached copies and metadata in historical releases may remain; CDN/privacy erasure needs separate cleanup.</p>${photo.deleting ? '<p class="hint">A previous deletion was interrupted. Retry to finish removing any remaining files.</p>' : ''}${dirty ? '<p class="hint">Unsaved edits will be discarded if you delete.</p>' : ''}<div class="editor-footer"><button id="cancel-delete" autofocus>Cancel</button><button class="danger" id="confirm-delete">${photo.deleting ? 'Retry deletion' : 'Delete permanently'}</button></div>`;
  deletion.showModal(); $('#cancel-delete').focus();
  $('#cancel-delete').onclick = () => { if (!busy) deletion.close(); };
  $('#confirm-delete').onclick = () => work(async () => {
    status('Deleting photo and stored files…');
    try {
      Object.assign(state, await api(`/photos/${id}`, { method: 'DELETE' }));
    } catch (error) {
      if (error !== operationStarted) conflicted = true; // Cleanup may have partly completed; reload before retrying.
      throw error;
    }
    dirty = false; deletion.close(); editor.close(); render(); status('Photo and stored files deleted. Removed from every album.');
  });
  refreshButtons();
}
deletion.addEventListener('cancel', event => { if (busy) event.preventDefault(); });

// XHR exposes bytes sent while fetch only exposes completion. Signed part requests
// deliberately bypass api(): no catalog precondition or Studio headers go to R2.
function putPart(url, bytes, signal, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const abort = () => xhr.abort();
    const finish = (error) => {
      signal.removeEventListener('abort', abort);
      error ? reject(error) : resolve();
    };
    xhr.open('PUT', url);
    xhr.withCredentials = new URL(url, location.href).origin === location.origin;
    xhr.timeout = 120000;
    xhr.upload.onprogress = event => onProgress(Math.min(event.loaded, bytes.size));
    xhr.onload = () => finish(xhr.status >= 200 && xhr.status < 300 ? null : Object.assign(new Error(`Chunk upload failed (${xhr.status}).`), { retryable: [408, 429].includes(xhr.status) || xhr.status >= 500 }));
    xhr.onerror = xhr.ontimeout = () => finish(Object.assign(new Error('Chunk upload interrupted. Check your connection.'), { retryable: true }));
    xhr.onabort = () => finish(new DOMException('Upload cancelled', 'AbortError'));
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) { finish(new DOMException('Upload cancelled', 'AbortError')); return; }
    xhr.send(bytes);
  });
}
function uploadDelay(ms, signal) {
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(new DOMException('Upload cancelled', 'AbortError')); };
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve(); }, ms);
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) abort();
  });
}
async function sendParts(file, session, controller, label) {
  const { signal } = controller;
  const loaded = new Map();
  const progress = $('#upload-progress progress'); progress.max = file.size; progress.value = 0;
  const update = (number, bytes) => {
    loaded.set(number, bytes);
    const sent = [...loaded.values()].reduce((sum, value) => sum + value, 0);
    progress.value = sent;
    $('#upload-progress span').textContent = `Transferring ${label} · ${(sent / 1048576).toFixed(1)} / ${(file.size / 1048576).toFixed(1)} MiB. Keep this page open; you can browse.`;
  };
  let next = 0;
  const worker = async () => {
    while (next < session.parts.length) {
      signal.throwIfAborted();
      const part = session.parts[next++];
      const start = (part.number - 1) * session.partSize;
      const bytes = file.slice(start, Math.min(start + session.partSize, file.size));
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          update(part.number, 0);
          await putPart(part.url, bytes, signal, count => update(part.number, count));
          update(part.number, bytes.size); break;
        } catch (error) {
          update(part.number, 0);
          if (signal.aborted || !error.retryable || attempt === 2) throw error;
          await uploadDelay(500 * 2 ** attempt, signal);
        }
      }
    }
  };
  try { await Promise.all(Array.from({ length: Math.min(3, session.parts.length) }, worker)); }
  catch (error) { controller.abort(); throw error; }
}
async function discardUpload(id) {
  if (!id) return;
  // Cancellation is best effort and must not start a catalog operation or mask
  // the original failure. The server never deletes a committed library item.
  try { await fetch(`/api/uploads/${encodeURIComponent(id)}`, { method: 'DELETE', signal: AbortSignal.timeout(5000) }); } catch { /* Abandoned uploads require lifecycle/operator cleanup. */ }
}
async function upload(files) {
  if (!files.length || busy || publishing() || changing() || conflicted || editor.open || deletion.open || (dirty && !confirm('Discard unsaved edits before uploading?'))) return;
  if (dirty) streamOrder = null;
  dirty = false;
  const batch = transfer = { controller: new AbortController(), id: null, processing: false };
  const { signal } = batch.controller;
  const ids = [];
  $('#error').hidden = true; $('#upload-progress').hidden = false; refreshButtons();
  try {
    try {
      for (const [i, file] of [...files].entries()) {
        signal.throwIfAborted();
        const video = /\.(mp4|mov)$/i.test(file.name) || ['video/mp4', 'video/quicktime'].includes(file.type);
        const limit = video ? 512 : 30;
        if (!file.size || file.size > limit * 1048576) throw new Error(`${file.name} must be nonempty and at most ${limit} MiB. Remaining files were not uploaded.`);
        const label = `${i + 1} of ${files.length} · ${file.name}`;
        $('#upload-progress span').textContent = `Preparing ${label}…`;
        $('#upload-progress progress').removeAttribute('value');
        const session = await api('/uploads', { method: 'POST', body: { filename: file.name, size: file.size, contentType: file.type || (video ? /\.mov$/i.test(file.name) ? 'video/quicktime' : 'video/mp4' : 'application/octet-stream') } });
        batch.id = session.upload.id;
        signal.throwIfAborted();
        await sendParts(file, session, batch.controller, label);
        signal.throwIfAborted();
        ids.push(batch.id); batch.id = null;
      }
    } catch (error) {
      const cancelled = signal.aborted && error.name === 'AbortError';
      batch.controller.abort();
      await discardUpload(batch.id); batch.id = null;
      status(cancelled ? 'Remaining transfers cancelled.' : 'Upload stopped. Remaining files were not uploaded.');
      if (!cancelled) showError(error);
    }
    if (!ids.length) return;
    batch.processing = true; refreshButtons();
    $('#upload-progress span').textContent = `${ids.length} file${ids.length === 1 ? '' : 's'} transferred. Starting background processing…`;
    $('#upload-progress progress').removeAttribute('value');
    try { await api('/uploads/process', { method: 'POST', body: { ids } }); }
    catch (error) { if (error !== operationStarted) throw error; }
    status(`${ids.length} file${ids.length === 1 ? '' : 's'} transferred. Processing continues if you leave this page. Publish separately when ready.`);
  } catch (error) {
    // An acknowledgement can be lost after acceptance. Do not delete transferred
    // originals: the worker may already own them. Reload to check its outcome.
    status('Could not confirm background processing. Reload to check what was accepted before retrying.'); showError(error);
  } finally {
    transfer = null; $('#upload-progress').hidden = true; $('#files').value = ''; refreshButtons();
  }
}

$('#cancel-upload').onclick = () => {
  if (!transfer || transfer.processing) return;
  transfer.controller.abort();
  refreshButtons();
  $('#upload-progress span').textContent = 'Stopping transfers… Fully transferred files will still be processed.';
};
$('#files').onchange = event => upload(event.target.files);
document.addEventListener('click', event => {
  const target = event.target.closest('[data-upload],[data-photo],[data-delete-photo],[data-archive-photo]');
  if (!target || busy || conflicted) return;
  if (target.hasAttribute('data-upload')) $('#files').click();
  else if (target.hasAttribute('data-archive-photo')) {
    const action = target.dataset.action;
    if (dirty && !confirm('Discard unsaved edits and archive this photo?')) return;
    work(async () => {
      try { Object.assign(state, await api(`/photos/${target.dataset.archivePhoto}/${action}`, { method: 'POST' })); }
      catch (error) { if (error !== operationStarted) conflicted = true; throw error; }
      dirty = false; editor.close(); render();
      status(action === 'restore' ? 'Photo restored. Published-album photos are ready; publish separately to update the website.' : 'Photo archived. Files kept; public manifests updated.');
    });
  }
  else if (target.hasAttribute('data-delete-photo')) confirmDeletePhoto(target.dataset.deletePhoto);
  else editPhoto(target.dataset.photo);
});
document.addEventListener('dragover', event => { if (event.dataTransfer.types.includes('Files')) event.preventDefault(); });
document.addEventListener('drop', event => { if (event.dataTransfer.files.length) { event.preventDefault(); upload(event.dataTransfer.files); } });
$('#close-editor').onclick = closeEditor;
editor.addEventListener('close', () => editor.querySelectorAll('video').forEach(video => video.pause()));
editor.addEventListener('cancel', event => { event.preventDefault(); closeEditor(); });
let editorBackdropPressed = false;
editor.addEventListener('pointerdown', event => { editorBackdropPressed = event.target === editor; });
editor.addEventListener('click', event => {
  if (!editorBackdropPressed || event.target !== editor) return;
  const bounds = editor.getBoundingClientRect();
  if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) closeEditor();
});
$('#reload').onclick = () => { if (!dirty || confirm('Discard unsaved edits and reload?')) location.reload(); };
$('#logout').onclick = () => work(async () => { if (dirty && !confirm('Discard unsaved edits and sign out?')) return; await api('/logout', { method: 'POST' }); dirty = false; location.reload(); });
async function reviewPublication(republish = false) {
  if (dirty) { showError(new Error('Save or discard the open edits before publishing.')); return; }
  const latest = await api('/catalog');
  if (latest.etag !== state.etag) { conflicted = true; throw new Error('The library changed. Reload before publishing.'); }
  Object.assign(state, latest); renderPublication();
  const changes = state.publicationChanges;
  if (republish && (changes.changed || state.publishedRevision == null)) throw new Error('Republish requires an existing publication with no pending publication changes. Publish or undo saved changes first.');
  if (publishing() || (!republish && !changes.changed && state.publication?.state !== 'failed')) return;
  const projections = [['Public', changes.photos, changes.albums]];
  if (changes.protectedPhotos && changes.protectedAlbums) projections.push(['Private · share-link and password protected', changes.protectedPhotos, changes.protectedAlbums]);
  const counts = projections.map(([title, photos, albums]) => `<table class="publication-changes"><caption>${title}</caption><thead><tr><th scope="col">Change</th><th scope="col">Photos</th><th scope="col">Albums</th></tr></thead><tbody>${[['Live now', 'live'], ['Added', 'added'], ['Updated', 'updated'], ['Removed', 'removed'], ['Unchanged', 'unchanged'], ['After publishing', 'next']].map(([label, key]) => `<tr><th scope="row">${label}</th><td>${photos[key]}</td><td>${albums[key]}</td></tr>`).join('')}</tbody></table>`).join('');
  openDialog(republish ? `<div class="simple-dialog"><h2 id="dialog-title">Republish current library?</h2><p>Rebuild the public and protected manifests and fill missing BlurHashes from existing image previews and video posters. No re-upload is needed.</p><p class="muted">Published photos, album order and access settings stay the same. Private drafts stay private. Missing or unreadable previews keep neutral placeholders.</p><div class="editor-footer"><span class="hint">${state.development ? 'Local preview only. No R2 writes.' : 'Runs in the background. You can leave this page.'}</span><button class="primary" id="confirm-publish">Republish now ↗</button></div></div>` : `<div class="simple-dialog"><h2 id="dialog-title">Publish changes?</h2>${counts}${changes.changed && projections.every(([, photos, albums]) => [photos, albums].every(count => count.added + count.updated + count.removed === 0)) ? '<p>Order, access, link, or password changes will be applied.</p>' : ''}<p class="muted">Saved access, regenerated links, and password changes take effect now; replaced links and old viewing sessions are revoked. Draft photos stay private. Previous releases are retained; unpublishing is not permanent deletion.</p><div class="editor-footer"><span class="hint">${state.development ? 'Local preview only. No R2 writes.' : 'Images copy first. The live index updates last.'}</span><button class="primary" id="confirm-publish">Publish now ↗</button></div></div>`, false);
  $('#confirm-publish').onclick = () => work(async () => {
    Object.assign(state, await api(republish ? '/publish?republish=1' : '/publish', { method: 'POST' }));
    editor.close(); renderPublication(); status('Publication started. It will continue if you close this page.');
    await watchPublication();
  });
}
$('#publish').onclick = () => work(() => reviewPublication());
$('#republish').onclick = () => work(() => reviewPublication(true));
$('#login-form').onsubmit = async event => {
  event.preventDefault();
  const button = event.target.querySelector('button'); button.disabled = true;
  try { await api('/login', { method: 'POST', body: { password: new FormData(event.target).get('password') } }); event.target.reset(); await boot(); }
  catch (error) { $('#login-error').textContent = error.message; }
  finally { button.disabled = false; }
};
let previousHash = location.hash || '#photos';
window.addEventListener('hashchange', () => {
  if (dirty && !confirm('Discard unsaved edits?')) { history.replaceState(null, '', previousHash); return; }
  dirty = false; streamOrder = null; previousHash = location.hash; editor.close(); render();
});
window.addEventListener('beforeunload', event => { if (dirty || busy || transfer) event.preventDefault(); });
async function boot() {
  try {
    Object.assign(state, await api('/catalog'));
    $('#login').hidden = true; $('#studio').hidden = false; render();
    if (publishing() || state.publication?.state === 'failed') await watchPublication();
    if (changing() || state.operation?.state === 'failed') await watchOperation();
  } catch (error) {
    $('#login').hidden = false;
    $('#login-error').textContent = error.message;
  }
}
boot();
