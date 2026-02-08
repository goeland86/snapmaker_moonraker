# Session Log - 2026-02-08

## Objective
Implement a Moonraker-to-Snapmaker SACP bridge in Go, following a detailed plan from a prior planning session (transcript: `/home/john/.claude/projects/-home-john-github-snapmaker-moonraker/74f06222-ec0b-45d6-b4d4-8f9c94a2f64a.jsonl`).

## What Was Done

### 1. Research Phase
- Read the prior session transcript for implementation details (Moonraker API formats, SACP protocol, sm2uploader API).
- Explored the sm2uploader module at `~/go/pkg/mod/github.com/macdylan/sm2uploader@v0.0.0-20240922063303-278df6b7698f/`.
- Discovered sm2uploader is `package main` (a standalone program), **not importable as a Go library**. This was a key finding that changed the approach.

### 2. Implementation

#### Files Created (21 files total, 2,739 lines)

**Root package (`package main`):**
- `main.go` - Entry point: config loading, printer connection, state poller, graceful shutdown (SIGINT/SIGTERM), `--discover` flag for printer discovery mode.
- `config.go` - `Config` struct with `ServerConfig`, `PrinterConfig`, `FilesConfig`. YAML loading via `gopkg.in/yaml.v3`. Resolves relative gcode dir paths.
- `config.yaml` - Template config: server host/port, printer IP/token/model/poll_interval, files gcode_dir.

**`sacp/` - Vendored SACP Protocol:**
- `sacp.go` - Full SACP binary protocol: `Packet` type with `Encode()`/`Decode()`, CRC8 header checksum, U16 data checksum, `Connect()` (TCP:8888 handshake), `Read()`, `SendCommand()`, `Disconnect()`, `SetToolTemperature()`, `SetBedTemperature()`, `Home()`, `StartUpload()` (chunked file transfer with MD5). Thread-safe sequence counter.
- `discover.go` - UDP broadcast discovery on port 20054: `Discover()`, `ParsePrinter()` (parses `ID@IP|model:X|status:Y|SACP:1` format), `getBroadcastAddresses()`.

**`printer/` - Printer Client Layer:**
- `client.go` - `Client` struct wrapping SACP connection with `sync.Mutex`. Methods: `Connect()`, `Disconnect()`, `Connected()`, `Home()`, `SetToolTemperature()`, `SetBedTemperature()`, `Upload()`, `UploadFile()`, `ExecuteGCode()` (via HTTP), `GetStatus()` (via HTTP), `Ping()`.
- `http.go` - Snapmaker HTTP API helpers (port 8080): `executeGCodeHTTP()` (`POST /api/v1/execute_code`), `getStatusHTTP()` (`GET /api/v1/status`), `connectHTTP()` (`POST /api/v1/connect`).
- `state.go` - `StateData` (copy-safe value type) with temperatures, position, print progress, homing, speed factors. `State` wraps `StateData` with `sync.RWMutex` and `Snapshot()` method. `StatePoller` polls printer status periodically, parses Snapmaker status fields (t0Temp, t1Temp, heatbedTemp, progress, etc.).
- `discovery.go` - Thin wrapper: `Discover()` calls `sacp.Discover()` and maps to `DiscoveredPrinter` structs.

**`moonraker/` - Moonraker-Compatible API Server:**
- `server.go` - `Server` struct with `net/http.ServeMux`, CORS middleware (`Access-Control-Allow-Origin: *`), route registration. `GET /{$}` root, `GET /access/info`, `GET /websocket` for WebSocket upgrade. Exposes `Hub()` for external status broadcasts.
- `handler_server.go` - `GET /server/info` (klippy_connected, klippy_state, components, moonraker_version, api_version), `GET /server/config`, `POST /server/restart`. Shared `writeJSON()` helper.
- `handler_printer.go` - `GET /printer/info` (state, hostname, software_version), `GET /printer/objects/list`, `GET|POST /printer/objects/query` (supports query string and JSON body), `POST /printer/gcode/script`, `POST /printer/print/start` (reads file from storage, uploads to printer), `POST /printer/print/pause|resume|cancel` (M25/M24/M26), `POST /printer/emergency_stop` (M112).
- `handler_files.go` - `GET /server/files/list`, `GET /server/files/metadata`, `POST /server/files/upload` (multipart, 512MB max), `DELETE /server/files/{root}/{path...}`, `GET /server/files/{root}/{path...}` (download), `GET /server/files/roots`. Broadcasts `notify_filelist_changed` on upload/delete.
- `websocket.go` - WebSocket JSON-RPC 2.0 handler. `WSHub` manages clients, `WSClient` tracks subscriptions. Handles methods: `server.info`, `server.connection.identify`, `printer.info`, `printer.objects.list`, `printer.objects.query`, `printer.objects.subscribe`, `printer.gcode.script`, `printer.print.*`, `printer.emergency_stop`, `server.files.list`, `server.files.metadata`. Broadcasts: `notify_status_update`, `notify_gcode_response`, `notify_klippy_ready`, `notify_filelist_changed`.
- `objects.go` - `PrinterObjects` builds Klipper-compatible object tree from `StateData`. Objects: `toolhead` (position, homed_axes, velocity limits, axis bounds 325x325x340), `extruder`/`extruder1` (temp, target, power, can_extrude), `heater_bed`, `gcode_move` (speed/extrude factors), `print_stats` (state mapping: idle->standby, printing, paused, error), `virtual_sdcard` (progress, is_active), `webhooks` (ready/shutdown).

