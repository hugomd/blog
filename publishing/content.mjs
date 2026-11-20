import { isAlias, isMap, isScalar, parseDocument, stringify, visit } from 'yaml';

const FRONT_MATTER_KEYS = new Set(['title', 'date', 'slug', 'description', 'tags', 'toc', 'cover', 'draft']);
const DRAFT_KEYS = new Set(['title', 'body', 'slug', 'date', 'description', 'tags', 'toc', 'cover']);
const MAX_BODY_BYTES = 256 * 1024;

function fail(message) {
  throw Object.assign(new Error(message), { status: 400 });
}

function checkObject(input, keys) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) fail('Draft must be an object.');
  for (const key of Object.keys(input)) {
    if (!keys.has(key)) fail(`Unsupported field: ${key}`);
  }
}

function checkBody(body) {
  if (typeof body !== 'string' || !body.trim()) fail('Markdown body must be a nonempty string.');
  if (new TextEncoder().encode(body).length > MAX_BODY_BYTES) fail('Markdown body exceeds 256 KiB.');
  return body;
}

function stringField(value, name, max, required = false) {
  if (typeof value !== 'string') fail(`${name} must be a string.`);
  const result = value.trim();
  if (required && !result) fail(`${name} is required.`);
  if (result.length > max) fail(`${name} exceeds ${max} characters.`);
  return result;
}

function checkDate(date, allowFuture) {
  if (typeof date !== 'string') fail('date must be an RFC3339 string with an explicit timezone.');
  const match = /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/.exec(date);
  if (!match) fail('date must be RFC3339 with an explicit timezone.');
  const [, year, month, day, hour, minute, second, zone] = match;
  const y = Number(year);
  const m = Number(month);
  const d = Number(day);
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (y < 1 || m < 1 || m > 12 || d < 1 || d > days[m - 1] || Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59 ||
      (zone.length > 1 && (Number(zone.slice(1, 3)) > 23 || Number(zone.slice(4)) > 59))) {
    fail('date contains an invalid calendar date, time, or timezone.');
  }
  const timestamp = Date.parse(date);
  if (!Number.isFinite(timestamp)) fail('date is invalid.');
  if (!allowFuture && timestamp > Date.now()) fail('Future dates are not supported; scheduling is not implemented.');
  return date;
}

