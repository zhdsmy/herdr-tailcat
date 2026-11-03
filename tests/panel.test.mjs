import test from "node:test";
import assert from "node:assert/strict";
import { validName, parseResult, revealResult, ownMachines, machineStatus, safeText } from "../mods/tailcat/model.js";
import { Client } from "../mods/tailcat/client.js";
const publicKey = "nodekey:" + "a".repeat(64);
const machine = { name: "box", id: "id", host: "host", version: "0.1.16", session: "work", state: "connected", why: "" };
const profile = { id: "home", client_fingerprint: "aaaaaaaa", server_fingerprint: "bbbbbbbb", daemon_running: true, machines: [machine] };
const service = { loaded: false, pid: 0, label: "dev.herdr.tailcat" };
const cases = [
  [["server", "init"], {}],
  [["server", "allow", "add", publicKey], {}],
  [["server", "allow", "rm", publicKey], { warning: "last client removed" }],
  [["server", "allow", "ls"], { clients: [{ public_key: publicKey, fingerprint: "aaaaaaaa" }] }],
  [["server", "export", "add", "work"], {}],
  [["server", "export", "rm", "work"], {}],
  [["server", "export", "ls"], { exports: ["work"] }],
  [["server", "status"], { initialized: true, running: false, pid: 0, started_at: "", version: "v1", exports: ["work"], allowed_clients: 1, streams: 0, restart_required: true, service }],
  [["server", "install-service"], { path: "/tmp/service.plist", os: "darwin" }],
  [["server", "install-service", "--dry-run"], { text: "unit text", os: "linux" }],
  ...["start", "stop", "status"].map((action) => [["server", "service", action], service]),
  [["profile", "add", "home"], {}],
  [["profile", "pubkey", "home"], { public_key: publicKey, fingerprint: "aaaaaaaa" }],
  [["profile", "ls"], { profiles: [profile] }],
  [["profile", "test", "home"], { rtt_ms: 123.4, server: { version: "v1", exports: ["work"] } }],
  [["profile", "register", "home"], { machine }],
  [["profile", "unregister", "home"], {}],
  [["profile", "rm", "home"], {}],
];
for (const [args, value] of cases) {
  test(args.join(" ") + " success/failure projections", () => {
    const parsed = parseResult(args, { code: 0, stdout: JSON.stringify({ ok: true, ...value, target: "must-not-enter-state", address: "must-not-enter-state", config: { secret: true } }) });
    assert.deepEqual(parsed, args.slice(0, 3).join(" ") === "server allow add" ? { warning: "" } : value);
    assert.ok(!JSON.stringify(parsed).includes("must-not-enter-state"));
    assert.ok(!JSON.stringify(parsed).includes('"config"'));
    const error = { kind: "no_answer", message: "server did not answer", hint: "check its whitelist" };
    assert.throws(() => parseResult(args, { code: 1, stdout: JSON.stringify({ ok: false, error }) }), (e) => e.kind === error.kind && e.message === error.message && e.hint === error.hint);
  });
}
test("explicit reveal is isolated from normal projections", () => {
  const secret = "tc" + "Z9_".repeat(70);
  assert.equal(revealResult({ code: 0, stdout: JSON.stringify({ address: secret }) }), secret);
  assert.throws(() => parseResult(["server", "show-address"], { code: 0, stdout: JSON.stringify({ address: secret }) }));
  assert.throws(() => revealResult({ code: 1, stdout: JSON.stringify({ ok: false, error: { kind: "io_error", message: "not initialized", hint: "" } }) }), /not initialized/);
});
test("reject malformed success and errors without echoing raw JSON", () => {
  for (const r of [{ code: 0, stdout: "broken-secret" }, { code: 1, stdout: "{}" }, { code: 0, stdout: "[]" }, { code: 0, stdout: '{"ok":true,"profiles":{}}' }]) {
    assert.throws(() => parseResult(["profile", "ls"], r), (e) => !e.message.includes("broken-secret"));
  }
});
test("only own registrations are joined with App live layouts", () => {
  const live = [{ name: "box", layout: "ready" }, { name: "ssh-box", layout: "ready" }, { name: null, layout: "ready" }];
  const owned = ownMachines([profile], live);
  assert.deepEqual(owned.map((m) => m.name), ["box"]);
  assert.equal(owned[0].profile, "home");
  assert.equal(owned[0].layout, "ready");
  assert.equal(ownMachines([profile], [])[0].layout, "unavailable");
});
test("connection status and why", () => {
  for (const [state, why, tone] of [["connected", "", "done"], ["connecting", "", "working"], ["reconnecting", "lost", "working"], ["disconnected", "denied", "blocked"], ["disconnected", "", "idle"]]) assert.equal(machineStatus({ ...machine, state, why }).tone, tone);
});
test("state/errors cannot retain encoded address or secret tokens", () => {
  const secret = "tc" + "Z9_".repeat(70);
  assert.ok(!safeText("bad " + secret + " priv" + "key:" + "abcdef psk:" + "123456").includes(secret));
  assert.throws(() => parseResult(["profile", "test", "home"], { code: 1, stdout: JSON.stringify({ ok: false, error: { kind: "no_answer", message: secret, hint: secret } }) }), (e) => !e.message.includes(secret) && !e.hint.includes(secret));
  const v = parseResult(["profile", "ls"], { code: 0, stdout: JSON.stringify({ ok: true, profiles: [{ ...profile, address: secret, machines: [{ ...machine, why: secret, target: secret }] }] }) });
  assert.ok(!JSON.stringify(v).includes(secret));
});
test("profile id validation agrees with core naming edge cases", () => {
  for (const v of ["", "a;b", "WORK", "t7", "t1b", "t9zzzzzzzzzz", "../work", "a".repeat(33)]) assert.equal(validName(v), false, v);
  for (const v of ["work", "tc-work", "t0", "t3po", "t1l", "a.b_1"]) assert.equal(validName(v), true, v);
});
test("exec PATH lookup, start fallback, no retries after nonzero exit", async () => {
  const calls = [];
  const client = new Client(async (cmd, args) => { calls.push([cmd, args]); if (cmd === "herdr-tailcat") throw Error("spawn"); return { code: 0, stdout: '{"ok":true,"profiles":[]}' }; }, "/demo");
  await client.call(["profile", "ls"]);
  assert.equal(calls[0][0], "herdr-tailcat");
  assert.equal(calls[1][0], "/demo/.local/bin/herdr-tailcat");
  assert.deepEqual(calls[0][1], ["profile", "ls", "--json"]);
  let count = 0;
  const mutation = new Client(async () => { count++; return { code: 1, stdout: '{"ok":false,"error":{"kind":"core:name_taken","message":"taken","hint":""}}' }; }, "/demo");
  await assert.rejects(mutation.call(["profile", "register", "home"]));
  assert.equal(count, 1);
});
