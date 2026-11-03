# Tailcat v0.7.0 API verification

Verified against tag v0.7.0, commit 15ab9e68bfc6534a61797d7af28cedd42b54a3a5.
Source: github.com/tailscale/tailcat, BSD-3-Clause. This project depends on the
published Go module; the implementation uses its public API.
Line numbers below refer to that tag's tailcat.go unless stated otherwise.

| API used | Source | Use and verified contract |
|---|---|---|
| Addr, ConnInfo | 149–188 | Serialized connection address contains PSK; Region embeds a relay, RegionID requires a map lookup. Both short and full addresses are secret. |
| NewPrivateKey, PrivateKey.Private/Public | 307–329 | Creates node identity, deterministic independent disco key and random nonzero PSK. Persist the entire value as server/server.key (0600). |
| ConnInfo.Expand, ExpandForServer | 138–142, 1333–1416 | Init explicitly sets RegionID=-1 before Expand; zero means no expansion. The selected full Region is persisted before serve starts. The explicit value avoids leaving the region unexpanded. |
| ConnInfo.Addr | 1033–1076 | Full address from stored region; short address from a copy with RegionID set and Region nil. No address is placed in argv or logs. |
| ParseAddr | 1134–1182 | Validates pasted profile address. Reject a missing ServerDiscoPublic (legacy address) or zero PSK before saving. |
| PresharedKey.MarshalText, IsZero | 221–224, 246–251 | Used to validate identity and add PSK hex/text to the redactor, never to diagnostics. |
| Server.Key/PresharedKey/Region | 408–435 | Restored on every launch. Disco private key is derived from node private key (1007), so restoring Key also restores disco identity. |
| Server.AllowedClients | 446–450, 620–622, 1674–1677 | Empty means allow all; must validate nonempty file BEFORE Start. Unknown clients are silently dropped. AddAllowedClient (976–987) has no removal counterpart; no live mutation is used. |
| Server.OnTCP | 467–478 | Only port 6470 is offered a handler. Our handler performs the bounded protocol prelude and enforces max_conns before spawning connect. No ExecConnHandler or arbitrary remote exec is exposed. |
| Server.Start/Close | 567–640, 862–879 | Start after all policy validation; close on SIGINT/SIGTERM. Service generation is documented separately. |
| Server.PeerEnv | tailcat_exec.go:37–58 | Retrieves authenticated peer key from accepted connection addresses; logs only first 8 public-key hex characters. Not passed through as selectors to core. |
| Client.Server/Key/Logf | 1823–1860 | One instance per profile daemon, with persistent nonzero Key assigned before any operation. Library verbose logging is suppressed to avoid secret-bearing dependency diagnostics. Application/child logs use internal/redact. |
| Client.DialTCPPort | 2203–2211 | Opens a new stream per call; implicit first-use handshake. Concurrent streams share the same engine/node key. |
| Client.Ping | 2062–2137 | Up remembers first acknowledgement. Ping's internal maximum is 10s and respects shorter context. Unknown allowlist client and offline server produce the same timeout. |
| Client.Close | 2003–2010 | Closes old client before constructing replacement, retaining the same stored node key. Dial timeout 8s; single synchronized rebuild then Ping and one retry, bounded by the request budget. |
| CloseWrite | 2278–2340; tailcat_exec.go:64–108 | gVisor TCP streams support half-close. Our proxy propagates EOF independently and waits at most 5s for the other direction before closing. Raw hc bytes, including buffered bytes immediately after the prelude, are preserved. |

The library's server peer registry is memory-only (1680–1694, 1439–1445), while
Client.up skips future handshakes after success (2062–2068). Therefore a restarted
server requires a reconstructed Client. Injected-dialer tests cover one rebuild
for concurrent failures and both retry outcomes; the scratch fault matrix covers real server restart.

Additional direct dependency APIs: tailscale.com/types/key/node.go NewNode:55,
NodePrivate.IsZero:95, Public:106, UnmarshalText:126, NodePublic.IsZero:242,
String:323 and UnmarshalText:345. Key JSON uses their text marshal methods.
golang.org/x/sys/unix provides flock, Linux SO_PEERCRED/PR_SET_PDEATHSIG and
Darwin LOCAL_PEERCRED; these are local process controls, not Tailcat APIs.

The real-network smoke sets TS_DEBUG_ALWAYS_USE_DERP=true, documented by
tailscale.com/wgengine/magicsock/debugknobs.go:37 and magicsock.go:3707, disabling
direct UDP. No local-loopback transport is substituted for the Tailcat link.
