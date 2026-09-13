'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {File} = require('node:buffer');
const {TextEncoder} = require('node:util');

function load() {
  const context = {window: {}, FormData, File, TextEncoder};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'static/pxl-upload.js'), 'utf8'), context);
  return context.window.PXLUpload;
}

function entry(name, size = 1, type = 'image/jpeg', relativePath = name) {
  return {file: new File([new Uint8Array(size)], name, {type}), path: relativePath};
}

function entries(count, folder = 'Trip') {
  return Array.from({length: count}, (_, i) => entry(`${i}.jpg`, 1, 'image/jpeg', `${folder}/${i}.jpg`));
}

const policy = {
  allowedTypes: ['image/jpeg', 'image/png', 'image/gif', 'image/webp'],
  maxSize: 20 * 1024 * 1024
};

function harness(overrides = {}, respond) {
  const api = load();
  const requests = [], images = [], albums = [], progress = [], waits = [];
  let imageID = 0, refreshes = 0, waiting = 0;
  const options = {
    maxFiles: 10,
    authenticated: true,
    title: 'Upload',
    cancelled: () => false,
    refresh: async () => { refreshes++; return false; },
    onWait: () => { waiting++; },
    wait: async ms => { waits.push(ms); },
    onImage: image => { images.push(image); },
    onAlbum: (album, count) => { albums.push({album, count}); },
    onProgress: count => { progress.push(count); },
    send: async (url, body) => {
      const request = {url, body};
      requests.push(request);
      if (respond) {
        const response = await respond(request, requests.length);
        if (response !== undefined) return response;
      }
      if (url === '/api/v1/upload?auto_album=false') {
        assert.ok(body instanceof FormData);
        assert.ok(Array.from(body.keys()).every(key => key === 'file'));
        return {status: 200, data: {images: body.getAll('file').map(file => {
          assert.ok(file instanceof File);
          return {id: `image-${++imageID}`, name: file.name};
        })}};
      }
      assert.equal(url, '/api/v1/albums');
      return {status: 201, data: {short_id: `album-${requests.length}`}};
    },
    ...overrides
  };
  return {
    api, options, requests, images, albums, progress, waits,
    get refreshes() { return refreshes; },
    get waiting() { return waiting; },
    uploads: () => requests.filter(request => request.url.startsWith('/api/v1/upload?')),
    albumRequests: () => requests.filter(request => request.url === '/api/v1/albums'),
    run: files => api.run(api.planFiles(files, policy), options)
  };
}

test('batches preserve order and respect count and byte boundaries', () => {
  const api = load();
  for (const {sizes, maxFiles, maxBytes, expected} of [
    {sizes: [], maxFiles: 10, maxBytes: 32, expected: []},
    {sizes: [1, 1, 1, 1, 1], maxFiles: 2, maxBytes: 32, expected: [[1, 1], [1, 1], [1]]},
    {sizes: [12, 12, 12, 8, 1], maxFiles: 10, maxBytes: 20, expected: [[12], [12], [12, 8], [1]]},
    {sizes: [5, 5, 1, 1, 1], maxFiles: 2, maxBytes: 10, expected: [[5, 5], [1, 1], [1]]}
  ]) {
    const files = sizes.map((size, i) => entry(`${i}.jpg`, size));
    const actual = Array.from(api.batches(files, maxFiles, maxBytes), batch => Array.from(batch));
    assert.deepEqual(actual.map(batch => batch.map(item => item.file.size)), expected);
    assert.deepEqual(actual.flat(), files);
    assert.ok(actual.every(batch => batch.length <= maxFiles));
    assert.ok(actual.every(batch => batch.reduce((sum, item) => sum + item.file.size, 0) <= maxBytes));
  }
});

test('planning filters empty, oversized and unsupported files; extensions only fill missing MIME', () => {
  const files = [
    entry('typed.bin', 5, 'image/jpeg'),
    ...['JPG', 'jpeg', 'PNG', 'gif', 'webp'].map(ext => entry(`fallback.${ext}`, 1, '')),
    entry('wrong.jpg', 1, 'text/plain'),
    entry('unknown.bin', 1, ''),
    entry('empty.png', 0, 'image/png'),
    entry('large.jpg', 6)
  ];
  const plan = load().planFiles(files, {...policy, maxSize: 5});
  assert.equal(plan.groups.length, 1);
  assert.deepEqual(Array.from(plan.groups[0].files), files.slice(0, 6));
  assert.deepEqual(Array.from(plan.skipped, item => ({...item})), [
    {name: 'wrong.jpg', reason: 'format non pris en charge'},
    {name: 'unknown.bin', reason: 'format non pris en charge'},
    {name: 'empty.png', reason: 'fichier vide'},
    {name: 'large.jpg', reason: 'taille maximale depassee'}
  ]);
});

