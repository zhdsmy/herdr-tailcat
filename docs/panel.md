# Tailcat panel

The mod lives in mods/tailcat. Open it with the App command mod.open {name:"tailcat"}. It is a 620px-wide, 640px-high dialog with a flexible scrolling body and has no layout or keybinding dependency. The explicit height prevents the scrolling body from collapsing in a real window; check alone does not validate layout. It requires the CLI from this repository on PATH or at ~/.local/bin/herdr-tailcat. It runs local programs and uses the core connection already held by App; the CLI's daemon and via processes own remote links independently of the panel.

Connections shows profiles and only their matching Tailcat registrations. SSH machines are never added to the panel's actions. Wait for App's local core layout to be ready before loading registrations; profile ls/register/unregister/rm can otherwise start core automatically. Live App machines provide layout readiness, not additional entries. Test a profile to select an exported session, give the registration a machine name, and open its remote space. Removing a machine and deleting a profile require confirmation; deleting an in-use profile explicitly confirms force removal. Closing or reloading this mod does not stop a daemon, via or server.

Add profile collects only an id. A dedicated terminal runs profile add <id> --prompt with hidden input; paste the address there and press Return. The panel never reads that terminal or clipboard, and never injects its input. After success, press Return to close the terminal; the next poll or manual refresh discovers the import and displays its public key to allow on the server. If the terminal cannot open, follow the external-terminal instructions shown by the panel. The CLI prompt requires this release of herdr-tailcat. The noninteractive pipe-to-EOF form still works in a user's external terminal.

A refresh removes cached public keys and successful tests when a profile disappears or its complete public identity changes. Selection and session follow the surviving profile, and Register requires a new test for a replaced identity. Unchanged identities retain their evidence; an older CLI without complete identities clears evidence at each refresh.

Server reads the local server status only when selected. An uninitialized server shows deployment instructions, with no initialize button. An initialized server shows exports and allowed-client fingerprints, configuration reload requirements, and macOS service start/stop. Create the service plist explicitly in a terminal first; Start never generates one. Linux service control is unavailable. Show address is an explicit exception to secret-free panel state: a separate overlay holds the result in a private closure, clears on close/after 30 seconds, and never stores it in props, shared state or history. Copy is user-triggered; the system clipboard is not automatically cleared. No background polling requests an address.

The connections dialog refreshes quietly at five-second intervals while the app is active and no command or confirmation is queued. Before the first successful profile load, polling may load profiles despite queued commands once the local layout becomes ready; no management CLI runs while that layout is unavailable. Polling keeps controls enabled, preserves existing success/error feedback, and does not redraw unchanged data. A small last-refresh-failed marker reports polling failures. Manual refresh and user operations still show a busy state; a compact 24px feedback row prevents the scrolling body from jumping. Long feedback scrolls within that row. Scripted commands enter a FIFO queue of up to 16 entries; processing waits for the first successful profile load and for the current operation/confirmation to finish. Confirm and cancel use a separate single response slot for the currently visible confirmation. The first response wins; duplicates and responses arriving without a pending confirmation cannot authorize a future rm or unregister. This slot remains available when the ordinary queue is full, and ordinary commands retain FIFO order. Rejected commands show an error. After a failed initial load, polling or an explicit scripted/manual Refresh retries loading profiles; commands remain queued without an immediate retry loop. All operations in a copy are serialized. App's 30-second exec limit and the CLI's 24.5-second management budget are shown as busy/error states; the panel never launches foreground serve/daemon. The import launcher starts only a bounded interactive terminal. A terminal overlay closes when its program ends (App's documented behaviour). Opening a remote space waits up to 20 seconds for App layout readiness, dispatches space.new or space.select, then waits up to 10 seconds for the machine’s space to appear. App timers check every 250ms. Layout state, pending confirmation, and success/error feedback stay visible; the panel does not close itself after opening a space. Closing cancels its waits, queued commands, confirmation and saved response.

The deployment text in model.js exports SERVER_STEPS so the final RECIPE can use the same steps: install both programs on the other computer; explicitly initialize and export a session there; import its address on the client; allow the client's public key and run/restart serve; test and register the exported session. Never put the server address in argv, environment variables, screenshots or mod state.

## Scriptable actions

A shown copy accepts nextCommand strings. Examples for a profile named home and an export named work:

    herdr-app do tailcat.test:home
    herdr-app do tailcat.register:home:work:home-work
    herdr-app do tailcat.open:home:home-work
    herdr-app do tailcat.unregister:home:home-work
    herdr-app do tailcat.confirm

Unregister shows an inline confirmation card above the scrolling body, with Confirm and Cancel buttons. Enter confirms; Escape cancels that request while keeping the panel open. Other operation buttons stay disabled until the request is resolved. Scripted confirm acts only on that pending request; cancel dismisses it and resumes queued work. General forms are test:<profile>, pubkey:<profile>, register:<profile>:<session>:<name>, open:<profile>:<name>, unregister:<profile>:<name>, rm:<profile>[:force], add:<profile>, refresh, and page:connect/server. The panel rejects names outside its current profile list. The explicit server page action loads server status, so do not use it in client-only verification. Confirm and cancel apply only to a currently visible pending request.

## Checks

    node --test tests/panel.test.mjs
    node --experimental-loader ./tests/app-loader.mjs --test tests/panel-app.test.mjs
    python3 scripts/app-check.py
    python3 scripts/prompt-e2e.py
    python3 scripts/cli-e2e.py

Build bin/herdr-tailcat first. Runtime runners require macOS and network/launchd access. All core operations in the runners use private core copies, tc-* sessions, scratch XDG directories and HERDR_TAILCAT_HOME. App check uses scratch HERDR_APP_HOME, compares real App status and file metadata only, and runs native checks for demo connection/server/error/reveal/title-bar states. It never opens a real window or writes the real App folder. App generates ignored jsconfig.json and type files beside the checked mod. Fixtures and mock tests do not replace visible interaction checks.
