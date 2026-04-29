import { readFileSync, writeFileSync, copyFileSync, mkdirSync } from 'fs';
import { dirname } from 'path';
import { execSync } from 'child_process';

const dest = process.argv[2] || 'build/wasm_exec.js';

// 1. Copy TinyGo's wasm_exec.js
const tgr = execSync('tinygo env TINYGOROOT', { encoding: 'utf8' }).trim();
mkdirSync(dirname(dest), { recursive: true });
copyFileSync(`${tgr}/targets/wasm_exec.js`, dest);
console.log(`Copied TinyGo wasm_exec.js -> ${dest}`);

let s = readFileSync(dest, 'utf8');
const lines = s.split('\n');

function commentBlock(marker) {
  for (let i = 0; i < lines.length; i++) {
    if (!lines[i].includes(marker)) continue;
    let depth = 0;
    for (let j = i; j < lines.length; j++) {
      depth += (lines[j].match(/{/g) || []).length - (lines[j].match(/}/g) || []).length;
      if (depth <= 0 && j > i) {
        lines[i] = '/* ' + lines[i];
        lines[j] = lines[j] + ' */';
        return true;
      }
    }
  }
  return false;
}

commentBlock('if (!global.require && typeof require !== "undefined")');
commentBlock('if (!global.fs && global.require)');
commentBlock('if (!global.crypto)');
commentBlock('if (!global.TextEncoder)');
commentBlock('if (!global.TextDecoder)');

for (let i = 0; i < lines.length; i++) {
  if (lines[i]?.includes('const wasi_EBADF = 8;') && lines[i + 1]?.includes('const wasi_ENOSYS = 52;')) {
    lines.splice(i, 2); break;
  }
}
for (const stub of [
  'fd_read: () => wasi_ENOSYS,',
  'fd_prestat_get: () => wasi_EBADF,',
  'fd_prestat_dir_name: () => wasi_ENOSYS,',
  'path_open: () => wasi_ENOSYS,',
]) {
  const idx = lines.findIndex(l => l.includes(stub.replace(/,\s*$/, '')));
  if (idx >= 0) lines.splice(idx, 1);
}
for (let i = 0; i < lines.length; i++) {
  if (lines[i].includes('fd_close: () => wasi_ENOSYS,')) lines[i] = lines[i].replace('wasi_ENOSYS,', '0,      // dummy');
  if (lines[i].includes('fd_fdstat_get: () => wasi_ENOSYS,')) lines[i] = lines[i].replace('wasi_ENOSYS,', '0, // dummy');
  if (lines[i].includes('fd_seek: () => wasi_ENOSYS,')) lines[i] = lines[i].replace('wasi_ENOSYS,', '0,       // dummy');
  if (lines[i].includes('WASI/blob/snapshot-01')) {
    lines[i] = lines[i].replace('snapshot-01/phases/snapshot/docs.md', 'main/phases/snapshot/docs.md#fd_write');
  }
}
commentBlock('global.require.main === module');

// Add globalProxy to run() (simplified, no context special case)
for (let i = 0; i < lines.length; i++) {
  if (lines[i].trim() === 'async run(instance) {') {
    lines[i] = [
      'async run(instance, context) {',
      '\t\t\tconst globalProxy = new Proxy(global, {',
      '\t\t\t\tget(target, prop) { return Reflect.get(...arguments); }',
      '\t\t\t})',
    ].join('\n');
    break;
  }
}
for (let i = 0; i < lines.length; i++) {
  const pre = lines[i - 1] || '';
  if (lines[i].trim() === 'global,' && pre.includes('globalProxy')) {
    lines[i] = lines[i].replace('global,', 'globalProxy,');
    break;
  }
}

// Add runtime.getRandomData
if (!s.includes('"runtime.getRandomData"')) {
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].includes('"runtime.sleepTicks"')) {
      for (let j = i; j < lines.length; j++) {
        if (lines[j].trim() === '},' && /^\t{4,5}$/.test(lines[j].replace(/[^ \t]/g, ''))) {
          lines[j] += [
            '\n\t\t\t\t"runtime.getRandomData": (sp) => {',
            '\t\t\t\t\tsp >>>= 0;',
            '\t\t\t\t\tcrypto.getRandomValues(loadSlice(sp + 8));',
            '\t\t\t\t},',
          ].join('\n');
          break;
        }
      }
      console.log('Added runtime.getRandomData');
      break;
    }
  }
}

writeFileSync(dest, lines.join('\n'));

// 2. Rewrite worker.mjs with instance caching
const workerPath = dest.replace('wasm_exec.js', 'worker.mjs');
const wmjs = readFileSync(workerPath, 'utf8');

const newWorker = `import "./wasm_exec.js";
import { createRuntimeContext, loadModule } from "./runtime.mjs";

let mod, go, instance;
const binding = {};

async function run(ctx) {
  if (mod === undefined) {
    mod = await loadModule();
  }
  if (go === undefined) {
    go = new Go();
    let ready;
    const readyPromise = new Promise((resolve) => { ready = resolve; });
    globalThis.context = createRuntimeContext({ env: ctx.env, ctx: ctx.ctx, binding });
    instance = new WebAssembly.Instance(mod, {
      ...go.importObject,
      workers: { ready: () => { ready(); } },
    });
    go.run(instance, ctx);
    await readyPromise;
  } else {
    globalThis.context = createRuntimeContext({ env: ctx.env, ctx: ctx.ctx, binding });
  }
}

async function fetch(req, env, ctx) {
  await run({ env, ctx });
  const result = binding.handleRequest(req);
  if (result && typeof result.then === "function") {
    return result.then(r => {
      if (r instanceof Response) return r;
      console.error("Not a Response:", typeof r, r);
      return new Response(String(r), { status: 500 });
    }).catch(e => {
      console.error("handleRequest rejected:", e);
      return new Response("handler error", { status: 500 });
    });
  }
  return result;
}

async function scheduled(event, env, ctx) {
  await run({ env, ctx });
  return binding.runScheduler(event);
}

async function queue(batch, env, ctx) {
  await run({ env, ctx });
  return binding.handleQueueMessageBatch(batch);
}

async function onRequest(ctx) {
  const { request, env } = ctx;
  await run({ env, ctx });
  return binding.handleRequest(request);
}

export default { fetch, scheduled, queue, onRequest };
`;

writeFileSync(workerPath, newWorker);
console.log('Rewrote worker.mjs with cached Go instance');

// Verify
let depth = 0, errs = 0;
const output = readFileSync(dest, 'utf8').split('\n');
for (let i = 0; i < output.length; i++) {
  depth += (output[i].match(/\/\*/g) || []).length - (output[i].match(/\*\//g) || []).length;
  if (depth < 0) { errs++; depth = 0; }
}
if (depth > 0) errs++;
console.log(errs === 0 ? 'Comment balance OK' : 'Comment balance FAILED');
console.log('Done');
