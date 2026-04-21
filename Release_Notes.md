# Release Notes

## v1.4.0 — 2026-04-20

### Fix Mainsail Connection Stability

Mainsail previously showed periodic "disconnected" states and rapid reconnect storms, with the bridge serving ~2 `printer.objects.query` requests per second instead of receiving the single `printer.objects.subscribe` that real Moonraker expects.

- **Fix `printer.info` always returns `"ready"`** — The endpoint was returning `"printing"` when a print was active, but Mainsail treats any `klippy_state` other than `"ready"` as "Klipper is starting" and falls back to polling `printer.info` every 2 seconds without dispatching `printer/init → printer.objects.list → printer.objects.subscribe`. Actual print state is conveyed via the `print_stats` object, not `printer.info`. This single change drops `printer.objects.query` traffic by ~95%.
- **Send proper WebSocket close frame** — The WebSocket handler was closing the TCP socket without sending a `CloseMessage` frame, so Mainsail saw close code 1005 (no status), flagged it as unclean, and triggered its 1-second reconnect loop. Now sends a normal closure frame before closing the socket.
- **Add WebSocket ping/pong keepalive** — 30-second ping interval with 60-second pong timeout. Uses `WriteControl` without holding the client mutex so slow broadcasts can't starve pings.
- **Preserve cached SACP data on brief disconnects** — `handleDisconnect()` no longer zeroes cached temperatures, fan speed, position, and print state. During short SACP reconnection cycles, Mainsail continues to see the last known values instead of everything dropping to zero.
- **Log full WebSocket close reasons** — Previously filtered out "normal" closes; now logs every close reason for debugging.

### Fix Spoolman Error Loop for Touchscreen Prints

- **Attempt Spoolman tracking only once per print** — When a print started from the J1S touchscreen, the file isn't in the local gcode directory, so `StartSpoolmanTracking` failed every status poll (~every 5 seconds), spamming the logs. Tracking is now attempted once per print and skipped on failure until the next print transition.

---

## v1.3.1 — 2026-04-13

### Fix IDEX Profile Compatibility with Vendor Print Profiles

- **Add vendor inheritance to Copy/Mirror printer profiles** — The IDEX printer profiles had no `inherits` line and an empty `printer_vendor`, which meant PrusaSlicer resolved their vendor pointer to `nullptr`. Due to PrusaSlicer's vendor space isolation (Gate 1 in the 3-gate compatibility check), all Snapmaker vendor print profiles were silently hidden — not just marked incompatible, but completely invisible. Adding `inherits = Snapmaker J1 (0.4 nozzle)` propagates the Snapmaker vendor pointer through the inheritance chain, making all vendor print profiles (with speeds up to 250mm/s and 10000mm/s² acceleration) available for Copy and Mirror modes.
- **Update default print profile** — Changed `default_print_profile` from the IDEX wrapper to the vendor profile directly (`0.16 Optimal @Snapmaker J1 (0.4 nozzle)`).

---

## v1.3.0 — 2026-04-10

### Fix IDEX Copy/Mirror Mode — Both Toolheads Now Print

- **Fix `;Extruder Mode:` header value** — The V1 header wrote `Duplication` / `Mirror` but the J1S Screen MCU expects `IDEX Duplication` / `IDEX Mirror` (matching Luban and SMFix). The Screen MCU parses this header before starting a print and sends its own `SetPrintMode` SACP command to the Controller — with the wrong string, it overrode the bridge's correctly-sent mode back to Default, causing only T0 to extrude while T1 stayed idle despite heating.
- **Force both extruders in header for IDEX modes** — In Copy/Mirror mode the slicer only generates T0 commands (T1 is firmware-driven), so the post-processor now forces T1 as used and copies T0's temperature/material/retraction settings to T1's header fields.

### NFC Spoolman Daemon Compatibility

