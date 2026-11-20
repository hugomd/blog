import assert from 'node:assert/strict';
import test from 'node:test';
import { parse } from 'yaml';
import { normalizeDraft, serializePost, validateDraft } from './content.mjs';

const NOW = '2024-06-20T12:00:00Z';
const FALLBACK = 'a1b2c3d4';
const capture = (content, extra = {}) => normalizeDraft({ content, ...extra }, NOW, FALLBACK);
const base = () => capture('Some **Markdown**.\n', { title: 'A draft', categories: ['writing'] });
const badRequest = (fn, pattern) => assert.throws(fn, error => error.status === 400 && (!pattern || pattern.test(error.message)));
function splitPost(post) {
  assert.ok(post.startsWith('---\n'));
  const end = post.indexOf('\n---\n', 4);
  assert.ok(end !== -1);
  const metadata = parse(post.slice(4, end));
  assert.deepEqual(parse(post.slice(4, end), { version: '1.1' }), metadata);
  return { metadata, body: post.slice(end + 5) };
}

test('capture defaults, title precedence, Unicode slug and heading fallbacks', () => {
  assert.deepEqual(base(), {
    title: 'A draft', body: 'Some **Markdown**.\n', slug: 'a-draft', date: NOW,
    description: '', tags: ['writing'], toc: false, cover: '',
  });
  assert.equal(capture('# Heading title\n\nBody').title, 'Heading title');
  assert.equal(capture('# C#\nBody').title, 'C#');
  assert.equal(capture('# Heading ###\nBody').title, 'Heading');
  assert.equal(capture('---\n---\nBody').title, 'Untitled draft');
  assert.equal(capture('Body', { title: '   ' }).title, 'Untitled draft');
  assert.equal(capture('Body', { title: '你好' }).slug, FALLBACK);
  assert.equal(capture('Body', { title: 'Café & Déjà Vu!' }).slug, 'cafe-deja-vu');
  assert.equal(capture('Body', { title: 'a '.repeat(100) }).slug.length <= 100, true);
  assert.equal(capture('---\ntitle: Actual title\n---\nBody', { title: 'Filename' }).title, 'Actual title');
});

test('front matter parsed once; Markdown, HTML, shortcodes and CRLF body are byte-preserved', () => {
  const body = '\r\n# Content  \r\n\r\n{{< sidenote "test" >}}\r\n<div>Raw HTML</div>\r\n---\r\ntitle: This is body\r\n---\r\n尾\r\n';
  const draft = capture(`---\r\ntitle: From YAML\r\ndate: 2024-02-29T18:00:00+11:00\r\nslug: explicit-slug\r\ndescription: Summary\r\ntags: [hugo, writing]\r\ntoc: true\r\ncover: ./post/example/cover.png\r\ndraft: true\r\n---\r\n${body}`);
  assert.equal(draft.body, body);
  assert.deepEqual(draft.tags, ['hugo', 'writing']);
  const output = splitPost(serializePost(draft));
  assert.equal(output.body, body);
  assert.equal(output.metadata.title, 'From YAML');
  assert.equal(output.metadata.slug, 'explicit-slug');
  assert.equal(output.metadata.date, '2024-02-29T18:00:00+11:00');
  assert.equal(output.metadata.cover, './post/example/cover.png');
  assert.equal(output.metadata.draft, false);
  assert.equal(output.metadata.toc, true);
  assert.equal('cover' in splitPost(serializePost(base())).metadata, false);
});

test('YAML serialization safely round-trips adversarial scalar strings', () => {
  for (const value of ['false', 'null', 'yes', 'on', '2024-01-01', '*alias', '&anchor', '!!str value', '{title: evil}', '[a, b]', 'a: b # comment', '---\ndraft: true\n---', '"quoted" \\ text', '<script>alert(1)</script>']) {
    const draft = { ...base(), title: value, description: value, tags: [value] };
    const { metadata, body } = splitPost(serializePost(draft));
    assert.equal(metadata.title, value);
    assert.equal(metadata.description, value);
    assert.deepEqual(metadata.tags, [value]);
    assert.equal(metadata.draft, false);
    assert.equal(body, draft.body);
  }
});

test('reject malformed, duplicate, unsupported, tagged and aliased YAML with 400', () => {
  for (const yaml of [
    'title: First\ntitle: Second', 'layout: unsafe', '__proto__: {}', 'constructor: bad',
    'tags: &tags [one]\ntitle: *tags', 'title: &title Good\ndescription: *title',
    'title: !!js/function something', 'title: [unclosed', '- not a mapping',
    '? [complex, key]\n: value', 'title: Good\n<<: {draft: false}',
  ]) badRequest(() => capture(`---\n${yaml}\n---\nBody`));
  badRequest(() => capture('---\ntitle: Missing end\nBody'), /closing/);
  badRequest(() => capture('---\ntitle: Empty body\n---\n  '), /nonempty/);
  badRequest(() => validateDraft({ ...base(), layout: 'unsafe' }), /Unsupported/);
  badRequest(() => validateDraft({ ...base(), draft: false }), /Unsupported/);
});

