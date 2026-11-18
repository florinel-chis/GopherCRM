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

FROM alpine:3.20

# chromium and font-liberation are what quote PDF output needs: the server
# prints APP_BASE_URL/quote-render with a headless Chromium, and without a
# font the PDF has no readable text although it is a valid file. tini is the
# init of the container (see ENTRYPOINT).
RUN apk add --no-cache ca-certificates tzdata chromium font-liberation tini \
    && adduser -D -u 10001 gophercrm

# Flags of the Alpine launcher (/usr/bin/chromium-browser reads this file),
# for this image only; the server itself never passes them. As uid 10001 in a
# container Chromium cannot set up its own sandbox (no user namespaces, no
# setuid helper), so it is switched off here, where the container is the
# boundary; and a container's /dev/shm is 64 MB, so shared memory goes to /tmp.
RUN mkdir -p /etc/chromium \
    && printf '%s\n' 'CHROMIUM_FLAGS="--no-sandbox --disable-dev-shm-usage"' > /etc/chromium/chromium.conf

# PDF output is on by default in the image. Set it to an empty string to
# switch it off (the development compose files do: their APP_BASE_URL is not
# reachable from inside this container).
ENV QUOTE_PDF_CHROME_PATH=/usr/bin/chromium-browser

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
ENTRYPOINT ["/sbin/tini", "--", "gophercrm"]