- **Silently accept `SAVE_VARIABLE` commands** — The klipper-nfc daemon sends `SAVE_VARIABLE` GCode commands when `klipper_variables=true` to push filament metadata to Klipper macros. These are now silently accepted instead of being forwarded to the Snapmaker (which would generate errors). This allows the NFC daemon to run with a single config against both Klipper and the snapmaker_moonraker bridge.

---

## v1.1.0 — 2026-03-16

### IDEX Copy & Mirror Mode Support

The Snapmaker J1S supports IDEX Copy (Duplication) and Mirror modes, where both toolheads print simultaneously. This release adds full support for activating these modes through PrusaSlicer and the upload pipeline.

#### GCode Post-Processor: IDEX Mode Detection

- **M605 detection** — The GCode scanner now detects `M605 S2` (Duplication/Copy) and `M605 S3` (Mirror) commands during metadata extraction.
- **V1 header fix** — The `;Extruder Mode:` field in the Snapmaker V1 header is now set dynamically based on the detected M605 command, instead of being hardcoded to `Default`. This was the root cause of IDEX modes not activating — the J1S HMI reads this header to set the mode before executing GCode.

#### SACP IDEX Mode Command

- **SetPrintMode (0xAC/0x0A)** — New SACP command sends the IDEX mode byte (0=Default, 2=Duplication, 3=Mirror) to the controller before starting a print, as an additional reliability measure alongside the V1 header.

#### PrusaSlicer IDEX Profiles

- **Printer profiles** — Pre-configured profiles for Copy and Mirror modes with half-bed dimensions (150×200mm), dual nozzle config, and proper M605 start/end GCode.
- **Print profiles** — 8 quality presets (0.08mm–0.28mm) inheriting from vendor J1 settings, linked to the IDEX printer profiles.
- **Physical printer profiles** — Moonraker connection profiles for both modes.
- All profiles included in `PrusaSlicer_profile/` for easy import.

---

## v1.0.0 — 2026-03-09

### Full Dual-Extruder Support

The Snapmaker J1S is an IDEX printer with two independent extruders, and the bridge now fully supports both across all subsystems.

#### Per-Extruder Spoolman Spool Tracking

- **Independent spool per extruder** — Each tool (T0/T1) can be assigned a different Spoolman spool. Mainsail's Spoolman panel now supports selecting spools per extruder.
- **Per-tool filament tracking** — Filament usage is parsed and reported per extruder. During a dual-material print, each tool's consumption is reported to its own spool independently.
- **API `tool` parameter** — `get_spool_id` and `post_spool_id` endpoints (HTTP and WebSocket) accept a `tool` parameter (default 0 for backward compatibility).
- **Legacy migration** — Existing single spool ID (`spoolman.spool_id`) is automatically migrated to tool 0 on first startup.

#### Per-Extruder Fan Control

- **Dual part fan objects** — Mainsail now shows separate `extruder_partfan` and `extruder1_partfan` fan objects with independent speed monitoring.
- **Active extruder routing** — The primary `fan` object reports the active extruder's fan speed. `M106`/`M107` commands without a `P` parameter are routed to the active extruder's fan (matching Klipper behavior).
- **M106/M107 interception** — Fan GCode commands are intercepted and routed with the correct `P` parameter for per-extruder control.
- **SET_FAN_SPEED support** — Klipper-style `SET_FAN_SPEED FAN=extruder1_partfan SPEED=0.5` commands are handled with fan name mapping.
- **Fan speed history** — Both fans are recorded in the temperature store, appearing on Mainsail's temperature graph.

---

## v0.2.1 — 2026-03-09

### Security Hardening

- **Path traversal in file move** — `MoveFile` now validates both source and destination resolve within allowed root directories (gcodes/config), matching existing checks on delete operations.
- **Database namespace injection** — Namespace names containing path separators or `..` are rejected, preventing reads/writes outside the database directory.
- **HTTP header injection** — Content-Disposition filename in file downloads is now sanitized with `filepath.Base()` and proper quoting.
- **Systemctl action validation** — Only `start`, `stop`, and `restart` are accepted as service actions. Internal error details are logged server-side instead of being returned to clients.
- **WebSocket read limit** — 1 MB maximum message size prevents memory exhaustion from oversized messages.
- **Database body limit** — POST requests to the database endpoint are limited to 1 MB.
- **Error disclosure** — File save errors and systemctl failures now return generic messages to clients with details logged server-side.

