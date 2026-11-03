import { View, div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input } from "gpui-base";
import { exec, hc, home, machines, spaces, run, theme, props, nextCommand, openOverlay, closeOverlay, window as appWindow } from "herdr";
import { Client } from "./client.js";
import { CLIError, validName, ownMachines, machineStatus, SERVER_STEPS, revealResult } from "./model.js";

const note = (text, color) => div().text_size(12).text_color(color).child(text);
const button = (id, text, action, disabled = false, danger = false) => {
  const t = theme();
  const b = div().id(id).role("button").accessibility_label(text).px(10).py(6).rounded(6).border(1).border_color(t.border).text_size(12).text_color(disabled ? t.dim : danger ? t.blocked : t.foreground).child(text);
  return disabled ? b : b.cursor_pointer().hover((s) => s.bg(t.hover)).on_click((_e, cx) => { cx.stop_propagation(); action(cx); });
};
const card = () => v_flex().gap(8).p(12).rounded(8).border(1).border_color(theme().border).bg(theme().background);

export default class Tailcat extends View {
  init(_props, cx) {
    this.closed = false;
    this.client = new Client(exec, home());
    this.part = props()?.part;
    this.error = null;
    this.busy = "";
    this.refreshing = false;
    this.refreshFailed = false;
    this.profiles = [];
    this.profilesLoaded = false;
    this.queue = [];
    this.waiters = new Set();
    this.success = "";
    // Profile ids must never resolve to inherited object properties.
    this.tests = new Map();
    this.keys = new Map();
    this.clients = [];
    this.page = "connect";
    this.selected = "";
    this.session = "";
    this.server = null;
    this.pending = null;
    this.revealOverlay = null;
    this.handle = cx.focus_handle();
    if (this.part === "bar") return;
    if (this.part === "address") { this.initReveal(cx); return; }
    this.idInput = InputState.new({ placeholder: "Profile id, e.g. home" });
    this.nameInput = InputState.new({ placeholder: "Machine name, e.g. home-tailcat" });
    this.sessionInput = InputState.new({ placeholder: "Session to export" });
    this.keyInput = InputState.new({ placeholder: "Client public key (nodekey:…)" });
    this.refresh(cx);
    // This mod opens only as a dialog. Closed dialogs lose their timers; do not
    // poll when the app is in the background or while an operation is pending.
    this.poll = cx.timer.every(5000, (cx) => { if (!this.closed && appWindow()?.active && !this.busy && !this.refreshing && !this.pending && !this.queue.length) this.refresh(cx, true); });
    cx.spawn(async (cx) => {
      for (;;) {
        const command = await nextCommand();
        if (this.closed) return;
        this.enqueue(command, cx);
      }
    });
  }

