const {test} = require('node:test');
const assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const {mkdtempSync, writeFileSync, rmSync} = require('node:fs');
const {tmpdir} = require('node:os');
const path = require('node:path');
const {pathToFileURL} = require('node:url');

// Run with CHROMIUM_BIN pointing to a headless Chromium executable.
test('mobile fullscreen centers a contained page in the whole viewport', () => {
  assert.ok(process.env.CHROMIUM_BIN, 'Set CHROMIUM_BIN');
  const dir = mkdtempSync(path.join(tmpdir(), 'reader-fullscreen-'));
  try {
    const css = pathToFileURL(path.resolve(__dirname, '../web/static/styles.css'));
    const html = `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">
      <link rel="stylesheet" href="${css}">
      <body class="fullscreen-reading"><section id="viewReader">
      <div class="reader-stage" data-mode="paged" data-fit="contain" data-spread="single">
      <div class="reader-paged-wrap"><div class="paged-spread">
      <div class="reader-page-slot"><div class="reader-page-media" style="width:300px;height:450px;animation:none"></div></div>
      </div></div></div></section>
      <script>onload = () => {
        const rect = document.querySelector('.reader-page-media').getBoundingClientRect();
        const result = document.createElement('pre');
        result.id = 'geometry';
        result.textContent = JSON.stringify({center:rect.top + rect.height/2, expected:innerHeight/2, width:innerWidth});
        document.body.append(result);
      };</script>`;
    const fixture = path.join(dir, 'index.html');
    writeFileSync(fixture, html);
    const dom = execFileSync(process.env.CHROMIUM_BIN, [
      '--headless', '--no-sandbox', '--disable-gpu', '--no-first-run',
      '--window-size=390,844', `--user-data-dir=${path.join(dir, 'profile')}`,
      '--dump-dom', pathToFileURL(fixture).href
    ], {encoding:'utf8', stdio:['ignore', 'pipe', 'ignore'], timeout:20000});
    const match = dom.match(/<pre id="geometry">([^<]+)<\/pre>/);
    assert.ok(match, 'browser must report actual page geometry');
    const geometry = JSON.parse(match[1]);
    assert.ok(geometry.width <= 780, 'exercise mobile CSS');
    assert.ok(Math.abs(geometry.center - geometry.expected) <= 2,
      `page center=${geometry.center}, viewport center=${geometry.expected}`);
  } finally {
    rmSync(dir, {recursive:true, force:true});
  }
});
