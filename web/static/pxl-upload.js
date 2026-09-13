(function () {
  'use strict';

  // Directory readers return pages (often only 100 entries), not the whole folder.
  async function droppedFiles(items, fallback, signal) {
    const roots = Array.from(items || []).filter(item => item.kind === 'file').map(item => ({
      entry: item.webkitGetAsEntry?.(), file: item.getAsFile()
    }));
    const files = [];
    async function walk(entry, path) {
      if (signal?.aborted) throw new Error('Lecture du dossier annulee.');
      if (entry.isFile) {
        const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
        files.push({file, path: path + file.name});
      } else if (entry.isDirectory) {
        const reader = entry.createReader();
        for (;;) {
          if (signal?.aborted) throw new Error('Lecture du dossier annulee.');
          const page = await new Promise((resolve, reject) => reader.readEntries(resolve, reject));
          if (!page.length) break;
          for (const child of page) await walk(child, path + entry.name + '/');
        }
      }
    }
    for (const root of roots) {
      if (root.entry) await walk(root.entry, '');
      else if (root.file) files.push({file: root.file, path: root.file.name});
    }
    return roots.length ? files : Array.from(fallback || [], file => ({file, path: file.webkitRelativePath || file.name}));
  }

  function planFiles(entries, policy) {
    const groups = new Map();
    const skipped = [];
    const extensions = {jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif', webp: 'image/webp'};
    for (const entry of entries) {
      const {file} = entry;
      const type = file.type || extensions[file.name.split('.').pop().toLowerCase()];
      let reason = '';
      if (!policy.allowedTypes.includes(type)) reason = 'format non pris en charge';
      else if (!file.size) reason = 'fichier vide';
      else if (file.size > policy.maxSize) reason = 'taille maximale depassee';
      if (reason) { skipped.push({name: entry.path, reason}); continue; }
      const path = entry.path.replace(/\\/g, '/');
      const title = path.includes('/') ? path.split('/')[0] : '';
      if (!groups.has(title)) groups.set(title, []);
      groups.get(title).push(entry);
    }
    return {groups: Array.from(groups, ([title, files]) => ({title, files})), skipped};
  }

  function batches(files, maxFiles, maxBytes) {
    const result = [];
    let batch = [], bytes = 0;
    for (const entry of files) {
      if (batch.length && (batch.length >= maxFiles || bytes + entry.file.size > maxBytes)) {
        result.push(batch); batch = []; bytes = 0;
      }
      batch.push(entry); bytes += entry.file.size;
    }
    if (batch.length) result.push(batch);
    return result;
  }

  async function run(plan, options) {
    const summary = {uploaded: 0, albums: 0, skipped: plan.skipped.length, error: '', cancelled: false};
    const wait = options.wait || (async (ms, finalize) => {
      const end = Date.now() + ms;
      while (Date.now() < end && (finalize || !options.cancelled())) {
        await new Promise(resolve => setTimeout(resolve, Math.min(250, end - Date.now())));
      }
    });
    // Only replay explicit pre-handler rejections. Network/5xx failures may have committed data.
    async function request(url, body, finalize = false) {
      let refreshed = false;
      for (let attempt = 0; ; attempt++) {
        if (!finalize && options.cancelled()) throw new Error('Import arrete entre deux requetes.');
        const response = await options.send(url, body);
        if (response.data?.images?.length) return response;
        if (response.status === 401 && !refreshed && await options.refresh()) {
          refreshed = true;
          continue;
        }
        if (response.status !== 429 || attempt >= 3) return response;
        const seconds = Number(response.retryAfter);
        options.onWait();
        await wait((Number.isFinite(seconds) && seconds > 0 ? seconds : 60) * 1000, finalize);
        if (!finalize && options.cancelled()) throw new Error('Import arrete entre deux requetes.');
      }
    }
    outer: for (const group of plan.groups) {
      // The album API and viewer support 1,000 images. Split explicitly, never truncate.
      for (let offset = 0; offset < group.files.length; offset += 1000) {
        if (options.cancelled()) break outer;
        const files = group.files.slice(offset, offset + 1000);
        const ids = new Set();
        let failure = '';
        try {
          for (const batch of batches(files, options.maxFiles, 32 * 1024 * 1024)) {
            if (options.cancelled()) break;
            const form = new FormData();
            for (const {file} of batch) form.append('file', file, file.name);
            const response = await request('/api/v1/upload?auto_album=false', form);
            const data = response.data;
            const images = Array.isArray(data?.images) ? data.images : (data?.id ? [data] : []);
            for (const image of images) {
              ids.add(image.id);
              summary.uploaded++;
              options.onImage(image);
            }
            options.onProgress(summary.uploaded);
            if (response.status < 200 || response.status >= 300 || images.length !== batch.length) {
              throw new Error(data?.error || 'Reponse inattendue. Verifiez la galerie avant de renvoyer les fichiers.');
            }
          }
        } catch (error) {
          failure = error.message;
        }
        if (options.authenticated && ids.size && (group.title || group.files.length > 1)) {
          try {
            const suffix = group.files.length > 1000 ? ' (' + (offset / 1000 + 1) + ')' : '';
            // UTF-8 titles must fit the backend's 255-byte bound, including the suffix.
            let title = options.explicitTitle || group.title || options.title;
            while (new TextEncoder().encode(title + suffix).length > 255) title = Array.from(title).slice(0, -1).join('');
            const response = await request('/api/v1/albums', {title: title + suffix, image_ids: Array.from(ids)}, true);
            if (response.status < 200 || response.status >= 300 || !response.data?.short_id) {
              throw new Error(response.data?.error || 'Reponse inattendue');
            }
            summary.albums++;
            options.onAlbum(response.data, ids.size);
          } catch (error) {
            failure = (failure ? failure + ' ' : '') + 'Album non confirme : ' + error.message + ' Les images enregistrees restent disponibles dans Mes images.';
          }
        }
        if (failure) { summary.error = failure; break outer; }
      }
    }
    summary.cancelled = options.cancelled();
    return summary;
  }

  window.PXLUpload = {droppedFiles, planFiles, batches, run};
}());