  localReady() { return machines().some((m) => m.name === null && m.layout === "ready"); }
  reject(message, cx, hint = "Refresh and check the selected profile and machine.") {
    this.error = { message, hint };
    cx.notify();
  }
  requireProfile(id, cx) {
    if (!this.profile(id)) { this.reject("This profile is not in the loaded profile list.", cx); return false; }
    if (!this.localReady()) { this.reject("App's local core layout is not ready.", cx); return false; }
    return true;
  }
  gateCommand(command) { return command === "confirm" || command === "cancel"; }
  enqueue(command, cx) {
    if (this.closed) return;
    // Confirm and cancel are not subject to the 16-command cap. They still queue
    // behind a refresh or busy operation; drainQueue pulls them ahead of ordinary work.
    if (!this.gateCommand(command) && this.queue.filter((c) => !this.gateCommand(c)).length >= 16) {
      this.reject("The command queue is full (16). This command was rejected.", cx);
      return;
    }
    this.queue.push(command);
    this.drainQueue(cx);
    cx.notify();
  }
  drainQueue(cx) {
    if (this.draining || this.closed || this.busy || this.refreshing || !this.profilesLoaded) return;
    this.draining = true;
    try {
      while (this.queue.length && !this.closed && !this.busy && !this.refreshing) {
        // A confirmation resolves the gate even if ordinary commands arrived
        // ahead of it. All other commands retain their FIFO order.
        const index = this.pending ? this.queue.findIndex((c) => this.gateCommand(c)) : 0;
        if (index < 0) break;
        const [command] = this.queue.splice(index, 1);
        if (typeof command === "function") command(cx);
        else this.command(command, cx);
      }
    } finally { this.draining = false; }
  }
  perform(label, action, cx, clearFeedback = true) {
    if (this.closed) return;
    if (this.refreshing) { this.enqueue((cx) => this.perform(label, action, cx, clearFeedback), cx); return; }
    if (this.busy) { this.reject("Another operation is still running.", cx, "Wait for it to finish, then try again."); return; }
    this.busy = label;
    if (clearFeedback) { this.error = null; this.success = ""; }
    this.late = false;
    cx.notify();
    const deadline = cx.timer.after(30000, (cx) => {
      if (!this.busy || this.closed) return;
      this.late = true;
      this.error = { message: "The command reached App's 30-second limit.", hint: "Wait for it to finish before trying again." };
      cx.notify();
    });
    cx.spawn(async (cx) => {
      try { await action(cx); }
      catch (e) {
        if (!this.closed) this.error = e instanceof CLIError ? { message: e.message, hint: e.hint } : { message: "The operation could not finish.", hint: "Refresh and try again." };
      } finally {
        deadline.cancel();
        if (!this.closed) { this.busy = ""; this.drainQueue(cx); cx.notify(); }
      }
    });
  }
  async loadProfiles() {
    if (!this.localReady()) return;
    this.profiles = (await this.client.call(["profile", "ls"])).profiles;
    this.profilesLoaded = true;
    if (!this.profiles.some((p) => p.id === this.selected)) this.selected = this.profiles[0]?.id ?? "";
    if (this.importing && this.profiles.some((p) => p.id === this.importing)) {
      const id = this.importing;
      this.importing = "";
      this.keys.set(id, await this.client.call(["profile", "pubkey", id]));
      this.selected = id;
    }
  }
  async loadServer() {
    this.server = await this.client.call(["server", "status"]);
    this.clients = this.server.initialized ? (await this.client.call(["server", "allow", "ls"])).clients : [];
  }
  refresh(cx, quiet = false) {
    if (this.busy || this.closed) return;
    if (quiet) {
      if (this.refreshing || this.pending || this.queue.length) return;
      this.refreshing = true;
      const before = JSON.stringify([this.profiles, this.server, this.clients, [...this.keys], this.refreshFailed]);
      cx.spawn(async (cx) => {
        try {
          if (this.page === "server") await this.loadServer();
          else await this.loadProfiles();
          this.refreshFailed = false;
        } catch { this.refreshFailed = true; }
        finally {
          this.refreshing = false;
          if (!this.closed) {
            if (before !== JSON.stringify([this.profiles, this.server, this.clients, [...this.keys], this.refreshFailed])) cx.notify();
            this.drainQueue(cx);
          }
        }
      });
      return;
    }
    this.perform("Refreshing…", async () => {
      if (this.page === "server") await this.loadServer();
      else await this.loadProfiles();
      this.refreshFailed = false;
    }, cx, false);
  }
  profile(id) { return this.profiles.find((p) => p.id === id); }
  machine(id, name) { return this.profile(id)?.machines.find((m) => m.name === name); }
  select(id, cx) {
    if (!this.profile(id)) return;
    this.selected = id;
    this.session = this.tests.get(id)?.server.exports[0] ?? "";
    this.nameInput.set_value((id + "-tailcat").slice(0, 32));
    cx.notify();
  }
  test(id, cx) {
    if (!this.requireProfile(id, cx)) return;
    this.perform("Testing " + id + "…", async () => {
      this.tests.set(id, await this.client.call(["profile", "test", id]));
      if (this.selected === id) this.session = this.tests.get(id)?.server.exports[0] ?? "";
    }, cx);
  }
  pubkey(id, cx) {
    if (!this.requireProfile(id, cx)) return;
    this.perform("Loading public key…", async () => { this.keys.set(id, await this.client.call(["profile", "pubkey", id])); }, cx);
  }
  register(id, name, session, cx) {
    if (!this.requireProfile(id, cx)) return;
    if (!validName(name) || !this.tests.get(id)?.server.exports.includes(session)) {
      this.error = { message: "Enter a valid machine name and choose a session from a successful test.", hint: "Use 1–32 lowercase letters, digits, _, . or -; terminal ids such as t7 are reserved." }; cx.notify(); return;
    }
    this.perform("Registering " + name + "…", async () => {
      await this.client.call(["profile", "register", id, "--name", name, "--session", session]);
      await this.loadProfiles();
    }, cx);
  }
  layout(name) { return machines().find((m) => m.name === name)?.layout ?? "missing"; }
  wait(cx) {
    return new Promise((resolve) => {
      const waiter = { finish: () => { waiter.timer.cancel(); this.waiters.delete(waiter); resolve(); } };
      waiter.timer = cx.timer.after(250, waiter.finish);
      this.waiters.add(waiter);
    });
  }
  open(id, name, cx) {
    if (!this.requireProfile(id, cx)) return;
    if (!this.machine(id, name)) { this.reject("This machine does not belong to the selected Tailcat profile.", cx); return; }
    this.perform("Opening " + name + " · waiting for layout…", async (cx) => {
      for (let i = 0; ; i++) {
        if (this.closed) return;
        const layout = this.layout(name);
        if (layout === "ready") break;
        if (layout === "unavailable" || i === 80) throw new CLIError("layout", "Cannot open " + name + " (layout: " + layout + ").", "The remote layout must become ready within 20 seconds.");
        await this.wait(cx);
      }
      const space = spaces().find((s) => s.machine === name);
      if (space) run("space.select", { id: space.id });
      else run("space.new", { machine: name });
      this.busy = "Opening " + name + " · waiting for space…"; cx.notify();
      for (let i = 0; ; i++) {
        if (this.closed) return;
        if (spaces().some((s) => s.machine === name)) { this.success = "Remote space opened: " + name; return; }
        const layout = this.layout(name);
        if (layout === "unavailable" || i === 40) throw new CLIError("layout", "No remote space appeared for " + name + " (layout: " + layout + ").", "App did not expose a space within 10 seconds.");
        await this.wait(cx);
      }
    }, cx);
  }
  confirm(action, cx) {
    if (this.refreshing) { this.enqueue((cx) => this.confirm(action, cx), cx); return; }
    if (this.busy || this.pending) { this.reject("Another operation or confirmation is pending.", cx); return; }
    if (!this.requireProfile(action.id, cx)) return;
    if (action.type === "unregister" && !this.machine(action.id, action.name)) { this.reject("This machine does not belong to the selected Tailcat profile.", cx); return; }
    this.pending = action;
    this.handle.focus();
    cx.notify();
  }
  confirmPending(cx) {
    const action = this.pending;
    if (!action) { this.reject("There is no pending confirmation.", cx); return; }
    this.pending = null;
    if (!this.requireProfile(action.id, cx)) { this.drainQueue(cx); return; }
    if (action.type === "unregister" && !this.machine(action.id, action.name)) { this.reject("This machine no longer belongs to the selected profile.", cx); this.drainQueue(cx); return; }
    this.perform("Removing…", async () => {
      if (action.type === "unregister") {
        await this.client.call(["profile", "unregister", action.id, "--name", action.name]);
      } else {
        await this.client.call(["profile", "rm", action.id, ...(action.force ? ["--force"] : [])]);
        this.keys.delete(action.id); this.tests.delete(action.id);
      }
      await this.loadProfiles();
    }, cx);
  }
  cancelPending(cx) {
    if (!this.pending) { this.reject("There is no pending confirmation.", cx); return; }
    this.pending = null;
    this.drainQueue(cx); cx.notify();
  }
  importProfile(id, cx) {
    if (!validName(id)) { this.error = { message: "Enter a valid profile id.", hint: "Use 1–32 lowercase letters, digits, _, . or -; terminal ids such as t7 are reserved." }; cx.notify(); return; }
    if (!this.localReady()) { this.reject("App's local core layout is not ready.", cx); return; }
    this.perform("Opening hidden-input terminal…", async () => {
      this.fallback = "";
      let terminal = null;
      try {
        const bin = await this.client.absolute();
        const r = await hc(["run", "--bg", "--json", "--no-parent", "--name", ("tailcat-add-" + id).slice(0, 32), "--", bin, "profile", "add", id, "--prompt"]);
        const v = r.code === 0 ? JSON.parse(r.stdout) : null;
        if (!/^t[1-9][0-9bcdfghjkmnpqrstvwxyz]*$/.test(v?.address ?? "")) throw new Error("terminal unavailable");
        terminal = v.address;
        openOverlay({ kind: "dialog", width: 620, height: 320, layout: { type: "column", children: [
          { type: "block", size: { px: 36 }, content: { mod: "tailcat", props: { part: "bar", id } } },
          { type: "block", content: { terminal } },
        ] } });
        this.importing = id;
        this.idInput.set_value("");
      } catch {
        if (terminal) run("terminal.kill", { terminal });
        this.fallback = "In your own terminal, run: herdr-tailcat profile add " + id + " --prompt. Or: herdr-tailcat profile add " + id + " < ADDRESS_FILE. Then refresh here.";
      }
    }, cx);
  }
  serverAction(args, cx) {
    if (!this.server?.initialized) return;
    this.perform("Updating server…", async () => {
      const result = await this.client.call(["server", ...args]);
      this.warning = result.warning ?? "";
      this.keyInput.set_value("");
      this.sessionInput.set_value("");
      await this.loadServer();
    }, cx);
  }
  reveal(cx) {
    if (this.busy || !this.server?.initialized) return;
    this.revealOverlay = openOverlay({ mod: "tailcat", props: { part: "address", binary: this.client.command }, kind: "dialog", width: 580 });
    cx.notify();
  }
  initReveal(cx) {
    // Explicit reveal is the sole exception to secret-free UI projections.
    // Keep it in a private closure, never in props, shared state or history.
    let value = "";
    let expires = 0;
    this.clearReveal = () => { value = ""; expires = 0; };
    this.revealValue = () => Date.now() < expires ? value : "";
    this.perform("Loading address…", async (cx) => {
      const binary = props()?.binary;
      if (binary !== "herdr-tailcat" && binary !== home() + "/.local/bin/herdr-tailcat") throw new Error("invalid executable");
      const r = await exec(binary, ["server", "show-address", "--json"]);
      if (this.closed) return;
      value = revealResult(r);
      expires = Date.now() + 30000;
      cx.timer.after(30000, (cx) => { this.clearReveal(); if (!this.closed) { closeOverlay(props().overlay); cx.notify(); } });
    }, cx);
  }
  command(command, cx) {
    if (typeof command !== "string") { this.reject("Invalid panel command.", cx); return; }
    const parts = command.split(":");
    const [verb, id, session, name] = parts;
    const arity = { refresh: [1], test: [2], pubkey: [2], register: [4], open: [3], unregister: [3], rm: [2, 3], confirm: [1], cancel: [1], add: [2], page: [2] };
    if (!Object.hasOwn(arity, verb) || !arity[verb].includes(parts.length) || parts.slice(1).some((v) => !validName(v)) || (verb === "rm" && session && session !== "force") || (verb === "page" && !["connect", "server"].includes(id))) {
      this.reject("Unknown panel command or invalid parameters.", cx); return;
    }
    if (verb === "refresh") this.refresh(cx);
    else if (verb === "test") this.test(id, cx);
    else if (verb === "pubkey") this.pubkey(id, cx);
    else if (verb === "register") this.register(id, name, session, cx);
    else if (verb === "open") this.open(id, session, cx);
    else if (verb === "unregister") this.confirm({ type: "unregister", id, name: session }, cx);
    else if (verb === "rm") this.confirm({ type: "rm", id, force: session === "force" }, cx);
    else if (verb === "confirm") this.confirmPending(cx);
    else if (verb === "cancel") this.cancelPending(cx);
    else if (verb === "add") this.importProfile(id, cx);
    else if (verb === "page" && ["connect", "server"].includes(id) && !this.busy) { this.page = id; this.refresh(cx); }
  }
  close() {
    this.closed = true;
    this.poll?.cancel();
    this.queue = [];
    for (const waiter of this.waiters) waiter.finish();
    this.clearReveal?.();
    this.keys = new Map(); this.tests = new Map();
    if (this.revealOverlay) closeOverlay(this.revealOverlay);
  }

