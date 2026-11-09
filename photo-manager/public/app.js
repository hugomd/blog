const $ = selector => document.querySelector(selector);
const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const state = { catalog: { photos: [], albums: [] }, etag: 'new', publishedRevision: null };
let busy = false, dirty = false, conflicted = false, filter = 'all', search = '';
const editor = $('#editor');
const image = id => `/api/photos/${encodeURIComponent(id)}/image`;
const photoById = id => state.catalog.photos.find(photo => photo.id === id);
const albumIds = id => state.catalog.albums.filter(album => album.photoIds.includes(id)).map(album => album.id);
const badge = published => `<span class="badge ${published ? '' : 'draft'}">${published ? 'Ready to publish' : 'Draft'}</span>`;

async function api(path, { method = 'GET', body, headers = {} } = {}) {
  const response = await fetch(`/api${path}`, {
    method, headers: { ...(method === 'GET' ? {} : { 'If-Match': state.etag }), ...(body && !(body instanceof File) ? { 'Content-Type': 'application/json' } : {}), ...headers },
    body: body instanceof File ? body : body ? JSON.stringify(body) : undefined,
  });
  const result = await response.json();
  if (!response.ok) {
    if (response.status === 409) conflicted = true;
    if (response.status === 401 && path !== '/login') { editor.close(); $('#studio').hidden = true; $('#login').hidden = false; }
    throw new Error(result.error || 'The request failed. Reload before retrying.');
  }
  return result;
}

function showError(error) {
  $('#error span').textContent = error.message; $('#error').hidden = false;
  if ($('#studio').hidden) $('#login-error').textContent = error.message;
  if (editor.open) {
    $('#dialog-error')?.remove();
    const notice = document.createElement('p');
    notice.id = 'dialog-error'; notice.className = 'error'; notice.setAttribute('role', 'alert'); notice.textContent = error.message;
    if (conflicted) {
      const reload = document.createElement('button'); reload.textContent = 'Reload library'; reload.dataset.reload = '';
      reload.onclick = () => $('#reload').click(); notice.append(reload);
    }
    $('#editor-body').prepend(notice); notice.scrollIntoView({ block: 'nearest' });
  }
}
function status(message) { $('#status').textContent = message; }
function refreshButtons() {
  document.querySelectorAll('button').forEach(button => { button.disabled = busy || button.hasAttribute('data-boundary') || (conflicted && button.id !== 'reload' && button.id !== 'close-editor' && !button.hasAttribute('data-reload')); });
  $('#publish').disabled = busy || conflicted || state.publishedRevision === state.catalog.revision;
}
async function work(task) {
  if (busy) return;
  busy = true; refreshButtons(); $('#error').hidden = true; $('#dialog-error')?.remove();
  try { await task(); } catch (error) { showError(error); }
  finally { busy = false; refreshButtons(); }
}

function render() {
  $('#photo-count').textContent = state.catalog.photos.length;
  $('#album-count').textContent = state.catalog.albums.length;
  $('#sandbox').hidden = !state.development;
  $('#logout').hidden = state.authMode === 'proxy';
  $('#publish-status').textContent = state.publishedRevision === state.catalog.revision ? 'All changes published' : 'Unpublished changes';
  const route = location.hash.slice(1) || 'photos';
  $('#nav-photos').toggleAttribute('aria-current', route === 'photos');
  $('#nav-albums').toggleAttribute('aria-current', route.startsWith('album'));
  if (route.startsWith('album/')) renderAlbum(route.slice(6));
  else if (route === 'albums') renderAlbums();
  else renderPhotos();
  refreshButtons();
}

