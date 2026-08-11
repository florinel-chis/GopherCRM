# Backend image: Go API server plus the create-admin CLI.
# The sqlite driver is only used by tests, so CGO stays off and the
# binaries are fully static.

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

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 gophercrm

COPY --from=build /out/gophercrm /out/create-admin /usr/local/bin/

USER gophercrm

EXPOSE 8080

ENTRYPOINT ["gophercrm"]
