#!/usr/bin/env bash
#
# build-image.sh - Build a Raspberry Pi 3 SD card image with Mainsail + snapmaker_moonraker
#
# Usage: sudo ./image/build-image.sh <path-to-armv7-binary>
#
# Requirements: qemu-user-static, parted, e2fsprogs, xz-utils, systemd-container, wget
#
set -euo pipefail

BINARY="${1:?Usage: $0 <snapmaker_moonraker-armv7-binary>}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORK_DIR="$(pwd)/image-build"
DATE="$(date +%Y%m%d)"
OUTPUT_NAME="snapmaker-moonraker-rpi3-${DATE}"

# Raspberry Pi OS Lite image
RPI_IMAGE_URL="https://downloads.raspberrypi.com/raspios_lite_armhf/images/raspios_lite_armhf-2024-11-19/2024-11-19-raspios-bookworm-armhf-lite.img.xz"
RPI_IMAGE_XZ="$(basename "${RPI_IMAGE_URL}")"
RPI_IMAGE="${RPI_IMAGE_XZ%.xz}"

BOOT_MNT="${WORK_DIR}/mnt/boot"
ROOT_MNT="${WORK_DIR}/mnt/root"
LOOP_DEV=""

cleanup() {
    echo "==> Cleaning up..."
    set +e
    if mountpoint -q "${ROOT_MNT}/proc" 2>/dev/null; then umount "${ROOT_MNT}/proc"; fi
    if mountpoint -q "${ROOT_MNT}/sys" 2>/dev/null; then umount "${ROOT_MNT}/sys"; fi
    if mountpoint -q "${ROOT_MNT}/dev/pts" 2>/dev/null; then umount "${ROOT_MNT}/dev/pts"; fi
    if mountpoint -q "${ROOT_MNT}/dev" 2>/dev/null; then umount "${ROOT_MNT}/dev"; fi
    if mountpoint -q "${BOOT_MNT}" 2>/dev/null; then umount "${BOOT_MNT}"; fi
    if mountpoint -q "${ROOT_MNT}" 2>/dev/null; then umount "${ROOT_MNT}"; fi
    if [ -n "${LOOP_DEV}" ]; then losetup -d "${LOOP_DEV}" 2>/dev/null; fi
    set -e
}
trap cleanup EXIT

# Verify we're running as root
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: This script must be run as root (or via sudo)."
    exit 1
fi

# Verify the binary exists
if [ ! -f "${BINARY}" ]; then
    echo "ERROR: Binary not found: ${BINARY}"
    exit 1
fi

mkdir -p "${WORK_DIR}" "${BOOT_MNT}" "${ROOT_MNT}"

# --- Step 1: Download Raspberry Pi OS ---
echo "==> Downloading Raspberry Pi OS Lite..."
if [ ! -f "${WORK_DIR}/${RPI_IMAGE_XZ}" ]; then
    wget -q --show-progress -O "${WORK_DIR}/${RPI_IMAGE_XZ}" "${RPI_IMAGE_URL}"
fi

# --- Step 2: Decompress ---
echo "==> Decompressing image..."
if [ ! -f "${WORK_DIR}/${RPI_IMAGE}" ]; then
    xz -dk "${WORK_DIR}/${RPI_IMAGE_XZ}"
fi
cp "${WORK_DIR}/${RPI_IMAGE}" "${WORK_DIR}/${OUTPUT_NAME}.img"
IMG="${WORK_DIR}/${OUTPUT_NAME}.img"

# --- Step 3: Expand image to fit additional software ---
echo "==> Expanding image by 1536MB..."
truncate -s +1536M "${IMG}"

# Grow the root partition (partition 2) to fill available space
PART_START=$(parted -ms "${IMG}" unit s print | grep '^2:' | cut -d: -f2 | tr -d 's')
PART_END=$(parted -ms "${IMG}" unit s print | grep "^${IMG}" | cut -d: -f2 | tr -d 's')
LAST_SECTOR=$((PART_END - 1))

parted -s "${IMG}" resizepart 2 "${LAST_SECTOR}s"

# --- Step 4: Set up loop device and mount ---
echo "==> Setting up loop device..."
LOOP_DEV=$(losetup --find --show --partscan "${IMG}")
echo "    Loop device: ${LOOP_DEV}"

# Wait for partition devices to appear
sleep 1

# Resize the filesystem to fill the expanded partition
e2fsck -fy "${LOOP_DEV}p2" || true
resize2fs "${LOOP_DEV}p2"

echo "==> Mounting partitions..."
mount "${LOOP_DEV}p2" "${ROOT_MNT}"
mount "${LOOP_DEV}p1" "${BOOT_MNT}"

# --- Step 5: Set up QEMU for ARM chroot ---
echo "==> Setting up QEMU ARM emulation..."
if [ -f /usr/bin/qemu-arm-static ]; then
    cp /usr/bin/qemu-arm-static "${ROOT_MNT}/usr/bin/"
elif [ -f /usr/bin/qemu-arm ]; then
    cp /usr/bin/qemu-arm "${ROOT_MNT}/usr/bin/qemu-arm-static"
else
    echo "ERROR: qemu-arm-static not found. Install qemu-user-static."
    exit 1
fi

# Mount special filesystems for chroot
mount -t proc proc "${ROOT_MNT}/proc"
mount -t sysfs sys "${ROOT_MNT}/sys"
mount -o bind /dev "${ROOT_MNT}/dev"
mount -o bind /dev/pts "${ROOT_MNT}/dev/pts"

# Copy DNS config for network access inside chroot
cp /etc/resolv.conf "${ROOT_MNT}/etc/resolv.conf"

