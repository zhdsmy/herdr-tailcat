# Management CLI JSON contract (v1)

Place --json anywhere after server or profile in a management command. stdout contains exactly one JSON object plus a newline; stderr is empty. Exit 0 means success; exit 1 means failure. Without the flag, show-address and pubkey print a value, install-service prints text/a path, and other management commands print JSON.

Success is {"ok":true,...}. Failure is {"ok":false,"error":{"kind":"...","message":"...","hint":"..."}}; all three error fields are strings (hint may be empty). The one exception is server show-address --json: its entire result is {"address":"..."} with no ok. This explicit command reveals a connection secret; do not log or persist its result in UI history. Addresses, PSKs, private keys, config/env maps and raw target fields are absent from all other results. Core error and why text may mention the installed executable path. Public keys are intentionally returned only by pubkey/allow ls. Fingerprints are the first eight hex characters after nodekey:.

Foreground serve, daemon and via retain their transport/lifetime protocol without --json. With --json immediately following the foreground verb they return an unsupported error immediately; via preserves its line argument verbatim, including any --json text; JSON is never mixed into a raw core connection. Use status/test/service commands for the panel.

Management operations have a 24.5s overall budget. Register's ping preflight is at most 4s (including daemon startup), leaving time for core's 20s hello. Test's ping is at most 18s. Daemon ownership probes take at most 1s per ps, and stop waits at most 3s. Core reconnect/heartbeat timing is unchanged.

Profile ls, register, unregister and rm invoke hc. If the current session's core is not running, hc automatically starts it. The panel calls these commands only after App reports its local core layout ready. Scripts must set their intended HC_SESSION and XDG directories, even for profile ls; there is no no-start option in core v0.1.16–0.1.17.

## Commands

Targets use the absolute, symlink-resolved running executable, POSIX single quoted. Profile id and name/session follow core's ASCII name rule (line.ValidName): 1–32 characters, starting with a lowercase letter or digit, remaining characters lowercase letters, digits, underscore, dot or hyphen. Base-30 terminal-id shapes such as t7 are excluded. Profile add reads one address from stdin, at most 16 KiB. Secrets are never accepted as command arguments.

Fields below are additional to ok. Arrays are always arrays, including when empty. Machine session is a string or null; all other listed text fields are strings. pid is 0 when absent, flags are booleans, counters and rtt_ms are numbers.

| Command and input | Success fields | Typical errors |
|---|---|---|
| server init | none | already_exists, io_error |
| server show-address [--full] | address (only field) | io_error |
| server allow add <nodekey> | none | invalid_arguments, io_error |
| server allow rm <nodekey> | warning (empty unless last client removed) | invalid_arguments, not_found, io_error |
| server allow ls | clients: [{public_key,fingerprint}] | io_error |
| server export add <session> | none | invalid_arguments, io_error |
| server export rm <session> | none | invalid_arguments, not_found, io_error |
| server export ls | exports: [string] | io_error |
| server status [--label <label>] [--plist-dir <dir>] | initialized,running,pid,started_at,version,exports,allowed_clients,streams,restart_required; macOS only service: {loaded,pid,label} | io_error, service_error |
| server install-service [--label <label>] [--plist-dir <dir>] [--dry-run] [--os darwin or linux] [--force-derp] | path,os when writing a plist; text,os when dry-run or Linux | invalid_arguments, io_error |
| server service start/stop/status [--label <label>] [--plist-dir <dir>] | loaded,pid,label | unsupported, not_found, service_error, timeout |
| profile add <id> (stdin address) | none | invalid_arguments, invalid_address, already_exists, io_error |
| profile pubkey <id> | public_key,fingerprint | profile_not_found, io_error |
| profile ls | profiles: [{id,client_fingerprint,server_fingerprint,daemon_running,machines:[Machine]}] | core_unavailable, core_error, core:*, invalid_config, io_error |
| profile test <id> | rtt_ms,server:{version,exports:[string]} | profile_not_found, no_answer |
| profile register <id> --name <name> [--session <session>] [--temp] | machine: Machine | profile_not_found, no_answer, session_not_exported, core:*, core_error, core_unavailable |
| profile unregister <id> --name <name> | none | profile_not_found, target_mismatch, not_found, core:*, core_error |
| profile rm <id> [--force] | none | profile_not_found, profile_in_use, ownership_unverified, timeout, core:*, core_error, io_error |

