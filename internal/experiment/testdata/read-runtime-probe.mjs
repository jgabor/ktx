// Offline control only. The real, unmodified runtime executes its built-in read.
// Responses SSE matches @ai-sdk/openai 3.0.84, pinned by OpenCode 1.18.5.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {createServer} from 'node:http';
import {readFileSync, writeFileSync, existsSync, renameSync} from 'node:fs';
import {once} from 'node:events';

const directory = process.env.KTX_PROBE_DIRECTORY;
const mode = process.env.KTX_PROBE_MODE;
const digest = text => createHash('sha256').update(text).digest('hex');
const records = path => existsSync(path) ? readFileSync(path, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
const configDir = process.env.XDG_CONFIG_HOME + '/opencode';
// Explicit plugin order, not a filename-order assumption. Move the whole
// generated plugin unchanged out of auto-discovery; never splice its hooks.
const generated = configDir + '/plugins/ktx.ts';
const collector = configDir + '/ktx-probe-collector.ts';
if (existsSync(generated)) renameSync(generated, collector);
const pluginSHA256 = digest(readFileSync(collector));
writeFileSync(directory + '/before.mjs', `
import {appendFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
export default async () => ({'tool.definition': async (input, output) => {
  if (input.toolID !== 'read') return;
  const hash = text => createHash('sha256').update(text).digest('hex');
  const before = hash(output.description), parametersBefore = hash(JSON.stringify(output.parameters.ast));
  if (process.env.KTX_PROBE_MODE === 'description') output.description = output.description.replace('Read a file or directory', 'Inspect a file or directory');
  if (process.env.KTX_PROBE_MODE === 'schema') {
    const parameter = output.parameters.ast.propertySignatures.find(p => p.name === 'limit');
    parameter.type.annotations.description += ' Changed offline ingress.';
  }
  appendFileSync(process.env.KTX_PROBE_DIRECTORY + '/order.jsonl', JSON.stringify({stage:'before-ktx', tool:input.toolID, before, after:hash(output.description), parametersBefore, parametersAfter:hash(JSON.stringify(output.parameters.ast))}) + '\\n');
}});
`);
writeFileSync(directory + '/after.mjs', `
import {appendFileSync, existsSync, readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
export default async () => ({'tool.definition': async (input, output) => {
  if (input.toolID !== 'read') return;
  const directory = process.env.KTX_PROBE_DIRECTORY;
  const hooks = existsSync(directory + '/hooks.jsonl') ? readFileSync(directory + '/hooks.jsonl', 'utf8').trim().split('\\n').filter(Boolean).map(JSON.parse) : [];
  const ktx = hooks.filter(h => h.tool === 'read').at(-1);
  appendFileSync(directory + '/order.jsonl', JSON.stringify({stage:'after-ktx', tool:input.toolID, after:createHash('sha256').update(output.description).digest('hex'), ktxOrdinal:ktx?.ordinal ?? null}) + '\\n');
}});
`);

const model = 'gpt-5-offline-read'; // Synthetic, never the paid model registration.
let requests = 0, transportError = '', deliveredOutputs = [];
const transport = createServer(async (req, res) => {
  try {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/v1/responses');
    let body = '';
    for await (const chunk of req) {
      body += chunk;
      assert.ok(body.length < 1024 * 1024, 'oversized synthetic request');
    }
    const request = JSON.parse(body);
    assert.equal(request.model, model);
    assert.equal(request.stream, true);
    assert.ok(++requests <= 4, 'unexpected model request');
    const phase = Math.floor((requests - 1) / 2);
    const callID = 'call_offline_' + phase;
    const itemID = 'fc_offline_' + phase;
    const call = requests % 2 === 1;
    let answer;
    if (!call) {
      const result = request.input.find(item => item.type === 'function_call_output' && item.call_id === callID);
      assert.ok(result, 'runtime did not return real tool output');
      const output = typeof result.output === 'string' ? result.output : result.output.map(part => part.text ?? '').join('');
      deliveredOutputs.push(output);
      // Completion is synthetic; derive it from actual output, not an answer key.
      answer = [...output.matchAll(/^\d+: ([ab]-\d{4})$/gm)].at(-1)?.[1];
      assert.ok(answer, 'read output has no numbered data');
    }
    res.writeHead(200, {'content-type':'text/event-stream'});
    const event = (type, fields) => res.write('data: ' + JSON.stringify({type, ...fields}) + '\n\n');
    event('response.created', {response:{id:'resp_offline_' + requests, created_at:1, model}});
    if (call) {
      const args = {filePath:'/work/tools/bench/corpus/window-' + (phase ? 'b' : 'a') + '.txt'};
      if (mode === 'wrong-limit') args.limit = 1999;
      const item = {type:'function_call', id:itemID, call_id:callID, name:'read', arguments:''};
      event('response.output_item.added', {output_index:0, item});
      event('response.function_call_arguments.delta', {output_index:0, item_id:itemID, delta:JSON.stringify(args)});
      event('response.output_item.done', {output_index:0, item:{...item, arguments:JSON.stringify(args), status:'completed'}});
    } else {
      const item = {type:'message', id:'msg_offline_' + phase, role:'assistant', content:[{type:'output_text', text:answer, annotations:[]}]};
      event('response.output_item.added', {output_index:0, item});
      event('response.output_text.delta', {item_id:item.id, output_index:0, content_index:0, delta:answer});
      event('response.output_item.done', {output_index:0, item:{...item, status:'completed'}});
    }
    event('response.completed', {response:{usage:{input_tokens:1, output_tokens:1, input_tokens_details:{cached_tokens:0}, output_tokens_details:{reasoning_tokens:0}}}});
    res.end();
  } catch (error) {
    transportError = String(error);
    res.writeHead(400, {'content-type':'application/json'});
    res.end(JSON.stringify({error:{message:transportError, type:'invalid_request_error'}}));
  }
});
transport.listen(0, '127.0.0.1');
await once(transport, 'listening');
const configuration = {
  plugin:['file://' + directory + '/before.mjs', 'file://' + collector, 'file://' + directory + '/after.mjs'],
  enabled_providers:['openai'], model:'openai/' + model, small_model:'openai/' + model,
  provider:{openai:{npm:'@ai-sdk/openai', options:{baseURL:'http://127.0.0.1:' + transport.address().port + '/v1', apiKey:'offline-not-a-credential'}, models:{[model]:{id:model, name:'Offline synthetic read control', tool_call:true, limit:{context:100000, output:1000}}}}},
};
writeFileSync(directory + '/offline.json', JSON.stringify(configuration));
const base = 'http://127.0.0.1:4100';
const runtime = spawn('opencode', ['serve', '--hostname', '127.0.0.1', '--port', '4100', '--print-logs'], {
  env:{...process.env, OPENCODE_CONFIG:directory + '/offline.json'}, stdio:['ignore','pipe','pipe'],
});
let logs = '';
runtime.stdout.on('data', b => logs += b);
runtime.stderr.on('data', b => logs += b);
const exited = once(runtime, 'exit');
let stopped = false;
const stopRuntime = async () => {
  if (stopped) return;
  runtime.kill('SIGTERM');
  const kill = setTimeout(() => runtime.kill('SIGKILL'), 2000);
  await exited;
  clearTimeout(kill);
  stopped = true;
};
const cliOutput = async args => {
  const cli = spawn('opencode', args, {env:{...process.env, OPENCODE_CONFIG:directory + '/offline.json'}, stdio:['ignore','pipe','pipe']});
  let output = '', diagnostics = '';
  cli.stdout.on('data', b => output += b);
  cli.stderr.on('data', b => diagnostics += b);
  const [code] = await once(cli, 'exit');
  assert.equal(code, 0, diagnostics);
  return output;
};
const request = async (path, body) => {
  const response = await fetch(base + path, {method:body ? 'POST' : 'GET', headers:{'content-type':'application/json'}, body:body && JSON.stringify(body), signal:AbortSignal.timeout(30000)});
  if (!response.ok) throw Error(response.status + ' ' + await response.text());
  return response.json();
};
const result = {pluginSHA256, tools:[], session:'', messages:[], turns:[], requests:0, deliveredOutputs:[], guardError:'', guardDiagnostic:'', hookOrder:[]};
try {
  for (let n = 0;; n++) {
    try { if ((await fetch(base + '/global/health', {signal:AbortSignal.timeout(500)})).ok) break; } catch {}
    assert.ok(n < 100, 'runtime startup timeout');
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  try {
    result.tools = await request('/experimental/tool?provider=openai&model=' + model);
  } catch (error) {
    result.guardError = String(error);
    if (!['description', 'schema'].includes(mode)) throw error;
  }
  if (['normal', 'wrong-limit'].includes(mode)) {
    // Use the same standalone CLI turn path as comparisons, not --attach:
    // the pinned attached client can leave before the server's final step.
    await stopRuntime();
    for (const file of ['window-a.txt', 'window-b.txt']) {
      const prompt = 'Use a single default read of /work/tools/bench/corpus/' + file + '. Omit limit and offset; do not page further. Reply with only the last numbered line returned. Do not edit files or use any non-read tool.';
      const continuation = result.session ? ['--session', result.session] : ['--title', 'Offline synthetic read control'];
      const stream = await cliOutput(['run', '--format', 'json', '--model', 'openai/' + model, ...continuation, prompt]);
      result.turns.push(stream);
      const events = stream.trim().split('\n').map(JSON.parse);
      result.session ||= events[0]?.sessionID;
      assert.ok(result.session);
      assert.ok(events.every(event => event.sessionID === result.session));
      assert.equal(transportError, '');
    }
    result.messages = JSON.parse(await cliOutput(['export', result.session])).messages;
    assert.equal(transportError, '');
    assert.equal(requests, 4);
  }
} catch (error) {
  console.error(error, transportError, logs);
  process.exitCode = 1;
} finally {
  await stopRuntime();
  transport.closeAllConnections();
  await new Promise(resolve => transport.close(resolve));
  result.requests = requests;
  result.deliveredOutputs = deliveredOutputs;
  result.hookOrder = records(directory + '/order.jsonl');
  const drift = 'read-description source or parameter schema changed';
  if (logs.includes(drift)) result.guardDiagnostic = drift;
  if (mode === 'fixture') result.guardError = logs.includes('read-description fixture identity mismatch') ? 'read-description fixture identity mismatch' : '';
  assert.equal(digest(readFileSync(collector)), pluginSHA256, 'generated collector was modified');
  console.log(JSON.stringify(result));
}