# --- Step 5b: Pre-copy service files so chroot can enable them ---
cp -v "${SCRIPT_DIR}/rootfs/etc/systemd/system/snapmaker-moonraker.service" \
    "${ROOT_MNT}/etc/systemd/system/snapmaker-moonraker.service"
cp -v "${SCRIPT_DIR}/rootfs/etc/systemd/system/crowsnest.service" \
    "${ROOT_MNT}/etc/systemd/system/crowsnest.service"
cp -v "${SCRIPT_DIR}/rootfs/etc/systemd/system/moonraker-obico.service" \
    "${ROOT_MNT}/etc/systemd/system/moonraker-obico.service"

# --- Step 6: Run chroot install script ---
echo "==> Running chroot install script..."
cp "${SCRIPT_DIR}/chroot-install.sh" "${ROOT_MNT}/tmp/chroot-install.sh"
chmod +x "${ROOT_MNT}/tmp/chroot-install.sh"
chroot "${ROOT_MNT}" /tmp/chroot-install.sh
rm -f "${ROOT_MNT}/tmp/chroot-install.sh"

# --- Step 7: Install snapmaker_moonraker binary ---
echo "==> Installing snapmaker_moonraker binary..."
mkdir -p "${ROOT_MNT}/opt/snapmaker-moonraker"
cp "${BINARY}" "${ROOT_MNT}/opt/snapmaker-moonraker/snapmaker_moonraker"
chmod 755 "${ROOT_MNT}/opt/snapmaker-moonraker/snapmaker_moonraker"

# --- Step 8: Copy rootfs overlay files ---
echo "==> Copying rootfs overlay files..."
cp -v "${SCRIPT_DIR}/rootfs/etc/nginx/sites-available/mainsail" \
    "${ROOT_MNT}/etc/nginx/sites-available/mainsail"
ln -sf /etc/nginx/sites-available/mainsail "${ROOT_MNT}/etc/nginx/sites-enabled/mainsail"
rm -f "${ROOT_MNT}/etc/nginx/sites-enabled/default"

cp -v "${SCRIPT_DIR}/rootfs/etc/systemd/system/snapmaker-moonraker.service" \
    "${ROOT_MNT}/etc/systemd/system/snapmaker-moonraker.service"
cp -v "${SCRIPT_DIR}/rootfs/etc/systemd/system/crowsnest.service" \
    "${ROOT_MNT}/etc/systemd/system/crowsnest.service"
cp -v "${SCRIPT_DIR}/rootfs/etc/systemd/system/moonraker-obico.service" \
    "${ROOT_MNT}/etc/systemd/system/moonraker-obico.service"

cp -v "${SCRIPT_DIR}/rootfs/etc/hostname" "${ROOT_MNT}/etc/hostname"

# Install default config for pi user
mkdir -p "${ROOT_MNT}/home/pi/.snapmaker"
cp -v "${SCRIPT_DIR}/rootfs/home/pi/.snapmaker/config.yaml" \
    "${ROOT_MNT}/home/pi/.snapmaker/config.yaml"

# Install crowsnest config files
mkdir -p "${ROOT_MNT}/home/pi/printer_data/config" \
         "${ROOT_MNT}/home/pi/printer_data/logs"
cp -v "${SCRIPT_DIR}/rootfs/home/pi/printer_data/config/crowsnest.conf" \
    "${ROOT_MNT}/home/pi/printer_data/config/crowsnest.conf"
cp -v "${SCRIPT_DIR}/rootfs/home/pi/printer_data/config/crowsnest-usb.conf" \
    "${ROOT_MNT}/home/pi/printer_data/config/crowsnest-usb.conf"

# Create gcode directory
mkdir -p "${ROOT_MNT}/home/pi/printer_data/gcodes"

# Fix ownership (pi user is UID 1000 on RPi OS)
chown -R 1000:1000 "${ROOT_MNT}/home/pi/.snapmaker" \
    "${ROOT_MNT}/home/pi/printer_data"

# --- Step 9: Cleanup chroot mounts ---
echo "==> Unmounting chroot filesystems..."
rm -f "${ROOT_MNT}/usr/bin/qemu-arm-static"
rm -f "${ROOT_MNT}/etc/resolv.conf"
umount "${ROOT_MNT}/dev/pts"
umount "${ROOT_MNT}/dev"
umount "${ROOT_MNT}/sys"
umount "${ROOT_MNT}/proc"
umount "${BOOT_MNT}"
umount "${ROOT_MNT}"
losetup -d "${LOOP_DEV}"
LOOP_DEV=""

# --- Step 10: Shrink image with PiShrink ---
echo "==> Shrinking image..."
if command -v pishrink.sh &>/dev/null; then
    pishrink.sh "${IMG}"
else
    echo "    PiShrink not found, downloading..."
    wget -q -O /tmp/pishrink.sh https://raw.githubusercontent.com/Drewsif/PiShrink/master/pishrink.sh
    chmod +x /tmp/pishrink.sh
    /tmp/pishrink.sh "${IMG}"
    rm -f /tmp/pishrink.sh
fi

# --- Step 11: Compress ---
echo "==> Compressing image with xz..."
xz -T0 -9 "${IMG}"

# Move final artifact to the workspace root
mv "${WORK_DIR}/${OUTPUT_NAME}.img.xz" "./${OUTPUT_NAME}.img.xz"

echo ""
echo "==> Build complete: ${OUTPUT_NAME}.img.xz"
echo "    Size: $(du -h "./${OUTPUT_NAME}.img.xz" | cut -f1)"