---

## v0.2.0 — 2026-03-05

### New Features

- **Dual extruder control** — Select the active tool from Mainsail by clicking on extruder/extruder1 in the temperature panel. `ACTIVATE_EXTRUDER` commands are translated to SACP tool changes (T0/T1) and the active extruder is reflected in the Mainsail UI.
- **Temperature control via Mainsail** — `SET_HEATER_TEMPERATURE`, `M104`/`M109`, `M140`/`M190`, and `TURN_OFF_HEATERS` are now intercepted and routed through the reliable SACP binary protocol instead of being forwarded as raw GCode. Setting extruder1 temperature from Mainsail now works correctly.
- **Temperature history graph** — Implemented a 1200-reading ring buffer per sensor (extruder, extruder1, heater_bed). Mainsail temperature graphs now persist when switching browser tabs and no longer blank out periodically.

### Bug Fixes

- **Extruder1 temperature refused** — Mainsail sends Klipper-specific `SET_HEATER_TEMPERATURE HEATER=extruder1 TARGET=200` which the Snapmaker doesn't understand. Now intercepted and routed through SACP `SetToolTemperature()`.
- **Active tool not reflected in UI** — `toolhead.extruder` was hardcoded to `"extruder"`. Now dynamically tracks the active extruder.
- **Temperature graphs blanking** — The `server.temperature_store` API returned empty arrays, causing Mainsail to repeatedly clear and rebuild the graph. Now returns real historical data.
- **GCode post-processor: shared E position across tools** — Absolute extrusion tracking (`lastAbsE`) was shared between both extruders, causing incorrect filament accounting in dual-extruder jobs without `G92 E0` resets between tool changes. Now tracked per-tool.
- **GCode post-processor: retraction values** — `retract_length` and `retract_length_toolchange` only parsed the first comma-separated value for both extruders. Now correctly parses per-extruder values from slicer comments.

### New Features (continued)

- **PrusaSlicer "Upload and Print"** — The `print=true` form field on file uploads is now honored. PrusaSlicer and OrcaSlicer's "Upload and Print" button now uploads the file to the bridge, sends it to the printer via SACP, and starts printing automatically.
- **Printer disconnect/connect from Mainsail** — A virtual "printer" service appears in Mainsail's service panel alongside crowsnest and moonraker-obico. Stopping the service releases the SACP connection and suppresses auto-reconnect, giving full touchscreen access for Z offset adjustments. Starting the service reconnects.
- **File metadata and history** — Mainsail's file manager now shows real modification dates and print history status (printed/cancelled/errored) per file, matching standard Moonraker behavior.

### Known Limitations

- **Z baby-stepping not supported** — The J1S firmware accepts `M290` (result code 0) but does not implement it. `SET_GCODE_OFFSET` is silently accepted to prevent Mainsail errors. Z offset can be adjusted on the touchscreen after stopping the "printer" service from Mainsail's service panel.

---

## v0.1.1 — 2026-02-27

### Bug Fixes

- **Fix print start after upload** — SACP reconnect after upload now retries up to 5 times with 2-second gaps. The J1S sometimes needs more than 3 seconds after the double-disconnect before accepting new connections; the single attempt would timeout, causing the file to upload but never start printing.

### Build & Release

- **Cross-platform binaries** — Jenkins now builds and uploads Linux x86_64, Windows x86_64, and macOS ARM64 binaries alongside the RPi image
- **Release notes from file** — Jenkins extracts release notes from `Release_Notes.md` instead of auto-generating them

### Changes Since v0.1.0

