const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../panel/static/app.js'), 'utf8');
const health = source.slice(source.indexOf('function networkHealth('), source.indexOf('function view('));
assert.ok(health.includes('function networkHealth('));
const context = vm.createContext({ state: { job: { state: 'done' } }, metrics: {} });
vm.runInContext(health, context);
const ready = { on: true, enabled: true, checks: { ap: true, running: true, lan: true, dhcp: true } };
const fresh = () => ({ time: Date.now() / 1000, internet: { status: 'reachable' } });
context.metrics = fresh();
assert.equal(context.networkHealth({ ...ready, on: false }).color, 'off');
assert.equal(context.networkHealth(ready).color, 'ok');
for (const key of ['ap', 'running', 'lan', 'dhcp']) {
    assert.equal(context.networkHealth({ ...ready, checks: { ...ready.checks, [key]: false } }).color, 'warning', key);
    assert.equal(context.networkHealth({ ...ready, checks: { ...ready.checks, [key]: null } }).color, 'warning', key + ' unknown');
}
for (const status of ['no_reply', 'unknown', undefined]) {
    context.metrics = { ...fresh(), internet: { status } };
    assert.equal(context.networkHealth(ready).color, 'warning', String(status));
}
context.metrics = { ...fresh(), time: Date.now() / 1000 - 41 };
assert.equal(context.networkHealth(ready).color, 'warning', 'stale data');
context.metrics = { ...fresh(), paused: true };
assert.equal(context.networkHealth(ready).color, 'warning', 'paused metrics');
context.metrics = fresh();
context.state.job.state = 'running';
assert.equal(context.networkHealth(ready).color, 'warning', 'radio in use');
context.state.job.state = 'done';
context.state.recovery = true;
assert.equal(context.networkHealth(ready).color, 'warning', 'pending recovery');
console.log('NETWORK_HEALTH_RULES_OK');
