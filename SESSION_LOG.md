# Session Log - 2026-02-08

## Objective
Implement a Moonraker-to-Snapmaker SACP bridge in Go, following a detailed plan from a prior planning session.

## What Was Done

### 1. Research Phase
- Read the prior session transcript for implementation details (Moonraker API formats, SACP protocol, sm2uploader API).
- Explored the sm2uploader module.
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
- `go.mod` - Module definition, Go 1.22, deps: `gorilla/websocket v1.5.3`, `gopkg.in/yaml.v3 v3.0.1`.
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
- **Wrong sm2uploader version hash**: Initial `go.mod` had a guessed hash. Used `GOPROXY=direct go list -m -json` to find correct version.
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

- Committed on `main` branch: "Implement Moonraker-to-Snapmaker SACP bridge"
- Created GitHub repo and pushed to `origin/main` via SSH.

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

### Jenkins Agent Docker Image
- Created `image/Dockerfile.jenkins-agent` based on `jenkins/inbound-agent:latest`
- Installs all build dependencies: qemu-user-static, parted, e2fsprogs, xz-utils, systemd-container, wget, unzip, sudo
- Installs Go and GitHub CLI (versions parameterized via `ARG`)
- Grants jenkins user passwordless sudo for mount/chroot operations
- `--network=host` required during build to avoid DNS resolution failures in Docker

### Docker Compose Integration
The agent can be added to an existing Jenkins docker-compose setup:
- Service `jenkins-agent` builds from `image/Dockerfile.jenkins-agent`
- Requires `privileged: true` for losetup/mount/chroot
- Connects to controller via internal hostname `http://jenkins:8080`
- Agent secret managed via `.env` file (`JENKINS_AGENT_SECRET`)
- `depends_on: jenkins` ensures controller starts first

### Jenkins Pipeline Setup
- Create a **Pipeline** job in Jenkins UI
- Set **Definition** to "Pipeline script from SCM" pointing to the repo
- Script path: `Jenkinsfile` (repo root)
- Required credential: `github-token` (Secret text, GitHub PAT with `repo` scope)
- Required Jenkins plugins: Pipeline, Credentials Binding, Git, Workspace Cleanup

### README Update
- Added "Raspberry Pi Image Build" section with agent build instructions, local build steps, and image architecture diagram

### Git
- "Add Jenkins CI pipeline to build Raspberry Pi 3 SD card image"
- "Add Dockerfile for Jenkins agent with RPi image build dependencies"
- "Add RPi image build instructions to README"
- All pushed to `origin/main`

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

---

## Session 3 - 2026-02-09: Obico Integration Support

### Objective
Add the Moonraker APIs required for Obico (moonraker-obico) integration, enabling AI-powered print failure detection.

