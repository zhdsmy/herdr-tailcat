import { CLIError, parseResult } from "./model.js";

export class Client {
  constructor(exec, home) { this.exec = exec; this.home = home; this.command = "herdr-tailcat"; this.found = false; }
  async raw(args) {
    // Retry only a start failure, never a CLI failure (a mutation may have run).
    let r;
    try { r = await this.exec(this.command, args); }
    catch {
      if (this.found || this.command !== "herdr-tailcat") throw new CLIError("exec", "The command could not start or exceeded App's 30-second limit.", "Check the installation and try again.");
      this.command = this.home + "/.local/bin/herdr-tailcat";
      try { r = await this.exec(this.command, args); }
      catch { throw new CLIError("not_installed", "herdr-tailcat was not found.", "Install it on PATH or at ~/.local/bin/herdr-tailcat, then refresh."); }
    }
    this.found = true;
    return r;
  }
  async call(args) { return parseResult(args, await this.raw([...args, "--json"])); }
  async absolute() {
    if (this.command.startsWith("/")) return this.command;
    let r;
    try { r = await this.exec("/usr/bin/which", ["herdr-tailcat"]); } catch {}
    const path = r?.code === 0 ? r.stdout.trim() : "";
    if (!path.startsWith("/") || /[\r\n\x00]/.test(path)) throw new CLIError("not_installed", "Could not locate an absolute path to herdr-tailcat.", "Run the import command in your own terminal.");
    return path;
  }
}
