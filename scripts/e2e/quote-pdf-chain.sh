#!/usr/bin/env bash
# One command for the real chain of quote PDF output, start to finish:
# browser -> the web app's /quote-render page -> POST /quotes/public/render ->
# the printed document -> the text of the file. It starts what the chain
# needs, runs scripts/e2e/quote-pdf-smoke.sh against it, reads the PDF's text
# and stops everything it started.
#
#   QUOTE_PDF_CHROME_PATH="/path/to/Chrome or chrome-headless-shell" \
#     scripts/e2e/quote-pdf-chain.sh local
#   scripts/e2e/quote-pdf-chain.sh image
#
# local  builds the backend, create-admin and the web app into a temporary
#        directory, runs them on free local ports with a SQLite file and the
#        browser QUOTE_PDF_CHROME_PATH names (required: a full Chrome or
#        Chromium, or a headless shell), and renders one quote.
# image  builds the backend and ui images from this checkout and starts them
#        from a compose file of its own (a complete file in the temporary
#        directory, not an override of the repository's): read-only root,
#        /tmp on tmpfs, SQLite on a volume. The browser is the image's own
#        default, which the script never sets and compares with the image's
#        configuration. It renders ten quotes, then checks that no browser
#        process and no zombie is left in the backend container and that the
#        backend stops in under 10 seconds without being killed.
#
# In both modes the text of the PDF must hold the quote number, the line
# description, the net total and the gross total (each looked up on its own,
# with . or , as the decimal separator), and the two totals must differ.
# image mode also wants at least one embedded font. The text comes from
# pdftotext and pdffonts (poppler) on this machine when installed, otherwise
# from an alpine:3.20 container; without either the script fails.
#
# The admin password and the JWT secret are generated per run and never
# printed. Ports are picked free at run time. On exit, also a failed or an
# interrupted one, the script stops its processes or removes its containers,
# network, volume and image tags, checks that they are gone and fails if not.
# It is a manual tool: no workflow, Makefile target or gate runs it.
set -u

MODE="${1:-}"
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$SCRIPT_DIR/../.." && pwd)
SMOKE="$SCRIPT_DIR/quote-pdf-smoke.sh"

say() { echo "quote-pdf-chain: $*"; }
fail() { echo "quote-pdf-chain: FAILED: $*" >&2; exit 1; }
usage() {
  echo "usage: QUOTE_PDF_CHROME_PATH=<browser binary> $0 local" >&2
  echo "       $0 image" >&2
  exit 2
}

case "$MODE" in
  local)
    if [ -z "${QUOTE_PDF_CHROME_PATH:-}" ]; then
      echo "quote-pdf-chain: local needs QUOTE_PDF_CHROME_PATH (the browser binary to print with)" >&2
      usage
    fi
    ;;
  image) ;;
  *) usage ;;
esac

command -v python3 > /dev/null || fail "python3 is required"
command -v curl > /dev/null || fail "curl is required"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/gophercrm-quote-pdf-chain.XXXXXX") || fail "cannot create a temporary directory"
PROJECT="gophercrm-quote-pdf-chain-$$"
BACKEND_IMAGE="$PROJECT-backend:chain"
UI_IMAGE="$PROJECT-ui:chain"
COMPOSE_FILE="$WORK/compose.yml"
BACKEND_PID=""
PREVIEW_PID=""
COMPOSE_STARTED=0

