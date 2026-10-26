// Browser smoke tests use mocked comic APIs against a disposable running server.
// Install Playwright separately; no production dependency is required.
const {chromium}=require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const base=process.env.TEST_BASE_URL || 'http://127.0.0.1:18086';
const user=process.env.TEST_USERNAME;
const password=process.env.TEST_PASSWORD;
if (!user || !password) throw new Error('Set TEST_USERNAME and TEST_PASSWORD for a disposable server');
const assert=require('node:assert/strict');
(async()=>{
const browser=await chromium.launch({headless:true,executablePath:chromium.executablePath()});
const context=await browser.newContext({viewport:{width:1280,height:900}});
const page=await context.newPage();
const errors=[];
page.on('pageerror',e=>errors.push(e.stack));
page.on('dialog',d=>{console.log('DIALOG',d.message());d.dismiss();});
await context.request.post(base+'/api/login',{data:{username:user,password}});
const comic={id:1,mount_id:1,path:'/series/Book 1.zip',title:'测试漫画',format:'zip',page_count:20,cover_url:'/fake-cover.png',file_name_key:'book1.zip',is_favorite:false};
let saved=null,failPage=-1,failedOnce=false, unknown=false, pageCount=20, alwaysFail=-1;
const pageRequests=[];
let defaults={mode:'paged',fit:'width',quality:'standard',spread:'auto',cover_offset:true,enhance_zip:'level1',enhance_pdf:'level1',spread_direction:'rtl',mobile_compact:true};
await page.route('**/api/**',async route=>{
const url=new URL(route.request().url()), path=url.pathname;
let data;
if(path==='/api/me/settings'){
 if(route.request().method()==='PUT') defaults={...route.request().postDataJSON(),updated_at:new Date(Math.max(Date.now(),Date.parse(saved?.updated_at)||0,Date.parse(defaults.updated_at)||0)+1).toISOString()};
 data={settings:defaults,has_custom:true};
}
else if(path==='/api/mounts')data={items:[]};
else if(path.includes('/folders'))data={items:[]};
else if(path==='/api/comics') {
 const n=Number(url.searchParams.get('page')||1);
 data={items:[{...comic,title:`第 ${n} 页漫画`}],pagination:{page:n,page_size:100,total:250,total_pages:3}};
} else if(path==='/api/history'){
 const n=Number(url.searchParams.get('page')||1);
 data={items:[{comic:{...comic,title:`历史第 ${n} 页`},progress:{last_page:4,total_pages:20}}],page:n,page_size:100,total:250,total_pages:3};
} else if(path==='/api/comics/1/reader-state') {
 if(route.request().method()==='PUT'){saved=route.request().postDataJSON();data={state:saved};}
 else data={state:saved};
} else if(path==='/api/comics/1/neighbors')data={previous:null,next:{...comic,id:2,title:'下一册'}};
else if(path==='/api/comics/1/progress')data={progress:null};
else if(path==='/api/comics/1')data={comic:{...comic,page_count:unknown?1:pageCount}};
else if(path.includes('/pages/')) {
 const index=Number(path.split('/').at(-1));pageRequests.push(index);
 if(index===alwaysFail) return route.fulfill({status:502,json:{error:"temporary",code:"page_unavailable"}});
 if(index===failPage && !failedOnce){failedOnce=true;return route.fulfill({status:502,json:{error:'temporary',code:'page_unavailable'}});}
 return route.fulfill({contentType:'image/svg+xml',body:`<svg xmlns="http://www.w3.org/2000/svg" width="800" height="1600"><rect width="800" height="1600" fill="#f2eadc"/><text x="100" y="200" font-size="60">Page ${index+1}</text></svg>`});
} else return route.continue();
return route.fulfill({json:data});
});
async function changeSettings(changes) {
 await page.goto(base+'/settings');
 await page.locator('button.settings-index-btn[data-settings-section="reader"]').click();
 await page.waitForSelector('#settingsForm');
 for(const [field,value] of Object.entries(changes)) {
  const id={mode:'settingsMode',fit:'settingsFit',quality:'settingsQuality',spread:'settingsSpread',cover_offset:'settingsCoverOffset'}[field];
  if(typeof value==='boolean') await page.locator('#'+id).setChecked(value);
  else await page.selectOption('#'+id,value);
 }
 const response=page.waitForResponse(r=>r.url().endsWith('/api/me/settings')&&r.request().method()==='PUT');
 await page.locator('#settingsForm button[type="submit"]').click();await response;
 await page.waitForTimeout(50);
 await page.goto(base+'/reader/1');
 await page.waitForSelector('.reader-page-media[data-state="ready"]');
}
for(const [url,kind] of [['/','library'],['/favorites','favorites'],['/history','history']]){
 await page.goto(base+url);await page.waitForSelector(`[data-list-pager="${kind}"]`);
 const pager=page.locator(`[data-list-pager="${kind}"]`).last();
 await pager.locator('[data-page-input]').fill('3'); await pager.locator('[data-page-action="jump"]').click();
 await page.waitForFunction(k=>[...document.querySelectorAll(`[data-list-pager="${k}"] [data-page-label]`)].every(el=>el.textContent.includes('第 3 / 3')),kind);
 assert.equal(await pager.locator('[data-page-action="next"]').isDisabled(),true);
 await pager.locator('[data-page-action="prev"]').click();
 await page.waitForFunction(k=>document.querySelector(`[data-list-pager="${k}"] [data-page-label]`).textContent.includes('第 2 / 3'),kind);
 console.log('PASS pager',kind);
}
await page.goto(base+'/reader/1');
await page.waitForSelector('.reader-page-media[data-state="ready"]');
console.log('initial',await page.locator('#readerProgressText').textContent());
assert.equal(await page.locator('#readerPreferences, #readerToolbarBtn').count(),0);
await page.locator('#readerFullscreenBtn').click();
await page.waitForFunction(()=>!!document.fullscreenElement);
await page.waitForFunction(()=>getComputedStyle(document.querySelector('#readerTopbar')).display==='none');
assert.equal(await page.locator('#readerTopbar').isVisible(),false);
assert.equal(await page.locator('.topbar').isVisible(),false);
await page.evaluate(()=>document.exitFullscreen());
await page.waitForFunction(()=>!document.fullscreenElement);
await page.waitForTimeout(200);
assert.equal(await page.locator('#readerTopbar').isVisible(),true);
console.log('PASS fullscreen hides bars, normal reader has no options/toolbar');
await changeSettings({spread:'single'});
await page.waitForTimeout(200);
await page.locator('#readerNextBtn').click();
await page.waitForFunction(()=>document.querySelector('#readerProgressText').textContent.includes('第 2/20'));
const before=pageRequests.length;
await page.locator('#pageSlider').evaluate(el=>{el.value='10';el.dispatchEvent(new Event('input',{bubbles:true}));});
await page.waitForTimeout(100);assert.equal(pageRequests.length,before);
await page.locator('#pageSlider').evaluate(el=>el.dispatchEvent(new Event('change',{bubbles:true})));
await page.waitForFunction(()=>document.querySelector('#readerProgressText').textContent.includes('第 10/20'));
console.log('PASS slider preview');
failPage=12;
await page.locator('#pageSlider').evaluate(el=>{el.value='13';el.dispatchEvent(new Event('change',{bubbles:true}));});
await page.waitForSelector('#readerRetryBtn:not([hidden])');
assert.ok((await page.locator('#readerProgressText').textContent()).includes('第 10/20'));
await page.locator('#readerRetryBtn').click();
await page.waitForFunction(()=>document.querySelector('#readerProgressText').textContent.includes('第 13/20'));
console.log('PASS retry retains progress');
await page.locator('.reader-page-media[data-state="ready"]').first().dblclick();
assert.equal(await page.locator('#readerStage').getAttribute('data-zoomed'),'true');
const old=await page.locator('#readerProgressText').textContent();await page.locator('#readerNextBtn').click();await page.waitForTimeout(100);assert.equal(await page.locator('#readerProgressText').textContent(),old);
await page.locator('.reader-page-media[data-state="ready"]').first().dblclick();console.log('PASS zoom turn guard');
pageCount=6; saved=null;
await page.evaluate(()=>localStorage.clear());
await page.reload();
await page.waitForSelector('.reader-page-media[data-state="ready"]');
await changeSettings({spread:'double'});
await page.waitForTimeout(100);
for(const expected of [2,4,6]) {
 await page.locator('#readerNextBtn').click();
 await page.waitForFunction(n=>document.querySelector('#readerProgressText').textContent.includes(`第 ${n}/6`),expected);
}
for(const expected of [4,2,1]) {
 await page.locator('#readerPrevBtn').click();
 await page.waitForFunction(n=>document.querySelector('#readerProgressText').textContent.includes(`第 ${n}/6`),expected);
}
console.log('PASS double spread forward/backward boundaries');
await page.locator('#readerNextBtn').click();
await page.waitForSelector('#readerStage[data-spread="double"]');
await changeSettings({mode:'scroll'});
await page.waitForSelector('.scroll-item img[data-state="ready"]');
assert.ok(await page.evaluate(()=>{
 const img=[...document.querySelectorAll('.scroll-item img[data-state="ready"]')].find(el=>!el.hidden);
 return img && parseFloat(img.style.width)>document.querySelector('#readerStage').clientWidth*.8;
}));
await changeSettings({mode:'paged'});
await page.locator('#readerFirstBtn').click();
await page.waitForFunction(()=>document.querySelector('#readerProgressText').textContent.includes('第 1/6'));
console.log('PASS double to scroll retains full width');
await changeSettings({spread:'single'});
await page.waitForTimeout(100);
await page.evaluate(()=>{const input=document.createElement('input');input.id='shortcut-test';document.querySelector('#readerTopbar').append(input);input.focus();});
await page.keyboard.press('ArrowRight');await page.waitForTimeout(100);
assert.ok((await page.locator('#readerProgressText').textContent()).includes('第 1/6'));
console.log('PASS focused input shortcut guard');
pageCount=20; unknown=true;saved=null;await page.evaluate(()=>localStorage.clear());
await page.reload();await page.waitForSelector('.reader-page-media[data-state="ready"]');
await changeSettings({mode:'scroll'});
assert.equal(await page.locator('.scroll-item').count(),1);
unknown=false;
await page.waitForFunction(()=>document.querySelectorAll('.scroll-item').length===20);
console.log('PASS unknown count rebuilds scroll');
await page.waitForTimeout(200);
await page.waitForSelector('.scroll-item .reader-page-media[data-state="ready"]');
await page.locator('.scroll-item[data-index="15"]').scrollIntoViewIfNeeded();
await page.waitForTimeout(600);
await page.locator('.scroll-item[data-index="15"] img[data-state="ready"]').waitFor();
const count=await page.locator('.scroll-item img[src]').count(); assert.ok(count<12,`scroll loaded ${count}`);
console.log('PASS scroll window',count);

await page.waitForTimeout(500);
assert.equal(await page.locator('.scroll-item[data-index="0"] img[src]').count(),0);
assert.equal(await page.locator('#readerRetryBtn').isVisible(),false);
if(process.env.TEST_SCREENSHOT_DIR) await page.screenshot({path:`${process.env.TEST_SCREENSHOT_DIR}/reader-desktop.png`});
await page.waitForTimeout(500);assert.ok(saved && saved.settings.mode==='scroll');
assert.equal('sharpen' in saved.settings,false);
await page.reload();await page.waitForSelector('#readerStage[data-mode="scroll"]');console.log('PASS scoped state restore');
await page.setViewportSize({width:390,height:844});
await page.waitForTimeout(300);
await page.locator('#readerFullscreenBtn').click();
await page.waitForFunction(()=>document.body.classList.contains('fullscreen-reading'));
assert.equal(await page.locator('#readerTopbar').isVisible(),false);
await page.goBack();
await page.waitForFunction(()=>!document.body.classList.contains('fullscreen-reading'));
assert.equal(await page.locator('#readerTopbar').isVisible(),true);
console.log('PASS mobile fullscreen hides bars and back exits');
if(process.env.TEST_SCREENSHOT_DIR) await page.screenshot({path:`${process.env.TEST_SCREENSHOT_DIR}/reader-mobile.png`});
assert.deepEqual(errors,[]);
console.log('PASS browser no exceptions');
await browser.close();
})().catch(e=>{console.error(e);process.exit(1)});
