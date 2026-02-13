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
    git make gcc libc6-dev libjpeg62-turbo-dev libevent-dev v4l-utils crudini

echo "==> [chroot] Downloading Mainsail..."
MAINSAIL_URL=$(wget -qO- https://api.github.com/repos/mainsail-crew/mainsail/releases/latest \
    | grep -oP '"browser_download_url":\s*"\K[^"]*mainsail\.zip')
mkdir -p /var/www/mainsail
wget -q -O /tmp/mainsail.zip "${MAINSAIL_URL}"
unzip -qo /tmp/mainsail.zip -d /var/www/mainsail
rm -f /tmp/mainsail.zip

echo "==> [chroot] Building ustreamer from source..."
git clone --depth 1 https://github.com/pikvm/ustreamer.git /tmp/ustreamer
make -C /tmp/ustreamer -j$(nproc)
cp /tmp/ustreamer/ustreamer /usr/local/bin/ustreamer
chmod 755 /usr/local/bin/ustreamer
rm -rf /tmp/ustreamer

echo "==> [chroot] Installing crowsnest..."
git clone --depth 1 https://github.com/mainsail-crew/crowsnest.git /opt/crowsnest

echo "==> [chroot] Creating printer_data directories..."
mkdir -p /home/pi/printer_data/{config,logs,systemd}

echo "==> [chroot] Enabling services..."
systemctl enable nginx
systemctl enable snapmaker-moonraker
systemctl enable crowsnest

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
