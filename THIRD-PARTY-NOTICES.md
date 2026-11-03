# Third-party notices

The following modules are linked into the v0.0.1 builds, as reported by go version -m: 37 for darwin/arm64 and 41 for linux/amd64, or 42 distinct modules. The target column distinguishes platform-specific dependencies. This is a subset of go list -m all; unused graph modules are excluded. License identifiers and filenames were verified against the pinned modules in the Go module cache. Each link opens the license texts for that module version. No dependency source is vendored in this repository.

The Go runtime and standard library are covered by the [Go BSD license](https://go.dev/LICENSE). Distributors of built binaries should carry the applicable full license and copyright notices from these pinned dependencies, including any relevant subpackage notices. The root license file for compress includes additional Apache-2.0 and MIT sections; those sections identify their scope. The module's main code uses BSD-3-Clause.

| Module | Linked version | Build target | License | Verified file / license text |
|---|---|---|---|---|
| filippo.io/edwards25519 | v1.2.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/filippo.io/edwards25519@v1.2.0?tab=licenses) |
| github.com/anmitsu/go-shlex | v0.0.0-20200514113438-38f4b401e2be | darwin/arm64, linux/amd64 | MIT | [LICENSE](https://pkg.go.dev/github.com/anmitsu/go-shlex@v0.0.0-20200514113438-38f4b401e2be?tab=licenses) |
| github.com/coder/websocket | v1.8.14 | darwin/arm64, linux/amd64 | ISC | [LICENSE.txt](https://pkg.go.dev/github.com/coder/websocket@v1.8.14?tab=licenses) |
| github.com/creachadair/msync | v0.8.1 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/creachadair/msync@v0.8.1?tab=licenses) |
| github.com/creack/pty | v1.1.24 | darwin/arm64, linux/amd64 | MIT | [LICENSE](https://pkg.go.dev/github.com/creack/pty@v1.1.24?tab=licenses) |
| github.com/fxamacker/cbor/v2 | v2.9.0 | darwin/arm64, linux/amd64 | MIT | [LICENSE](https://pkg.go.dev/github.com/fxamacker/cbor/v2@v2.9.0?tab=licenses) |
| github.com/gaissmai/bart | v0.26.1 | darwin/arm64, linux/amd64 | MIT | [LICENSE](https://pkg.go.dev/github.com/gaissmai/bart@v0.26.1?tab=licenses) |
| github.com/go-json-experiment/json | v0.0.0-20260623181947-01eb4420fa68 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/go-json-experiment/json@v0.0.0-20260623181947-01eb4420fa68?tab=licenses) |
| github.com/godbus/dbus/v5 | v5.2.2 | linux/amd64 | BSD-2-Clause | [LICENSE](https://pkg.go.dev/github.com/godbus/dbus/v5@v5.2.2?tab=licenses) |
| github.com/golang/groupcache | v0.0.0-20241129210726-2c02b8208cf8 | darwin/arm64, linux/amd64 | Apache-2.0 | [LICENSE](https://pkg.go.dev/github.com/golang/groupcache@v0.0.0-20241129210726-2c02b8208cf8?tab=licenses) |
| github.com/google/btree | v1.1.3 | darwin/arm64, linux/amd64 | Apache-2.0 | [LICENSE](https://pkg.go.dev/github.com/google/btree@v1.1.3?tab=licenses) |
| github.com/hdevalence/ed25519consensus | v0.2.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/hdevalence/ed25519consensus@v0.2.0?tab=licenses) |
| github.com/jsimonetti/rtnetlink | v1.4.1 | linux/amd64 | MIT | [LICENSE.md](https://pkg.go.dev/github.com/jsimonetti/rtnetlink@v1.4.1?tab=licenses) |
| github.com/klauspost/compress | v1.19.1 | darwin/arm64, linux/amd64 | BSD-3-Clause; root also includes Apache-2.0 and MIT subpackage notices | [LICENSE](https://pkg.go.dev/github.com/klauspost/compress@v1.19.1?tab=licenses) |
| github.com/kr/fs | v0.1.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/kr/fs@v0.1.0?tab=licenses) |
| github.com/mdlayher/netlink | v1.7.3-0.20250113171957-fbb4dce95f42 | linux/amd64 | MIT | [LICENSE.md](https://pkg.go.dev/github.com/mdlayher/netlink@v1.7.3-0.20250113171957-fbb4dce95f42?tab=licenses) |
| github.com/mdlayher/socket | v0.5.1 | linux/amd64 | MIT | [LICENSE.md](https://pkg.go.dev/github.com/mdlayher/socket@v0.5.1?tab=licenses) |
| github.com/mitchellh/go-ps | v1.0.0 | darwin/arm64, linux/amd64 | MIT | [LICENSE.md](https://pkg.go.dev/github.com/mitchellh/go-ps@v1.0.0?tab=licenses) |
| github.com/pires/go-proxyproto | v0.8.1 | darwin/arm64, linux/amd64 | Apache-2.0 | [LICENSE](https://pkg.go.dev/github.com/pires/go-proxyproto@v0.8.1?tab=licenses) |
| github.com/pkg/sftp | v1.13.6 | darwin/arm64, linux/amd64 | BSD-2-Clause | [LICENSE](https://pkg.go.dev/github.com/pkg/sftp@v1.13.6?tab=licenses) |
| github.com/safchain/ethtool | v0.3.0 | linux/amd64 | Apache-2.0 | [LICENSE](https://pkg.go.dev/github.com/safchain/ethtool@v0.3.0?tab=licenses) |
| github.com/tailscale/certstore | v0.1.1-0.20260409135935-3638fb84b77d | darwin/arm64 | MIT | [LICENSE.md](https://pkg.go.dev/github.com/tailscale/certstore@v0.1.1-0.20260409135935-3638fb84b77d?tab=licenses) |
| github.com/tailscale/gliderssh | v0.3.4-0.20260716005906-1a0f895faf28 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/tailscale/gliderssh@v0.3.4-0.20260716005906-1a0f895faf28?tab=licenses) |
| github.com/tailscale/hujson | v0.0.0-20260302212456-ecc657c15afd | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/tailscale/hujson@v0.0.0-20260302212456-ecc657c15afd?tab=licenses) |
| github.com/tailscale/peercred | v0.0.0-20250107143737-35a0c7bd7edc | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/tailscale/peercred@v0.0.0-20250107143737-35a0c7bd7edc?tab=licenses) |
| github.com/tailscale/tailcat | v0.7.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/tailscale/tailcat@v0.7.0?tab=licenses) |
| github.com/tailscale/web-client-prebuilt | v0.0.0-20250124233751-d4cd19a26976 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/tailscale/web-client-prebuilt@v0.0.0-20250124233751-d4cd19a26976?tab=licenses) |
| github.com/tailscale/wireguard-go | v0.0.0-20260911194433-e3222a3340cd | darwin/arm64, linux/amd64 | MIT | [LICENSE](https://pkg.go.dev/github.com/tailscale/wireguard-go@v0.0.0-20260911194433-e3222a3340cd?tab=licenses) |
| github.com/u-root/u-root | v0.14.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/github.com/u-root/u-root@v0.14.0?tab=licenses) |
| github.com/x448/float16 | v0.8.4 | darwin/arm64, linux/amd64 | MIT | [LICENSE](https://pkg.go.dev/github.com/x448/float16@v0.8.4?tab=licenses) |
| go4.org/mem | v0.0.0-20240501181205-ae6ca9944745 | darwin/arm64, linux/amd64 | Apache-2.0 | [LICENSE](https://pkg.go.dev/go4.org/mem@v0.0.0-20240501181205-ae6ca9944745?tab=licenses) |
| go4.org/netipx | v0.0.0-20260823151212-3075585bcbeb | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/go4.org/netipx@v0.0.0-20260823151212-3075585bcbeb?tab=licenses) |
| golang.org/x/crypto | v0.57.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/crypto@v0.57.0?tab=licenses) |
| golang.org/x/exp | v0.0.0-20260410095643-746e56fc9e2f | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/exp@v0.0.0-20260410095643-746e56fc9e2f?tab=licenses) |
| golang.org/x/net | v0.59.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/net@v0.59.0?tab=licenses) |
| golang.org/x/sync | v0.23.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/sync@v0.23.0?tab=licenses) |
| golang.org/x/sys | v0.48.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/sys@v0.48.0?tab=licenses) |
| golang.org/x/term | v0.46.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/term@v0.46.0?tab=licenses) |
| golang.org/x/text | v0.42.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/text@v0.42.0?tab=licenses) |
| golang.org/x/time | v0.15.0 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/golang.org/x/time@v0.15.0?tab=licenses) |
| gvisor.dev/gvisor | v0.0.0-20260915211658-a6f909f08a72 | darwin/arm64, linux/amd64 | Apache-2.0 | [LICENSE](https://pkg.go.dev/gvisor.dev/gvisor@v0.0.0-20260915211658-a6f909f08a72?tab=licenses) |
| tailscale.com | v1.103.0-pre.0.20260916030321-a2263542f260 | darwin/arm64, linux/amd64 | BSD-3-Clause | [LICENSE](https://pkg.go.dev/tailscale.com@v1.103.0-pre.0.20260916030321-a2263542f260?tab=licenses) |