test('dates require valid RFC3339 calendar dates and an explicit timezone', () => {
  for (const date of ['2023-02-29T12:00:00Z', '1900-02-29T12:00:00Z', '2024-04-31T12:00:00Z', '2024-00-01T12:00:00Z', '2024-13-01T12:00:00Z', '2024-01-00T12:00:00Z', '2024-01-01T24:00:00Z', '2024-01-01T12:60:00Z', '2024-01-01T12:00:60Z', '2024-01-01T12:00:00+24:00', '2024-01-01T12:00:00+00:60', '2024-01-01', '2024-01-01T12:00:00', '2024-01-01 12:00:00Z', '0000-01-01T00:00:00Z', 123]) {
    badRequest(() => validateDraft({ ...base(), date }), /date/);
  }
  for (const date of ['2000-02-29T23:59:59.123Z', '2024-02-29T23:59:59-12:00', '2024-01-01T12:00:00+05:30']) {
    assert.equal(validateDraft({ ...base(), date }).date, date);
  }
  const future = capture('---\ndate: 2999-01-01T00:00:00Z\n---\nFuture private draft');
  badRequest(() => validateDraft(future), /Future/);
  badRequest(() => serializePost(future), /Future/);
});

test('slug and cover validation prevents paths, traversal and unsafe schemes', () => {
  for (const slug of ['../secret', '.', '..', '/absolute', 'a/b', 'a\\b', 'a%2fb', 'a?b', 'a#b', 'UPPER', 'a--b', '-a', 'a-', '', 'a'.repeat(101)]) {
    badRequest(() => validateDraft({ ...base(), slug }), /slug/);
  }
  assert.equal(validateDraft({ ...base(), slug: 'a'.repeat(100) }).slug.length, 100);
  for (const cover of ['javascript:alert(1)', 'data:image/png;base64,AA', 'http://example.com/a.png', '//example.com/a.png', '../a.png', './a/../../a.png', '/a/../b.png', 'a\\b.png', 'a/%2e%2e/b.png', 'a.png?x=1', 'a.png#fragment', 'https://user:pass@example.com/a.png', 'https://example.com/a.png?x=1', 'https://example.com/a.png#x', 'https://example.com/../a.png', 'https://example.com/%2e%2e/a.png', 'https://example.com/%252e%252e/a.png']) {
    badRequest(() => validateDraft({ ...base(), cover }), /cover/);
  }
  for (const cover of ['', 'cover.png', './post/example/cover.png', '/images/cover.webp', 'https://example.com/cover.png']) {
    assert.equal(validateDraft({ ...base(), cover }).cover, cover);
  }
});

test('Hugo index bundle filename is rejected during capture, editing and serialization', () => {
  badRequest(() => capture('---\nslug: index\n---\nBody'), /slug/);
  badRequest(() => capture('Body', { title: 'Index' }), /slug/);
  badRequest(() => validateDraft({ ...base(), slug: 'index' }), /slug/);
  badRequest(() => serializePost({ ...base(), slug: 'index' }), /slug/);
  assert.equal(validateDraft({ ...base(), slug: 'index-notes' }).slug, 'index-notes');
});

test('type, length and UTF-8 limits are enforced without modifying Markdown', () => {
  for (const patch of [
    { title: '' }, { title: 'x'.repeat(201) }, { title: 1 }, { description: 'x'.repeat(1001) },
    { description: null }, { tags: 'one' }, { tags: Array(31).fill('one') }, { tags: ['x'.repeat(81)] },
    { tags: [1] }, { toc: 'true' }, { cover: null }, { body: '' }, { body: ' \r\n ' }, { body: 1 },
    { body: 'é'.repeat(128 * 1024 + 1) },
  ]) badRequest(() => validateDraft({ ...base(), ...patch }));
  const boundary = 'é'.repeat(128 * 1024);
  assert.equal(validateDraft({ ...base(), body: boundary }).body, boundary);
  assert.equal(capture(`---\ntitle: At limit\n---\n${boundary}`).body, boundary);
  for (const yaml of ['tags: null', 'toc: null', 'slug: null', 'date: null', 'description: null', 'cover: null', 'title: null']) {
    badRequest(() => capture(`---\n${yaml}\n---\nBody`));
  }
  for (const input of [null, undefined, [], 'body']) badRequest(() => normalizeDraft(input, NOW, FALLBACK));
  assert.deepEqual(validateDraft(base()), base());
});