### Research: Obico Requirements
Analyzed the [moonraker-obico](https://github.com/TheSpaghettiDetective/moonraker-obico) source code to identify required APIs:

**Critical missing features:**
1. `server/database/item` - Obico stores `printer_id` for persistent linking
2. `server/history/list` + `notify_history_changed` - Print job tracking
3. Additional printer objects: `fan`, `heaters`, `display_status`

### What Was Done

#### Files Created (4 files, ~1,000 lines)

**`database/database.go`** - JSON-file backed key-value store:
- Organized by namespace (each namespace = separate JSON file)
- Supports dot notation for nested keys (e.g., `printer.id`)
- Thread-safe with `sync.RWMutex`
- Persists to `.moonraker_data/database/`
- Methods: `GetItem()`, `SetItem()`, `DeleteItem()`, `GetNamespace()`, `ListNamespaces()`

**`history/history.go`** - Print job history tracking:
- `Job` struct with: job_id, filename, status, start/end times, duration, filament used, metadata
- Job statuses: `in_progress`, `completed`, `cancelled`, `error`, `klippy_shutdown`
- `Manager` with: `StartJob()`, `FinishJob()`, `ListJobs()`, `GetJob()`, `DeleteJob()`
- `Totals` for cumulative statistics
- Callback support for `notify_history_changed` broadcasts
- Persists to `.moonraker_data/history/history.json`

**`moonraker/handler_database.go`** - Database API handlers:
- HTTP: `GET/POST/DELETE /server/database/item`, `GET /server/database/list`
- WebSocket JSON-RPC: `server.database.list`, `server.database.get_item`, `server.database.post_item`, `server.database.delete_item`

**`moonraker/handler_history.go`** - History API handlers:
- HTTP: `GET /server/history/list`, `GET /server/history/job`, `DELETE /server/history/job`, `GET /server/history/totals`, `POST /server/history/reset_totals`
- WebSocket JSON-RPC: `server.history.list`, `server.history.get_job`, `server.history.delete_job`, `server.history.totals`, `server.history.reset_totals`

#### Files Modified

**`moonraker/server.go`**:
- Added `database` and `history` fields to `Server` struct
- Updated `NewServer()` to accept database and history managers
- Added `History()` accessor method
- Registered database and history route handlers

**`moonraker/websocket.go`**:
- Added all database JSON-RPC methods to `handleRPC()` switch
- Added all history JSON-RPC methods
- Added `BroadcastHistoryChanged()` helper
- Added `BroadcastGCodeResponse()` helper

**`moonraker/objects.go`**:
- Added `fan` object (speed 0.0-1.0)
- Added `heaters` object (available_heaters, available_sensors lists)
- Added `display_status` object (progress, message)
- Updated `AvailableObjects()` to include new objects

**`moonraker/handler_server.go`**:
- Added "database" to loaded components list

**`printer/state.go`**:
- Added `FanSpeed` field to `StateData`
- Parse fan speed from Snapmaker status (converts 0-100% to 0.0-1.0)

**`main.go`**:
- Initialize database manager with data directory
- Initialize history manager
- Pass both to `moonraker.NewServer()`

### API Summary

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/server/database/list` | GET | List namespaces |
| `/server/database/item` | GET/POST/DELETE | Key-value CRUD |
| `/server/history/list` | GET | List print jobs (paginated) |
| `/server/history/job` | GET/DELETE | Get/delete specific job |
| `/server/history/totals` | GET | Cumulative statistics |
| `/server/history/reset_totals` | POST | Clear all history |

### Data Storage

```
.moonraker_data/
├── database/
│   ├── obico.json      # Obico stores printer_id here
│   └── mainsail.json   # Mainsail settings (if used)
└── history/
    └── history.json    # Print job records
```

### Obico Compatibility Status

| Feature | Status |
|---------|--------|
| `server/database/item` | ✅ Implemented |
| `server/history/list` | ✅ Implemented |
| `notify_history_changed` | ✅ Implemented |
| `notify_gcode_response` | ✅ Already existed |
| `fan` object | ✅ Implemented |
| `heaters` object | ✅ Implemented |
| `display_status` object | ✅ Implemented |
| Webcam config (`server/webcams`) | ❌ Not needed (manual config in Obico) |

### Other Changes

**`Jenkinsfile`**:
- Improved GitHub release artifact upload
- Separated release creation from artifact upload
- Added `--clobber` flag to allow re-uploading on pipeline reruns
- Handles case where release already exists

### Git
- "Improve GitHub release artifact upload in Jenkinsfile"
- "Add database and history APIs for Obico integration"
- Both pushed to `origin/main`

---

## Updated Next Steps

### Obico Testing
- Install moonraker-obico on a test system
- Verify database persistence works for printer linking
- Test history tracking during actual prints
- Confirm WebSocket notifications reach Obico

### Remaining from Previous Sessions
- Real printer testing (J1S)
- Reconnection logic
- Token refresh/persistence
- Pause/resume/cancel command verification

---

## Session 4 - 2026-02-09: Jenkins Tag Detection Fix

### Objective
Fix Jenkins pipeline not creating GitHub releases even when a tag exists on the commit.

### Problem
The `Publish to GitHub Release` stage was being skipped despite the tag `v0.0.2` existing on the commit. The pipeline used `when { buildingTag() }` which only returns true when Jenkins specifically triggers a build *for* a tag, not when a branch commit happens to have a tag pointing to it.

Jenkins was treating this as a branch build (`origin/main`), not a tag build.

### Solution
Replaced `buildingTag()` with a `git describe` check that detects tags regardless of how the build was triggered:

```groovy
when {
    expression {
        return sh(script: 'git describe --exact-match --tags HEAD 2>/dev/null', returnStatus: true) == 0
    }
}
```

Also added tag name detection inside the script block since `TAG_NAME` is no longer provided by Jenkins:
```bash
TAG_NAME=$(git describe --exact-match --tags HEAD)
```

### Files Modified

**`Jenkinsfile`**:
- Changed `when { buildingTag() }` to expression using `git describe --exact-match --tags HEAD`
- Added `TAG_NAME` variable extraction inside the shell script
- Added echo statement to log detected tag name

### Verification
- Recreated tag `v0.0.2` on the new commit
- Jenkins build successfully detected the tag
- GitHub release and artifact created successfully

### Git
- "Fix tag detection in Jenkinsfile to work with any build trigger"
- Pushed to `origin/main`

---

## Session 5 - 2026-02-12/13: SACP Temperature Parsing & Mainsail Dashboard Fixes

### Objective
Get Mainsail dashboard fully functional with real-time temperature data from the Snapmaker J1S printer over SACP, fix missing WebSocket/HTTP API methods that Mainsail requires, and resolve temperature parsing issues.

### Background
Previous sessions established the bridge architecture but never tested with a real printer. The J1S only supports SACP (no HTTP API on port 8080), and M105/M114 GCode commands return empty responses via SACP. This session focused on getting real temperature data flowing to the Mainsail UI using SACP binary protocol queries.

### What Was Done

#### Phase 1: Mainsail Dashboard Fixes

Fixed multiple missing API endpoints and WebSocket methods that Mainsail requires on initial load:

**`moonraker/handler_server.go`**:
- Added `GET /machine/system_info` - Returns OS info, CPU, memory, Python version
- Added `GET /machine/proc_stats` - Returns process statistics, CPU/memory usage
- Added `GET /server/webcams/list` - Returns empty webcam list
- Added `GET /server/temperature_store` - Returns temperature history (ring buffer of last 1200 readings for each sensor)
- Added `GET /server/gcode_store` - Returns recent GCode command history
- Implemented `TemperatureStore` with ring buffer for tracking temperature history over time

**`moonraker/websocket.go`**:
- Added WebSocket RPC handlers for: `server.connection.identify`, `machine.system_info`, `machine.proc_stats`, `server.webcams.list`, `server.temperature_store`, `server.gcode_store`, `server.files.get_directory`
- Fixed `klippy_connected` to return `true` when printer is connected (was hardcoded to `false`, preventing Mainsail from showing the dashboard)

**`moonraker/handler_printer.go`**:
- Fixed `printer.objects.subscribe` to return current object state (was returning empty result)

**`moonraker/handler_files.go`**:
- Added `GET /server/files/get_directory` endpoint for file browser

**`moonraker/objects.go`**:
- Added `system_stats` object (sysload, cputime, memavail)

**`files/manager.go`**:
- Added `GetDirectory()` method returning Moonraker-format directory listing with files, dirs, disk usage

#### Phase 2: SACP Binary Protocol Temperature Reading

Discovered that the J1S doesn't support M105/M114 GCode via SACP (returns empty responses). Implemented direct SACP binary protocol queries for temperature data.

**`sacp/sacp.go`**:
- Fixed `readFloat32LE()` - Was using unsafe pointer cast, replaced with `math.Float32frombits()`
- Fixed `Read()` - Replaced single `conn.Read()` with `io.ReadFull()` for robustness with TCP fragmentation
- Added `WritePacket()` function - Writes SACP command packets with auto-incrementing sequence numbers
- Added `ParseExtruderInfo()` - Parses extruder query response (CommandSet 0x10, CommandID 0xa0)
- Added `ParseBedInfo()` - Parses bed query response (CommandSet 0x14, CommandID 0xa0)
- Added `ExtruderData` and `BedZoneData` structs

**`printer/router.go`** (NEW FILE):
- `PacketRouter` - Background goroutine that reads all incoming SACP packets
- Routes command responses to waiting callers via channels keyed by sequence number
- Routes subscription/push data to a callback handler
- Cooperative stop mechanism with atomic stopped flag
- `WaitForResponse()` blocks until matching response arrives or times out

**`printer/client.go`** (major rewrite):
- Integrated `PacketRouter` for async packet handling
- Added `writeMu` mutex to serialize writes to the connection
- Added subscription data caching (`extruderData`, `bedData`) with `sync.RWMutex`
- `Connect()` creates and starts PacketRouter, triggers initial temperature query
- `QueryTemperatures()` sends one-shot queries to 0x10/0xa0 (extruder) and 0x14/0xa0 (bed)
- `sendQuery()` writes packet, waits for ACK via router, passes ACK data to subscription handler
- `handleSubscription()` parses and caches extruder/bed data, merges by HeadID
- `handleDisconnect()` clears connection state on unexpected disconnect
- `GetStatus()` returns cached temperature data
- `Upload()` stops router, performs upload, restarts router and re-queries temperatures

**`printer/state.go`**:
- Modified `poll()` to call `QueryTemperatures()` each cycle with 300ms delay for responses
- Auto-reconnect logic using `Ping()` + `Connect()` when connection is lost

#### Phase 3: Temperature Parsing Reverse Engineering

The J1S SACP temperature data format required significant reverse engineering:

**Discovery process:**
1. Initial attempt assumed float32 encoding (from SM2.0 docs) → temperatures showed 0.0°C
2. Raw hex analysis of push packets revealed uint16 LE millidegrees at specific offsets → partial success (T0 worked)
3. Research into SnapmakerController-IDEX firmware source code revealed the actual format
4. Verified against raw data samples with exact byte-level analysis

**J1S Extruder Response Format (0x10/0xa0):**
- 3-byte header: byte[0]=context, byte[1]=head_id, byte[2]=extruder_count
- 17-byte per-extruder record: index(1) + filament_status(1) + filament_enable(1) + is_available(1) + type(1) + diameter(int32 LE, 4) + cur_temp(int32 LE, 4) + target_temp(int32 LE, 4)
- Temperatures are int32 LE in millidegrees (÷1000 for °C)
- J1S sends **separate packets per nozzle**: HeadID=0 for T0 (left), HeadID=1 for T1 (right)
- The record `index` field is always 0; the **header HeadID** distinguishes nozzles
- Nozzle diameter confirmed: `90 01 00 00` = 400 → 0.4mm nozzle

**J1S Bed Response Format (0x14/0xa0):**
- 3-byte header: byte[0]=context, byte[1]=key (0x90), byte[2]=zone_count
- 7-byte per-zone record: zone_index(1) + cur_temp(int32 LE, 4) + target_temp(int16 LE, 2)
- cur_temp is int32 LE millidegrees; target_temp is int16 LE (units TBD)

**Key J1S protocol behaviors:**
- Temperature queries (0x10/0xa0, 0x14/0xa0) are **one-shot** (not periodic subscriptions)
- Each query returns an ACK response (with result byte prefix) AND a push packet (without prefix)
- The state poller re-queries every poll cycle to get fresh data

#### Phase 4: Infrastructure Fixes

**`image/chroot-install.sh`**:
- Added removal of `/etc/ssh/sshd_config.d/rename_user.conf` to suppress the "Please note that SSH may not work until a valid user has been set up" banner on RPi images

**`build-arm.ps1`** (NEW FILE):
- PowerShell script for cross-compiling ARM binary on Windows (environment variable setting differs from bash)

### Verified Results

Moonraker API returns correct data for all three heaters:
```json
{
  "extruder": {"temperature": 250.1, "target": 250.0},
  "extruder1": {"temperature": 25.7, "target": 0.0},
  "heater_bed": {"temperature": 22.7, "target": 0.0}
}
```

### Issues Encountered & Resolved

- **Cross-compilation on Windows**: `set GOOS=linux && go build` doesn't persist env vars across `&&` on Windows cmd. Solved with PowerShell build script.
- **Binary name mismatch**: Compiled as `snapmaker-moonraker` (hyphen) but systemd service expects `snapmaker_moonraker` (underscore). Resolved by copying to correct filename during deployment.
- **Temperature encoding discovery**: Went through three iterations (float32 → uint16 → int32) before finding correct format via firmware source analysis.
- **Single extruder returned**: J1S reports one extruder per packet with identity in header byte[1], not in the record index. Solved by merging packets by HeadID.
- **Exec format error on RPi**: Windows PowerShell `$env:GOOS` variable syntax required a dedicated `.ps1` script file.

### Files Created
- `printer/router.go` - PacketRouter (131 lines)
- `printer/gcode_parse.go` - M105/M114 GCode parsers (93 lines, for fallback use)
- `build-arm.ps1` - Windows ARM cross-compilation script (4 lines)

### Files Modified
- `sacp/sacp.go` - Added WritePacket, ParseExtruderInfo, ParseBedInfo, fixed Read/readFloat32LE
- `printer/client.go` - Major rewrite for PacketRouter integration and SACP subscriptions
- `printer/state.go` - Added periodic QueryTemperatures in poll loop
- `moonraker/handler_server.go` - Added machine info, temperature store, webcams, gcode store endpoints
- `moonraker/handler_printer.go` - Fixed printer.objects.subscribe
- `moonraker/handler_files.go` - Added get_directory endpoint
- `moonraker/websocket.go` - Added many missing RPC methods, fixed klippy_connected
- `moonraker/objects.go` - Added system_stats object
- `files/manager.go` - Added GetDirectory method
- `image/chroot-install.sh` - Added SSH banner removal

---

## Session 6 - 2026-02-13: HTTP Status Polling for Print Progress & Branding

### Objective
Wire up the Snapmaker HTTP status API so Mainsail shows real print progress, filename, and pause/resume/cancel buttons during prints. Also change the SACP connection identifier displayed on the printer screen.

### Background
`Client.GetStatus()` only returned SACP temperature data with a hardcoded `"IDLE"` status, even though `getStatusHTTP()` existed and returns real print state (RUNNING/PAUSED/IDLE, filename, progress, elapsed time, positions, fan speed). Mainsail always showed "standby" and never displayed print controls. Pause/resume/cancel were already implemented (M25/M24/M26 via SACP) but the buttons never appeared because the printer never reported as "printing".

### What Was Done

#### 1. `printer/client.go` — Merge HTTP status into `GetStatus()`

Updated `GetStatus()` to call `getStatusHTTP()` for the full printer status (state, progress, filename, positions, fan) and overlay SACP temperature data on top (SACP temps are more accurate from the binary protocol). Falls back to `"IDLE"` if the HTTP call fails.

The existing `StatePoller.poll()` → `parseStatus()` → `BroadcastStatusUpdate()` pipeline works unchanged — it just receives richer data now.

#### 2. `printer/state.go` — Fix state reset on print completion

Fixed three bugs in `parseStatus()` where fields never reset to zero after a print completes:

- **Progress**: Removed `v > 0` guard — now always writes, so progress resets to 0 when print finishes
- **Filename**: Clears to `""` when printer state is idle and HTTP doesn't return a fileName key
- **Duration**: Removed `v > 0` guard — always writes
- **FanSpeed**: Removed `v > 0` guard — always writes (keeps /100 conversion)

#### 3. `config.go` + `image/rootfs/.../config.yaml` — Poll interval to 5s

Changed default poll interval from 2s to 5s in both the Go default config and the deployed RPi config. The HTTP status endpoint adds latency per poll cycle, and 5s is sufficient for dashboard updates.

#### 4. `sacp/sacp.go` — SACP connection identifier

Changed the SACP connect handshake identifier from `sm2uploader` to `Moonraker Remote Control`. This string is displayed on the printer's touchscreen when the bridge connects. Updated the length prefix byte from 11 to 24 to match the new string length.

### Files Modified
- `printer/client.go` — `GetStatus()` now calls `getStatusHTTP()` + overlays SACP temps
- `printer/state.go` — `parseStatus()` always updates progress/duration/fan/filename
- `config.go` — Default `PollInterval: 5`
- `image/rootfs/home/pi/.snapmaker/config.yaml` — `poll_interval: 5`
- `sacp/sacp.go` — Connect handshake identifier string

### What Already Works (no changes needed)
- `StatePoller` polls at configured interval and calls `BroadcastStatusUpdate()` — `printer/state.go`
- `print_stats` / `virtual_sdcard` / `display_status` objects map state correctly — `moonraker/objects.go`
- Pause (M25), Resume (M24), Cancel (M26) via SACP `ExecuteGCode` — `moonraker/websocket.go`
- HTTP handlers for pause/resume/cancel — `moonraker/handler_printer.go`

### Verification
- `go build ./...` compiles clean

---

## Session 7 - 2026-02-13/17: SACP Subscription-Based Status & Image Build Fixes

### Objective
Replace the non-functional HTTP status polling with SACP subscription-based status tracking so Mainsail correctly shows print state, progress, filename, fan speed, position, and homed axes. Also fix the RPi image build failures.

### Background
Port scanning the J1S revealed only SACP port 8888 is open — **no HTTP API** (port 8080 is closed). The entire Session 6 approach of calling `getStatusHTTP()` was wrong. Required a complete reimplementation using SACP subscriptions.

### Research: SACP Status Commands

Researched SnapmakerController-IDEX firmware, Luban source, and sm2uploader to find SACP status commands:

**SACP Subscription Mechanism:**
- Subscribe via CommandSet=0x01, CommandID=0x00 with payload `[target_cmdset, target_cmdid, interval_lo, interval_hi]`
- Firmware periodically sends push packets with the target command set/ID

**Machine Status (0x01/0xA0 heartbeat):**
- 11 states: IDLE(0), STARTING(1), PRINTING(2), PAUSING(3), PAUSED(4), STOPPING(5), STOPPED(6), FINISHING(7), COMPLETED(8), RECOVERING(9), RESUMING(10)

**Other commands:**
- 0xAC/0xA0: Current print line number (uint32 LE)
- 0xAC/0xA5: Elapsed print time in seconds (uint32 LE)
- 0x10/0xA3: Fan info (head_id, fan_count, [fan_index, fan_type, speed])
- 0x01/0x30: XYZ coordinates (axis_id + position_um as int32 LE, ÷1000 for mm)
- 0xAC/0x00: File info from controller (MD5 + filename, length-prefixed)
- 0xAC/0x1A: Printing file info from screen MCU (filename + total_lines + estimated_time)

**Peer IDs:** ReceiverID 1=controller, 2=screen MCU (both accessible via same TCP connection)

### What Was Done

#### `sacp/sacp.go` — Major additions

New types and parsers:
- `MachineStatus` type with 11 states and `String()` method
- `ParseHeartbeat()` — parses 2-byte heartbeat data
- `ParseCurrentLine()` — parses 5-byte current line data (uint32 LE)
- `ParsePrintTime()` — parses 5-byte elapsed time data (uint32 LE)
- `FanData` struct and `ParseFanInfo()` — parses fan subscription data
- `CoordinateData` struct and `ParseCoordinateInfo()` — parses coordinate data (int32 LE microns)
- `PrintFileInfo` struct, `ParseFileInfo()` and `ParsePrintingFileInfo()`
- `WritePacketTo()` — sends to specific receiver IDs (controller=1, screen=2)
- Refactored `WritePacket()` to delegate to `WritePacketTo(conn, 1, ...)`

#### `printer/client.go` — Complete rewrite

New client fields: `machineStatus`, `currentLine`, `totalLines`, `printTime`, `printFilename`, `fanData`, `coordData`

**Key changes:**
- `Connect()` now calls `go c.setupSubscriptions()` instead of HTTP connect
- `setupSubscriptions()` queries temps, then subscribes to heartbeat, current line, print time, fan info via 0x01/0x00, then one-shot coordinate query
- `subscribeTo(targetCmdSet, targetCmdID, intervalMs)` sends subscription request
- `queryCoordinates()` one-shot query to 0x01/0x30
- `queryFileInfo()` queries 0xAC/0x00 from controller, then tries 0xAC/0x1A from screen MCU
- `handleSubscription()` expanded with cases for all new command types
- On heartbeat PRINTING transition: triggers `go c.queryFileInfo()`
- On IDLE/COMPLETED/STOPPED: clears all print data fields
- `GetStatus()` builds entirely from cached SACP subscription data (no HTTP at all)
- `handleDisconnect()` resets all new fields
- `Upload()` calls `setupSubscriptions()` after router restart

#### `printer/state.go` — Homed axes mapping
- Added `if v, ok := status["homed"].(bool); ok && v { sp.state.data.HomedAxes = "xyz" }`

#### `printer/http.go` — Deleted
- Removed entirely (`git rm`) — printer has no HTTP API

#### `image/chroot-install.sh` — Build dependency fixes
- Added `cmake` and `g++` to apt-get install (fixes camera-streamer build: "cmake: not found")

#### `image/build-image.sh` — Disk space fix
- Increased image expansion from 512MB to 1536MB (fixes "No space left on device" when cloning moonraker-obico)

### Live Verification (with active print)

Deployed to Pi with a print in progress:
- `print_stats.state = "printing"` ✅
- `print_stats.filename = "SealPROTO  Thickness05 0.5_1769091299430.gcode"` ✅
- `print_stats.print_duration = 5277` ✅
- `fan.speed = 0.4` ✅
- `toolhead.position = [226.729, 149.531, 2.04, 0]` ✅
- `toolhead.homed_axes = "xyz"` ✅
- State transitions verified: `IDLE -> PRINTING -> STOPPING -> IDLE` ✅

### Known Limitation
- Progress stays at 0% — screen MCU query (0xAC/0x1A) times out on J1S for touchscreen-initiated prints, so `totalLines` is unavailable for percentage calculation

### Git
- `b91769b` "Replace HTTP status polling with SACP subscriptions, fix image build"
- Tag `v0.0.5` moved to this commit

---

## Session 8 - 2026-02-17: Moonraker-Obico Compatibility Fixes

### Objective
Get moonraker-obico fully working with the bridge so the printer appears online in a self-hosted Obico server for AI-powered print failure detection.

### Background
Moonraker-obico was installed in the RPi image but had never successfully connected. Multiple issues were discovered and fixed iteratively.

### Issues Found & Fixed

#### 1. Obico config pointing to wrong server
- Default config had `url = https://app.obico.io` (cloud service)
- Updated to user's self-hosted server: `url = https://monitor.3detplus.ch`

#### 2. PYTHONPATH missing in systemd service
- `moonraker_obico` module not found — the Python package is at `/home/pi/moonraker-obico/moonraker_obico/` but nothing added the parent to `sys.path`
- Fixed: Added `Environment=PYTHONPATH=/home/pi/moonraker-obico` to service file

#### 3. Missing `/access/api_key` endpoint
- Obico calls `GET /access/api_key` to get an API key for WebSocket authentication
- The `@backoff.on_exception` decorator retried infinitely on 404, silently blocking startup
- Fixed: Added endpoint returning a static API key string

#### 4. Missing `/machine/update/status` endpoint
- Obico calls this to find installed plugins
- 404 plain text response caused `resp.json()` to crash
- Fixed: Added endpoint returning empty version_info

#### 5. Missing `connection.register_remote_method` WebSocket RPC
- Obico registers a remote method `obico_remote_event` after connecting
- Fixed: Added stub returning `"ok"`

#### 6. Database POST not accepting form-encoded data
- Obico uses `requests.post(url, data=params)` which sends form-encoded data
- Our handler only parsed JSON bodies and query params for namespace
- Fixed: Rewrote `handleDatabasePostItem()` to detect Content-Type and parse both `application/json` and form-encoded bodies, with query params as override

#### 7. Database returning `null` for missing keys
- `"value": null` in JSON → Python's `data.get('value', {})` returns `None` (key exists with null value) instead of default `{}`
- Obico then crashes on `.get('presets', {}).values()`
- Fixed: Return empty object `{}` instead of `null` for missing database keys

#### 8. File metadata returning 404 for touchscreen prints
- Obico queries metadata for the currently printing file
- Files started from the touchscreen don't exist in local storage → 404 error
- Obico crashes on `file_metadata['size']` when metadata is `None`
- Fixed: Return minimal metadata stub (`filename`, `size: 0`, `modified: 0`) instead of 404

### Files Modified
- `moonraker/server.go` — Added `/access/api_key` endpoint
- `moonraker/handler_server.go` — Added `/machine/update/status` endpoint
- `moonraker/websocket.go` — Added `connection.register_remote_method` RPC stub
- `moonraker/handler_database.go` — Form-encoded POST support, null→empty object for missing keys
- `moonraker/handler_files.go` — Metadata stub for missing files
- `image/rootfs/etc/systemd/system/moonraker-obico.service` — Added PYTHONPATH

### Verification
- Obico process running with Janus WebRTC streamer spawned ✅
- No errors in moonraker-obico log ✅
- Printer visible and online in self-hosted Obico server ✅

### Git
- `662c9c1` "Fix moonraker-obico compatibility: add missing API endpoints and fixes"

---

## Updated Status

### Working Features
- Real-time temperature monitoring (SACP binary protocol)
- Print status tracking via SACP subscriptions (state, filename, elapsed time, fan, position)
- Mainsail dashboard with live data
- File upload and print start from Mainsail
- Pause/resume/cancel from Mainsail
- Crowsnest webcam streaming
- Moonraker-obico AI print failure detection
- Jenkins CI image build pipeline

### Known Limitations
- Print progress percentage stuck at 0% (J1S doesn't provide total_lines via SACP for touchscreen prints)
- Auto-discovery not working (deferred — using static IP for now)
- SACP initial connect sometimes times out (10s) but auto-reconnect recovers