test('Windows and POSIX paths group by top-level folder, leaving loose files together', () => {
  const files = [
    entry('a.jpg', 1, 'image/jpeg', 'Trip\\nested\\a.jpg'),
    entry('b.jpg', 1, 'image/jpeg', 'Trip/b.jpg'),
    entry('c.jpg', 1, 'image/jpeg', 'Other\\c.jpg'),
    entry('loose.jpg'), entry('another.jpg')
  ];
  const plan = load().planFiles(files, policy);
  assert.deepEqual(Array.from(plan.groups, group => ({title: group.title, files: Array.from(group.files)})), [
    {title: 'Trip', files: files.slice(0, 2)},
    {title: 'Other', files: files.slice(2, 3)},
    {title: '', files: files.slice(3)}
  ]);
  assert.equal(files[0].path, 'Trip\\nested\\a.jpg');
});

test('dropped directories recursively drain every page, including pages beyond 100 entries', async () => {
  const reads = [], created = [];
  function directory(name, children) {
    return {name, isDirectory: true, createReader() {
      created.push(name);
      let offset = 0;
      return {readEntries(resolve) {
        reads.push(name);
        const page = children.slice(offset, offset + 100);
        offset += page.length;
        queueMicrotask(() => resolve(page));
      }};
    }};
  }
  const rootFiles = entries(105, 'root');
  const nestedFiles = entries(101, 'root/nested');
  const fileEntry = ({file}) => ({isFile: true, file(resolve) { queueMicrotask(() => resolve(file)); }});
  const root = directory('root', [
    ...rootFiles.slice(0, 100).map(fileEntry),
    directory('nested', nestedFiles.map(fileEntry)),
    ...rootFiles.slice(100).map(fileEntry)
  ]);
  const actual = await load().droppedFiles([
    {kind: 'string'},
    {kind: 'file', webkitGetAsEntry: () => root, getAsFile: () => null}
  ], [entry('ignored.jpg').file]);
  const expected = [...rootFiles.slice(0, 100), ...nestedFiles, ...rootFiles.slice(100)];
  assert.equal(actual.length, 206);
  assert.deepEqual(Array.from(actual, item => ({...item})), expected);
  assert.deepEqual(created, ['root', 'nested']);
  assert.equal(reads.filter(name => name === 'root').length, 3);
  assert.equal(reads.filter(name => name === 'nested').length, 3);
});

test('dropped files support plain items and the FileList relative-path fallback', async () => {
  const api = load();
  const {file} = entry('plain.jpg');
  assert.deepEqual(Array.from(await api.droppedFiles([{kind: 'file', getAsFile: () => file}], []), item => ({...item})), [{file, path: 'plain.jpg'}]);
  Object.defineProperty(file, 'webkitRelativePath', {value: 'folder/plain.jpg'});
  assert.deepEqual(Array.from(await api.droppedFiles([], [file]), item => ({...item})), [{file, path: 'folder/plain.jpg'}]);
});

test('25 successful files use batches of at most 10 and create exactly one complete album', async () => {
  const h = harness();
  const files = entries(25);
  const result = await h.run([...files, entry('ignored.txt', 1, 'text/plain')]);
  assert.deepEqual({...result}, {uploaded: 25, albums: 1, skipped: 1, error: '', cancelled: false});
  assert.deepEqual(h.uploads().map(request => request.body.getAll('file').length), [10, 10, 5]);
  assert.deepEqual(h.uploads().flatMap(request => request.body.getAll('file').map(file => file.name)), files.map(item => item.file.name));
  assert.deepEqual(h.progress, [10, 20, 25]);
  assert.equal(h.requests.at(-1).url, '/api/v1/albums');
  assert.equal(h.albumRequests().length, 1);
  assert.equal(h.albumRequests()[0].body.title, 'Trip');
  assert.deepEqual(Array.from(h.albumRequests()[0].body.image_ids), h.images.map(image => image.id));
  assert.equal(h.albums[0].count, 25);
});

test('run applies its 32 MiB batch limit independently of the file-count limit', async () => {
  const h = harness();
  const result = await h.run([entry('a.jpg', 17 * 1024 * 1024), entry('b.jpg', 16 * 1024 * 1024), entry('c.jpg', 16 * 1024 * 1024)]);
  assert.equal(result.error, '');
  assert.equal(result.uploaded, 3);
  assert.deepEqual(h.uploads().map(request => request.body.getAll('file').length), [1, 2]);
});

