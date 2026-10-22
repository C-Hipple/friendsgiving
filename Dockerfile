# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/brogiving . \
 && mkdir -p /out/data

# Static binary on distroless: no shell, no package manager, runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/brogiving /brogiving
# Pre-create /data owned by nonroot so a fresh named volume is writable.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV ADDR=:8080 \
    DB_PATH=/data/brogiving.db
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD ["/brogiving", "-healthcheck"]
USER nonroot
ENTRYPOINT ["/brogiving"]