compose() { docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"; }

# pids_of_work lists the processes whose command line names the temporary
# directory: the backend, the preview server and any browser of a render (the
# backend's TMPDIR lies inside it, so a browser's profile path does too).
pids_of_work() { pgrep -f "$WORK" 2> /dev/null | grep -vx "$$" || true; }

cleanup() {
  local status=$? left="" pid i
  trap - EXIT INT TERM
  if [ "$MODE" = "local" ]; then
    for pid in "$PREVIEW_PID" "$BACKEND_PID" $(pids_of_work); do
      [ -n "$pid" ] && kill "$pid" 2> /dev/null
    done
    for i in $(seq 1 50); do
      [ -z "$(pids_of_work)" ] && break
      sleep 0.2
    done
    for pid in $(pids_of_work); do kill -9 "$pid" 2> /dev/null; done
    for i in $(seq 1 25); do
      [ -z "$(pids_of_work)" ] && break
      sleep 0.2
    done
    left=$(pids_of_work)
    [ -z "$left" ] || { echo "quote-pdf-chain: FAILED: processes left running: $left" >&2; status=1; }
  fi
  if [ "$COMPOSE_STARTED" = "1" ]; then
    compose down -v --remove-orphans --timeout 10 > "$WORK/down.log" 2>&1
    docker image rm "$BACKEND_IMAGE" "$UI_IMAGE" > /dev/null 2>&1
    left="$(docker ps -aq --filter "name=$PROJECT")$(docker network ls -q --filter "name=$PROJECT")$(docker volume ls -q --filter "name=$PROJECT")$(docker image ls -q "$PROJECT-backend")$(docker image ls -q "$PROJECT-ui")"
    if [ -n "$left" ]; then
      echo "quote-pdf-chain: FAILED: containers, networks, volumes or images of $PROJECT are left: $left" >&2
      status=1
    fi
  fi
  if [ -n "${QUOTE_PDF_CHAIN_KEEP:-}" ] && [ -f "$WORK/quote.pdf" ]; then
    cp "$WORK/quote.pdf" "$QUOTE_PDF_CHAIN_KEEP" 2> /dev/null && say "PDF kept at $QUOTE_PDF_CHAIN_KEEP"
  fi
  rm -rf "$WORK"
  if [ "$status" = "0" ]; then say "everything this run started is stopped and removed"; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

free_port() {
  python3 -c 'import socket
s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
secret() { python3 -c 'import secrets, sys; print(secrets.token_urlsafe(int(sys.argv[1])))' "$1"; }

# wait_for_url <url> <seconds>
wait_for_url() {
  local i
  for i in $(seq 1 "$2"); do
    curl -fsS -m 2 -o /dev/null "$1" 2> /dev/null && return 0
    sleep 1
  done
  return 1
}

# smoke_value <key> <file> reads one key=value line of the smoke script.
smoke_value() { sed -n "s/^$1=//p" "$2" | head -1; }

# pdf_text <pdf> writes <pdf>.txt (pdftotext) and <pdf>.fonts (pdffonts),
# with the tools of this machine or, without them, of a container.
pdf_text() {
  local pdf="$1" dir name
  dir=$(dirname "$pdf"); name=$(basename "$pdf")
  if command -v pdftotext > /dev/null 2>&1 && command -v pdffonts > /dev/null 2>&1; then
    pdftotext "$pdf" "$pdf.txt" && pdffonts "$pdf" > "$pdf.fonts"
    return
  fi
  if ! command -v docker > /dev/null 2>&1 || ! docker info > /dev/null 2>&1; then
    fail "cannot read the text of the PDF: pdftotext/pdffonts (poppler-utils) are not installed and Docker is not available for the container fallback"
  fi
  docker run --rm -v "$dir":/w alpine:3.20 sh -c \
    'apk add -q poppler-utils && pdftotext "/w/$1" "/w/$1.txt" && pdffonts "/w/$1" > "/w/$1.fonts"' sh "$name"
}

# check_pdf_text <pdf> <smoke output> [fonts]: the text holds the number, the
# line, the net and the gross total, each on its own, and the totals differ.
check_pdf_text() {
  local pdf="$1" out="$2" want_fonts="${3:-}" number line total gross fonts
  number=$(smoke_value number "$out"); line=$(smoke_value line "$out")
  total=$(smoke_value total "$out"); gross=$(smoke_value gross_total "$out")
  [ -n "$number" ] && [ -n "$line" ] && [ -n "$total" ] && [ -n "$gross" ] \
    || fail "the smoke script did not print number=, line=, total= and gross_total="
  [ "$total" != "$gross" ] || fail "net and gross total are equal ($total)"
  pdf_text "$pdf" || fail "the text of the PDF could not be extracted"
  [ -s "$pdf.txt" ] || fail "the PDF has no text at all (no font, or an empty page)"
  grep -qF -- "$number" "$pdf.txt" || fail "the PDF text does not contain the quote number $number"
  say "found in the PDF text: number=$number"
  grep -qF -- "$line" "$pdf.txt" || fail "the PDF text does not contain the line '$line'"
  say "found in the PDF text: line=$line"
  # 246.90 is also accepted as 246,90: the document may print a decimal comma.
  grep -qE -- "(^|[^0-9])${total%.*}[.,]${total#*.}([^0-9]|\$)" "$pdf.txt" \
    || fail "the PDF text does not contain the net total $total"
  say "found in the PDF text: total=$total"
  grep -qE -- "(^|[^0-9])${gross%.*}[.,]${gross#*.}([^0-9]|\$)" "$pdf.txt" \
    || fail "the PDF text does not contain the gross total $gross"
  say "found in the PDF text: gross_total=$gross (differs from the net total)"
  if [ -n "$want_fonts" ]; then
    # pdffonts prints two header lines, then one line per font; emb is "yes".
    fonts=$(tail -n +3 "$pdf.fonts" | grep -c ' yes ' || true)
    [ "$fonts" -ge 1 ] || fail "the PDF embeds no font"
    say "embedded fonts in the PDF: $fonts"
  fi
}

ADMIN_EMAIL="pdf-chain-admin@example.com"
ADMIN_PASSWORD="Aa1!$(secret 18)"
JWT="$(secret 48)"

run_local() {
  local chrome="$QUOTE_PDF_CHROME_PATH" api_port ui_port api ui
  [ -f "$chrome" ] && [ -x "$chrome" ] || command -v "$chrome" > /dev/null 2>&1 \
    || fail "QUOTE_PDF_CHROME_PATH does not name an executable file"
  command -v go > /dev/null || fail "go is required"
  command -v node > /dev/null || fail "node is required"
  [ -f "$REPO/gocrm-ui/node_modules/vite/bin/vite.js" ] || fail "gocrm-ui/node_modules is missing: run npm ci in gocrm-ui first"

  api_port=$(free_port); ui_port=$(free_port)
  [ "$api_port" != "$ui_port" ] || ui_port=$(free_port)
  api="http://localhost:$api_port"; ui="http://localhost:$ui_port"
  mkdir -p "$WORK/tmp" "$WORK/run"

  say "building the backend and create-admin"
  (cd "$REPO" && go build -o "$WORK/gophercrm" ./cmd && go build -o "$WORK/create-admin" ./cmd/create-admin) \
    > "$WORK/go-build.log" 2>&1 || { tail -20 "$WORK/go-build.log" >&2; fail "go build failed"; }

  # vite preview has no API proxy, so the bundle gets the absolute API URL;
  # --outDir keeps gocrm-ui/dist untouched.
  say "building the web app"
  (cd "$REPO/gocrm-ui" && VITE_API_BASE_URL="$api/api/v1" node node_modules/vite/bin/vite.js build --outDir "$WORK/dist" --emptyOutDir) \
    > "$WORK/vite-build.log" 2>&1 || { tail -20 "$WORK/vite-build.log" >&2; fail "vite build failed"; }

  # A built environment, not this shell's: no .env (the working directory is
  # the temporary one) and no stray DB_ or SMTP_ variable. TMPDIR puts every
  # render directory, and with it the browser's command line, inside $WORK.
  # It execs, so it is always called in a subshell, whose pid then is the
  # program's.
  backend_env() {
    cd "$WORK/run" && exec env -i PATH="$PATH" HOME="$WORK/run" TMPDIR="$WORK/tmp" LANG="${LANG:-C}" \
      DB_DRIVER=sqlite DB_PATH="$WORK/pdf.db" SERVER_HOST=127.0.0.1 SERVER_PORT="$api_port" \
      SERVER_MODE=development API_PREFIX=/api/v1 JWT_SECRET="$JWT" SMTP_HOST= LOG_LEVEL=info \
      DISABLE_RATE_LIMIT=true APP_BASE_URL="$ui" CORS_ALLOWED_ORIGINS="$ui" \
      QUOTE_PDF_CHROME_PATH="$chrome" "$@"
  }
  say "starting the backend on $api and the web app on $ui"
  (backend_env "$WORK/gophercrm") > "$WORK/backend.log" 2>&1 &
  BACKEND_PID=$!
  disown "$BACKEND_PID" 2> /dev/null
  wait_for_url "$api/health" 60 || { tail -20 "$WORK/backend.log" >&2; fail "the backend did not answer /health within 60 s"; }
  (backend_env "$WORK/create-admin" -non-interactive -email "$ADMIN_EMAIL" -name "PDF Chain Admin" -password "$ADMIN_PASSWORD") \
    > /dev/null 2>&1 || fail "create-admin failed"

  (cd "$REPO/gocrm-ui" && exec node node_modules/vite/bin/vite.js preview --outDir "$WORK/dist" --host localhost --port "$ui_port" --strictPort) \
    > "$WORK/preview.log" 2>&1 &
  PREVIEW_PID=$!
  disown "$PREVIEW_PID" 2> /dev/null
  wait_for_url "$ui/quote-render" 60 || { tail -20 "$WORK/preview.log" >&2; fail "the web app did not answer within 60 s"; }

  say "rendering one quote with the browser of QUOTE_PDF_CHROME_PATH"
  BASE_URL="$api/api/v1" ADMIN_EMAIL="$ADMIN_EMAIL" ADMIN_PASSWORD="$ADMIN_PASSWORD" \
    bash "$SMOKE" "$WORK/quote.pdf" > "$WORK/smoke.out" || fail "the smoke script failed (its message is above)"
  cat "$WORK/smoke.out"
  check_pdf_text "$WORK/quote.pdf" "$WORK/smoke.out"
  say "local: OK"
}

run_image() {
  local ui_port container path image_path version size i health started elapsed code top_out procs
  command -v docker > /dev/null || fail "docker is required"
  docker info > /dev/null 2>&1 || fail "the Docker daemon is not reachable"
  docker compose version > /dev/null 2>&1 || fail "docker compose is required"

  ui_port=$(free_port)
  # A complete compose file of this run. It never names QUOTE_PDF_CHROME_PATH:
  # the browser is whatever the image sets. The JWT secret is passed through
  # this script's environment, not written to the file.
  REPO="$REPO" BACKEND_IMAGE="$BACKEND_IMAGE" UI_IMAGE="$UI_IMAGE" UI_PORT="$ui_port" PROJECT="$PROJECT" python3 - > "$COMPOSE_FILE" << 'PY'
import json, os
q = json.dumps
repo, port = os.environ["REPO"], os.environ["UI_PORT"]
print(f"""name: {os.environ["PROJECT"]}
services:
  backend:
    build:
      context: {q(repo)}
    image: {q(os.environ["BACKEND_IMAGE"])}
    read_only: true
    tmpfs:
      - /tmp
    environment:
      DB_DRIVER: sqlite
      DB_PATH: /data/gophercrm.db
      SERVER_PORT: 8080
      SERVER_MODE: production
      API_PREFIX: /api/v1
      JWT_SECRET: ${{CHAIN_JWT_SECRET:?}}
      LOG_LEVEL: info
      LOG_FORMAT: json
      SMTP_HOST: ""
      APP_BASE_URL: http://ui
      CORS_ALLOWED_ORIGINS: http://ui,http://localhost:{port}
      DISABLE_RATE_LIMIT: "true"
    volumes:
      - data:/data
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/health"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 10s
  ui:
    build:
      context: {q(os.path.join(repo, "gocrm-ui"))}
    image: {q(os.environ["UI_IMAGE"])}
    depends_on:
      backend:
        condition: service_healthy
    ports:
      - "127.0.0.1:{port}:80"
volumes:
  data: {{}}""")
PY
  [ -s "$COMPOSE_FILE" ] || fail "cannot write the compose file"
  export CHAIN_JWT_SECRET="$JWT"

  COMPOSE_STARTED=1
  say "building the backend and ui images (project $PROJECT)"
  compose build > "$WORK/build.log" 2>&1 || { tail -30 "$WORK/build.log" >&2; fail "docker compose build failed"; }
  say "starting the stack; the web app will be on http://localhost:$ui_port"
  compose up -d > "$WORK/up.log" 2>&1 || { tail -30 "$WORK/up.log" >&2; compose logs --tail 30 backend >&2; fail "docker compose up failed"; }

  container=$(compose ps -q backend)
  [ -n "$container" ] || fail "the backend container does not exist"
  health=""
  for i in $(seq 1 120); do
    health=$(docker inspect --format '{{.State.Health.Status}}' "$container" 2> /dev/null)
    [ "$health" = "healthy" ] && break
    sleep 1
  done
  [ "$health" = "healthy" ] || { compose logs --tail 30 backend >&2; fail "the backend is '$health', not healthy, after 120 s"; }
  say "backend health: healthy (the compose files' wget health check)"

  path=$(compose exec -T backend printenv QUOTE_PDF_CHROME_PATH | tr -d '\r')
  [ -n "$path" ] || fail "QUOTE_PDF_CHROME_PATH is empty in the container"
  image_path=$(docker image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$BACKEND_IMAGE" | sed -n 's/^QUOTE_PDF_CHROME_PATH=//p')
  [ "$path" = "$image_path" ] || fail "the container's QUOTE_PDF_CHROME_PATH ($path) is not the image default ($image_path)"
  compose exec -T backend test -x "$path" || fail "$path is not executable in the container"
  say "browser path, container = image default: $path"
  version=$(compose exec -T backend "$path" --version 2> /dev/null | tr -d '\r' | head -1)
  [ -n "$version" ] || fail "$path --version printed nothing"
  say "browser version: $version"
  size=$(docker image inspect --format '{{.Size}}' "$BACKEND_IMAGE")
  say "backend image size: $size bytes"

  compose exec -T backend create-admin -non-interactive -email "$ADMIN_EMAIL" -name "PDF Chain Admin" -password "$ADMIN_PASSWORD" \
    > /dev/null 2>&1 || fail "create-admin failed in the container"
  wait_for_url "http://localhost:$ui_port/quote-render" 60 || fail "the web app did not answer within 60 s"

  # One second between runs keeps ten runs (five authenticated requests each,
  # all from the proxy's address) under the moderate rate-limit tier, which
  # DISABLE_RATE_LIMIT does not lift. A pause, not a retry: any failure counts.
  for i in $(seq 1 10); do
    BASE_URL="http://localhost:$ui_port/api/v1" ADMIN_EMAIL="$ADMIN_EMAIL" ADMIN_PASSWORD="$ADMIN_PASSWORD" \
      bash "$SMOKE" "$WORK/quote.pdf" > "$WORK/smoke.out" \
      || { compose logs --tail 20 backend >&2; fail "render $i of 10 failed (the smoke script's message is above)"; }
    say "render $i of 10: 200 application/pdf, $(wc -c < "$WORK/quote.pdf" | tr -d ' ') bytes"
    sleep 1
  done
  cat "$WORK/smoke.out"
  check_pdf_text "$WORK/quote.pdf" "$WORK/smoke.out" fonts

  # The group kill after a render is asynchronous, so poll; the image has no
  # ps, docker compose top reads the process table from outside.
  top_out=""
  for i in $(seq 1 10); do
    top_out=$(compose top backend 2> /dev/null)
    echo "$top_out" | grep -qEi 'headless|chrom|defunct' || break
    sleep 1
  done
  if echo "$top_out" | grep -qEi 'headless|chrom|defunct'; then
    echo "$top_out" >&2
    fail "a browser process or a zombie is still in the backend container 10 s after the last render"
  fi
  # The first line of the output is the container's name, the second the
  # column titles; what follows is one line per process.
  procs=$(echo "$top_out" | tail -n +3 | grep -c . || true)
  echo "$top_out" | tail -n +3 | grep -q 'tini -- gophercrm' || fail "docker compose top shows no tini process"
  [ "$procs" = "2" ] || { echo "$top_out" >&2; fail "the backend container holds $procs processes, expected 2 (tini and gophercrm)"; }
  say "processes left in the backend container: $procs (tini and gophercrm), no browser, no zombie"

  started=$(python3 -c 'import time; print(time.time())')
  compose stop backend > "$WORK/stop.log" 2>&1 || fail "docker compose stop backend failed"
  elapsed=$(python3 -c 'import sys, time; print("%.1f" % (time.time() - float(sys.argv[1])))' "$started")
  code=$(docker inspect --format '{{.State.ExitCode}}' "$container")
  python3 -c 'import sys; sys.exit(0 if float(sys.argv[1]) < 10 else 1)' "$elapsed" \
    || fail "docker compose stop backend took $elapsed s (10 s or more: the stop signal did not reach the server)"
  [ "$code" != "137" ] || fail "the backend was killed (exit code 137) instead of stopping"
  say "backend stopped in $elapsed s with exit code $code"
  say "image: OK"
}

if [ "$MODE" = "local" ]; then run_local; else run_image; fi
