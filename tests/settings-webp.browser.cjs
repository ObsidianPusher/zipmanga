const {test} = require('node:test');
const assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const {mkdtempSync, readFileSync, writeFileSync, rmSync} = require('node:fs');
const {tmpdir} = require('node:os');
const path = require('node:path');
const {pathToFileURL} = require('node:url');

test('WebP task paths keep mobile settings controls inside the viewport', () => {
  assert.ok(process.env.CHROMIUM_BIN, 'Set CHROMIUM_BIN');
  const dir = mkdtempSync(path.join(tmpdir(), 'settings-webp-'));
  try {
    const source = readFileSync(path.resolve(__dirname, '../web/static/app.js'), 'utf8');
    const functions = ['archiveTaskStatusText', 'formatDateTime', 'escapeHtml', 'webpTaskItems',
      'renderWebPTaskList', 'renderWebPTaskSummary', 'renderWebPTaskProgress'].map(name => {
      const start = source.indexOf(`  function ${name}(`);
      assert.notEqual(start, -1);
      return source.slice(start, source.indexOf('\n  }', start) + 4);
    }).join('\n');
    const css = pathToFileURL(path.resolve(__dirname, '../web/static/styles.css'));
    const template = readFileSync(path.resolve(__dirname, '../web/templates/app.html'), 'utf8')
      .replace(/<base[^>]*>/, '')
      .replace(/<link rel="stylesheet"[^>]*>/, `<link rel="stylesheet" href="${css}">`)
      .replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
    const script = `<script>
      const state = {webpConvert:{items:[], selectedTaskID:'running'}};
      const el = Object.fromEntries(['webpConvertTaskList','webpConvertTaskSummary','webpConvertCancelBtn',
        'webpConvertProgressFill','webpConvertProgressText','webpConvertTaskMeta'].map(id=>[id,document.getElementById(id)]));
      function selectWebPTask(id) { document.body.dataset.selectedTask = id; }
      ${functions}
      onload = () => {
        document.querySelectorAll('main > .view').forEach(v=>v.hidden = v.id !== 'viewSettings');
        document.getElementById('viewSettings').hidden = false;
        document.querySelectorAll('.settings-module').forEach(v=>v.hidden = v.dataset.settingsSection !== 'webp-convert');
        const results = [];
        function measure(name) {
          const button = el.webpConvertCancelBtn;
          button.scrollIntoView({block:'center'});
          const rect = button.getBoundingClientRect();
          results.push({name, viewport:innerWidth, right:rect.right, left:rect.left,
            reachable:document.elementFromPoint(rect.x+rect.width/2, rect.y+rect.height/2) === button,
            moduleWidth:document.querySelector('[data-settings-section="webp-convert"].settings-module').getBoundingClientRect().width});
        }
        renderWebPTaskList(); renderWebPTaskProgress(null); measure('empty');
        const task = {id:'running', status:'running', progress_total:10, progress_done:1,
          source_path:'/mnt/comics/'+'LongMangaDirectory'.repeat(15),
          output_dir:'/mnt/output/'+'LongMangaDirectory'.repeat(15)+'-webp',
          current_item:'LongArchiveName'.repeat(20)+'.zip', current_archive:'LongArchiveName'.repeat(20)+'.zip'};
        state.webpConvert.runningTask = task;
        renderWebPTaskList(); measure('task list');
        el.webpConvertTaskList.querySelector('button').click();
        state.webpConvert.runningTask = null; renderWebPTaskList();
        renderWebPTaskProgress(task); measure('running progress');
        state.webpConvert.runningTask = task; renderWebPTaskList(); measure('list and progress');
        const result = document.createElement('pre'); result.id = 'geometry';
        result.textContent = JSON.stringify({results, selectedTask:document.body.dataset.selectedTask});
        document.body.append(result);
      };
    </script>`;
    const fixture = path.join(dir, 'index.html');
    writeFileSync(fixture, template.replace('</body>', script + '</body>'));
    for (const width of [390, 320, 1280]) {
      const dom = execFileSync(process.env.CHROMIUM_BIN, [
        '--headless', '--no-sandbox', '--disable-gpu', '--no-first-run',
        `--window-size=${width},844`, `--user-data-dir=${path.join(dir, 'profile'+width)}`,
        '--dump-dom', pathToFileURL(fixture).href
      ], {encoding:'utf8', stdio:['ignore','pipe','ignore'], timeout:20000});
      const match = dom.match(/<pre id="geometry">([^<]+)<\/pre>/);
      assert.ok(match, 'browser must report layout');
      const {results, selectedTask} = JSON.parse(match[1]);
      assert.equal(selectedTask, 'running');
      for (const result of results) {
        assert.ok(result.right <= result.viewport && result.left >= 0 && result.reachable,
          `${width}px ${JSON.stringify(result)}`);
      }
    }
  } finally {
    rmSync(dir, {recursive:true, force:true});
  }
});
