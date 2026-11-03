import test from "node:test";
import assert from "node:assert/strict";
import Tailcat from "../mods/tailcat/main.js";
import { host, reset, context } from "./app-host.mjs";
const drain = async () => { for (let i = 0; i < 10; i++) await new Promise(setImmediate); };
async function view() { const panel = new Tailcat(); panel.init({}, context()); await drain(); return panel; }
test("does not launch core while App local layout is unavailable", async () => {
  reset(); host.live = [];
  const panel = await view();
  assert.equal(host.calls.length, 0);
  panel.refresh(context()); await drain(); assert.equal(host.calls.length, 0);
  panel.close();
});
test("prompt terminal argv/props contain only id and executable; refresh displays new public key", async () => {
  reset(); const panel = await view();
  panel.importProfile("new-profile", context()); await drain();
  const call = host.calls.find((c) => c.type === "hc");
  assert.deepEqual(call.args.slice(-5), ["/demo/bin/herdr-tailcat", "profile", "add", "new-profile", "--prompt"]);
  assert.ok(call.args.includes("--no-parent") && call.args.includes("--bg"));
  const spec = host.overlays[0];
  assert.deepEqual(spec.layout.children[0].content.props, { part: "bar", id: "new-profile" });
  assert.equal(spec.layout.children[1].content.terminal, "t1");
  host.profiles.push({ ...host.profiles[0], id: "new-profile", machines: [] });
  panel.refresh(context()); await drain();
  assert.equal(panel.selected, "new-profile"); assert.ok(panel.keys["new-profile"].public_key.startsWith("nodekey:"));
  assert.ok(!JSON.stringify(host.calls).includes(host.reveal));
  panel.close();
});
test("hc terminal failure gives safe manual import fallback", async () => {
  reset(); const panel = await view(); host.failHC = true;
  panel.importProfile("new-profile", context()); await drain();
  assert.match(panel.fallback, /profile add new-profile --prompt/);
  assert.match(panel.fallback, /ADDRESS_FILE/);
  assert.equal(host.overlays.length, 0); panel.close();
});
test("only own machine can open/remove; force deletion needs confirmation", async () => {
  reset(); const panel = await view();
  panel.open("home", "ssh-box", context()); panel.confirm({ type: "unregister", id: "home", name: "ssh-box" }, context());
  assert.equal(host.runs.length, 0); assert.equal(host.dialog, null);
  assert.match(panel.error.message, /does not belong/);
  panel.open("home", "box", context());
  assert.deepEqual(host.runs[0], { name: "space.new", args: { machine: "box" } });
  host.spaces.push({ id: "box/s1", machine: "box" }); await tick();
  const before = host.calls.length;
  panel.confirm({ type: "rm", id: "home", force: true }, context());
  assert.equal(host.calls.length, before); assert.ok(find(panel.render(context()), "tc-confirm-yes"));
  panel.confirmPending(context()); await drain();
  assert.ok(host.calls.find((c) => c.args.includes("rm") && c.args.includes("--force")));
  panel.close();
});
test("shown command loop tests/registers through JSON without unrelated machine operations", async () => {
  reset(); const panel = await view();
  host.commands.shift()("test:home"); await drain();
  assert.deepEqual(panel.tests.home.server.exports, ["work"]);
  host.commands.shift()("register:home:work:new-box"); await drain();
  assert.ok(host.calls.find((c) => c.args.join(" ") === "profile register home --name new-box --session work --json"));
  assert.ok(!JSON.stringify(host.calls).includes("ssh-box")); panel.close();
});
test("one outstanding call; timeout UI does not release serialization gate", async () => {
  reset(); let release;
  host.hold = new Promise((resolve) => { release = resolve; });
  const panel = new Tailcat(); panel.init({}, context());
  panel.refresh(context()); host.timers.find((t) => t.ms === 5000).fn(context());
  assert.equal(host.calls.length, 1);
  host.timers.find((t) => t.ms === 30000).fn(context());
  assert.ok(panel.busy && panel.error.message.includes("30-second"));
  panel.refresh(context()); assert.equal(host.calls.length, 1);
  release(); await drain(); assert.equal(panel.busy, ""); panel.close();
  assert.ok(host.timers.find((t) => t.ms === 5000).cancelled);
});
test("explicit reveal lives only in closure, clears at 30s and on close", async () => {
  for (const mode of ["timer", "close"]) {
    reset(); host.props = { part: "address", binary: "herdr-tailcat", overlay: "reveal" };
    const panel = await view();
    assert.equal(panel.revealValue(), host.reveal);
    assert.ok(!JSON.stringify(panel).includes(host.reveal));
    if (mode === "timer") host.timers.filter((t) => t.ms === 30000 && !t.cancelled).at(-1).fn(context());
    else panel.close();
    assert.equal(panel.revealValue(), ""); panel.close();
  }
});