| Commit | Description |
|--------|-------------|
| `f03b186` | Fix print start after upload by retrying SACP reconnect |
| `af04cc2` | Add Release_Notes.md and use it in Jenkins pipeline |
| `2fd600c` | Build cross-platform binaries and upload to GitHub releases |

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.1.0...v0.1.1

---

## v0.1.0 — 2026-02-27

First minor release — the bridge is now fully functional with Mainsail, Obico, and Spoolman for the Snapmaker J1S.

### Highlights

- **Native SACP print control** — cancel, pause, and resume use SACP commands directly instead of HTTP, fixing reliability issues
- **HMI thumbnail support** — PrusaSlicer/OrcaSlicer thumbnails are extracted and embedded in the Snapmaker header format, appearing on the J1S touchscreen
- **Obico print monitoring** — full integration with moonraker-obico for remote print status tracking and AI failure detection
- **Mainsail config editor** — config files are browsable and editable from the Mainsail web UI

### New Features

- **GCode thumbnail extraction** — Scans uploaded GCode for PrusaSlicer thumbnail blocks, converts to single-line data URI in the Snapmaker V1 header format so thumbnails display on the HMI touchscreen
- **Config file root** — Exposes `printer_data/config/` as a Moonraker file root, enabling Mainsail's config file editor panel
- **Bridge config in printer_data** — Configuration moved from `~/.snapmaker/config.yaml` to `printer_data/config/snapmaker-moonraker.yaml`, editable directly from Mainsail

### Bug Fixes

- **Print cancel/pause/resume** — Replaced HTTP-based commands with native SACP `0xAC/0x06` (cancel), `0xAC/0x04` (pause), `0xAC/0x05` (resume) for reliable print control
- **Obico `KeyError: 'size'`** — Metadata endpoints now always return `size` and `modified` fields, preventing obico crashes on empty/missing filenames
- **Print history tracking** — State poller now records print start/finish in the history manager, with late filename arrival handling for SACP async queries
- **Janus binary permissions** — Pi image build correctly sets execute permissions on the precompiled Janus WebRTC binary for obico video streaming
- **WebSocket `get_directory` for config root** — Fixed handler to recognize `config` as a root name when passed as the `path` parameter
- **Print start after upload** — Reconnect after SACP upload now retries up to 5 times, fixing intermittent failures where the J1S needed extra time before accepting new connections

### Changes Since v0.0.8

| Commit | Description |
|--------|-------------|
| `32e2b52` | Fix print cancel/pause/resume using native SACP commands |
| `0df3530` | Add thumbnail extraction for Snapmaker HMI display |
| `607bdb8` | Fix moonraker-obico print status monitoring |
| `a7a93a5` | Fix Janus binary permissions in Pi image build |
| `039d086` | Add config file root for Mainsail config editor |
| `f03b186` | Fix print start after upload by retrying SACP reconnect |

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.8...v0.1.0

---

## v0.0.8 — 2026-02-26

### New Features

- **GCode Post-Processor** (`gcode/process.go`) — Automatically processes uploaded GCode files before sending to the printer:
  - Generates Snapmaker V1 metadata header for J1/J1S (required for HMI touchscreen to display files)
  - Legacy V0 header format for A150/A250/A350/Artisan models
  - Tool number remapping (T2/T3 → T0/T1) for dual-extruder configs from PrusaSlicer/OrcaSlicer
  - Unused nozzle shutoff — inserts `M104 S0` when a tool is no longer needed
- **Print Auto-Start** — Files uploaded from Mainsail now automatically start printing via the `StartScreenPrint` SACP command (`0xB0/0x08`), eliminating the need to start prints from the touchscreen
- **Line-based Spoolman Tracking** — Filament usage tracking now uses per-line extrusion data from the GCode for accurate spool consumption reporting

### Bug Fixes

