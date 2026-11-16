import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import { onRequest } from './likes.mjs';

const A = 'a'.repeat(32), B = 'b'.repeat(32), C = 'c'.repeat(32), D = 'd'.repeat(32);

function fixture() {
  // Real SQLite with the production schema, behind D1's prepare/bind/first API.
  const db = new DatabaseSync(':memory:');
  db.exec(readFileSync(new URL('./likes.sql', import.meta.url), 'utf8'));
  const catalog = { version: 1, albums: [], photos: [
    { id: A, published: true }, { id: B, kind: 'video' }, { id: C, published: false }, { id: D, archived: true },
  ] };
  const env = {
    REACTIONS: { prepare: sql => ({ bind: (...values) => ({ first: async column => db.prepare(sql).get(...values)?.[column] ?? null }) }) },
    PUBLIC_PHOTOS: { async get(key) { assert.equal(key, 'index.json'); return { text: async () => JSON.stringify(catalog) }; } },
  };
  const call = (id, method = 'GET', origin = 'https://hugo.md') => onRequest({ env,
    request: new Request(`https://hugo.md/photos/likes/${id}`, { method, headers: method === 'POST' && origin ? { Origin: origin } : {} }) });
  const rows = () => db.prepare('SELECT photo_id, count FROM photo_likes ORDER BY photo_id').all().map(row => ({ ...row }));
  return { env, call, rows };
}

test('likes count up per published photo and video, and reads return the total', async () => {
  const f = fixture();
  assert.deepEqual(await (await f.call(A)).json(), { count: 0 });
  for (const expected of [1, 2, 3]) {
    const response = await f.call(A, 'POST');
    assert.equal(response.status, 200);
    assert.equal(response.headers.get('Cache-Control'), 'no-store');
    assert.deepEqual(await response.json(), { count: expected });
  }
  assert.deepEqual(await (await f.call(B, 'POST')).json(), { count: 1 });
  assert.deepEqual(await (await f.call(A)).json(), { count: 3 });
  assert.deepEqual(f.rows(), [{ photo_id: A, count: 3 }, { photo_id: B, count: 1 }]);
});

test('only same-origin likes for currently published IDs are stored', async () => {
  const f = fixture();
  for (const id of [C, D, 'e'.repeat(32)]) assert.equal((await f.call(id, 'POST')).status, 404);
  assert.equal((await f.call(A, 'POST', 'https://evil.example')).status, 403);
  assert.equal((await f.call(A, 'POST', null)).status, 403);
  for (const path of ['A'.repeat(32), `${A}/`, `${A}/extra`, '']) assert.equal((await f.call(path)).status, 404);
  assert.equal((await f.call(A, 'DELETE')).status, 405);
  assert.deepEqual(f.rows(), []);
});

test('missing bindings or catalog fail closed', async () => {
  const f = fixture();
  f.env.PUBLIC_PHOTOS.get = async () => null;
  assert.equal((await f.call(A, 'POST')).status, 503);
  delete f.env.REACTIONS;
  assert.equal((await f.call(A)).status, 503);
  assert.equal((await f.call(A, 'POST')).status, 503);
});