**`files/` - File Management:**
- `manager.go` - `Manager` with configurable gcode directory. `ListFiles()` walks directory tree, `GetMetadata()` extracts slicer info from gcode comments, `SaveFile()` with parent dir creation, `ReadFile()`, `DeleteFile()` with path traversal protection. `extractGCodeMeta()` scans first/last 8KB for slicer, estimated_time, filament, layer height.

**Other:**
- `go.mod` - Module `github.com/john/snapmaker_moonraker`, Go 1.22, deps: `gorilla/websocket v1.5.3`, `gopkg.in/yaml.v3 v3.0.1`.
- `go.sum` - Auto-generated.
- `LICENSE` - MIT.
- `README.md` - Architecture diagram, features, build/run instructions, SACP attribution (sm2uploader + kanocz), AI disclosure.
- `.gitignore` - Excludes binary and gcodes/.

### 3. Key Design Decisions

1. **Vendored SACP instead of importing sm2uploader** - sm2uploader is `package main`, cannot be imported. All SACP protocol code was adapted into local `sacp/` package.
2. **Dual protocol approach** - SACP over TCP:8888 for connection/upload/temperature/homing; Snapmaker HTTP API on port 8080 for gcode execution (`/api/v1/execute_code`) and status polling (`/api/v1/status`).
3. **State/StateData split** - `State` has `sync.RWMutex` for thread safety; `StateData` is a plain struct safe to copy by value. `Snapshot()` returns `StateData`. This resolved `go vet` warnings about copying mutex values.
4. **Go 1.22+ enhanced routing** - Used `GET /{$}` for exact root match and method-prefixed patterns (`GET /server/info`, `POST /printer/gcode/script`). Fixed a conflict between `GET /` and `/websocket` by using `GET /{$}` and `GET /websocket`.

### 4. Issues Encountered & Resolved

- **sm2uploader not importable**: Discovered at build time. Resolved by vendoring SACP code.
- **Wrong sm2uploader version hash**: Initial `go.mod` had a guessed hash. Used `GOPROXY=direct go list -m -json` to find correct version `v0.0.0-20240922063303-278df6b7698f`.
- **go vet mutex copy warnings**: `State` contained `sync.RWMutex` and was passed by value. Resolved by splitting into `State` (with mutex) and `StateData` (plain data).
- **Route conflict**: `GET /` conflicted with `/websocket` in Go 1.22+ ServeMux. Resolved by changing to `GET /{$}` (exact match) and `GET /websocket`.

### 5. Verification

- `go vet ./...` passes clean.
- `go build` compiles successfully.
- Server starts on :7125 in offline mode (no printer configured).
- All HTTP endpoints tested and return valid JSON:
  - `GET /server/info` - Returns klippy_state, components, moonraker_version
  - `GET /printer/info` - Returns state, hostname, software_version
  - `GET /printer/objects/list` - Returns 8 object names
  - `GET /printer/objects/query?toolhead&extruder=temperature,target` - Returns filtered object data
  - `GET /server/files/list` - Returns empty file list
  - `GET /server/files/roots` - Returns gcodes root with path
  - `GET /` - Returns bridge identifier

### 6. Git & GitHub

- Committed as `365fe89` on `main` branch: "Implement Moonraker-to-Snapmaker SACP bridge"
- Created GitHub repo: https://github.com/goeland86/snapmaker_moonraker
- Pushed to `origin/main` via SSH.

## Session 2 - 2026-02-08: Jenkins CI Pipeline for RPi 3 Image Build

### Objective
Create a Jenkins CI pipeline that builds a Raspberry Pi 3 SD card image with Mainsail (web UI) and the snapmaker_moonraker bridge pre-installed. Users flash the image and immediately control a Snapmaker J1S from a browser.

### What Was Done

#### Files Created (7 files, 335 lines)

**`Jenkinsfile`** - Declarative pipeline with 3 stages:
- **Build Go Binary** - Cross-compiles with `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0`, stripped symbols (`-ldflags="-s -w"`)
- **Build RPi Image** - Runs `sudo image/build-image.sh` with the ARM binary
- **Publish to GitHub Release** - Only triggers on tag builds (`when { buildingTag() }`), uses `gh release create`
- Post-build: archives `.img.xz` artifact and cleans workspace

