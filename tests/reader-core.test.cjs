const {test}=require('node:test');
const assert=require('node:assert/strict');
const core=require('../web/static/reader-core.js');
test('width fit keeps a tall page readable; contain fits both dimensions',()=>{
 assert.equal(core.pageScale(800,2400,400,700,'width'),.5);
 assert.equal(core.pageScale(800,2400,400,700,'contain'),700/2400);
 assert.equal(core.pageScale(800,2400,400,700,'original'),1);
});
test('spread stepping preserves a solo cover and reverses a double spread',()=>{
 assert.equal(core.turnPage(0,1,10,true,true),1);
 assert.equal(core.turnPage(1,1,10,true,true),3);
 assert.equal(core.turnPage(3,-1,10,true,true),1);
 assert.equal(core.turnPage(1,-1,10,true,true),0);
 assert.equal(core.turnPage(8,1,10,true,false),9);
});
test('pagination clamps malformed, empty and beyond last page',()=>{
 assert.equal(core.clampListPage(999,5),5);
 assert.equal(core.clampListPage(-5,0),1);
 assert.equal(core.clampListPage('oops',3),1);
});
test('state recovery respects event time even when re-reading earlier pages',()=>{
 const remote={last_page:20,updated_at:'2026-10-20T00:00:00.000Z'};
 const local={last_page:2,updated_at:'2026-10-20T00:01:00.000Z'};
 assert.equal(core.newerState(remote,local),local);
 assert.equal(core.newerState(local,remote),local);
 assert.equal(core.newerState(null,remote),remote);
});
