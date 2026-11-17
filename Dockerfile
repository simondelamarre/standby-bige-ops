# One image for both roles. Tagged releases are published to the public GHCR;
# bige-ops normally pulls a digest-pinned image and can rebuild this Dockerfile
# from a pinned commit on the VM as an explicit recovery path.
FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/standby ./cmd/standby

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/standby /standby
USER 65532:65532
ENTRYPOINT ["/standby"]