test('1001 files split into albums of 1000 and 1 without truncation or duplicate IDs', async () => {
  const h = harness();
  const result = await h.run(entries(1001));
  assert.deepEqual({...result}, {uploaded: 1001, albums: 2, skipped: 0, error: '', cancelled: false});
  assert.deepEqual(h.uploads().map(request => request.body.getAll('file').length), [...Array(100).fill(10), 1]);
  const albums = h.albumRequests();
  assert.deepEqual(albums.map(request => request.body.title), ['Trip (1)', 'Trip (2)']);
  assert.deepEqual(albums.map(request => request.body.image_ids.length), [1000, 1]);
  assert.deepEqual(albums.flatMap(request => Array.from(request.body.image_ids)), h.images.map(image => image.id));
  assert.equal(new Set(h.images.map(image => image.id)).size, 1001);
  assert.equal(h.requests[100].url, '/api/v1/albums');
  assert.deepEqual(h.albums.map(album => album.count), [1000, 1]);
});

for (const status of [200, 401, 429, 500]) {
  test(`partial ${status} response retains confirmed images, creates their album and stops all further uploads`, async () => {
    const confirmed = [{id: 'saved-1'}, {id: 'saved-2'}];
    const h = harness({}, request => request.body instanceof FormData
      ? {status, data: {images: confirmed, error: 'partial upload'}} : undefined);
    const result = await h.run([...entries(25), ...entries(1, 'Later')]);
    assert.deepEqual({...result}, {uploaded: 2, albums: 1, skipped: 0, error: 'partial upload', cancelled: false});
    assert.equal(h.uploads().length, 1);
    assert.equal(h.albumRequests().length, 1);
    assert.deepEqual(h.images, confirmed);
    assert.deepEqual(Array.from(h.albumRequests()[0].body.image_ids), ['saved-1', 'saved-2']);
    assert.deepEqual(h.progress, [2]);
    assert.equal(h.refreshes, 0);
    assert.deepEqual(h.waits, []);
  });
}

for (const failure of ['network', 500, 503]) {
  test(`ambiguous ${failure} upload failure is never retried; earlier images still get an album`, async () => {
    const h = harness({}, (request, number) => {
      if (number !== 2) return;
      if (failure === 'network') throw new Error('connection lost');
      return {status: failure, data: {error: 'server failure'}};
    });
    const result = await h.run(entries(25));
    assert.equal(result.uploaded, 10);
    assert.equal(result.albums, 1);
    assert.match(result.error, /connection lost|server failure/);
    assert.equal(h.uploads().length, 2);
    assert.equal(h.requests.length, 3);
    assert.deepEqual(Array.from(h.albumRequests()[0].body.image_ids), h.images.map(image => image.id));
    assert.deepEqual(h.waits, []);
    assert.equal(h.refreshes, 0);
  });

  test(`ambiguous ${failure} album failure is never retried and stops the next group`, async () => {
    const h = harness({}, request => {
      if (request.url !== '/api/v1/albums') return;
      if (failure === 'network') throw new Error('connection lost');
      return {status: failure, data: {error: 'server failure'}};
    });
    const result = await h.run([...entries(2), ...entries(2, 'Later')]);
    assert.equal(result.uploaded, 2);
    assert.equal(result.albums, 0);
    assert.match(result.error, /Album non confirme/);
    assert.match(result.error, /Mes images/);
    assert.equal(h.uploads().length, 1);
    assert.equal(h.albumRequests().length, 1);
    assert.equal(h.images.length, 2);
    assert.deepEqual(h.waits, []);
  });
}

test('429 retries are bounded at four attempts and respect full and fallback Retry-After waits', async () => {
  const values = ['2', '999', 'invalid', '5'];
  const h = harness({}, (request, number) => ({status: 429, retryAfter: values[number - 1], data: {error: 'rate limited'}}));
  const result = await h.run(entries(2));
  assert.equal(result.error, 'rate limited');
  assert.equal(result.uploaded, 0);
  assert.equal(result.albums, 0);
  assert.equal(h.uploads().length, 4);
  assert.equal(h.waiting, 3);
  assert.deepEqual(h.waits, [2000, 999000, 60000]);
  assert.ok(h.requests.every(request => request.body === h.requests[0].body));
});

test('429 retries can succeed for both uploads and album creation', async () => {
  const h = harness({}, (request, number) => [1, 3].includes(number)
    ? {status: 429, retryAfter: '0.25', data: {}} : undefined);
  const result = await h.run(entries(2));
  assert.deepEqual({...result}, {uploaded: 2, albums: 1, skipped: 0, error: '', cancelled: false});
  assert.equal(h.uploads().length, 2);
  assert.equal(h.albumRequests().length, 2);
  assert.deepEqual(h.waits, [250, 250]);
  assert.equal(h.requests[0].body, h.requests[1].body);
  assert.equal(h.requests[2].body, h.requests[3].body);
});

