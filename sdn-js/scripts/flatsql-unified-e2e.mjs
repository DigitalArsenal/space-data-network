// Real SDN storage adapter -> canonical SDS -> unified WASM -> durable reopen.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { build } from 'esbuild';
const candidate=process.env.FLATSQL_CANDIDATE, sdk=process.env.SDK_CANDIDATE;
const engine=candidate?path.join(candidate,'wasm/index.js'):new URL(import.meta.resolve('flatsql')).pathname;
const flatc=candidate?path.join(candidate,'node_modules/flatc-wasm/src/runner.mjs'):new URL(import.meta.resolve('flatc-wasm/runner')).pathname;
const {FlatcRunner}=await import(pathToFileURL(flatc));
const root=await fs.mkdtemp(path.join(os.tmpdir(),'sdn-unified-e2e-'));let store;
try {
 await build({stdin:{contents:'export * from "./src/local-flatsql.ts"; export * from "./src/engine-record-store.ts";',resolveDir:process.cwd()},bundle:true,format:'esm',platform:'node',outfile:path.join(root,'store.mjs'),plugins:[{name:'candidate-packages',setup(b){
  b.onResolve({filter:/^flatsql$/},()=>({path:engine,external:true}));
  b.onResolve({filter:/^flatc-wasm\/runner$/},()=>({path:flatc,external:true}));
  if(sdk)b.onResolve({filter:/^space-data-module-sdk\/runtime-host$/},()=>({path:path.join(sdk,'src/runtime-host/index.js')}));
 }}]});
 const api=await import(pathToFileURL(path.join(root,'store.mjs'))),converter=await FlatcRunner.init();
 const source=await fs.readFile(new URL('../node_modules/spacedatastandards.org/schema/GJN/main.fbs',import.meta.url),'utf8');
 converter.mountFile('/GJN.fbs',source);converter.mountFile('/out/.keep','');
 const result=converter.runCommand(['--binary','--schema','-o','/out','/GJN.fbs']);assert.equal(result.code,0,result.stderr);
 const schema={standardId:'GJN',tableName:'GJN',fileId:'$GJN',bfbs:converter.readFile('/out/GJN.bfbs')};
 const make=(id,lon,lat)=>converter.generateBinary({entry:'GJN.fbs',files:{'GJN.fbs':source}},JSON.stringify({FEATURES:[{ID:id,HAS_GEOMETRY:true,GEOMETRY:{TYPE:'POINT',POINT:{LONGITUDE:lon,LATITUDE:lat,ALTITUDE:420,HAS_ALTITUDE:true}}}]}),{sizePrefix:true,strictJson:true});
 const frame=make('sdn-customer',2,3),second=make('sdn-shard',4,5);
 const options={schemas:[schema],engine:{root:path.join(root,'data')},nowMs:()=>1700000000000};
 store=await api.FlatSQLEngineRecordStore.open(options);
 const cid=await store.store('GJN.fbs',frame.slice(4),'peer-e2e',Uint8Array.of(1,2,3));assert.match(cid,/^bafkrei/);
 assert.equal(await store.store('GJN.fbs',frame.slice(4),'peer-e2e',Uint8Array.of(1,2,3)),cid);
 const saved=await store.get('GJN.fbs',cid);assert.deepEqual(saved.data,frame.slice(4));assert.deepEqual(saved.signature,Uint8Array.of(1,2,3));assert.equal(saved.timestamp,1700000000000);assert.equal(saved.peerId,"peer-e2e");
 assert.equal((await store.query('GJN.fbs',{peerId:'peer-e2e'})).length,1);
 assert.deepEqual((await store.sql('SELECT geo_contains(_data,3,2) FROM GJN')).rows,[[1]]);
 assert.deepEqual(await store.readStream('GJN',{source:'local'}),frame);
 assert.equal((await store.storeStream(second,{source:'shard'})).ingested,1);
 assert.equal((await store.storeStream(second,{source:'shard'})).ingested,0);
 assert.equal(await store.count(),2);assert.deepEqual(await store.standardsStore.listSources('GJN'),['local','shard']);
 await store.close();store=await api.FlatSQLEngineRecordStore.open(options);
 assert.deepEqual(await store.get('GJN.fbs',cid),saved);assert.deepEqual(await store.readStream('GJN',{source:'shard'}),second);
 assert.equal((await store.listRecords()).length,2);
 await store.delete(cid);assert.equal(await store.get('GJN.fbs',cid),null);assert.equal(await store.count(),1);
 await store.standardsStore.clearStandard('GJN');assert.equal(await store.count(),0);
 await store.close();store=await api.FlatSQLEngineRecordStore.open(options);assert.equal(await store.count(),0);
 await assert.rejects(store.ingestFlatBufferStream('GJN',frame.slice(0,-1)),/truncated/);
 await replacementFlow(api,schema,frame,second,root);
 await browserFlow(schema,frame,root);
 console.log('SDN JS FLATSQL E2E PASS: binary records, signatures, native CID dedupe, source streams, GJN query, deletion and durable reopen');
}finally{await store?.close();await fs.rm(root,{recursive:true,force:true});}