- **Fix StartScreenPrint timing** — StartScreenPrint was being sent after the SACP disconnect, causing the screen MCU to ignore it. Now sent on a fresh connection after the upload+disconnect cycle completes
- **Non-blocking print start** — The `printer.print.start` RPC handler now runs the upload in a background goroutine so Mainsail's UI doesn't freeze during the ~15 second upload process
- **Fix print start for subdirectory files** — Use `filepath.Base()` to strip subdirectory paths; the J1S stores files flat and paths confuse the HMI file index
- **HMI file visibility** — Double-disconnect pattern (matching sm2uploader) ensures the HMI properly finalizes and indexes uploaded files

### Upload Sequence (v0.0.8)

1. Stop PacketRouter (exclusive TCP access)
2. Process GCode (V1 header, tool remapping, nozzle shutoff)
3. Upload file chunks via SACP
4. Disconnect #1 (signals HMI to finalize file)
5. Disconnect #2 + close TCP
6. Wait 3s for HMI indexing
7. Reconnect fresh SACP connection
8. StartScreenPrint → print begins

### Files Changed

| File | Change |
|------|--------|
| `gcode/process.go` | **New** — GCode post-processor with V1/V0 header generation, tool remapping, nozzle shutoff |
| `printer/client.go` | Upload flow with double disconnect, StartScreenPrint on fresh connection, `uploading` flag |
| `sacp/sacp.go` | `StartScreenPrint()` function, first disconnect inside `StartUpload` |
| `moonraker/handler_printer.go` | Non-blocking print start handler |
| `printer/state.go` | Poller keeps `Connected=true` during upload |
| `spoolman/spoolman.go` | Line-based filament tracking |
| `files/manager.go` | `ParseFilamentByLine()` for per-line extrusion data |

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.7...v0.0.8

---

## v0.0.7 — 2026-02-18

### Spoolman Integration

- Full Moonraker Spoolman proxy API for Mainsail's built-in Spoolman panel
- Browse and select spools, show active spool on dashboard, material mismatch warnings
- Active spool ID persisted in database across restarts
- Progress-based filament usage tracking for bridge-started prints (reports deltas to Spoolman)
- Health checking with 30s polling and WebSocket status notifications
- New config option: `spoolman.server` (empty = disabled)

### Bug Fixes

- **Fix position updates during printing** — X/Y/Z coordinates were only queried once at connection time and went stale. Now polled every cycle alongside temperatures.
- **Fix HELP/? console commands** — These Klipper console commands were sent to the Snapmaker which ignores them. Now intercepted and return a help message listing supported bridge commands.
- **GCode errors in console** — Failed GCode commands now show error messages in Mainsail's console instead of being silently swallowed.

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.6...v0.0.7

---

## v0.0.6 — 2026-02-18

### Moonraker-Obico Compatibility

- Add `/access/api_key` endpoint (Obico blocked on infinite retry without it)
- Add `/machine/update/status` endpoint (prevents JSON parse crash)
- Add `connection.register_remote_method` WebSocket RPC stub
- Fix database POST to accept form-encoded data (Obico uses `requests.post(data=)`)
- Return empty object instead of `null` for missing database keys (fixes Obico `.get()` crash)
- Return minimal metadata stub for files not in local storage (touchscreen-started prints)
- Add PYTHONPATH to moonraker-obico systemd service

### Mainsail Improvements

- Bump `software_version` to `v0.13.0-snapmaker_moonraker` (suppresses "Update Klipper" warning)
- Expand `gcode.commands` from 9 to 25+ commands for console autocomplete
- Add service management API (`/machine/services/restart|stop|start`) for Mainsail power menu (crowsnest, moonraker-obico)

### Camera Module 3 Support

- Install libcamera-dev and ffmpeg dev packages in RPi image build
- Build camera-streamer with libcamera support (RPi 3 compatible flags)
- Default crowsnest config uses camera-streamer with IMX708 device path
- Add `crowsnest-usb.conf` alternative config for USB webcams

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.5...v0.0.6

---

## v0.0.5 — 2026-02-16

### SACP-Based Print Status Tracking