**`image/build-image.sh`** - Main image builder (runs as root on Jenkins agent):
1. Downloads RPi OS Lite Bookworm 32-bit (2024-11-19 release)
2. Decompresses and expands image by 512MB
3. Grows root partition with `parted resizepart` + `resize2fs`
4. Sets up loop device, mounts boot + root partitions
5. Copies `qemu-arm-static` for ARM chroot emulation
6. Mounts proc/sys/dev/pts and runs `chroot-install.sh` inside ARM environment
7. Installs cross-compiled binary to `/opt/snapmaker-moonraker/`
8. Copies rootfs overlay files (nginx config, systemd service, config, hostname)
9. Creates `/home/pi/gcodes` directory, fixes ownership to UID 1000
10. Shrinks with PiShrink, compresses with `xz -T0 -9`
11. Full cleanup trap on EXIT (unmounts, loop device detach)

**`image/chroot-install.sh`** - Runs inside ARM chroot (via QEMU):
- Installs nginx + unzip via apt
- Downloads latest Mainsail release zip from GitHub API
- Extracts to `/var/www/mainsail`
- Enables nginx, snapmaker-moonraker, and SSH services
- Sets hostname to `snapmaker` in `/etc/hostname` and `/etc/hosts`
- Cleans apt cache

**`image/rootfs/` overlay files:**
- `etc/nginx/sites-available/mainsail` - Serves Mainsail static files at `/`, proxies `/printer/`, `/server/`, `/access/`, `/machine/`, `/api/` and `/websocket` to port 7125 with WebSocket upgrade support, 512MB upload limit
- `etc/systemd/system/snapmaker-moonraker.service` - Runs as pi user with restart-on-failure, after network-online.target
- `home/pi/.snapmaker/config.yaml` - Default config with gcode dir `/home/pi/gcodes`
- `etc/hostname` - Set to `snapmaker`

### Architecture

```
Jenkins Pipeline:
  1. Cross-compile snapmaker_moonraker for ARMv7
  2. Download Raspberry Pi OS Lite (32-bit)
  3. Mount image, chroot into it
  4. Install: nginx, Mainsail static files, snapmaker_moonraker binary
  5. Configure: systemd service, nginx reverse proxy, default config
  6. Shrink + compress image
  7. Upload to GitHub Releases (on tag only)
```

Final image stack on the Pi:
```
[Browser] → [nginx :80] → [Mainsail static files]
                        → proxy_pass /websocket, /printer/*, /server/* → [snapmaker_moonraker :7125]
                                                                              ↓
                                                                    [Snapmaker J1S via SACP/HTTP]
```

### Jenkins Agent Requirements
- Linux (Debian/Ubuntu preferred)
- Go 1.22+
- `qemu-user-static` (ARM chroot emulation)
- `parted`, `e2fsprogs`, `xz-utils`, `systemd-container`
- `gh` CLI (GitHub Releases)
- Root/sudo access (mount/chroot)
- ~10GB free disk space

### Verification
- ARM cross-compilation confirmed: `file` shows `ELF 32-bit LSB executable, ARM, EABI5, statically linked, stripped`
- All scripts have executable permissions

### Git
- Committed as `1f51953` on `main`: "Add Jenkins CI pipeline to build Raspberry Pi 3 SD card image"
- Pushed to `origin/main`

---

## Next Steps (from the plan)

### Phase 2: Printer State + Control (partially done, needs real printer testing)
- Test with actual J1S printer on network
- Verify status polling parses real Snapmaker status responses correctly
- Test temperature control, homing, gcode execution end-to-end
- Tune status field mappings based on actual firmware responses

### Phase 3: File Management + Print Control (partially done)
- Test file upload to printer + print start workflow
- Verify pause/resume/cancel with correct Snapmaker gcode commands (M25/M24/M26 may not be correct for SACP printers)

### Phase 4: Polish
- Reconnection logic (auto-reconnect on SACP disconnect)
- Token refresh/persistence (tokens invalidate on printer power cycle)
- ~~Systemd service file for Pi deployment~~ (done in Session 2 - Jenkins CI)
- Better error handling and user-facing error messages
- Printer discovery as a subcommand with interactive selection

### Known Limitations
- No authentication on the Moonraker API (Moonraker itself doesn't require auth by default)
- GCode execution and status polling use HTTP API (port 8080), not SACP - requires both ports accessible
- Token management is manual (must be confirmed at printer HMI)
- Print pause/resume/cancel gcode commands (M25/M24/M26) may need adjustment for Snapmaker firmware
- No reconnection logic yet - if SACP connection drops, server continues but printer commands fail
