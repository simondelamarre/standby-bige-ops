# One image for both roles: bige-ops builds it on the VMs at a pinned commit and
# runs the tap in the app container's network namespace and the guardian with
# the host network. The local demo uses it too.
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
