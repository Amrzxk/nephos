FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG TARGETARCH
ARG BUILD_COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-X github.com/Amrzxk/nephos/internal/version.commit=$BUILD_COMMIT" \
        -o /out/nephosd ./cmd/nephosd \
    && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-X github.com/Amrzxk/nephos/internal/version.commit=$BUILD_COMMIT" \
        -o /out/nephos-hook ./cmd/nephos-hook

FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        podman crun conmon containers-storage fuse-overlayfs uidmap \
        slirp4netns nftables iproute2 iputils-ping procps \
        ca-certificates curl catatonit \
    && rm -rf /var/lib/apt/lists/*

RUN printf 'containers:200000:1048576\n' > /etc/subuid \
    && printf 'containers:200000:1048576\n' > /etc/subgid \
    && mkdir -p /var/lib/nephos/containers /var/lib/nephos/runroot \
        /run/nephos /usr/local/share/nephos

COPY images/appliance/storage.conf /etc/containers/storage.conf
COPY images/appliance/containers.conf /etc/containers/containers.conf
COPY images/appliance/entrypoint.sh /usr/local/bin/nephos-entrypoint
COPY --from=build /out/nephosd /usr/local/bin/nephosd
COPY --from=build /out/nephos-hook /usr/local/bin/nephos-hook
COPY dist/ubuntu-24.04.oci.tar /usr/local/share/nephos/ubuntu-24.04.oci.tar
RUN chmod 0755 /usr/local/bin/nephos-entrypoint /usr/local/bin/nephosd /usr/local/bin/nephos-hook

VOLUME /var/lib/nephos
EXPOSE 7788
ENTRYPOINT ["/usr/bin/catatonit", "--", "/usr/local/bin/nephos-entrypoint"]
CMD ["/usr/local/bin/nephosd"]
