// One page of a paged PingOne collection (embeddedSnapshots or
// promotionSteps). Appends it to the pages collected so far and returns the
// next URL, enforcing pingone.Client.Pages: same collection path, no cycles,
// at most 10000 pages, and the summed items must reach the largest count.
const key = String(params.collectionKey || '');
const pages = Array.isArray(core.parseBody(params.pagesSoFar)) ? core.parseBody(params.pagesSoFar) : [];
const seen = Array.isArray(core.parseBody(params.seenUrls)) ? core.parseBody(params.seenUrls) : [];
const current = String(params.currentUrl || '');
const first = core.apiURL(params.apiOrigin, String(params.firstUrl || ''));
const fail = (msg) => ({ state: 'error', error: msg, pages: pages, seenUrls: seen, nextUrl: '', pause: 'false' });
const status = Number(params.statusCode);
if (status !== 200) { return fail('PingOne returned HTTP ' + status + ' while reading ' + key + '.'); }
const body = core.parseBody(params.body);
const items = body && body._embedded && body._embedded[key];
if (!Array.isArray(items)) { return fail('Incomplete PingOne response: missing ' + key + ' collection.'); }
pages.push({ count: typeof body.count === 'number' ? body.count : undefined, _embedded: { referencedResources: items } });
seen.push(current);
const href = core.nextPageURL(body);
if (href) {
  let next;
  if (href.charAt(0) === '?') {
    next = current.split('?')[0] + href;
  } else {
    next = href;
  }
  const u = core.apiURL(params.apiOrigin, next);
  if (!u || !first || u.path !== first.path) { return fail('Pagination changed resource collection.'); }
  const resolved = params.apiOrigin + u.path + (u.hasQuery ? '?' + u.query : '');
  if (seen.indexOf(resolved) >= 0 || pages.length >= 10000) { return fail('Cyclic or excessive PingOne pagination.'); }
  // pause: hand back to the browser every 5 pages so no single request runs long.
  return { state: 'more', error: '', pages: pages, seenUrls: seen, nextUrl: resolved, pause: pages.length % 5 === 0 ? 'true' : 'false' };
}
if (!core.pagesComplete(pages)) { return fail('Incomplete PingOne pagination.'); }
return { state: 'done', error: '', pages: pages, seenUrls: seen, nextUrl: '', pause: 'false' };
