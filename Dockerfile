# --- build -------------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum VERSION ./
RUN go mod download
COPY *.go CHANGELOG.md ./
COPY internal ./internal
COPY templates ./templates
COPY assets ./assets
ARG BUILD=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.build=${BUILD}" -o /numeros . && mkdir -p /out/data

# --- runtime -----------------------------------------------------------------
# distroless static: no shell, ca-certs and a nonroot user; the zone database is built into the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /numeros /numeros
# /data owned by the app's user: a new Docker volume mounted there takes this owner, so the
# snapshot and the public pages list can be written (Kubernetes: fsGroup 65532 does the same).
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD ["/numeros", "-healthcheck"]
ENTRYPOINT ["/numeros"]