function renderPhotos() {
  const photos = [...state.catalog.photos].reverse().filter(photo => {
    if (filter === 'draft' && photo.published) return false;
    if (filter === 'ready' && !photo.published) return false;
    if (filter === 'standalone' && albumIds(photo.id).length) return false;
    return `${photo.filename} ${photo.alt} ${photo.caption} ${photo.tags.join(' ')}`.toLowerCase().includes(search.toLowerCase());
  });
  $('#main').innerHTML = `<div class="page-heading"><div><p class="eyebrow">A FEW PLACES, A FEW MOMENTS</p><h1>All photos</h1><p>Your photostream starts here. Albums are entirely optional.</p></div><button class="primary" data-upload>＋ Upload photos</button></div>
    <div class="toolbar"><label class="search"><span class="hint">Search your library</span><input id="search" type="search" placeholder="Names, captions, tags…" value="${escape(search)}"></label><label><span class="hint">Show</span><select id="filter"><option value="all">All photos</option><option value="draft">Drafts</option><option value="ready">Ready to publish</option><option value="standalone">Not in an album</option></select></label></div>
    ${photos.length ? `<div class="grid">${photos.map(photo => `<button class="photo-card" data-photo="${photo.id}" aria-label="Edit ${escape(photo.alt || photo.filename)}"><div class="photo-frame"><img src="${image(photo.id)}" alt="${escape(photo.alt)}" loading="lazy"></div><div class="card-line"><strong>${escape(photo.caption || photo.filename)}</strong>${badge(photo.published)}</div><div class="card-subtitle">${albumIds(photo.id).length ? `${albumIds(photo.id).length} album${albumIds(photo.id).length === 1 ? '' : 's'}` : 'Standalone photo'}${photo.capturedDate ? ` · ${escape(photo.capturedDate)}` : ''}</div></button>`).join('')}</div>` : `<div class="empty"><span class="brand-mark" aria-hidden="true">◧</span><h2>${state.catalog.photos.length ? 'Nothing here just yet.' : 'Make room for a few moments.'}</h2><p>${state.catalog.photos.length ? 'Try another filter or search.' : 'Drop your JPEGs or PNGs anywhere here, or choose a few to upload. Every photo starts as a private draft.'}</p><button data-upload>Choose photos →</button></div>`}`;
  $('#filter').value = filter;
  $('#filter').onchange = event => { filter = event.target.value; renderPhotos(); };
  $('#search').oninput = event => {
    search = event.target.value;
    const caret = event.target.selectionStart;
    renderPhotos(); $('#search').focus(); $('#search').setSelectionRange(caret, caret);
  };
}

