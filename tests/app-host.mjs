export const host = {};
export function reset() {
  delete host.closedOverlay;
  Object.assign(host, { calls: [], overlays: [], runs: [], timers: [], commands: [], props: {}, profiles: [{ id: "home", client_fingerprint: "aaaaaaaa", server_fingerprint: "bbbbbbbb", daemon_running: true, machines: [{ name: "box", id: "core-id", host: "demo", version: "v1", session: "work", state: "connected", why: "" }] }], live: [{ name: null, layout: "ready" }, { name: "box", layout: "ready" }, { name: "ssh-box", layout: "ready" }], spaces: [], dialog: null, failHC: false, hold: null, failExec: false, notifications: 0, focused: false, reveal: "tc" + "X9_".repeat(70) });
  globalThis.window = { open_dialog: () => { throw Error("Native dialog forbidden in overlay"); }, close_dialog: () => { throw Error("Native dialog forbidden in overlay"); } };
}
export class View {}
// Keep a stable proxy for chained builders.
function el() {
  const target = { nodes: [], handlers: {}, attrs: {} };
  const proxy = new Proxy(target, { get(t, method) {
    if (method in t) return t[method];
    if (method === "then") return undefined;
    return (...args) => {
      if (method === "child") t.nodes.push(args[0]);
      else if (method === "children") t.nodes.push(...args[0]);
      else if (method === "when" && args[0]) args[1](proxy);
      else if (String(method).startsWith("on_")) t.handlers[method] = args[0];
      else t.attrs[method] = args;
      return proxy;
    };
  } });
  return proxy;
}
export const div = el, v_flex = el, h_flex = el;
export const InputState = { new: (options = {}) => ({ text: options.value ?? "", value() { return this.text; }, set_value(v) { this.text = v; }, on() {}, release() {} }) };
export const Input = { new: el };
export function context() {
  const cx = { notify() { host.notifications++; }, stop_propagation() {}, focus_handle: () => ({ focus() { host.focused = true; } }), write_to_clipboard: (v) => { host.clipboard = v; }, spawn: (fn) => { const promise = fn(context()); return { cancel() {}, promise }; } };
  cx.timer = { after: (ms, fn) => timer(ms, fn), every: (ms, fn) => timer(ms, fn) };
  return cx;
}
function timer(ms, fn) { const t = { ms, fn, cancelled: false, cancel() { this.cancelled = true; } }; host.timers.push(t); return t; }
const result = (v, code = 0) => ({ code, stdout: JSON.stringify(v), stderr: "" });
export async function exec(command, args) {
  host.calls.push({ type: "exec", command, args });
  if (host.hold) { const hold = host.hold; host.hold = null; await hold; }
  if (host.failExec) throw Error("refresh failed");
  if (command === "/usr/bin/which") return { code: 0, stdout: "/demo/bin/herdr-tailcat\n", stderr: "" };
  const [group, action, id] = args;
  if (group === "server" && action === "show-address") return result({ address: host.reveal });
  if (group === "profile" && action === "ls") return result({ ok: true, profiles: host.profiles });
  if (group === "profile" && action === "test") return result({ ok: true, rtt_ms: 12, server: { version: "v1", exports: ["work"] } });
  if (group === "profile" && action === "pubkey") return result({ ok: true, public_key: "nodekey:" + "a".repeat(64), fingerprint: "aaaaaaaa" });
  if (group === "profile" && action === "register") return result({ ok: true, machine: { ...host.profiles[0].machines[0], name: args[args.indexOf("--name") + 1] } });
  if (group === "profile" && action === "unregister") host.profiles.find((p) => p.id === id).machines = [];
  if (group === "profile" && action === "rm") host.profiles = host.profiles.filter((p) => p.id !== id);
  return result({ ok: true });
}
export async function hc(args) { host.calls.push({ type: "hc", args }); if (host.failHC) throw Error("spawn failed"); return result({ address: "t1" }); }
export const home = () => "/demo";
export const machines = () => host.live;
export const spaces = () => host.spaces;
export const terminals = () => [];
export const props = () => host.props;
export const nextCommand = () => new Promise((resolve) => { host.commands.push(resolve); });
export const openOverlay = (spec) => { host.overlays.push(spec); return "overlay-" + host.overlays.length; };
export const closeOverlay = (id) => { host.closedOverlay = id; };
export const run = (name, args) => { host.runs.push({ name, args }); };
export const window = () => ({ active: true, platform: "macos" });
export const theme = () => new Proxy({}, { get: (_, k) => String(k) });
reset();
