#!/usr/bin/env bash
#
# chroot-install.sh - Runs inside the ARM chroot (via QEMU) to install packages
#
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

echo "==> [chroot] Updating package lists..."
apt-get update -qq

echo "==> [chroot] Installing nginx..."
apt-get install -y -qq --no-install-recommends nginx unzip

echo "==> [chroot] Downloading Mainsail..."
MAINSAIL_URL=$(wget -qO- https://api.github.com/repos/mainsail-crew/mainsail/releases/latest \
    | grep -oP '"browser_download_url":\s*"\K[^"]*mainsail\.zip')
mkdir -p /var/www/mainsail
wget -q -O /tmp/mainsail.zip "${MAINSAIL_URL}"
unzip -qo /tmp/mainsail.zip -d /var/www/mainsail
rm -f /tmp/mainsail.zip

echo "==> [chroot] Enabling services..."
systemctl enable nginx
systemctl enable snapmaker-moonraker

echo "==> [chroot] Setting hostname..."
echo "snapmaker" > /etc/hostname
sed -i 's/127\.0\.1\.1.*/127.0.1.1\tsnapmaker/' /etc/hosts

echo "==> [chroot] Enabling SSH..."
systemctl enable ssh

echo "==> [chroot] Setting default password for pi user..."
echo 'pi:temppwd' | chpasswd

echo "==> [chroot] Cleaning up..."
apt-get clean
rm -rf /var/lib/apt/lists/*

echo "==> [chroot] Done."
