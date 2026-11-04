// Only projections of the documented JSON contract enter the panel's state.
export function validName(s) {
  return typeof s === "string" && /^[a-z0-9][a-z0-9_.-]{0,31}$/.test(s) && !/^t[1-9][0-9bcdfghjkmnpqrstvwxyz]*$/.test(s);
}
export function safeText(s) {
  return typeof s === "string" ? s.replace(/tc[A-Za-z0-9_-]{16,}|(?:psk:|privk[e]y:)[a-fA-F0-9]+/g, "[redacted]").replace(/[\x00-\x1f\x7f]/g, " ").slice(0, 512) : "";
}
const text = (s) => { if (typeof s !== "string") throw new Error("Invalid CLI response."); return safeText(s); };
const flag = (v) => { if (typeof v !== "boolean") throw new Error("Invalid CLI response."); return v; };
const number = (v) => { if (typeof v !== "number" || !Number.isFinite(v) || v < 0) throw new Error("Invalid CLI response."); return v; };
const array = (v, fn) => { if (!Array.isArray(v)) throw new Error("Invalid CLI response."); return v.map(fn); };
const name = (v) => { if (!validName(v)) throw new Error("Invalid CLI response."); return v; };
const pubkey = (v) => { if (typeof v !== "string" || !/^nodekey:[a-f0-9]{64}$/.test(v)) throw new Error("Invalid CLI response."); return v; };
const fp = (v) => { if (!/^[a-f0-9]{8}$/.test(v)) throw new Error("Invalid CLI response."); return v; };
const service = (v) => ({ loaded: flag(v.loaded), pid: number(v.pid), label: text(v.label) });
const machine = (v) => ({ name: name(v.name), id: text(v.id), host: text(v.host), version: text(v.version), session: v.session === null ? null : name(v.session), state: text(v.state), why: text(v.why) });
export class CLIError extends Error {
  constructor(kind, message, hint = "") { super(safeText(message)); this.kind = safeText(kind); this.hint = safeText(hint); }
}
export function parseResult(args, r) {
  let v;
  try { v = JSON.parse(r.stdout); } catch { throw new CLIError("protocol", "The CLI returned invalid JSON."); }
  if (!v || typeof v !== "object" || Array.isArray(v)) throw new CLIError("protocol", "Invalid CLI response.");
  if (v.ok === false && v.error && typeof v.error.message === "string" && typeof v.error.hint === "string" && typeof v.error.kind === "string") {
    throw new CLIError(v.error.kind, v.error.message, v.error.hint);
  }
  if (r.code !== 0 || v.ok !== true) throw new CLIError("protocol", "Invalid CLI response.");
  const cmd = args.slice(0, 2).join(" ");
  switch (cmd) {
    case "profile ls": return { profiles: array(v.profiles, (p) => ({ id: name(p.id), ...(typeof p.profile_identity === "string" && /^[a-f0-9]{64}$/.test(p.profile_identity) ? { profile_identity: p.profile_identity } : {}), client_fingerprint: fp(p.client_fingerprint), server_fingerprint: fp(p.server_fingerprint), daemon_running: flag(p.daemon_running), machines: array(p.machines, machine) })) };
    case "profile pubkey": return { public_key: pubkey(v.public_key), fingerprint: fp(v.fingerprint) };
    case "profile test": return { rtt_ms: number(v.rtt_ms), server: { version: text(v.server.version), exports: array(v.server.exports, name) } };
    case "profile register": return { machine: machine(v.machine) };
    case "server status": return { initialized: flag(v.initialized), running: flag(v.running), pid: number(v.pid), started_at: text(v.started_at), version: text(v.version), exports: array(v.exports, name), allowed_clients: number(v.allowed_clients), streams: number(v.streams), restart_required: flag(v.restart_required), service: v.service ? service(v.service) : null };
    case "server service": return service(v);
    case "server allow":
      if (args[2] === "ls") return { clients: array(v.clients, (c) => ({ public_key: pubkey(c.public_key), fingerprint: fp(c.fingerprint) })) };
      return { warning: v.warning ? text(v.warning) : "" };
    case "server export": return args[2] === "ls" ? { exports: array(v.exports, name) } : {};
    case "server install-service": return v.text !== undefined ? { text: text(v.text), os: text(v.os) } : { path: text(v.path), os: text(v.os) };
    case "server init": case "profile add": case "profile rm": case "profile unregister": return {};
    default: throw new CLIError("protocol", "Unsupported panel command.");
  }
}
// The explicit show-address result bypasses normal state projections. Only the
// short-lived reveal overlay may call this; never save its response or props.
export function revealResult(r) {
  let v;
  try { v = JSON.parse(r.stdout); } catch { throw new CLIError("protocol", "Invalid CLI response."); }
  if (v?.ok === false) return parseResult(["server", "show-address"], r);
  if (r.code !== 0 || typeof v?.address !== "string" || !/^tc[A-Za-z0-9_-]{50,}$/.test(v.address)) throw new CLIError("protocol", "Invalid address response.");
  return v.address;
}
export function ownMachines(profiles, live) {
  return profiles.flatMap((p) => p.machines.map((m) => ({ ...m, profile: p.id, layout: live.find((l) => l.name === m.name)?.layout ?? "unavailable" })));
}
export function machineStatus(m) {
  if (m.state === "connected") return { text: "Connected", tone: "done", detail: m.why };
  if (["connecting", "reconnecting"].includes(m.state)) return { text: "Connecting", tone: "working", detail: m.why };
  if (m.why) return { text: "Disconnected", tone: "blocked", detail: m.why };
  return { text: "Offline", tone: "idle", detail: "" };
}
export const SERVER_STEPS = [
  "On the other computer, install herdr-core and herdr-tailcat.",
  "Run herdr-tailcat server init, then server export add <session>.",
  "Import its address through the hidden-input terminal on this computer.",
  "On the server, run server allow add <client-public-key>; run serve or install/start its service.",
  "Test this profile, choose an exported session, then register a machine.",
];