async function tick() {
  const timer = host.timers.find((t) => t.ms === 250 && !t.cancelled);
  assert.ok(timer, 'waiting operation has an App timer'); timer.fn(context()); await drain();
}
const renderedText = (node) => typeof node === 'string' ? node : (node?.nodes ?? []).map(renderedText).join(' ');
test('busy commands retain FIFO order and polling cannot steal their turn', async () => {
  reset(); const panel = await view(); let release;
  host.hold = new Promise((resolve) => { release = resolve; });
  panel.refresh(context());
  host.commands.shift()('test:home'); await drain();
  host.commands.shift()('pubkey:home'); await drain();
  assert.equal(panel.queue.length, 2);
  const before = host.calls.length;
  host.timers.find((t) => t.ms === 5000).fn(context());
  assert.equal(host.calls.length, before);
  release(); await drain();
  assert.deepEqual(host.calls.slice(-2).map((c) => c.args[1]), ['test', 'pubkey']);
  assert.equal(panel.queue.length, 0); assert.equal(panel.busy, ''); panel.close();
});
test('unregister before initial profile load becomes a visible confirmation', async () => {
  reset(); let release;
  host.hold = new Promise((resolve) => { release = resolve; });
  const panel = new Tailcat(); panel.init({}, context());
  host.commands.shift()('unregister:home:box'); await drain();
  assert.equal(panel.profilesLoaded, false); assert.equal(panel.queue.length, 1); assert.equal(host.dialog, null);
  release(); await drain();
  assert.equal(panel.pending.name, 'box'); assert.ok(find(panel.render(context()), "tc-confirm-yes"));
  assert.match(renderedText(panel.render(context())), /Remove machine box\?/);
  host.commands.shift()('confirm'); await drain();
  assert.equal(panel.pending, null); assert.equal(host.dialog, null);
  assert.ok(host.calls.some((c) => c.args.join(' ') === 'profile unregister home --name box --json'));
  panel.close();
});
test('pending confirmation gates FIFO work; confirm and cancel resolve the gate', async () => {
  for (const resolve of ['confirm', 'cancel']) {
    reset(); const panel = await view();
    panel.enqueue('unregister:home:box', context());
    panel.enqueue('test:home', context());
    assert.equal(panel.queue.length, 1); assert.equal(panel.tests.home, undefined);
    const before = host.calls.length;
    host.timers.find((t) => t.ms === 5000).fn(context()); assert.equal(host.calls.length, before);
    panel.enqueue(resolve, context()); await drain();
    assert.equal(panel.queue.length, 0); assert.equal(panel.pending, null); assert.ok(panel.tests.home);
    assert.equal(host.calls.some((c) => c.args[1] === 'unregister'), resolve === 'confirm');
    panel.close();
  }
});
test('invalid commands and foreign targets show errors without acting', async () => {
  reset(); const panel = await view();
  for (const command of ['unknown', '__proto__', 'constructor', 'test', 'test:home:extra', 'test:../home', 'page:nope', 'rm:home:yes', 'confirm', 'test:foreign', 'unregister:home:ssh-box', 'open:home:ssh-box', null]) {
    const before = host.calls.length;
    panel.enqueue(command, context()); await drain();
    assert.ok(panel.error?.message, String(command));
    assert.ok(renderedText(panel.render(context())).includes(panel.error.message));
    assert.equal(host.calls.length, before); assert.equal(host.runs.length, 0); assert.equal(host.dialog, null);
  }
  panel.close();
});
test('queue cap rejects overflow visibly; close discards queued work', async () => {
  reset(); const panel = await view(); let release;
  host.hold = new Promise((resolve) => { release = resolve; }); panel.refresh(context());
  for (let i = 0; i < 16; i++) panel.enqueue('test:home', context());
  panel.enqueue('pubkey:home', context());
  assert.equal(panel.queue.length, 16); assert.match(panel.error.message, /queue is full/);
  panel.close(); const count = host.calls.length; release(); await drain();
  assert.equal(panel.queue.length, 0); assert.equal(host.calls.length, count);
});
test('open waits for layout ready and then for observable space, keeping overlay open', async () => {
  reset(); const panel = await view(); host.live[1].layout = 'loading';
  panel.enqueue('open:home:box', context()); await drain();
  assert.ok(panel.busy); assert.equal(host.runs.length, 0);
  assert.match(renderedText(panel.render(context())), /App layout: loading/);
  await tick(); assert.equal(host.runs.length, 0);
  host.live[1].layout = 'ready'; await tick();
  assert.deepEqual(host.runs, [{ name: 'space.new', args: { machine: 'box' } }]);
  assert.ok(panel.busy); assert.equal(panel.success, '');
  host.spaces.push({ id: 'foreign/s1', machine: 'ssh-box' }); await tick(); assert.ok(panel.busy);
  host.spaces.push({ id: 'box/s1', machine: 'box' }); await tick();
  assert.equal(panel.busy, ''); assert.equal(panel.success, 'Remote space opened: box');
  assert.match(renderedText(panel.render(context())), /Remote space opened: box/);
  assert.equal(host.closedOverlay, undefined); panel.close();
});
test('open selects existing owned space and never creates another', async () => {
  reset(); const panel = await view(); host.spaces.push({ id: 'box/s1', machine: 'box' });
  panel.enqueue('open:home:box', context()); await drain();
  assert.deepEqual(host.runs, [{ name: 'space.select', args: { id: 'box/s1' } }]);
  assert.equal(panel.success, 'Remote space opened: box'); panel.close();
});
test('unavailable layout gives visible state and no structural command', async () => {
  reset(); const panel = await view(); host.live[1].layout = 'unavailable';
  panel.enqueue('open:home:box', context()); await drain();
  assert.equal(panel.busy, ''); assert.match(panel.error.message, /layout: unavailable/);
  assert.ok(renderedText(panel.render(context())).includes(panel.error.message));
  assert.equal(host.runs.length, 0); panel.close();
});
test('layout and space waits have 20s/10s limits with layout in error', async () => {
  for (const mode of ['layout', 'space']) {
    reset(); const panel = await view();
    if (mode === 'layout') host.live[1].layout = 'loading';
    panel.enqueue('open:home:box', context()); await drain();
    for (let i = 0; i < (mode === 'layout' ? 80 : 40); i++) await tick();
    assert.equal(panel.busy, ''); assert.match(panel.error.message, mode === 'layout' ? /layout: loading/ : /layout: ready/);
    assert.equal(host.runs.length, mode === 'layout' ? 0 : 1); panel.close();
  }
});
test('closing during layout/space wait cancels timers and never dispatches later', async () => {
  for (const mode of ['layout', 'space']) {
    reset(); const panel = await view();
    if (mode === 'layout') host.live[1].layout = 'loading';
    panel.enqueue('open:home:box', context()); await drain();
    const count = host.runs.length; panel.close(); await drain();
    host.live[1].layout = 'ready';
    assert.equal(host.timers.filter((t) => t.ms === 250 && !t.cancelled).length, 0);
    assert.equal(host.runs.length, count); assert.equal(panel.success, '');
  }
});

