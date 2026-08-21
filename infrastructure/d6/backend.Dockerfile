FROM golang:1.25.4 AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/power-iot-server ./cmd/server
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/d6-migrate ./cmd/d6-migrate
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/d6-drain ./cmd/d6-drain

FROM ubuntu:24.04
RUN useradd --system --uid 10001 --no-create-home poweriot
COPY --from=build /out/power-iot-server /usr/local/bin/power-iot-server
COPY --from=build /out/d6-migrate /usr/local/bin/d6-migrate
COPY --from=build /out/d6-drain /usr/local/bin/d6-drain
COPY infrastructure/d6/backend-entrypoint.sh /usr/local/bin/power-iot-entrypoint
RUN chmod 0555 /usr/local/bin/power-iot-entrypoint
USER 10001
ENTRYPOINT ["/usr/local/bin/power-iot-entrypoint"]
