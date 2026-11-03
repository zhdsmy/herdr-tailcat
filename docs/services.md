# Running serve as a user service

install-service generates service files. server service start/stop/status controls launchd on macOS; Linux operations never run systemctl.
Use an absolute installed binary path; rebuilding or moving that binary changes what the service executes.

On macOS, install the binary outside ~/Documents, ~/Desktop and ~/Downloads, for example at ~/.local/bin/herdr-tailcat. Tailscale reads its own executable during startup; running a new build from these privacy-protected folders can trigger a permission prompt and stall a launchd service. install-service rejects resolved executable paths in these folders with invalid_arguments, including symlinks into them. Use a binary outside the folders; signing changes or privacy permission grants are not required.

Runtime tests copy the built binary and core into a private scratch bin directory. Direct calls, via targets and launchd ProgramArguments all use those copies, with path assertions; child working directories also point to scratch. A repository build under Documents is only a copy source and must not be executed there. The recipe's Watch out section must preserve this installation requirement.

On macOS:

    herdr-tailcat server install-service --label dev.herdr.tailcat --dry-run
    herdr-tailcat server install-service --label dev.herdr.tailcat
    herdr-tailcat server service start --label dev.herdr.tailcat

The second command writes the plist and private 0600 stdout/stderr logs, then prints the plist path; it never calls launchctl. The third loads it. install-service and server service start share the same --plist-dir default, ~/Library/LaunchAgents; pass the same --plist-dir <directory> to both when using another directory. Generation is separate from loading. Runtime tests only load random test labels from scratch directories.

The plist records HERDR_TAILCAT_HOME, XDG_STATE_HOME, XDG_CONFIG_HOME and XDG_RUNTIME_DIR, HOME and a PATH containing only the configured core directory and /usr/bin:/bin:/usr/sbin:/sbin, and an absolute binary path. Set XDG variables before generation to match the core you intend to export. An unset runtime directory is written empty so core retains its normal platform fallback. HC_SESSION, when explicitly set, is retained for the serve process; exported connect children still select their own session. Arbitrary inherited environment, TS_DEBUG_* and keys are not copied. Only explicit --force-derp writes TS_DEBUG_ALWAYS_USE_DERP=true.

RunAtLoad starts the job; KeepAlive SuccessfulExit=false restarts crashes. AbandonProcessGroup=true prevents launchd from terminating detached core/holder processes when serve stops. ExitTimeOut=10 accommodates handler cancellation (6s) and TCP draining (3s). The helper does not stop core terminals.

For Linux, generate text locally, including on macOS:

    herdr-tailcat server install-service --os linux --label dev.herdr.tailcat --dry-run

Linux output is a systemd user unit and is never installed by this command. KillMode=process prevents stopping serve from killing a core/holder through the whole cgroup. herdr-core client.rs:181-184 attempts its own systemd user startup, placing core in a separate unit when that path is available; its fallback detaches the core. The unit has Restart=on-failure and UMask=0077. Linux runtime behavior is untested; generated text and cross-compilation are checked locally.

Normal serve shutdown closes streams, waits up to 6s for handlers, drains TCP for up to 3s, then closes Tailcat. A crash cannot send FIN. With default core timing a crash can interrupt a line for about 45–70s before detection and reconnection; normal restarts are expected to be detected promptly. A new successful stream with the same client key and session replaces the prior stream. If every max_conns slot is taken, that same client may use one of four temporary replacement handshakes; the new stream takes the old stream's slot, and any other client is still refused with server busy. A replacement that does not match an existing stream is refused the same way, so the steady number of streams stays within max_conns.