async function replacementFlow(api,schema,frame,second,root) {
 const persistenceStore=new api.MemoryFlatSqlPersistenceStore();
 const schemas=[{...schema,tableName:'Geometries'},{...schema,standardId:'SECOND',tableName:'OtherGeometries'}];
 const options={schemas,persistenceKey:'replacement',persistenceStore,engine:{root:path.join(root,'replacement-data')}};
 const pin=(standardId,cid)=>({standardId,schemaName:standardId+'.fbs',cid,role:'record',verificationState:'verified'});
 let live=await api.createLocalFlatSqlStore(options),staging;
 try {
  await live.ingestFlatBufferStream('GJN',second);
  await live.ingestFlatBufferStream('SECOND',second);
  await live.recordPinLedgerEntries([pin('SECOND','keep-pin')]);
  staging=await live.createStandardReplacementStore('GJN');
  for(const [provider,source,batch,peer,time,sig] of [['provider-a','alpha','one','peer-a',1700000000,1],['provider-b','beta','two','peer-b',1700000001,2],['provider-b','archive','three','peer-b',1700000001,2],['provider-b','beta','four','peer-b',1700000001,2]])
   await staging.ingestRecords('GJN',[{schemaName:'GJN.fbs',cid:'',dataBytes:frame,providerId:provider,sourceName:source,batchId:batch,peerId:peer,timestamp:new Date(time*1000).toISOString(),signature:Uint8Array.of(sig)}]);
  assert.deepEqual(await staging.listSources('GJN'),['alpha','archive','beta']);
  const sql="SELECT p.provider,p.source,p.batch,p.peer,p.ts,p.sig FROM (SELECT DISTINCT _cid FROM Geometries) r JOIN flatsql_provenance('Geometries',r._cid) p ORDER BY p.provider,p.source,p.batch";
  const expected=(await staging.query(sql)).rows;
  assert.equal(expected.length,4);
  await assert.rejects(live.replaceStandardFrom('GJN',live,[]),/separate staging/);
  assert.equal((await live.query('SELECT count(*) FROM Geometries')).rows[0][0],1);
  await live.replaceStandardFrom('GJN',staging,[pin('GJN','replacement-pin')]);
  assert.deepEqual((await live.query(sql)).rows,expected);
  assert.deepEqual(await live.listSources('GJN'),['alpha','archive','beta']);
  assert.deepEqual(await live.queryRawFlatBufferStream('GJN','SELECT DISTINCT _data FROM Geometries'),frame);
  await live.destroy();live=await api.createLocalFlatSqlStore(options);
  assert.deepEqual((await live.query(sql)).rows,expected);
  assert.equal((await live.listPinLedgerEntries()).length,2);
  await live.destroy();live=null;
  await api.clearLocalFlatSqlStore({...options,standardIds:['GJN.fbs','MISSING']});
  await api.clearLocalFlatSqlStore({...options,standardIds:['GJN']});
  live=await api.createLocalFlatSqlStore(options);
  assert.equal((await live.query('SELECT count(*) FROM Geometries')).rows[0][0],0);
  assert.deepEqual(await live.queryRawFlatBufferStream('SECOND','SELECT _data FROM OtherGeometries'),second);
  assert.deepEqual((await live.listPinLedgerEntries()).map(p=>p.cid),['keep-pin']);
  console.log('SDN REPLACEMENT E2E PASS: all providers/sources/batches/signatures, unchanged bytes, reopen, custom root/table clear and isolated pins');
 }finally{await staging?.destroy();await live?.destroy();}
}