- Replaced non-functional HTTP status polling with native SACP subscription-based status tracking
- Mainsail now correctly shows printer state (printing/paused/standby), filename, elapsed time, fan speed, toolhead position, and homed axes during prints
- Pause, resume, and cancel buttons now appear in Mainsail during active prints
- Subscribes to heartbeat, print line, print time, fan info, and coordinate data via SACP protocol
- Automatically queries file info when a print starts and clears state when it completes

### Mainsail File Manager Fixes

- Fixed file uploads placing files in wrong directories
- Added gcode printer object so Mainsail initializes file management correctly
- Added file move/copy, directory creation, and file deletion via WebSocket API
- Fixed subdirectory browsing in the file manager
- Fixed gcode directory path configuration

### Moonraker-Obico Integration

- Added [Obico](https://www.obico.io/) (AI-powered print failure detection) to the RPi image
- Includes systemd service for automatic startup

### Service Management API

- Added `/machine/services/restart`, `/machine/services/stop`, `/machine/services/start` endpoints
- Enables restarting services directly from Mainsail's interface

### RPi Image Build Fixes

- Added missing cmake and g++ build dependencies for camera-streamer/crowsnest
- Increased image size from 512MB to 1536MB to accommodate all packages

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.4...v0.0.5

---

## v0.0.4 — 2026-02-13

### Print Status in Mainsail

- Live print progress: Mainsail now shows real-time print state (printing/paused/idle), filename, progress bar, and elapsed time during prints
- Pause/Resume/Cancel buttons: These now appear in Mainsail during prints (they were already implemented but never shown because the printer always reported as idle)
- `GetStatus()` now fetches real status from the Snapmaker HTTP API (`/api/v1/status`) and merges it with SACP temperature data
- Fixed state reset bugs where progress, duration, fan speed, and filename never returned to zero after a print completed

### SACP Temperature Parsing & Mainsail Dashboard

- Implemented direct SACP binary protocol queries for temperature data (the J1S doesn't support M105/M114 via SACP)
- Reverse-engineered J1S extruder and bed temperature packet formats (int32 LE millidegrees)
- Added PacketRouter for async SACP packet handling with sequence-based response routing
- Added missing Moonraker API endpoints required by Mainsail: `machine/system_info`, `machine/proc_stats`, `server/temperature_store`, `server/gcode_store`, `server/webcams/list`, `server/files/get_directory`
- Fixed `klippy_connected` returning false (was preventing Mainsail from showing the dashboard)

### Webcam Streaming

- Added crowsnest webcam streaming to the RPi image (ustreamer-based MJPEG streaming)
- Webcam accessible at `/webcam/` via nginx proxy
- Fixed crowsnest installation to use the project's default path (`~/crowsnest`) with its own ustreamer build
- Fixed systemd service startup (EnvironmentFile variable expansion issue)

### Other Changes

- SACP connection now identifies as "Moonraker Remote Control" on the printer's touchscreen (was "sm2uploader")
- Status poll interval changed from 2s to 5s
- Set default password `temppwd` for pi user in RPi image
- Added libbsd-dev and xxd to image build dependencies

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.3...v0.0.4

---

## v0.0.3 — 2026-02-10

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.2...v0.0.3

---

## v0.0.2 — 2026-02-09

### What's Changed

**Features**
- Add database and history APIs for Obico integration (a6bbf53)

**CI/Build**
- Improve GitHub release artifact upload in Jenkinsfile (c8578c3)
- Fix tag detection in Jenkinsfile to work with any build trigger (8f40edc)

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/compare/v0.0.1...v0.0.2

---

## v0.0.1 — 2026-02-09

### Initial release

This first release is really a test-case to validate all elements of the build:
- the moonraker to snapmaker J1 bridge
- the docker image build agent
- the Jenkins CI configuration
- the artifact storage on Github.

**Full Changelog**: https://github.com/goeland86/snapmaker_moonraker/commits/v0.0.1
