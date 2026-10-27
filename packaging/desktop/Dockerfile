# The desktop client's build environment: GTK 4 and WebKitGTK 6.0, the cgo dependencies Wails v3
# links on Linux, which a developer's machine need not have (delta `2026-10-desktop-client`).
# The Go toolchain and libghostty-vt come from the host, mounted by build.sh, so the container
# builds with exactly what `task test` does.
FROM ubuntu:26.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      libgtk-4-dev libwebkitgtk-6.0-dev build-essential pkg-config git ca-certificates \
      xvfb xauth dbus-x11 \
    && rm -rf /var/lib/apt/lists/*
