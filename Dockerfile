# --- build -------------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum VERSION ./
RUN go mod download
COPY *.go CHANGELOG.md ./
COPY internal ./internal
COPY templates ./templates
ARG BUILD=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.build=${BUILD}" -o /numeros .

# --- runtime -----------------------------------------------------------------
# distroless static: no shell, ca-certs and a nonroot user; the zone database is built into the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /numeros /numeros
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD ["/numeros", "-healthcheck"]
ENTRYPOINT ["/numeros"]