function renderAlbums() {
  $('#main').innerHTML = `<div class="page-heading"><div><p class="eyebrow">COLLECTIONS, NOT CONTAINERS</p><h1>Albums</h1><p>A story, a place, a weekend. Give a few photos a home together.</p></div><button class="primary" id="create-album">＋ New album</button></div>
  ${state.catalog.albums.length ? `<div class="grid album-grid">${state.catalog.albums.map(album => {
    const cover = photoById(album.coverId || album.photoIds[0]);
    return `<a class="photo-card" href="#album/${album.id}"><div class="photo-frame">${cover ? `<img src="${image(cover.id)}" alt="${escape(cover.alt)}" loading="lazy">` : '<div class="placeholder" aria-hidden="true">◧</div>'}</div><div class="card-line"><strong>${escape(album.title)}</strong>${badge(album.published)}</div><div class="card-subtitle">${album.photoIds.length} photographs · /${escape(album.slug)}/</div></a>`;
  }).join('')}</div>` : '<div class="empty"><h2>Start a collection.</h2><p>Your photos don’t need an album to appear in the photostream. Create one when you have a story to tell.</p></div>'}`;
  $('#create-album').onclick = () => {
    openDialog(`<form id="create-form" class="simple-dialog"><p class="eyebrow">NEW COLLECTION</p><h2 id="dialog-title">Create an album</h2><label>Album title<input name="title" placeholder="New York, September 2022" maxlength="150" required autofocus></label><p class="muted">Start with a title. Choose photos, a cover, and a URL next.</p><button class="primary">Create draft album →</button></form>`);
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
  $('#main').innerHTML = `<a class="back" href="#albums">← All albums</a><div class="page-heading"><div><p class="eyebrow">EDIT COLLECTION</p><h1>${escape(album.title)}</h1><p>Save your edits, then publish when everything feels right.</p></div>${badge(album.published)}</div>
    <form id="album-form"><div class="album-form"><label>Title<input name="title" value="${escape(album.title)}" maxlength="150" required></label><label>URL slug<input name="slug" value="${escape(album.slug)}" pattern="[a-z0-9]+(-[a-z0-9]+)*" maxlength="100" required><small>Used for the album page’s address.</small></label><label class="wide">Description<textarea name="description" maxlength="4000">${escape(album.description)}</textarea></label><label class="check wide"><input name="published" type="checkbox" ${album.published ? 'checked' : ''}>Include this album in the next publication</label></div>
    <div class="section-heading"><h2>Photographs <span class="muted">${album.photoIds.length}</span></h2><button type="button" id="choose-photos">＋ Choose photos</button></div>
    <p class="hint">Order is top to bottom. Draft photos stay private, even when the album is published.</p><ol class="ordered-list" id="ordered-list"></ol>
    <div class="editor-footer"><button type="button" class="danger" id="delete-album">Remove album</button><button class="primary" type="submit">Save album</button></div></form>`;
  let order = [...album.photoIds], cover = album.coverId;
  function renderOrder() {
    $('#ordered-list').innerHTML = order.map((id, i) => {
      const photo = photoById(id);
      return `<li><img src="${image(id)}" alt="${escape(photo.alt)}"><div class="order-title"><p>${escape(photo.caption || photo.filename)}</p><small>${photo.published ? 'Ready to publish' : 'Draft · not publicly visible'}</small></div><div class="order-buttons"><button type="button" data-move="${i}" data-direction="-1" aria-label="Move photo ${i + 1} up" ${i === 0 ? 'disabled data-boundary' : ''}>↑</button><button type="button" data-move="${i}" data-direction="1" aria-label="Move photo ${i + 1} down" ${i === order.length - 1 ? 'disabled data-boundary' : ''}>↓</button><button type="button" data-remove="${id}" aria-label="Remove photo ${i + 1} from album">−</button></div><label class="check cover-label"><input type="radio" name="cover" value="${id}" ${cover === id || (!cover && i === 0) ? 'checked' : ''}>Cover</label></li>`;
    }).join('') || '<p class="muted">No photographs selected yet.</p>';
    $('#ordered-list').onclick = event => {
      const move = event.target.closest('[data-move]'), remove = event.target.closest('[data-remove]');
      if (move) { const i = Number(move.dataset.move), target = i + Number(move.dataset.direction); if (target < 0 || target >= order.length) return; [order[i], order[target]] = [order[target], order[i]]; dirty = true; renderOrder(); }
      if (remove) { order = order.filter(id => id !== remove.dataset.remove); if (!order.includes(cover)) cover = ''; dirty = true; renderOrder(); }
    };
    $('#ordered-list').onchange = event => { cover = event.target.value; dirty = true; };
  }
  renderOrder();
  $('#album-form').oninput = () => { dirty = true; };
  $('#album-form').onsubmit = event => {
    event.preventDefault();
    const form = new FormData(event.target);
    work(async () => {
      const result = await api(`/albums/${id}`, { method: 'PUT', body: { title: form.get('title'), slug: form.get('slug'), description: form.get('description'), published: form.has('published'), photoIds: order, coverId: form.get('cover') || '' } });
      Object.assign(state, result); dirty = false; render(); status('Album saved. Publish changes to update the public manifest.');
    });
  };
  $('#choose-photos').onclick = () => {
    openDialog(`<form id="choose-form" class="simple-dialog"><p class="eyebrow">CURATE YOUR COLLECTION</p><h2 id="dialog-title">Choose photographs</h2><p class="muted">Photos can belong to more than one album.</p><div class="grid">${state.catalog.photos.map(photo => `<label class="choice"><img src="${image(photo.id)}" alt="${escape(photo.alt)}" loading="lazy"><input type="checkbox" name="photo" value="${photo.id}" ${order.includes(photo.id) ? 'checked' : ''}><span>${escape(photo.caption || photo.filename)}</span></label>`).join('') || '<p class="muted">Upload some photos first.</p>'}</div><div class="editor-footer"><span class="hint">Changes are applied when you save the album.</span><button class="primary">Use selected photos</button></div></form>`, false);
    $('#choose-form').onsubmit = event => {
      event.preventDefault();
      const selected = new FormData(event.target).getAll('photo');
      order = [...order.filter(id => selected.includes(id)), ...selected.filter(id => !order.includes(id))];
      if (!order.includes(cover)) cover = '';
      dirty = true; editor.close(); renderOrder();
    };
  };
  $('#delete-album').onclick = () => {
    if (!confirm(`Remove “${album.title}”? Its photos will remain in the library and photostream. Publish afterward to update the website.`)) return;
    work(async () => { Object.assign(state, await api(`/albums/${id}`, { method: 'DELETE' })); dirty = false; location.hash = 'albums'; render(); status('Album removed from the working catalog. Photos retained.'); });
  };
}

function openDialog(html, trackEdits = true) {
  $('#editor-body').innerHTML = html;
  $('#editor-body').oninput = trackEdits ? () => { dirty = true; } : null;
  editor.showModal();
  editor.querySelector('[autofocus]')?.focus();
}
function closeEditor() {
  if (busy || (!location.hash.startsWith('#album/') && dirty && !confirm('Discard unsaved edits?'))) return;
  // An album's unsaved edits remain in its page when dismissing the chooser.
  if (!location.hash.startsWith('#album/')) dirty = false;
  editor.close();
}

function editPhoto(id) {
  const photo = photoById(id);
  openDialog(`<div class="photo-editor"><div class="editor-image"><img src="${image(id)}" alt="${escape(photo.alt)}"></div><form id="photo-form" class="editor-fields"><p class="eyebrow">PHOTO DETAILS</p><h2 id="dialog-title">A little context.</h2><p class="muted">${escape(photo.filename)} · ${photo.width} × ${photo.height}</p>
    <label>Alt text<input name="alt" value="${escape(photo.alt)}" maxlength="1000" autofocus><small>Describe what’s in the photo. Required before publishing.</small></label><label>Caption<textarea name="caption" maxlength="4000">${escape(photo.caption)}</textarea></label><label>Tags<input name="tags" value="${escape(photo.tags.join(', '))}" placeholder="Travel, New York, Street"><small>Separate tags with commas.</small></label><label>Capture date<input type="date" name="capturedDate" value="${escape(photo.capturedDate)}"></label>
    <fieldset><legend>Albums · optional</legend>${state.catalog.albums.map(album => `<label class="check"><input type="checkbox" name="album" value="${album.id}" ${album.photoIds.includes(id) ? 'checked' : ''}>${escape(album.title)}</label>`).join('') || '<span class="hint">No albums yet. Standalone photos work just fine.</span>'}</fieldset>
    <label class="check"><input type="checkbox" name="published" ${photo.published ? 'checked' : ''}>Include this photo in the next publication</label><p class="hint">Unchecking removes it from the next manifest, but previously public image URLs and old releases remain accessible.</p>
    <dl class="exif">${Object.entries(photo.exif).map(([key, value]) => `<div><dt>${escape(key)}</dt><dd>${escape(value)}</dd></div>`).join('')}</dl><p class="hint">Only selected camera EXIF is public. Embedded metadata and GPS are stripped from web images.</p><button class="primary" type="submit">Save photo</button></form></div>`);
  $('#photo-form').onsubmit = event => {
    event.preventDefault();
    const form = new FormData(event.target);
    work(async () => {
      Object.assign(state, await api(`/photos/${id}`, { method: 'PATCH', body: { alt: form.get('alt'), caption: form.get('caption'), tags: form.get('tags').split(',').map(tag => tag.trim()).filter(Boolean), capturedDate: form.get('capturedDate'), published: form.has('published'), albumIds: form.getAll('album') } }));
      dirty = false; editor.close(); render(); status('Photo saved. Publish changes when you’re ready.');
    });
  };
}

async function upload(files) {
  if (!files.length || busy || conflicted || editor.open || (dirty && !confirm('Discard unsaved edits before uploading?'))) return;
  dirty = false;
  await work(async () => {
    $('#upload-progress').hidden = false;
    const progress = $('#upload-progress progress'); progress.max = files.length;
    try {
      for (const [i, file] of [...files].entries()) {
        if (file.size > 30 * 1024 * 1024) throw new Error(`${file.name} exceeds 30 MB. Remaining files were not uploaded.`);
        $('#upload-progress span').textContent = `Uploading ${i + 1} of ${files.length} · ${file.name}`; progress.value = i;
        Object.assign(state, await api('/photos', { method: 'POST', body: file, headers: { 'Content-Type': 'application/octet-stream', 'X-Filename': encodeURIComponent(file.name) } }));
        status(`${i + 1} photo${i ? 's' : ''} uploaded as private drafts.`);
      }
    } finally { $('#upload-progress').hidden = true; location.hash = 'photos'; render(); $('#files').value = ''; }
  });
}

$('#files').onchange = event => upload(event.target.files);
document.addEventListener('click', event => {
  const target = event.target.closest('[data-upload],[data-photo]');
  if (!target || busy || conflicted) return;
  if (target.hasAttribute('data-upload')) $('#files').click();
  else editPhoto(target.dataset.photo);
});
document.addEventListener('dragover', event => { if (event.dataTransfer.types.includes('Files')) event.preventDefault(); });
document.addEventListener('drop', event => { if (event.dataTransfer.files.length) { event.preventDefault(); upload(event.dataTransfer.files); } });
$('#close-editor').onclick = closeEditor;
editor.addEventListener('cancel', event => { event.preventDefault(); closeEditor(); });
$('#reload').onclick = () => { if (!dirty || confirm('Discard unsaved edits and reload?')) location.reload(); };
$('#logout').onclick = () => work(async () => { if (dirty && !confirm('Discard unsaved edits and sign out?')) return; await api('/logout', { method: 'POST' }); dirty = false; location.reload(); });
$('#publish').onclick = () => {
  if (dirty) { showError(new Error('Save or discard the open edits before publishing.')); return; }
  const count = state.catalog.photos.filter(photo => photo.published).length;
  openDialog(`<div class="simple-dialog"><p class="eyebrow">MAKE IT PUBLIC</p><h2 id="dialog-title">Publish this collection?</h2><p>${count} photo${count === 1 ? '' : 's'} and ${state.catalog.albums.filter(album => album.published).length} albums will be included. Draft photos stay private.</p><p class="muted">This updates the public manifest in R2. Previous releases are retained; unpublishing is not permanent deletion.</p><div class="editor-footer"><span class="hint">${state.development ? 'Local preview only. No R2 writes.' : 'Images upload first. The live index updates last.'}</span><button class="primary" id="confirm-publish">Publish now ↗</button></div></div>`, false);
  $('#confirm-publish').onclick = () => work(async () => {
    status('Publishing images and manifest…');
    Object.assign(state, await api('/publish', { method: 'POST' }));
    editor.close(); render(); status(`Published revision ${state.publishedRevision}.`);
  });
};
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
window.addEventListener('beforeunload', event => { if (dirty || busy) event.preventDefault(); });
async function boot() {
  try {
    Object.assign(state, await api('/catalog'));
    $('#login').hidden = true; $('#studio').hidden = false; render();
  } catch (error) {
    $('#login').hidden = false;
    $('#login-error').textContent = error.message;
  }
}
boot();
