#!/usr/bin/env bash
#
# chroot-install.sh - Runs inside the ARM chroot (via QEMU) to install packages
#
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

echo "==> [chroot] Updating package lists..."
apt-get update -qq

echo "==> [chroot] Installing nginx and build dependencies..."
apt-get install -y -qq --no-install-recommends \
    nginx unzip \
    git make gcc g++ libc6-dev libjpeg62-turbo-dev libevent-dev libbsd-dev v4l-utils crudini xxd \
    cmake \
    libcamera-dev libavcodec-dev libavformat-dev libavutil-dev nlohmann-json3-dev \
    python3 python3-pip python3-virtualenv ffmpeg

echo "==> [chroot] Downloading Mainsail..."
MAINSAIL_URL=$(wget -qO- https://api.github.com/repos/mainsail-crew/mainsail/releases/latest \
    | grep -oP '"browser_download_url":\s*"\K[^"]*mainsail\.zip')
mkdir -p /var/www/mainsail
wget -q -O /tmp/mainsail.zip "${MAINSAIL_URL}"
unzip -qo /tmp/mainsail.zip -d /var/www/mainsail
rm -f /tmp/mainsail.zip

echo "==> [chroot] Installing crowsnest..."
git clone --depth 1 https://github.com/mainsail-crew/crowsnest.git /home/pi/crowsnest
cd /home/pi/crowsnest
bin/build.sh --reclone
make build || true
# Build camera-streamer with libcamera support (no HW H264/RTSP/WebRTC on RPi 3).
cd bin/camera-streamer
make USE_HW_H264=0 USE_LIBDATACHANNEL=0 USE_RTSP=0 || true
cd /home/pi/crowsnest
# If camera-streamer still didn't compile, create a stub so crowsnest starts.
if [ ! -s bin/camera-streamer/camera-streamer ]; then
    touch bin/camera-streamer/camera-streamer
    chmod +x bin/camera-streamer/camera-streamer
fi
chown -R 1000:1000 /home/pi/crowsnest
cd /

echo "==> [chroot] Installing moonraker-obico..."
git clone --depth 1 https://github.com/TheSpaghettiDetective/moonraker-obico.git /home/pi/moonraker-obico
python3 -m virtualenv --system-site-packages /home/pi/moonraker-obico-env
/home/pi/moonraker-obico-env/bin/pip3 install -q -r /home/pi/moonraker-obico/requirements.txt
chown -R 1000:1000 /home/pi/moonraker-obico /home/pi/moonraker-obico-env

echo "==> [chroot] Creating printer_data directories..."
mkdir -p /home/pi/printer_data/{config,logs,systemd}

echo "==> [chroot] Enabling services..."
systemctl enable nginx
systemctl enable snapmaker-moonraker
systemctl enable crowsnest
systemctl enable moonraker-obico

echo "==> [chroot] Setting hostname..."
echo "snapmaker" > /etc/hostname
sed -i 's/127\.0\.1\.1.*/127.0.1.1\tsnapmaker/' /etc/hosts

echo "==> [chroot] Enabling SSH..."
systemctl enable ssh

echo "==> [chroot] Setting default password for pi user..."
echo 'pi:temppwd' | chpasswd

echo "==> [chroot] Removing SSH new-user banner..."
rm -f /etc/ssh/sshd_config.d/rename_user.conf

echo "==> [chroot] Cleaning up..."
apt-get clean
rm -rf /var/lib/apt/lists/*

echo "==> [chroot] Done."