test('401 refreshes authentication once per request and replays the same upload or album body', async () => {
  let refreshes = 0;
  const h = harness({refresh: async () => { refreshes++; return true; }}, (request, number) =>
    [1, 3].includes(number) ? {status: 401, data: {}} : undefined);
  const result = await h.run(entries(2));
  assert.equal(result.error, '');
  assert.equal(result.uploaded, 2);
  assert.equal(result.albums, 1);
  assert.equal(refreshes, 2);
  assert.equal(h.requests.length, 4);
  assert.equal(h.requests[0].body, h.requests[1].body);
  assert.equal(h.requests[2].body, h.requests[3].body);
  assert.deepEqual(h.waits, []);
});

for (const refreshed of [false, true]) {
  test(`persistent 401 stops after ${refreshed ? 'one replay' : 'failed refresh'}`, async () => {
    let refreshes = 0;
    const h = harness({refresh: async () => { refreshes++; return refreshed; }}, () =>
      ({status: 401, data: {error: 'unauthorized'}}));
    const result = await h.run(entries(2));
    assert.equal(result.error, 'unauthorized');
    assert.equal(result.uploaded, 0);
    assert.equal(refreshes, 1);
    assert.equal(h.requests.length, refreshed ? 2 : 1);
    assert.deepEqual(h.waits, []);
  });
}

test('cancellation after a batch keeps confirmed images and creates only their partial album', async () => {
  let cancelled = false;
  const h = harness({
    cancelled: () => cancelled,
    onProgress: () => { cancelled = true; }
  });
  const result = await h.run([...entries(25), ...entries(2, 'Later')]);
  assert.deepEqual({...result}, {uploaded: 10, albums: 1, skipped: 0, error: '', cancelled: true});
  assert.equal(h.uploads().length, 1);
  assert.equal(h.albumRequests().length, 1);
  assert.equal(h.albums[0].count, 10);
  assert.deepEqual(Array.from(h.albumRequests()[0].body.image_ids), h.images.map(image => image.id));
});

test('cancellation during a 429 wait prevents replay and preserves the earlier batch in an album', async () => {
  let cancelled = false;
  const waits = [];
  const h = harness({
    cancelled: () => cancelled,
    wait: async ms => { waits.push(ms); cancelled = true; }
  }, (request, number) => number === 2 ? {status: 429, retryAfter: '1', data: {}} : undefined);
  const result = await h.run(entries(25));
  assert.equal(result.uploaded, 10);
  assert.equal(result.albums, 1);
  assert.equal(result.cancelled, true);
  assert.match(result.error, /Import arrete/);
  assert.equal(h.uploads().length, 2);
  assert.equal(h.albumRequests().length, 1);
  assert.deepEqual(waits, [1000]);
  assert.deepEqual(Array.from(h.albumRequests()[0].body.image_ids), h.images.map(image => image.id));
});

test('already-cancelled imports send no requests', async () => {
  const h = harness({cancelled: () => true});
  const result = await h.run(entries(25));
  assert.deepEqual({...result}, {uploaded: 0, albums: 0, skipped: 0, error: '', cancelled: true});
  assert.deepEqual(h.requests, []);
});

test('cancelled imports still retry a rate-limited final album', async () => {
  let cancelled = false;
  const h = harness({cancelled: () => cancelled, onProgress: () => { cancelled = true; }},
    (request, number) => number === 2 ? {status: 429, retryAfter: '1', data: {}} : undefined);
  const result = await h.run(entries(25));
  assert.equal(result.error, '');
  assert.equal(result.uploaded, 10);
  assert.equal(result.albums, 1);
  assert.equal(h.uploads().length, 1);
  assert.equal(h.albumRequests().length, 2);
});

test('explicit album title overrides folder name and remains within UTF-8 byte limit', async () => {
  const h = harness({explicitTitle: '日本語'.repeat(100)});
  await h.run(entries(2));
  const title = h.albumRequests()[0].body.title;
  assert.ok(title.startsWith('日本語'));
  assert.ok(new TextEncoder().encode(title).length <= 255);
});

test('anonymous uploads do not attempt albums', async () => {
  const h = harness({authenticated: false});
  const result = await h.run(entries(25));
  assert.equal(result.uploaded, 25);
  assert.equal(result.albums, 0);
  assert.equal(h.albumRequests().length, 0);
});

test('stop during authentication refresh does not replay the rejected batch', async () => {
  let cancelled = false;
  const h = harness({cancelled: () => cancelled, refresh: async () => { cancelled = true; return true; }},
    () => ({status: 401, data: {}}));
  const result = await h.run(entries(2));
  assert.equal(result.uploaded, 0);
  assert.equal(result.cancelled, true);
  assert.equal(h.uploads().length, 1);
});
