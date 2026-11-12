const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: { photos: [], albums: [] }, etag: 'new' };
let busy = false, dirty = false, conflicted = false, filter = 'all', search = '';
let publicationTimer;
let operationTimer;
let transfer = null;
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
const photoById = id => state.catalog.photos.find(photo => photo.id === id);
const albumIds = id => state.catalog.albums.filter(album => album.photoIds.includes(id)).map(album => album.id);
const cameraFields = ['Make', 'Camera', 'Lens', 'ISO', 'Shutter', 'Aperture', 'Focal length'];
const photoReady = (photo, albums = state.catalog.albums) => !photo.archived && !photo.archiving && !photo.deleting && (photo.published || albums.some(album => album.published && album.photoIds.includes(photo.id)));
const privateAlbum = album => album.access === 'password' || album.access === 'link';
const photoPublic = (photo, albums = state.catalog.albums) => photoReady(photo, albums.filter(album => !privateAlbum(album)));
const accessLabel = album => album.access === 'link' ? 'Private share link' : album.access === 'password' ? 'Password protected' : 'Public';
const badge = published => `<span class="badge ${published ? '' : 'draft'}">${published ? 'Ready to publish' : 'Draft'}</span>`;

// Same standard Base83/DCT decoding as the independently bundled public gallery.
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

function revealPreviews(container) {
  for (const media of container.querySelectorAll('[data-preview]')) {
    const photo = photoById(media.dataset.preview);
    const surface = media instanceof HTMLImageElement ? media.parentElement : media;
    surface.style.backgroundImage = blurhashBackground(photo);
    surface.style.setProperty('--aspect', photo.width / photo.height);
    if (media instanceof HTMLImageElement) {
      const reveal = () => { if (media.naturalWidth) media.classList.add('loaded'); };
      media.addEventListener('load', reveal, { once: true });
      reveal();
    }
  }
}

async function api(path, { method = 'GET', body, headers = {} } = {}) {
  const backgroundChange = ['PUT', 'PATCH', 'DELETE'].includes(method) || (method === 'POST' && (/^\/photos\/[^/]+\/(archive|restore)$/.test(path) || path === '/uploads/process' || path === '/photostream'));
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
  document.querySelectorAll('button').forEach(button => { button.disabled = busy || ((publishing() || changing()) && !button.matches('.photo-card,#close-editor,#reload,#cancel-delete,#logout,#cancel-upload')) || button.hasAttribute('data-boundary') || (conflicted && !button.matches('#reload,#close-editor,#cancel-delete,#cancel-upload,[data-reload]')); });
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
  $('#archived-count').textContent = state.catalog.photos.filter(photo => photo.archived).length;
  $('#album-count').textContent = state.catalog.albums.length;
  $('#sandbox').hidden = !state.development;
  $('#logout').hidden = state.authMode === 'proxy';
  renderPublication();
  renderOperation();
  const route = location.hash.slice(1) || 'photos';
  $('#nav-photos').toggleAttribute('aria-current', route === 'photos');
  $('#nav-albums').toggleAttribute('aria-current', route.startsWith('album'));
  $('#nav-archived').toggleAttribute('aria-current', route === 'archived');
  if (route.startsWith('album/')) renderAlbum(route.slice(6));
  else if (route === 'albums') renderAlbums();
  else if (route === 'archived') renderArchived();
  else renderPhotos();
  refreshButtons();
}

