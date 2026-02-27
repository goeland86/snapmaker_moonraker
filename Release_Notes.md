# Release Notes

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
