FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=arm64
WORKDIR /src
COPY go.mod ./
COPY panel ./panel
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o /shelly-panel ./panel \
    && mkdir -p /rootfs/data /rootfs/tmp /rootfs/licenses \
    && chmod 700 /rootfs/data && chmod 1777 /rootfs/tmp \
    && cp /usr/local/go/LICENSE /rootfs/licenses/Go-LICENSE
COPY LICENSE /rootfs/licenses/Shelly-APadder-LICENSE
FROM scratch
COPY --from=build /rootfs /
COPY --from=build /shelly-panel /shelly-panel
EXPOSE 8088
VOLUME ["/data"]
ENTRYPOINT ["/shelly-panel"]
