'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const modulePath = path.resolve(__dirname, '../../public/cross-border-flow.js');
const flow = fs.existsSync(modulePath) ? require(modulePath) : {};
const page = require('../../public/cross-border');
let failures = 0;
function test(name, fn) {
  try { fn(); console.log('PASS ' + name); }
  catch (error) { failures++; console.error('FAIL ' + name + ': ' + error.message); }
}
const sample = {
  status: 'available', identityComplete: true, eligibleContents: 15, unresolvedContents: 3,
  directions: [
    { key: 'ca_us', contents: 10, scoped: 5, unknownScopeName: 2, unscoped: 3, transportWildcard: 1, mixed: 1, repeaters: 4, furthestKm: 23.4 },
    { key: 'us_ca', contents: 0, scoped: 0, unknownScopeName: 0, unscoped: 0, transportWildcard: 0, mixed: 0, repeaters: 0, furthestKm: null }
  ]
};
test('crossing shares count unnamed scoped traffic once and retain mixed evidence', () => {
  assert.strictEqual(typeof flow.directionSummary, 'function');
  const result = flow.directionSummary(sample, 'ca_us');
  assert.strictEqual(result.scopedPercent, 50);
  assert.strictEqual(result.unscopedPercent, 30);
  assert.deepStrictEqual(result.composition.map(row => row.count), [3, 2, 3, 1, 1]);
  assert.strictEqual(result.composition.reduce((sum, row) => sum + row.percent, 0), 100);
  assert.strictEqual(flow.directionSummary(sample, 'us_ca').scopedPercent, null, 'an empty denominator is not 0% scoped');
});
test('inconsistent, unavailable and missing crossing counts never manufacture zero or percentages', () => {
  assert.strictEqual(typeof flow.directionSummary, 'function');
  assert.strictEqual(flow.directionSummary({ ...sample, status: 'unavailable' }, 'ca_us').contents, null);
  assert.strictEqual(flow.directionSummary({}, 'ca_us').scopedPercent, null);
  const invalid = { ...sample, directions: [{ ...sample.directions[0], unknownScopeName: 9 }] };
  assert.strictEqual(flow.directionSummary(invalid, 'ca_us').scopedPercent, null);
  assert(flow.directionSummary(invalid, 'ca_us').composition.every(row => row.percent === null));
});
test('incomplete repeater identities make attribution unavailable while reliable empty and partial counts survive', () => {
  const empty={...sample,status:'partial',identityComplete:false,directions:[{key:'ca_us',contents:0,scoped:0,unknownScopeName:0,unscoped:0,transportWildcard:0,mixed:0,repeaters:0,furthestKm:null,timeline:[{at:'2026-01-01T00:00:00Z',contents:0,complete:true}]}]};
  assert.strictEqual(flow.directionSummary(empty,'ca_us').contents,null);
  assert.strictEqual(flow.directionSummary(empty,'ca_us').repeaters,null);
  assert.strictEqual(flow.directionSummary(empty,'ca_us').scopedPercent,null);
  assert(flow.timeline(empty).series.every(series=>series.values.every(value=>value===null)));
  assert.deepStrictEqual(flow.selectedPaths({...empty,paths:[{id:'unverified',directions:['ca_us']}]},'both'),[]);
  assert.deepStrictEqual(flow.crossingRepeaters({...empty,repeaters:[{publicKey:'unverified',contents:2}]}),[]);
  assert.strictEqual(flow.directionSummary({...empty,status:'empty',identityComplete:true},'ca_us').contents,0,'reliably empty evidence keeps a real zero');
  assert.strictEqual(flow.directionSummary({...sample,status:'partial',identityComplete:true},'ca_us').contents,10,'attributable partial evidence preserves its observed lower bound');
  assert.match(page.qualityNotice({enabled:true,status:'partial',completeness:{packetsTruncated:true},dashboard:{...sample,flow:empty}}),/Repeater identity coverage is incomplete/);
});
test('crossing timelines retain incomplete buckets and align both directions on one clock', () => {
  assert.strictEqual(typeof flow.timeline, 'function');
  const data = { ...sample, directions: [
    { key:'ca_us', timeline:[{at:'2026-01-01T00:00:00Z',contents:99,complete:false},{at:'2026-01-01T00:05:00Z',contents:0,complete:true}] },
    { key:'us_ca', timeline:[{at:'2026-01-01T00:05:00Z',contents:4,complete:true}] }
  ] };
  assert.deepStrictEqual(flow.timeline(data).series.map(series => series.values), [[null,0],[null,4]]);
});
test('map segments cannot jump over an unresolved hop or missing index', () => {
  assert.strictEqual(typeof flow.pathSegments, 'function');
  const hops = [
    {index:0,kind:'repeater',country:'CA',publicKey:'a',location:{lat:43.4,lon:-79.5}},
    {index:1,kind:'unknown',reason:'ambiguous',location:null},
    {index:2,kind:'repeater',country:'US',publicKey:'b',location:{lat:43.1,lon:-78.9}},
    {index:3,kind:'receiver',country:'US',publicKey:'c',location:{lat:43.0,lon:-78.8}}
  ];
  assert.deepStrictEqual(flow.pathSegments({hops}), [[[43.1,-78.9],[43,-78.8]]]);
  assert.deepStrictEqual(flow.pathSegments({hops:[hops[0],hops[2]]}), []);
  assert.deepStrictEqual(flow.pathSegments({hops:[{...hops[2],location:{lat:0,lon:0}},hops[3]]}), []);
  ['', ' ', false, true].forEach(lat => assert.deepStrictEqual(flow.pathSegments({hops:[{...hops[2],location:{lat,lon:-78.9}},hops[3]]}), []));
});
test('direction filtering keeps genuine returning paths in each direction without duplicating records', () => {
  assert.strictEqual(typeof flow.selectedPaths, 'function');
  const paths = [{id:'one',directions:['ca_us']},{id:'return',directions:['ca_us','us_ca']},{id:'two',directions:['us_ca']}];
  assert.deepStrictEqual(flow.selectedPaths({paths},'us_ca').map(p=>p.id), ['return','two']);
  assert.deepStrictEqual(flow.selectedPaths({paths},'both').map(p=>p.id), ['one','return','two']);
  assert.strictEqual(flow.selectedPaths({paths:Array.from({length:1000},(_,i)=>({id:String(i),directions:['ca_us']}))},'both').length,100);
});
test('known catalog choices stay available without observations and preserve case and distinct names', () => {
  assert.strictEqual(typeof flow.catalogRows, 'function');
  const data = {scopeCatalog:[{name:'ca',label:'ca',sources:[{label:'Observed upload',status:'reported'}]},{name:'can',label:'Canada',sources:[{label:'Community proposal',status:'proposed'}]},{name:'ykf',label:'Waterloo',sources:[]}],options:{scopes:[{value:'all',label:'All scopes'},{value:'can',label:'can'},{value:'Custom',label:'Custom'}]}};
  assert.deepStrictEqual(flow.catalogRows(data,'').map(row=>row.name),['ca','can','Custom','ykf']);
  assert.deepStrictEqual(flow.catalogRows(data,'waterloo').map(row=>row.name),['ykf']);
  assert.match(flow.catalogRows(data,'can')[0].evidence,/proposed/);
});
test('reserved scope names and literal named prefixes keep one catalog row and the exact filter encoding', () => {
  assert.strictEqual(typeof flow.catalogRows, 'function');
  const data={scopeCatalog:[{name:'unknown'},{name:'named:all'},{name:'transport-zero'}],options:{scopes:[
    {value:'all',label:'All scopes'},{value:'unknown',label:'Name unresolved'},
    {value:'named:unknown',label:'unknown'},{value:'named:named:all',label:'named:all'},{value:'transport-zero',label:'transport-zero'}
  ]}};
  const rows=flow.catalogRows(data,'');
  assert.strictEqual(rows.length,3);
  assert.deepStrictEqual(rows.map(row=>[row.name,row.filterValue]),[['named:all','named:named:all'],['transport-zero','transport-zero'],['unknown','named:unknown']]);
});
test('path, direction and viewport shareable state is bounded and does not fetch new data', () => {
  assert.strictEqual(page.normalizeFilters({direction:'ca_us',path:'path-1'}).direction,'ca_us');
  assert.strictEqual(page.normalizeFilters({direction:'invalid'}).direction,'both');
  assert.strictEqual(page.normalizeFilters({path:'a'.repeat(500)}).path.length,160);
  assert.strictEqual(page.dataKey(page.normalizeFilters({direction:'ca_us',path:'x'})),page.dataKey(page.normalizeFilters({direction:'us_ca',path:'y'})));
});
test('crossing repeater ranking uses crossing evidence and preserves a bounded independent table', () => {
  assert.strictEqual(typeof flow.crossingRepeaters, 'function');
  const repeaters=[{publicKey:'a',name:'A',contents:100,crossingContents:1},{publicKey:'b',name:'B',contents:2,crossingContents:2}];
  assert.deepStrictEqual(flow.crossingRepeaters({repeaters}).map(r=>r.publicKey),['b','a']);
  assert.strictEqual(flow.crossingRepeaters({repeaters:Array.from({length:1000},(_,i)=>({publicKey:String(i),name:'Fixture',contents:1,crossingContents:1}))}).length,25);
});
test('region chips use friendly labels and only offer supplied options', () => {
  assert.strictEqual(typeof page.regionChoices, 'function');
  assert.strictEqual(typeof page.regionLabel, 'function');
  assert.strictEqual(page.regionLabel('YKF',[]),'Waterloo (YKF)');
  assert.strictEqual(page.regionLabel('BUF',[]),'Buffalo (BUF)');
  assert.strictEqual(page.regionLabel('ZZZ',[{iata:'ZZZ',label:'Fixture city'}]),'Fixture city (ZZZ)');
  assert.strictEqual(page.regionLabel('YYZ',[{iata:'YYZ',label:'Toronto (YYZ)'}]),'Toronto (YYZ)');
  assert.deepStrictEqual(page.regionChoices([{iata:'YKF',label:'YKF'},{iata:'YYZ',label:'YYZ'},{iata:'YYZ',label:'duplicate'},{iata:'invalid'}], 'YKF').map(x=>x.iata),['YYZ']);
  assert.deepStrictEqual(page.regionChoices([],''),[],'friendly labels do not invent available regions');
});
test('MeshMapper ready, stale and loading states use the provider contract rather than unknown', () => {
  assert.strictEqual(typeof flow.meshMapperStatusLabel, 'function');
  assert.strictEqual(flow.meshMapperStatusLabel('ready'),'available');
  assert.strictEqual(flow.meshMapperStatusLabel('stale'),'stale cached data');
  assert.strictEqual(flow.meshMapperStatusLabel('loading'),'loading');
  assert.strictEqual(flow.meshMapperStatusLabel('rate_limited'),'rate limited; cached data may be shown');
  assert.strictEqual(flow.meshMapperStatusLabel('access_denied'),'access not permitted');
  assert.strictEqual(flow.meshMapperStatusLabel('unavailable'),'unavailable');
  assert.strictEqual(flow.meshMapperStatusLabel('unexpected'),'status unknown');
  assert.strictEqual(flow.meshMapperStatusLabel('__proto__'),'status unknown');
});
test('scope-name and catalog limits remain visible when source graphs are otherwise complete', () => {
  const base={enabled:true,status:'partial',completeness:{complete:false},dashboard:{identityComplete:true,local:{status:'available',nodeIndexComplete:true},peer:{status:'available',nodeIndexComplete:true}}};
  assert.match(page.qualityNotice({...base,scopeResolution:{limited:true}}),/Scope-name matching is limited/);
  assert.match(page.qualityNotice({...base,scopeCatalogTruncated:true}),/scope catalog is limited/);
  assert.strictEqual(typeof flow.scopeLimitMessages,'function');
  assert.strictEqual(flow.scopeLimitMessages({...base,scopeResolution:{limited:true},scopeCatalogTruncated:true}).length,2);
  assert.deepStrictEqual(flow.scopeLimitMessages({...base,scopeResolution:{limited:false},scopeCatalogTruncated:false}),[]);
  assert.match(page.qualityNotice(base),/Partial comparison.*incomplete/,'a partial response with no recognized cause must not say ready');
  assert.match(page.qualityNotice({enabled:true,status:'partial',completeness:{complete:false}}),/Partial comparison/,'legacy generic partial fallback stays intact');
});
test('path details translate reason codes and qualify only matched scope-name candidates', () => {
  assert.strictEqual(typeof flow.hopReasonLabel,'function');
  assert.strictEqual(typeof flow.pathScopeLabel,'function');
  const reasons={unresolved_hop:'Repeater could not be identified',one_byte_path:'ID too short to identify reliably',identity_index_incomplete:'Identity coverage incomplete',position_unavailable:'Position unavailable',position_conflict:'Conflicting position reports',country_unresolved:'Country unknown'};
  for(const [reason,label] of Object.entries(reasons))assert.strictEqual(flow.hopReasonLabel(reason),label);
  assert.strictEqual(flow.hopReasonLabel('new_internal_reason'),'Identity or location unavailable');
  assert.strictEqual(flow.pathScopeLabel({scope:{kind:'named',name:'example',resolution:'catalog_match'}}),'example · matched against known names');
  assert.strictEqual(flow.pathScopeLabel({scope:{kind:'named',name:'example',resolution:'configured'}}),'example');
  assert.strictEqual(flow.pathScopeLabel({scope:{kind:'unknown',resolution:'catalog_match'}}),'Scoped · name unresolved');
  assert.strictEqual(flow.pathScopeLabel({scopeKey:'transport-zero',scope:{kind:'unscoped'}}),'Transport wildcard');
});
if (failures) process.exitCode = 1;
