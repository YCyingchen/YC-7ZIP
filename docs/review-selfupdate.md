# Self-update review — zip2609.002

`go vet ./...` clean. No test covers this (no `update_test.go`; nothing in the other `*_test.go` in this package
touches `/api/update*`, `/api/wallpaper`, `installFromFile`, `extractBinary`, `sniffImageExt`). Findings only; no
file changed.

**`deploy/start.sh:29` + `update.go:631` — high — every successful self-update leaves the app down.**
Self-update is permitted only in the bare-binary deployment (README:91), whose launcher runs
`setsid --fork "$DIR/yc7zip"` once: no supervisor, unit, or restart loop exists, and `SetRestart` is test-only.
`scheduleRestart` → `os.Exit(0)` (639) thus kills the process with nothing to revive it, while the UI says
正在重启 (`assets/app.js:1983`). Sequence: bare-binary apply → 200 OK → down until `start.sh` re-runs.

**`update.go:348-385`/`:589`/`:498` — high — the uploaded package runs before validation and installs whatever it
prints.** `probeVersion` execs the upload (chmod 0755 first) with uncapped `CombinedOutput` (OOM) and a timeout
killing only the direct child. Validation is hollow: `Contains(reported, version)` (498) compares the probe
against the version derived from that same probe (485) — always true — and upload passes `wantVersion=""` (377),
leaving only the tag-shaped downgrade guard (505); SHA256 is never consulted. Trigger: `POST /api/update/upload`,
`package=@x`, `x` = `#!/bin/sh\necho zip9999.999\n<cmd>`, filename without `.tgz`.

**`server.go:173` — high — CSRF on a Basic-auth API with code execution as payoff.**
`POST /api/update/upload` accepts `multipart/form-data`, a CORS simple request; browsers attach cached Basic
credentials to cross-origin form POSTs and no Origin/token check exists. Trigger: admin visits a page
auto-submitting that tarball; `POST /api/update/apply` parses no body, so any form forces a download+restart.
`YC7ZIP_AUTH` defaults empty (`main.go:84`) and `start.sh` binds `:8090` → unauthenticated LAN variant.

**`update.go:69-78` — medium — detection is `/.dockerenv` only:** containerd/k8s and podman create none, and the
fpk compose mounts `${TRIM_APPDEST}` without passing it into the container, so a pod reports `binary`,
`CanSelfUpdate=true`, and loses the "update" on reschedule.

**`update.go:573` — medium — `io.Copy(out, io.LimitReader(tr, max))` returns nil at the cap**, so a >256 MB member
is written truncated, then executed; `tar.Next` also decompresses every skipped member, so an accepted 256 MB
archive (410) forces ~250 GB of decompression with no timeout.

**`update.go:227` vs `:320` — medium — `checksumUA` is written outside the mutex (race) and never reset:** a
release without `SHA256SUMS.txt` reuses the old one's URL (baffling failure); one that adds it after `""` was
cached installs unverified (416). `updateCheckTTL` (33) is dead, so cached `assetURL`/`latest` never expire.

**`update.go:481-521` — medium — past the rename (516), `os.Chmod` (519) can still fail** and
`handleUpdateUpload:378` returns before `scheduleRestart` → 500 with a possibly-broken binary live and `.bak`
unrestored. Concurrent applies share `.yc7zip-new`, each deferring `os.Remove` (483) → spurious "替换失败" after
the swap. Ordering is otherwise sound: nothing leaves the host without *a* binary.

**`ui_settings.go:264-285` — low — no `nosniff` on the served wallpaper; `:305` opens any `Stat`ed path (a FIFO
blocks the handler).**

## Verified correct

`isVersionTag` (258-281) pins an 11-char zero-padded tag → lexicographic == numeric (198/215/330/505); release
selection, asset name (186-201, 220) and checksum parsing (452-461) match `release.yml:202-224,236`.
`probeVersion` matches `main.go:99`/CI tag. Tar safety: `hdr.Name` only via `filepath.Base`, non-regular members
skipped (563), fixed `dest` → no traversal. `staged` is in `self`'s resolved dir (474-481) → atomic
same-filesystem rename; backup first (510); write probe first (98-102); SHA256 over the exact bytes. Wallpaper
type comes from magic bytes with a fixed `wallpaper.<ext>` name, so no arbitrary file or directory escape;
download cap (405-412) and container/fpk refusal are correct.