function find(node, id) { if (node?.attrs?.id?.[0] === id) return node; for (const child of node?.nodes ?? []) { const found = find(child, id); if (found) return found; } }
function click(panel, id) { const node = find(panel.render(context()), id); assert.ok(node?.handlers.on_click, id + " is enabled"); node.handlers.on_click({}, context()); }
function key(panel, key) { panel.render(context()).handlers.on_key_down({ key }, context()); }
test('inline remove card offers Cancel and Confirm; all other operations are disabled', async () => {
  reset(); const panel = await view(); const before = host.calls.length;
  click(panel, 'tc-unregister-box');
  let tree = panel.render(context()); assert.ok(host.focused);
  assert.ok(find(tree, 'tc-confirm-yes')); assert.ok(find(tree, 'tc-confirm-no'));
  for (const [id, label] of [['tc-confirm-yes', 'Confirm'], ['tc-confirm-no', 'Cancel'], ['tc-test', 'Test']]) {
    assert.deepEqual(find(tree,id).attrs.role, ['button']);
    assert.deepEqual(find(tree,id).attrs.accessibility_label, [label]);
  }
  for (const id of ['tc-refresh', 'tc-test', 'tc-add', 'tc-open-box', 'tc-profile-home', 'tc-server-tab']) assert.equal(find(tree,id).handlers.on_click, undefined, id);
  click(panel, 'tc-confirm-no'); assert.equal(panel.pending, null); assert.equal(host.calls.length, before);
  assert.equal(panel.profiles[0].machines.length, 1);
  click(panel, 'tc-unregister-box'); click(panel, 'tc-confirm-yes'); await drain();
  assert.deepEqual(host.calls.slice(-2).map(c=>c.args[1]), ['unregister', 'ls']);
  assert.equal(panel.profiles[0].machines.length, 0); assert.equal(find(panel.render(context()),'tc-confirm-yes'), undefined); panel.close();
});
test('Enter confirms, Escape cancels only inline confirmation, and force deletion explains removal', async () => {
  reset(); const panel = await view();
  click(panel, 'tc-delete-profile'); assert.match(renderedText(panel.render(context())), /also removes its registered machines/);
  key(panel, 'escape'); assert.equal(panel.pending, null); assert.equal(host.closedOverlay, undefined);
  assert.ok(!host.calls.some(c=>c.args[1]==='rm'));
  click(panel, 'tc-delete-profile'); key(panel, 'enter'); await drain();
  assert.ok(host.calls.some(c=>c.args.includes('--force'))); assert.equal(panel.profiles.length,0); panel.close();
});
test('quiet polling keeps controls enabled, unchanged data does not notify, and operations wait', async () => {
  reset(); const panel = await view(); let release;
  host.hold = new Promise(r=>{release=r;}); const before=host.notifications;
  host.timers.find(t=>t.ms===5000).fn(context());
  assert.equal(panel.refreshing,true); assert.equal(panel.busy,'');
  assert.ok(!renderedText(panel.render(context())).includes('Refreshing…'));
  assert.ok(find(panel.render(context()),'tc-test').handlers.on_click);
  const calls=host.calls.length; host.timers.find(t=>t.ms===5000).fn(context()); assert.equal(host.calls.length,calls);
  release(); await drain(); assert.equal(host.notifications,before);
  host.hold = new Promise(r=>{release=r;}); panel.refresh(context(),true);
  click(panel,'tc-test'); assert.equal(panel.queue.length,1); assert.equal(panel.busy,'');
  release(); await drain(); assert.equal(panel.queue.length,0); assert.ok(panel.tests.home); panel.close();
});
test('quiet failure preserves feedback in fixed height area; manual refresh remains busy', async () => {
  reset(); const panel=await view(); const error={message:'Existing error',hint:'Try later'};
  panel.error=error; panel.success='Previous success'; host.failExec=true;
  panel.refresh(context(),true); await drain();
  assert.equal(panel.error,error); assert.equal(panel.success,'Previous success'); assert.equal(panel.refreshFailed,true);
  let tree=panel.render(context()); assert.match(renderedText(tree),/Last refresh failed/); assert.equal(find(tree,'tc-feedback').attrs.h[0],24);
  host.failExec=false; let release; host.hold=new Promise(r=>{release=r;}); click(panel,'tc-refresh');
  assert.equal(panel.busy,'Refreshing…'); assert.equal(find(panel.render(context()),'tc-feedback').attrs.h[0],24);
  release(); await drain(); assert.equal(panel.refreshFailed,false); panel.close();
});
