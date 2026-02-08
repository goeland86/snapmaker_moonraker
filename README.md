# snapmaker_moonraker

A Moonraker-compatible bridge to control the Snapmaker J1S via standard Klipper frontends (Mainsail, Fluidd).

## What it does

This application exposes a [Moonraker](https://moonraker.readthedocs.io/)-compatible HTTP/WebSocket API on port 7125 and communicates with a Snapmaker J1S printer using the SACP (Snapmaker Application Communication Protocol) over TCP and the Snapmaker HTTP API. This lets you use Mainsail, Fluidd, or any other Klipper frontend to monitor and control your J1S.

```
[Mainsail/Fluidd] <--HTTP/WebSocket--> [Bridge :7125] <--SACP TCP:8888 / HTTP:8080--> [J1S Printer]
```

## Features

- Printer status monitoring (temperatures, position, print progress)
- Temperature control (dual extruders + heated bed)
- GCode execution
- File management (upload, list, download, delete gcode files)
- Print control (start, pause, resume, cancel)
- Emergency stop
- Printer discovery via UDP broadcast
- WebSocket JSON-RPC with object subscriptions and live status updates

## Building

Requires Go 1.22 or later.

```bash
go build -o snapmaker_moonraker .
```

## Configuration

Copy and edit `config.yaml`:

```yaml
server:
  host: "0.0.0.0"
  port: 7125

printer:
  ip: "192.168.1.100"    # Your Snapmaker J1S IP address
  token: ""               # Authentication token (confirmed at printer HMI)
  model: "Snapmaker J1S"
  poll_interval: 2        # Status poll interval in seconds

files:
  gcode_dir: "gcodes"    # Local directory for gcode file storage
```

## Running

```bash
./snapmaker_moonraker -config config.yaml
```

### Discover printers on the network

```bash
./snapmaker_moonraker -discover
```

### Verify it's working

```bash
curl http://localhost:7125/server/info
curl http://localhost:7125/printer/info
```

## Raspberry Pi Image Build

The CI pipeline can build a self-contained Raspberry Pi 3 SD card image with Mainsail and snapmaker_moonraker pre-installed. Flash the image, boot, and control your J1S from a browser.

### Building the Jenkins agent

The Jenkins agent Docker image includes all dependencies needed to build the RPi image (Go, QEMU, parted, etc.):

```bash
docker build --network=host -t snapmaker-jenkins-agent -f image/Dockerfile.jenkins-agent .
```

### Building the image locally

Cross-compile the binary, then run the image build script:

```bash
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="-s -w" -o snapmaker_moonraker-armv7 .
sudo image/build-image.sh snapmaker_moonraker-armv7
```

This produces a `snapmaker-moonraker-rpi3-YYYYMMDD.img.xz` file ready to flash to an SD card.

### What's on the image

```
[Browser] → [nginx :80] → [Mainsail static files]
                        → proxy_pass → [snapmaker_moonraker :7125]
                                            ↓
                                  [Snapmaker J1S via SACP/HTTP]
```

- Raspberry Pi OS Lite (Bookworm, 32-bit)
- nginx serving Mainsail on port 80
- snapmaker_moonraker as a systemd service on port 7125
- SSH enabled, hostname `snapmaker`

## SACP Protocol

The SACP implementation in the `sacp/` package is adapted from source code in the following projects:

- **[sm2uploader](https://github.com/macdylan/sm2uploader)** by [@macdylan](https://github.com/macdylan) (MIT License) - Go-based Snapmaker file uploader providing the SACP packet encoding/decoding, TCP connection handshake, file upload protocol, temperature/homing commands, and UDP printer discovery.
- **[snapmaker-sm2uploader](https://github.com/kanocz/snapmaker-sm2uploader)** by [@kanocz](https://github.com/kanocz) - Original SACP protocol implementation in Go from which sm2uploader's SACP code derives.

The protocol code has been vendored into our `sacp/` package since sm2uploader is a standalone program (`package main`) and cannot be imported as a Go library.

## License

MIT License - see [LICENSE](LICENSE).

## AI Disclosure

This project was developed with assistance from Claude (Anthropic). The architecture, code structure, and implementation were produced through human-AI collaboration.