  render(cx) {
    const t = theme();
    if (this.part === "bar") return h_flex().size_full().items_center().px(12).gap(8).bg(t.chrome)
      .child(div().flex_1().text_color(t.muted).child("Add profile " + (props()?.id ?? "") + " · input stays hidden"))
      .child(button("tc-import-close", "Close", () => closeOverlay(props().overlay)));
    if (this.part === "address") return card().child(div().font_semibold().child("Server address · clears after 30 seconds"))
      .child(note("This grants access. Share it only with a trusted client.", t.muted))
      .child(div().text_size(12).text_color(t.foreground).child(this.revealValue?.() ?? ""))
      .child(h_flex().gap(8).child(button("tc-copy-address", "Copy address", (cx) => { const v = this.revealValue(); if (v) cx.write_to_clipboard(v); }, !this.revealValue?.()))
        .child(button("tc-hide-address", "Hide", () => { this.clearReveal(); closeOverlay(props().overlay); })))
      .when(Boolean(this.busy || this.pending), (el) => el.child(note(this.busy, t.working)))
      .when(Boolean(this.error), (el) => el.child(note(this.error?.message ?? "", t.blocked)).child(note(this.error?.hint ?? "", t.muted)));
    return v_flex().id("tailcat-root").on_size_change((size, cx) => { this.size = size; cx.notify(); })
      .w_full().h(640).max_h(720).p(18).gap(12).bg(t.surface).text_color(t.foreground).text_size(13).track_focus(this.handle)
      .on_key_down((e, cx) => {
        if (this.pending && (e.key === "enter" || e.key === "escape")) {
          if (e.key === "enter") this.confirmPending(cx); else this.cancelPending(cx);
          cx.stop_propagation();
        } else if (e.key === "escape") { closeOverlay(props().overlay); cx.stop_propagation(); }
      })
      .child(h_flex().items_center().justify_between().child(v_flex().gap(2).child(div().font_semibold().text_size(19).child("Tailcat")).child(note("Private connections to remote herdr-core sessions", t.muted)))
        .child(button("tc-close", "Close", () => closeOverlay(props().overlay))))
      .child(h_flex().gap(8).child(button("tc-connect-tab", "Connections", (cx) => this.enqueue("page:connect", cx), Boolean(this.busy || this.pending)))
        .child(button("tc-server-tab", "Server", (cx) => this.enqueue("page:server", cx), Boolean(this.busy || this.pending)))
        .child(button("tc-refresh", "Refresh", (cx) => this.refresh(cx), Boolean(this.busy || this.pending))))
      .child(v_flex().id("tc-feedback").h(24).flex_shrink_0().overflow_y_scrollbar()
        .child(note([this.busy, this.queue?.length ? "Queued commands: " + this.queue.length : "", this.success,
          this.error ? this.error.message + " " + (this.error.hint ?? "") : "", this.refreshFailed ? "Last refresh failed" : ""]
          .filter(Boolean).join(" · ") || " ", this.error || this.late ? t.blocked : this.busy ? t.working : t.muted)))
      .when(Boolean(this.pending), (el) => {
        const action = this.pending;
        return el.child(card().id("tc-confirmation").border_color(t.blocked)
          .child(div().font_semibold().child(action.type === "rm" ? "Delete profile " + action.id + "?" : "Remove machine " + action.name + "?"))
          .child(note(action.force ? "This also removes its registered machines and stops its daemon." : "Existing remote terminals are managed by core.", t.muted))
          .child(h_flex().gap(8)
            .child(button("tc-confirm-yes", "Confirm", (cx) => this.confirmPending(cx), false, true))
            .child(button("tc-confirm-no", "Cancel", (cx) => this.cancelPending(cx)))));
      })
      .child(v_flex().id("tc-scroll").flex_1().min_h(0).overflow_y_scrollbar().gap(12).children(this.page === "server" ? this.serverPage(t) : this.connectionPage(t)));
  }
  connectionPage(t) {
    const own = ownMachines(this.profiles, machines());
    const p = this.profile(this.selected);
    const result = this.tests.get(this.selected);
    const key = this.keys.get(this.selected);
    return [
      ...(!this.localReady() ? [note("Waiting for App to connect to its local core…", t.muted)] : []),
      card().child(div().font_semibold().child("Profiles"))
        .children(this.profiles.map((profile) => h_flex().gap(8).items_center()
          .child(button("tc-profile-" + profile.id, profile.id, (cx) => this.select(profile.id, cx), Boolean(this.busy || this.pending)))
          .child(v_flex().gap(2).child(note("Client " + profile.client_fingerprint + " · server " + profile.server_fingerprint, t.muted))
            .child(note(profile.daemon_running ? "Daemon running" : "Daemon idle", profile.daemon_running ? t.done : t.dim)))))
        .when(this.profiles.length === 0, (el) => el.child(note("No profiles yet. Add one through a hidden-input terminal.", t.muted)))
        .child(h_flex().gap(8).child(Input.new(this.idInput).flex_1().border(1).border_color(t.input).rounded(6).p(6))
          .child(button("tc-add", "Add profile…", (cx) => this.importProfile(this.idInput.value().trim(), cx), Boolean(this.busy || this.pending) || !this.localReady())))
        .when(Boolean(this.fallback), (el) => el.child(note(this.fallback ?? "", t.muted))),
      ...(p ? [card().child(div().font_semibold().child(p.id))
        .child(h_flex().gap(8).child(button("tc-test", "Test", (cx) => this.test(p.id, cx), Boolean(this.busy || this.pending)))
          .child(button("tc-public-key", "Client public key", (cx) => this.pubkey(p.id, cx), Boolean(this.busy || this.pending)))
          .child(button("tc-delete-profile", "Delete profile…", (cx) => this.confirm({ type: "rm", id: p.id, force: p.machines.length > 0 }, cx), Boolean(this.busy || this.pending))))
        .when(Boolean(result), (el) => el.child(note(result ? result.rtt_ms.toFixed(1) + " ms · " + result.server.version : "", t.done)))
        .when(Boolean(key), (el) => el.child(note(key?.public_key ?? "", t.foreground)).child(note("Add this public key to the server whitelist and restart serve.", t.muted)))
        .child(Input.new(this.nameInput).border(1).border_color(t.input).rounded(6).p(6))
        .child(h_flex().flex_wrap().gap(6).children((result?.server.exports ?? []).map((s) => button("tc-session-" + s, (this.session === s ? "✓ " : "") + s, (cx) => { this.session = s; cx.notify(); }, Boolean(this.busy || this.pending)))))
        .child(button("tc-register", "Register machine", (cx) => this.register(p.id, this.nameInput.value().trim(), this.session, cx), Boolean(this.busy || this.pending) || !result || !this.session))] : []),
      card().child(div().font_semibold().child("Registered machines"))
        .children(own.map((m) => { const status = machineStatus(m); return v_flex().gap(6)
          .child(h_flex().items_center().gap(8).child(div().font_semibold().child(m.name)).child(note(m.session ?? "default", t.muted)).child(note(status.text, t[status.tone])))
          .child(note("App layout: " + m.layout, m.layout === "ready" ? t.done : t.muted))
          .when(Boolean(status.detail), (el) => el.child(note(status.detail, t.muted)))
          .child(h_flex().gap(8)
            .child(button("tc-open-" + m.name, "Open remote space", (cx) => this.enqueue("open:" + m.profile + ":" + m.name, cx), Boolean(this.busy || this.pending) || m.state !== "connected"))
            .child(button("tc-unregister-" + m.name, "Remove…", (cx) => this.enqueue("unregister:" + m.profile + ":" + m.name, cx), Boolean(this.busy || this.pending)))); }))
        .when(own.length === 0, (el) => el.child(note("Test a profile, then register one of its exported sessions.", t.muted))),
    ];
  }
  serverPage(t) {
    const s = this.server;
    if (!s?.initialized) return [card().child(div().font_semibold().child("Server not initialized"))
      .child(note("Deploy a server on the computer you want to connect to:", t.muted)).children(SERVER_STEPS.map((step, i) => note((i + 1) + ". " + step, t.muted)))];
    return [card().child(div().font_semibold().child(s.running ? "Server running" : "Server stopped"))
      .child(note(s.streams + " active streams · " + s.allowed_clients + " allowed clients · " + s.version, t.muted))
      .when(s.restart_required, (el) => el.child(note("Configuration changed. Restart serve to apply it.", t.blocked)))
      .when(Boolean(this.warning), (el) => el.child(note(this.warning ?? "", t.blocked)))
      .child(button("tc-show-address", "Show address for 30 seconds…", (cx) => this.reveal(cx), Boolean(this.busy || this.pending)))
      .when((appWindow()?.platform ?? (s.service ? "macos" : "")) === "macos", (el) => el.child(h_flex().gap(8)
        .child(note(s.service?.loaded ? "Service loaded" : "Service not loaded", t.muted))
        .child(button("tc-start-service", "Start service", (cx) => this.serverAction(["service", "start"], cx), Boolean(this.busy || this.pending) || s.service?.loaded))
        .child(button("tc-stop-service", "Stop service", (cx) => this.serverAction(["service", "stop"], cx), Boolean(this.busy || this.pending) || !s.service?.loaded)))),
      card().child(div().font_semibold().child("Exported sessions"))
        .children(s.exports.map((session) => h_flex().gap(8).items_center().child(div().flex_1().child(session)).child(button("tc-export-rm-" + session, "Remove", (cx) => this.serverAction(["export", "rm", session], cx), Boolean(this.busy || this.pending)))))
        .child(Input.new(this.sessionInput).p(6).border(1).border_color(t.input).rounded(6))
        .child(button("tc-export-add", "Add session", (cx) => { const v = this.sessionInput.value().trim(); if (validName(v)) this.serverAction(["export", "add", v], cx); }, Boolean(this.busy || this.pending))),
      card().child(div().font_semibold().child("Allowed clients"))
        .children(this.clients.map((client) => h_flex().gap(8).items_center().child(div().flex_1().child(client.fingerprint)).child(button("tc-allow-rm-" + client.fingerprint, "Remove", (cx) => this.serverAction(["allow", "rm", client.public_key], cx), Boolean(this.busy || this.pending)))))
        .child(Input.new(this.keyInput).p(6).border(1).border_color(t.input).rounded(6))
        .child(button("tc-allow-add", "Allow client", (cx) => { const key = this.keyInput.value().trim(); if (/^nodekey:[a-f0-9]{64}$/.test(key)) this.serverAction(["allow", "add", key], cx); }, Boolean(this.busy || this.pending))),
    ];
  }
}