function renderPhotos() {
  const candidates = [...state.catalog.photos].reverse().filter(photo => !photo.archived && !photo.archiving && !photo.deleting && !photoPublic(photo));
  const photos = [...state.catalog.photos].reverse().filter(photo => {
    if (photo.archived) return false;
    if (filter === 'draft' && photoReady(photo)) return false;
    if (filter === 'ready' && !photoReady(photo)) return false;
    if (filter === 'standalone' && albumIds(photo.id).length) return false;
    return `${photo.filename} ${photo.alt} ${photo.caption} ${photo.tags.join(' ')}`.toLowerCase().includes(search.toLowerCase());
  });
  $('#main').innerHTML = `<div class="page-heading"><div><h1>All photos & videos</h1></div><div class="library-actions"><button id="choose-photostream" ${candidates.length ? '' : 'disabled data-boundary'}>＋ Add to photostream</button><button class="primary" data-upload>＋ Upload files</button></div></div>
    <div class="toolbar"><label class="search"><span class="hint">Search your library</span><input id="search" type="search" placeholder="Names, captions, tags…" value="${escape(search)}"></label><label><span class="hint">Show</span><select id="filter"><option value="all">All photos & videos</option><option value="draft">Drafts</option><option value="ready">Ready to publish</option><option value="standalone">Not in an album</option></select></label></div>
    ${photos.length ? `<div class="grid">${photos.map(photo => `<button class="photo-card" data-photo="${photo.id}" aria-label="${photo.deleting ? 'Retry deletion of' : 'Edit'} ${escape(photo.alt || photo.filename)}"><div class="photo-frame">${photo.deleting ? '<div class="placeholder">×</div>' : thumbnail(photo)}${videoBadge(photo)}</div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong>${photo.deleting ? '<span class="badge draft">Deletion incomplete</span>' : badge(photoReady(photo))}</div><div class="card-subtitle">${albumIds(photo.id).length ? `${albumIds(photo.id).length} album${albumIds(photo.id).length === 1 ? '' : 's'}` : photo.kind === 'video' ? 'Standalone video' : 'Standalone photo'}${photo.capturedDate ? ` · ${escape(photo.capturedDate)}` : ''}</div></button>`).join('')}</div>` : `<div class="empty"><h2>${state.catalog.photos.length ? 'No matching photos or videos.' : 'No photos or videos yet.'}</h2><p>${state.catalog.photos.length ? 'Try another filter or search.' : 'Drop JPEGs, PNGs, MP4s or MOVs here, or choose files. Photos: up to 30 MiB; videos: up to 512 MiB. Every file starts as a private draft.'}</p><button data-upload>Choose files →</button></div>`}`;
  revealPreviews($('#main'));
  $('#filter').value = filter;
  $('#filter').onchange = event => { filter = event.target.value; renderPhotos(); refreshButtons(); };
  $('#search').oninput = event => {
    search = event.target.value;
    const caret = event.target.selectionStart;
    renderPhotos(); refreshButtons(); $('#search').focus(); $('#search').setSelectionRange(caret, caret);
  };
  $('#choose-photostream').onclick = () => {
    openDialog(`<form id="photostream-form" class="simple-dialog photostream-chooser"><h2 id="dialog-title">Add to the public photostream</h2><p>Select multiple photos or videos to publish independently of albums. Items already ready for the public photostream are omitted.</p><p class="hint access-warning">This makes selected files public, including files in private share-link or password-protected albums. Saving stages the change; Publish changes makes it live.</p><div class="grid">${candidates.map(photo => `<label class="choice">${thumbnail(photo)}<input type="checkbox" name="photo" value="${photo.id}"><span>${escape(photo.caption || photo.filename)}${photo.kind === 'video' ? ` · Video ${duration(photo.duration)}` : ''}</span>${state.catalog.albums.some(album => privateAlbum(album) && album.photoIds.includes(photo.id)) ? '<small class="hint access-warning">Private album · becomes public</small>' : ''}</label>`).join('')}</div><div class="editor-footer"><span class="hint" id="photostream-selection-count" role="status"></span><button class="primary" id="add-selected-photos">Add selected files</button></div></form>`);
    const form = $('#photostream-form');
    const updateSelection = () => {
      const count = new FormData(form).getAll('photo').length;
      $('#photostream-selection-count').textContent = `${count} file${count === 1 ? '' : 's'} selected. Publish separately when ready.`;
      $('#add-selected-photos').textContent = count ? `Add ${count} file${count === 1 ? '' : 's'}` : 'Add selected files';
      $('#add-selected-photos').toggleAttribute('data-boundary', !count);
      refreshButtons();
    };
    form.onchange = updateSelection;
    form.onsubmit = event => {
      event.preventDefault();
      const photoIds = new FormData(form).getAll('photo');
      if (!photoIds.length) return;
      work(async () => {
        Object.assign(state, await api('/photostream', { method: 'POST', body: { photoIds } }));
        dirty = false; editor.close(); render(); status('Selection saved for the public photostream. Publish changes when ready.');
      });
    };
    updateSelection();
  };
}

function renderArchived() {
  const photos = [...state.catalog.photos].reverse().filter(photo => photo.archived);
  $('#main').innerHTML = `<div class="page-heading"><div><h1>Archived</h1><p>Files and album memberships are kept. Restored photos follow their albums’ publication status.</p></div></div>
    ${photos.length ? `<div class="grid">${photos.map(photo => `<button class="photo-card" data-photo="${photo.id}" aria-label="View archived ${escape(photo.alt || photo.filename)}"><div class="photo-frame">${photo.deleting ? '<div class="placeholder">×</div>' : thumbnail(photo)}${videoBadge(photo)}</div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong><span class="badge draft">${photo.deleting ? 'Deletion incomplete' : photo.archiving ? 'Archive incomplete' : 'Archived'}</span></div></button>`).join('')}</div>` : '<div class="empty"><h2>No archived photos or videos.</h2><p>Archive a photo or video from its details to remove it from public manifests without deleting its files.</p></div>'}`;
  revealPreviews($('#main'));
}

function renderAlbums() {
  $('#main').innerHTML = `<div class="page-heading"><div><h1>Albums</h1></div><button class="primary" id="create-album">＋ New album</button></div>
  ${state.catalog.albums.length ? `<div class="grid album-grid">${state.catalog.albums.map(album => {
    const cover = [album.coverId, ...album.photoIds].map(photoById).find(photo => photo && !photo.deleting && !photo.archived);
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
    openDialog(`<form id="choose-form" class="simple-dialog"><h2 id="dialog-title">Choose photos & videos</h2><p class="muted">Photos and videos can belong to more than one album.</p><div class="grid">${[...state.catalog.photos].reverse().filter(photo => !photo.deleting).map(photo => `<label class="choice">${thumbnail(photo)}<input type="checkbox" name="photo" value="${photo.id}" ${order.includes(photo.id) ? 'checked' : ''}><span>${escape(photo.caption || photo.filename)}${photo.kind === 'video' ? ` · Video ${duration(photo.duration)}` : ''}</span></label>`).join('') || '<p class="muted">Upload files or finish pending deletions first.</p>'}</div><div class="editor-footer"><span class="hint">Changes are applied when you save the album.</span><button class="primary">Use selected files</button></div></form>`, false);
    $('#choose-form').onsubmit = event => {
      event.preventDefault();
      const selected = new FormData(event.target).getAll('photo');
      order = [...order.filter(id => selected.includes(id)), ...selected.filter(id => !order.includes(id))];
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
  dirty = false; previousHash = location.hash; editor.close(); render();
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
