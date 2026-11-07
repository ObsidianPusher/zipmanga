const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// Execute the production functions without booting the application's DOM and API clients.
const source = fs.readFileSync(require.resolve('../web/static/app.js'), 'utf8');
function readerHarness() {
  const pending = [];
  const context = vm.createContext({
    AbortController,
    state: {renderEpoch: 0, imageCache: new Map(), imageObjectURLs: new Set()},
    imageRequests: new Map(),
    cachedImageSources: new Map(),
    computeRequestedWidth: () => 1280,
    imageCacheKey: (index, width) => `${index}:${width}`,
    pageURL: (index, width) => `/pages/${index}?w=${width}`,
    releaseReaderImage: () => {},
    URL,
    fetchReaderImage: (url, controller) => new Promise((resolve, reject) => {
      pending.push({url, resolve});
      controller.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
    })
  });
  for (const name of ['nextReaderRenderEpoch', 'clearReaderImageCache', 'getImage']) {
    const start = source.indexOf(`  function ${name}(`);
    assert.notEqual(start, -1, `${name} must exist`);
    const end = source.indexOf('\n  }', start) + '\n  }'.length;
    vm.runInContext(source.slice(start, end), context);
  }
  return {context, pending};
}

test('turning to a page reuses its unfinished preload', async () => {
  const {context, pending} = readerHarness();
  const preload = context.getImage(1);
  // Handle rejection even in the broken implementation before making assertions.
  preload.catch(() => {});
  assert.equal(context.nextReaderRenderEpoch(), 1);
  const displayed = context.getImage(1);
  displayed.catch(() => {});
  assert.equal(displayed, preload, 'turning the page must not restart its download');
  const image = {src: 'blob:next-page'};
  pending[0].resolve(image);
  assert.equal(await displayed, image);
});

test('clearing the reader cancels unfinished image requests', async () => {
  const {context} = readerHarness();
  const preload = context.getImage(1);
  const cancelled = assert.rejects(preload, {name: 'AbortError'});
  context.clearReaderImageCache();
  assert.equal(context.imageRequests.size, 0);
  assert.equal(context.state.imageCache.size, 0);
  await cancelled;
});