async function browserFlow(schema, frame, root) {
 const { chromium }=await import('@playwright/test'),{createServer}=await import('node:http');
 const web=path.join(root,'web');await fs.mkdir(web);
 const engineRoot=path.dirname(engine);
 const plugins=[{name:'release-binaries',setup(b){
   b.onResolve({filter:/^flatsql$/},()=>({path:engine}));
   b.onResolve({filter:/^flatc-wasm\/runner$/},()=>({path:flatc}));
   if(sdk)b.onResolve({filter:/^space-data-module-sdk\/runtime-host$/},()=>({path:path.join(sdk,'src/runtime-host/index.js')}));
 }}];
 const common={bundle:true,format:'esm',platform:'browser',target:'es2022',external:['node:*','module','fs','path','url','./runtime/*'],plugins};
 await build({...common,entryPoints:['src/ui/runtime/local-flatsql-store.worker.ts'],outfile:path.join(web,'store.worker.mjs')});
 await build({...common,stdin:{contents:`import {createWorkerLocalFlatSqlStore} from './src/ui/runtime/local-flatsql-worker-client.ts';
 import {createLocalFlatSqlStore,clearLocalFlatSqlStore} from './src/local-flatsql.ts';
 const schema={...${JSON.stringify({...schema,bfbs:Array.from(schema.bfbs)})},bfbs:new Uint8Array(${JSON.stringify(Array.from(schema.bfbs))})};
 const frame=new Uint8Array(${JSON.stringify(Array.from(frame))});
 const options={schemas:[schema],persistenceKey:'unified-e2e',engine:{wasmUrl:location.origin+'/flatsql.wasm',workerUrl:location.origin+'/runtime/browser-guest.mjs',threadWorkerUrl:location.origin+'/runtime/wasiThreadBrowserWorker.mjs',ioWorkerUrl:location.origin+'/runtime/opfsIoWorker.mjs'}};
 const open=()=>createWorkerLocalFlatSqlStore(options,{createWorker:()=>new Worker('/store.worker.mjs',{type:'module'})});
 let store=await open();window.store=store;
 const status=document.getElementById('status');
 async function show(){const result=await store.query('SELECT _rowid,geo_contains(_data,3,2),_data FROM GJN');document.getElementById('rows').replaceChildren(...result.rows.map(row=>{const tr=document.createElement('tr');for(const cell of row){const td=document.createElement('td');td.textContent=cell instanceof Uint8Array?cell.length+' bytes':String(cell);tr.append(td)}return tr}));return result;}
 async function replace(){
  await store.destroy();
  const live=await createLocalFlatSqlStore(options),staging=await live.createStandardReplacementStore('GJN');
  try {
   for(const [source,batch] of [['alpha','one'],['beta','two'],['beta','three']])await staging.ingestRecords('GJN',[{schemaName:'GJN.fbs',cid:'',dataBytes:frame,providerId:'browser-provider',sourceName:source,batchId:batch,peerId:'browser-peer',timestamp:'2023-11-14T22:13:20.000Z',signature:new Uint8Array([7,8])}]);
   await live.replaceStandardFrom('GJN',staging,[]);
  }finally{await staging.destroy();await live.destroy();}
  store=await open();window.store=store;
  document.getElementById('provenance').textContent=(await store.listSources('GJN')).join(', ')+' / '+(await store.query("SELECT count(*) FROM (SELECT DISTINCT _cid FROM GJN) r JOIN flatsql_provenance('GJN',r._cid) p")).rows[0][0]+' provenance entries';
 }
 async function clearSelected(){await store.destroy();await clearLocalFlatSqlStore({...options,standardIds:['GJN']});store=await open();window.store=store;}
 for(const [id,run] of Object.entries({ingest:()=>store.ingestFlatBufferStream('GJN',frame),replace,reopen:async()=>{await store.destroy();store=await open();window.store=store},clear:()=>store.clearStandard('GJN'),'clear-selected':clearSelected}))document.getElementById(id).onclick=async()=>{status.textContent='Working';try{await run();await show();status.textContent=id+' complete'}catch(error){status.textContent=String(error);throw error}};
 window.raw=()=>store.queryRawFlatBufferStream('GJN','SELECT _data FROM GJN');
 await show();status.textContent='Ready';`,resolveDir:process.cwd()},outfile:path.join(web,'page.mjs')});
 for(const file of ['flatsql.wasm','integrity.json'])await fs.copyFile(path.join(engineRoot,file),path.join(web,file));
 await fs.cp(path.join(engineRoot,'runtime'),path.join(web,'runtime'),{recursive:true});
 await fs.writeFile(path.join(web,'index.html'),'<!doctype html><meta name="viewport" content="width=device-width"><title>SDN binary storage E2E</title><style>body{font:18px system-ui;margin:24px}button{padding:12px;margin:4px}td{padding:12px;border-bottom:1px solid #ccc}</style><h1>SDN binary storage</h1><p id="status">Opening</p><button id="ingest">Ingest</button><button id="replace">Replace</button><button id="reopen">Reopen</button><button id="clear">Clear</button><button id="clear-selected">Clear selected</button><p id="provenance"></p><table><thead><tr><th>Record</th><th>Contains point</th><th>FlatBuffer</th></tr></thead><tbody id="rows"></tbody></table><script type="module" src="/page.mjs"></script>');
 const server=createServer(async(req,res)=>{const file=path.resolve(web,'.'+(req.url==='/'?'/index.html':req.url));try{if(!file.startsWith(web+'/'))throw Error();const data=await fs.readFile(file);res.writeHead(200,{'content-type':file.endsWith('.mjs')?'text/javascript':file.endsWith('.html')?'text/html':file.endsWith('.json')?'application/json':'application/octet-stream','cross-origin-opener-policy':'same-origin','cross-origin-embedder-policy':'require-corp'});res.end(data);}catch{res.writeHead(req.url==='/favicon.ico'?204:404);res.end();}});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const cache=process.env.PLAYWRIGHT_BROWSERS_PATH??path.join(os.homedir(),process.platform==='darwin'?'Library/Caches/ms-playwright':'.cache/ms-playwright');let shell=process.env.GAUNTLET_HEADLESS_SHELL;
 if(!shell)for(const dir of(await fs.readdir(cache)).filter(d=>/^chromium_headless_shell-\d+$/.test(d)).sort((a,b)=>Number(b.split('-')[1])-Number(a.split('-')[1]))) {for(const sub of await fs.readdir(path.join(cache,dir))){const bin=path.join(cache,dir,sub,'chrome-headless-shell');try{await fs.access(bin);shell=bin;break;}catch{}}if(shell)break;}
 if(!shell)throw Error('Chromium headless shell required');
 const browser=await chromium.launch({executablePath:shell,headless:true});
 const evidence=process.env.FLATSQL_BROWSER_RESULTS??await fs.mkdtemp(path.join(os.tmpdir(),'sdn-unified-browser-'));
 try {
  const page=await browser.newPage({viewport:{width:1000,height:760}}),errors=[];page.on('pageerror',error=>errors.push(error.message));page.on('console',message=>{if(message.type()==='error')errors.push(message.text())});
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  const wait=async label=>{
   try { await page.waitForFunction(label=>document.getElementById('status').textContent===label,label,{timeout:30000}); }
   catch(error) { throw new Error(`${label}: ${await page.locator('#status').textContent()}; browser errors: ${errors.join('; ')}`,{cause:error}); }
  };
  await wait('Ready');await page.click('#ingest');await wait('ingest complete');
  assert.deepEqual(await page.locator('tbody td').allTextContents(),['1','1',frame.length-4+' bytes']);
  assert.deepEqual(await page.evaluate(async()=>Array.from(await window.raw())),Array.from(frame));
  await page.screenshot({path:path.join(evidence,'sdn-binary-desktop.png')});
  await page.click('#reopen');await wait('reopen complete');assert.deepEqual(await page.evaluate(async()=>Array.from(await window.raw())),Array.from(frame));
  await page.reload();await wait('Ready');assert.equal(await page.locator('tbody tr').count(),1);
  await page.click('#replace');await wait('replace complete');
  assert.equal(await page.locator('#provenance').textContent(),'alpha, beta / 3 provenance entries');
  const provenance=()=>page.evaluate(async()=>{
   const result=await window.store.query("SELECT p.provider,p.source,p.batch,p.peer,p.ts,p.sig FROM (SELECT DISTINCT _cid FROM GJN) r JOIN flatsql_provenance('GJN',r._cid) p ORDER BY p.source,p.batch");
   return result.rows.map(row=>row.map(value=>value instanceof Uint8Array?Array.from(value):value));
  });
  const expected=[['alpha','one'],['beta','three'],['beta','two']].map(([source,batch])=>['browser-provider',source,batch,'browser-peer',1700000000,[7,8]]);
  assert.deepEqual(await provenance(),expected);
  await page.click('#reopen');await wait('reopen complete');assert.deepEqual(await provenance(),expected);
  await page.screenshot({path:path.join(evidence,'sdn-replacement-desktop.png')});
  await page.setViewportSize({width:390,height:844});await page.screenshot({path:path.join(evidence,'sdn-binary-mobile.png')});
  await page.click('#clear-selected');await wait('clear-selected complete');assert.equal(await page.locator('tbody tr').count(),0);
  await page.click('#ingest');await wait('ingest complete');
  await page.click('#clear');await wait('clear complete');await page.click('#reopen');await wait('reopen complete');assert.equal(await page.locator('tbody tr').count(),0);
  await page.evaluate(()=>window.store.destroy());assert.deepEqual(errors,[]);
  console.log(`SDN BROWSER E2E PASS: actual store worker, binary QRP/GJN, complete replacement provenance, OPFS reopen/reload, standalone and worker clear. Screenshots: ${evidence}`);
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
}
