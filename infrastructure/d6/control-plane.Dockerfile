FROM golang:1.25.4 AS build
WORKDIR /src/control-plane
COPY power-iot-a3-deployment-control-plane/go.mod power-iot-a3-deployment-control-plane/go.sum ./
RUN go mod download
COPY power-iot-a3-deployment-control-plane/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/d1l-authority ./cmd/d1l-authority

FROM ubuntu:24.04
RUN useradd --system --uid 10002 --no-create-home poweriot-d1l
COPY --from=build /out/d1l-authority /usr/local/bin/d1l-authority
COPY infrastructure/d6/control-plane-entrypoint.sh /usr/local/bin/d1l-entrypoint
RUN chmod 0555 /usr/local/bin/d1l-entrypoint
USER 10002
ENTRYPOINT ["/usr/local/bin/d1l-entrypoint"]
