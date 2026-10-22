// Capture the actual Grafana UI from observability-smoke.py's disposable stack.
// Uses Node's built-in WebSocket and the Chrome DevTools Protocol; no npm deps.
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

if (Number(process.versions.node.split('.')[0]) < 22) throw new Error('Screenshot capture requires Node 22 or newer.');
const [configPath, output] = process.argv.slice(2);
if (!configPath || !output) throw new Error('Usage: node scripts/capture-grafana.mjs FIXTURE_CONFIG OUTPUT.png');
const config = JSON.parse(await readFile(configPath, 'utf8'));
if (config.purpose !== 'tado-bridge-synthetic-preview' || !/^http:\/\/127\.0\.0\.1:\d+$/.test(config.url)) {
  throw new Error('Capture requires the isolated synthetic Compose fixture.');
}
const profile = await mkdtemp(join(tmpdir(), 'tado-grafana-chrome-'));
const binary = process.env.CHROME_BIN || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : 'google-chrome');
const chrome = spawn(binary, ['--headless=new', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', `--user-data-dir=${profile}`, '--no-first-run', '--no-default-browser-check', '--disable-background-networking', '--disable-component-update', '--disable-sync', '--hide-scrollbars', 'about:blank'], { stdio: 'ignore' });
let socket;
let processError;
chrome.on('error', error => { processError = error; });
try {
  async function wait(check, description, timeout = 60000) {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
      if (processError) throw processError;
      try { if (await check()) return; } catch (error) { if (processError) throw error; }
      await delay(250);
    }
    throw new Error(`Timed out waiting for ${description}`);
  }
  let endpoint;
  await wait(async () => {
    const [port, path] = (await readFile(join(profile, 'DevToolsActivePort'), 'utf8')).trim().split('\n');
    endpoint = `ws://127.0.0.1:${port}${path}`;
    return true;
  }, 'Chrome');
  socket = new WebSocket(endpoint);
  await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }); });
  let nextID = 0;
  const pending = new Map();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (!message.id) return;
    const callbacks = pending.get(message.id);
    if (!callbacks) return;
    pending.delete(message.id);
    if (message.error) callbacks.reject(new Error(message.error.message)); else callbacks.resolve(message.result);
  });
  function send(method, params = {}, sessionId) {
    return new Promise((resolve, reject) => {
      const id = ++nextID;
      const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`Chrome command timed out: ${method}`)); }, 30000);
      pending.set(id, { resolve: result => { clearTimeout(timeout); resolve(result); }, reject: error => { clearTimeout(timeout); reject(error); } });
      socket.send(JSON.stringify({ id, method, params, sessionId }));
    });
  }
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const page = (method, params) => send(method, params, sessionId);
  async function evaluate(expression) {
    const result = await page('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (result.exceptionDetails) throw new Error('Grafana browser evaluation failed');
    return result.result.value;
  }
  await page('Page.enable');
  await page('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1360, deviceScaleFactor: 1, mobile: false });
  await page('Page.navigate', { url: `${config.url}/login` });
  await wait(() => evaluate(`location.origin === ${JSON.stringify(config.url)} && document.readyState === 'complete'`), 'login page');
  const status = await evaluate(`(async () => (await fetch('/login', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(${JSON.stringify({ user: 'admin', password: config.password })})})).status)()`);
  if (status !== 200) throw new Error('Synthetic Grafana login failed');
  const room = 'Synthetic home / Room 1';
  await page('Page.navigate', { url: `${config.url}/d/tado-bridge/tado-bridge?from=now-24h&to=now&timezone=utc&var-job=tado-bridge&var-instance=tado-bridge%3A8080&var-room=${encodeURIComponent(room)}&kiosk` });
  await wait(() => evaluate("document.body.innerText.includes('Measured temperature') && document.body.innerText.includes('Heating activity') && document.body.innerText.includes('Temperature · Synthetic home / Room 1') && !document.body.innerText.includes('Loading...')"), 'selected-room gauges and history');
  await evaluate('document.fonts.ready.then(() => true)');
  // Give canvas plots their first paint after asynchronous data-source queries.
  await delay(6000);
  if (await evaluate("document.body.innerText.includes('No data')")) throw new Error('Selected synthetic room gauges should have data; inspect dashboard queries before capturing.');
  if (!await evaluate("['19.2','20.5','46.0'].every(value => document.body.innerText.includes(value))")) throw new Error('The visible gauges do not match synthetic Room 1 readings.');
  await evaluate("document.title = 'tado-bridge — Synthetic preview'; window.scrollTo(0,0)");
  const result = await page('Page.captureScreenshot', { format: 'png', fromSurface: true, captureBeyondViewport: false });
  await mkdir(dirname(output), { recursive: true });
  await writeFile(output, Buffer.from(result.data, 'base64'));
  console.log('Saved real Grafana screenshot with synthetic data.');
} finally {
  if (socket) socket.close();
  chrome.kill('SIGTERM');
  await Promise.race([new Promise(resolve => chrome.once('exit', resolve)), delay(5000)]);
  await rm(profile, { recursive: true, force: true });
}