function checkCover(value) {
  const cover = stringField(value, 'cover', 2048);
  if (!cover) return '';
  if (/[\s\\\u0000-\u001f\u007f]/u.test(cover)) fail('cover must be a safe HTTPS URL or relative image path.');
  if (/^https:\/\//i.test(cover)) {
    let url;
    try { url = new URL(cover); } catch { fail('cover URL is invalid.'); }
    if (!url.hostname || url.username || url.password || url.search || url.hash || cover.includes('?') || cover.includes('#')) {
      fail('cover URL must not include credentials, a query, or a fragment.');
    }
    // Reject encoded traversal and separators as well as literal traversal.
    let path;
    try { path = decodeURIComponent(cover.slice(cover.indexOf('://') + 3).replace(/^[^/]*/, '')); } catch { fail('cover URL has invalid escaping.'); }
    if (path.split('/').some(segment => segment === '..' || segment === '.') || /[%\\]/.test(path)) fail('cover must not contain traversal.');
    return cover;
  }
  const path = cover.replace(/^\.\//, '').replace(/^\/(?!\/)/, '');
  if (!/^[a-zA-Z0-9_~.-]+(?:\/[a-zA-Z0-9_~.-]+)*$/.test(path) ||
      path.split('/').some(segment => segment === '.' || segment === '..') ||
      !/\.(?:avif|bmp|gif|ico|jpe?g|png|svg|tiff?|webp)$/i.test(path)) {
    fail('cover must be a safe HTTPS URL or relative image path without query, fragment, or traversal.');
  }
  return cover;
}

function checkDraft(input, allowFuture) {
  checkObject(input, DRAFT_KEYS);
  const title = stringField(input.title, 'title', 200, true);
  const body = checkBody(input.body);
  if (typeof input.slug !== 'string' || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(input.slug) || input.slug.length > 100) {
    fail('slug must be 1–100 lowercase ASCII letters, digits, or single separating hyphens (no paths).');
  }
  if (input.slug === 'index') fail('slug index is reserved by Hugo for leaf bundles. Choose a different slug.');
  const date = checkDate(input.date, allowFuture);
  const description = stringField(input.description === undefined ? '' : input.description, 'description', 1000);
  if (!Array.isArray(input.tags) || input.tags.length > 30) fail('tags must be an array with at most 30 strings.');
  const tags = input.tags.map(tag => stringField(tag, 'tag', 80, true));
  if (typeof input.toc !== 'boolean') fail('toc must be a boolean.');
  const cover = checkCover(input.cover === undefined ? '' : input.cover);
  return { title, body, slug: input.slug, date, description, tags, toc: input.toc, cover };
}

function extractFrontMatter(content) {
  const opening = /^(?:\uFEFF)?---[\t ]*\r?\n/.exec(content);
  if (!opening) return { fields: {}, body: content };
  const rest = content.slice(opening[0].length);
  const closing = /^---[\t ]*(?:\r?\n|$)/m.exec(rest);
  if (!closing) fail('YAML front matter is missing its closing --- delimiter.');
  const document = parseDocument(rest.slice(0, closing.index), { uniqueKeys: true, strict: true, prettyErrors: false });
  if (document.errors.length || document.warnings.length) fail(`Invalid YAML front matter: ${(document.errors[0] ?? document.warnings[0]).message}`);
  visit(document, (_, node) => {
    if (isAlias(node)) fail('YAML aliases are not supported in front matter.');
  });
  if (document.contents !== null && !isMap(document.contents)) fail('YAML front matter must be a mapping.');
  for (const { key } of document.contents?.items ?? []) {
    if (!isScalar(key) || typeof key.value !== 'string' || !FRONT_MATTER_KEYS.has(key.value)) {
      fail(`Unsupported front matter key: ${isScalar(key) ? String(key.value) : '(complex key)'}`);
    }
  }
  return { fields: document.toJS({ maxAliasCount: 0 }) ?? {}, body: rest.slice(closing.index + closing[0].length) };
}

/** Capture Markdown once; future-dated captures remain private until corrected. */
export function normalizeDraft(input, nowISO, fallbackSlug) {
  checkObject(input, new Set(['title', 'content', 'categories']));
  const { title = '', content, categories = [] } = input;
  if (typeof title !== 'string') fail('title must be a string.');
  if (typeof content !== 'string') fail('Markdown content must be a string.');
  const { fields, body } = extractFrontMatter(content);
  const heading = /^#[\t ]+(.+?)\r?$/m.exec(body)?.[1]?.replace(/[\t ]+#+[\t ]*$/, '').trim();
  const resolvedTitle = fields.title !== undefined ? fields.title : (title.trim() || heading || 'Untitled draft');
  if (typeof resolvedTitle !== 'string') fail('title must be a string.');
  const generatedSlug = resolvedTitle.normalize('NFKD').replace(/[\u0300-\u036f]/g, '').toLowerCase()
    .replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 100).replace(/-$/, '');
  return checkDraft({
    title: resolvedTitle,
    body,
    slug: fields.slug === undefined ? (generatedSlug || fallbackSlug) : fields.slug,
    date: fields.date === undefined ? nowISO : fields.date,
    description: fields.description === undefined ? '' : fields.description,
    tags: fields.tags === undefined ? categories : fields.tags,
    toc: fields.toc === undefined ? false : fields.toc,
    cover: fields.cover === undefined ? '' : fields.cover,
  }, true);
}

/** Browser edits must contain the normalized draft shape, not raw front matter. */
export function validateDraft(input) {
  return checkDraft(input, false);
}

/** Publication revalidates the draft and always sets Hugo's draft flag to false. */
export function serializePost(draft) {
  const { title, body, slug, date, description, tags, toc, cover } = validateDraft(draft);
  const metadata = { title, date, slug, description, tags, toc, ...(cover ? { cover } : {}), draft: false };
  // Quote strings explicitly so Hugo's YAML 1.1 readers cannot coerce dates or yes/no.
  return `---\n${stringify(metadata, { lineWidth: 0, defaultStringType: 'QUOTE_DOUBLE', defaultKeyType: 'PLAIN' })}---\n${body}`;
}