Machine has only name,id,host,version,session,state,why. Add has empty state/why when core omits them. Profile ls includes only machines whose via is true AND target exactly equals the generated target for that profile. Unregister rejects all others. Rm refuses registered machines unless --force, which removes matching registrations, verifies and stops only the profile daemon, then deletes that profile directory. An unverifiable busy daemon lock prevents deletion; it never authorizes a signal. Lost/stale state is reported as daemon_running=false.

Register validates id/name/session, verifies a ping through the tunnel, and checks the export list locally before invoking hc. Omitted session means default for this check; it is omitted from hc arguments. --temp is forwarded to core.

Status uses a held flock and matching process start/command identity to reject stale PIDs. server/serve.state.json is private (0600) and updated every 5s, so streams may lag by 5s. It counts successful connect streams, excluding pings. restart_required compares normalized config and allowed keys loaded at serve start against disk, including an empty whitelist. Config changes require restart; no control socket is opened. Removing the last client succeeds with a warning, and the next serve start fails closed. Old status files may remain after exit but never mean a process is running on their own.

Service generation never loads a service. For macOS output, install-service rejects binaries in ~/Documents, ~/Desktop or ~/Downloads (including resolved symlinks) with invalid_arguments, even for --dry-run; use an installed path such as ~/.local/bin instead. On macOS, start requires a generated plist and bootstraps it; stop boots it out and waits for absence; status uses launchctl print. Default label: dev.herdr.tailcat. Start's default plist directory is ~/Library/LaunchAgents. Status/stop do not read the plist directory. Linux service operations return unsupported; install-service can generate Linux unit text on either OS. Tests always pass a scratch --plist-dir and random label containing test. Debug environment is excluded unless --force-derp is explicit. Generated PATH is configured core's directory plus /usr/bin:/bin:/usr/sbin:/sbin.

## Interactive profile import

profile add <id> --prompt is a human-only TTY operation, outside the JSON contract. It rejects non-TTY stdin and rejects --json with invalid_arguments. Without --prompt, the existing stdin-to-EOF behaviour is unchanged.

The prompt disables echo and canonical input before asking for the address, strips bracketed-paste wrappers, accepts Return, and limits input to 16 KiB. Ctrl-C cancels; Ctrl-D/Z and terminal control bytes cannot suspend or end the prompt. SIGINT/TERM/HUP/QUIT/TSTP cancel with terminal restoration. Broken output writes also restore the TTY. Output contains only fixed messages and the new client public key; the address is never written back. It waits for Return after the result, with a five-minute bound for each input stage. Catchable cancellation and all ordinary return/error/panic paths restore termios.

The App panel collects only the profile id. It resolves the executable and uses hc run --bg --no-parent to launch the prompt in a terminal overlay. It never reads the clipboard or uses hc send/keys/read. Import failures show instructions for running the same command in an external terminal. Address input travels directly from the person's terminal to the CLI, never through mod props, state or document data.

server status.allowed_clients is a count. The fingerprints and corresponding public keys for removal come from server allow ls. Only an explicit, transient show-address overlay may retain/display a returned address, clearing it on close or after 30 seconds; other panel state is a projection of public JSON fields.

## Finite error kinds

Local kinds: invalid_arguments, unsupported, io_error, already_exists, not_found, invalid_address, invalid_config, profile_not_found, profile_in_use, session_not_exported, target_mismatch, ownership_unverified, no_answer, core_unavailable, core_error, service_error, timeout.

Core kinds are prefixed core:; message and hint retain core's wording after secret redaction. Recognized suffixes (current core plus protocol kinds):

    invalid_params not_found name_taken already_added unreachable io internal cancelled timeout protocol version_mismatch permission_denied unsupported conflict busy invalid_request method_not_found denied edit_failed hc_missing incompatible install_failed invalid_template live needs_framed needs_login needs_update not_text refused spawn_failed unsupported_generation parse_error usage machine_unknown unavailable needs_target config blocked status_mismatch finished unknown_method

Unrecognized future core kinds map to core_error, preserving redacted message/hint. Invalid/missing hc JSON maps to core_error; missing executable maps to core_unavailable. All commands may also return invalid_arguments, io_error or timeout as appropriate. The no_answer message for an unanswered tunnel is: tailcat: server did not answer (offline, or this client key is not allowed). Failure does not distinguish offline from an unauthorized client.
