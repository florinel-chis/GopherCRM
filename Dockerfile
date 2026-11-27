# Backend image: Go API server plus the create-admin CLI.
# The sqlite driver (DB_DRIVER=sqlite) is pure Go, so CGO stays off and the
# binaries are fully static regardless of the configured database.

# The browser of quote PDF output: chrome-headless-shell (the old headless
# mode as its own small binary, without the full browser's UI) from Chrome for
# Testing, Google's archive of browser builds for automation. Google keeps
# every published version there, so this pin does not expire the way a pinned
# Debian package version does (the security repository drops it at the next
# update). This is the only place the version is written. To change it, set
# the version and both checksums below (sha256sum of the linux64 and of the
# linux-arm64 zip of that version), then run
# `scripts/e2e/quote-pdf-chain.sh image` (see docs/operations/docker.md).
ARG HEADLESS_SHELL_VERSION=156.0.8078.80
ARG HEADLESS_SHELL_SHA256_AMD64=547dcd374e02961bf998f6046a267644f8502225c36bb78ca307dab32a0b3eb4
ARG HEADLESS_SHELL_SHA256_ARM64=5bb6a00a4285c13303d3f72c5afdb70f144ea7a93eed462365a52b69631c9607

# The builder must be at least the go directive in go.mod (1.25 since the AEO
# provider SDKs landed), otherwise the toolchain refuses to build.
FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gophercrm ./cmd \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/create-admin ./cmd/create-admin

# Downloads and checks the browser. It runs on the build platform, so the
# zip of the other architecture needs no emulation; curl and unzip stay in
# this stage. TARGETARCH is set by BuildKit; the legacy builder leaves it
# empty and is refused here.
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS headless-shell

ARG HEADLESS_SHELL_VERSION
ARG HEADLESS_SHELL_SHA256_AMD64
ARG HEADLESS_SHELL_SHA256_ARM64
ARG TARGETARCH

RUN case "${TARGETARCH}" in \
        amd64) platform=linux64; sum="${HEADLESS_SHELL_SHA256_AMD64}" ;; \
        arm64) platform=linux-arm64; sum="${HEADLESS_SHELL_SHA256_ARM64}" ;; \
        *) echo "chrome-headless-shell: unsupported target architecture '${TARGETARCH}' (supported: amd64, arm64; build with BuildKit)" >&2; exit 1 ;; \
    esac \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
        -o /tmp/chrome-headless-shell.zip \
        "https://storage.googleapis.com/chrome-for-testing-public/${HEADLESS_SHELL_VERSION}/${platform}/chrome-headless-shell-${platform}.zip" \
    && echo "${sum}  /tmp/chrome-headless-shell.zip" | sha256sum -c - \
    && unzip -q /tmp/chrome-headless-shell.zip -d /tmp/unpacked \
    && mv "/tmp/unpacked/chrome-headless-shell-${platform}" /opt/chrome-headless-shell \
    && test -x /opt/chrome-headless-shell/chrome-headless-shell \
    && rm -rf /tmp/chrome-headless-shell.zip /tmp/unpacked /var/lib/apt/lists/*

FROM debian:bookworm-slim

# fonts-liberation, fontconfig and the shared libraries below are what quote
# PDF output needs: the server prints APP_BASE_URL/quote-render with the
# headless shell, and without a font the PDF has no readable text although it
# is a valid file. fontconfig brings /etc/fonts, whose aliases map Arial,
# sans-serif and serif to Liberation Sans and Serif; without it every text
# falls back to Liberation Mono and the quote's table columns wrap. The
# libraries are the ones of the zip's deb.deps that the headless shell loads,
# at start or later through dlopen (libnss3 also brings the NSS soft token);
# the linux64 binary also links the ATK bridge, the linux-arm64 one does not.
# Its GTK, Cairo, Pango, CUPS, curl and xdg-utils entries serve the full
# browser, which shares that list. No Chromium package is installed.
# tini is the init of the container (see ENTRYPOINT). wget is here for the
# health checks of the compose files, which call it inside this container;
# the base image has none.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates tzdata fonts-liberation fontconfig tini wget \
        libasound2 libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 libdbus-1-3 libexpat1 libgbm1 libnspr4 libnss3 \
        libudev1 libvulkan1 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 \
        libxfixes3 libxkbcommon0 libxrandr2 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --user-group --no-create-home --home-dir /data --shell /usr/sbin/nologin gophercrm

COPY --from=headless-shell /opt/chrome-headless-shell /opt/chrome-headless-shell

# A library the binary links against but the image lacks stops the build
# here instead of the first render.
RUN if ldd /opt/chrome-headless-shell/chrome-headless-shell | grep 'not found'; then \
        echo "chrome-headless-shell: shared libraries missing (above)" >&2; exit 1; \
    fi

# Launcher of the browser, for this image only; the server itself never
# passes these flags. As uid 10001 in a container the headless shell cannot
# set up its own sandbox (no user namespaces, no setuid helper) and refuses to
# start, so the sandbox is switched off here, where the container is the
# boundary; and a container's /dev/shm is 64 MB, so shared memory goes to
# /tmp. exec is required: the server kills the process group of the process it
# started, and that process has to be the browser, not a shell waiting on it.
RUN printf '%s\n' '#!/bin/sh' 'exec /opt/chrome-headless-shell/chrome-headless-shell --no-sandbox --disable-dev-shm-usage "$@"' > /usr/local/bin/quote-pdf-browser \
    && chmod 0755 /usr/local/bin/quote-pdf-browser

# PDF output is on by default in the image, through the launcher above. Set
# it to an empty string to switch it off (the development compose files do:
# their APP_BASE_URL is not reachable from inside this container). The path of
# a full Chrome or Chromium, or of a launcher for one, is accepted as well.
ENV QUOTE_PDF_CHROME_PATH=/usr/local/bin/quote-pdf-browser

COPY --from=build /out/gophercrm /out/create-admin /usr/local/bin/

# Default home for a SQLite database file (DB_PATH=/data/gophercrm.db). It has
# to exist and be owned by the app user before the USER switch: a named volume
# mounted here inherits the ownership of the image's directory, and a
# root-owned /data makes the very first boot fail on "unable to open database
# file". Harmless when the MySQL driver is used.
RUN mkdir -p /data && chown gophercrm /data

# Also the working directory, so the image's default relative DB_PATH
# (gophercrm.db) resolves inside the writable volume instead of at /. The
# directory already exists, so this only selects it — ownership is untouched.
# It is also where godotenv looks for .env, i.e. /data/.env — which matters
# only if a host directory containing one is bind-mounted here.
WORKDIR /data

USER gophercrm

EXPOSE 8080

# tini runs as PID 1 and starts the server. A render kills the browser's
# whole process group, and the helper processes that lose their parent that
# way are handed to PID 1: tini reaps them, the server would leave them as
# zombies. It forwards signals, so a stop still reaches the server.
ENTRYPOINT ["/usr/bin/tini", "--", "gophercrm"]
