# Backend image: Go API server plus the create-admin CLI.
# The sqlite driver (DB_DRIVER=sqlite) is pure Go, so CGO stays off and the
# binaries are fully static regardless of the configured database.

# The builder must be at least the go directive in go.mod (1.25 since the AEO
# provider SDKs landed), otherwise the toolchain refuses to build.
FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gophercrm ./cmd \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/create-admin ./cmd/create-admin

FROM debian:bookworm-slim

# The browser of quote PDF output, one literal: the upstream version of the
# Debian package chromium-headless-shell (chrome-headless-shell, the old
# headless mode as its own small binary, without the full browser's UI). The
# Debian revision is appended in the install line below. Debian's security
# repository drops a version once a newer one is published, so a build that
# stops on "Version ... was not found" means: raise this to the candidate of
# `apt-cache policy chromium-headless-shell` (see docs/DOCKER.md).
ARG HEADLESS_SHELL_VERSION=156.0.8078.79

# chromium-headless-shell and fonts-liberation are what quote PDF output
# needs: the server prints APP_BASE_URL/quote-render with the headless shell,
# and without a font the PDF has no readable text although it is a valid
# file. The full chromium package is not installed. tini is the init of the
# container (see ENTRYPOINT). wget is here for the health checks of the
# compose files, which call it inside this container; the base image has none.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates tzdata fonts-liberation tini wget \
        "chromium-headless-shell=${HEADLESS_SHELL_VERSION}-1~deb12u1" \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --user-group --no-create-home --home-dir /data --shell /usr/sbin/nologin gophercrm

# Launcher of the browser, for this image only; the server itself never
# passes these flags. As uid 10001 in a container the headless shell cannot
# set up its own sandbox (no user namespaces, no setuid helper) and refuses to
# start, so the sandbox is switched off here, where the container is the
# boundary; and a container's /dev/shm is 64 MB, so shared memory goes to
# /tmp. exec is required: the server kills the process group of the process it
# started, and that process has to be the browser, not a shell waiting on it.
# For the same reason the launcher names the binary itself: the package's
# /usr/bin/chromium-headless-shell is a shell script that does not exec.
RUN printf '%s\n' '#!/bin/sh' 'exec /usr/lib/chromium/chromium-headless-shell --no-sandbox --disable-dev-shm-usage "$@"' > /usr/local/bin/quote-pdf-browser \
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
