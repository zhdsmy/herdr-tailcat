# Captured connect lines

herdr-core runs a `--via` target as `sh -c "<target> \"$1\"" sh <line>`, where `<line>` is the shell command core would otherwise run over SSH. `herdr-tailcat via` accepts only a line equal to the template in `internal/line` (built from the core source, with the session name as the only variable part) and exits 255 for anything else.

These files are real lines captured from each herdr-core release this project was validated against, for the default session and for `--session work`. `internal/line` tests check that every capture equals the template and that small mutations are rejected. The lines are identical across 0.1.16 and 0.1.17; both are kept so each validated release has its own evidence.

`scripts/capture-lines.py` regenerates the directory for the installed core in a scratch session. It writes `testdata/lines/v<version>/` itself and stops if the capture differs from v0.1.16. It currently accepts only 0.1.16 and 0.1.17. Supporting a new release means extending that check and the version list in `internal/line/line_test.go`; a changed line format needs a template change, not a looser match.
