# Owncast source plugin for Integrated Recorder

This repository builds a standalone Integrated Recorder Adapter Protocol v1
executable for Owncast. The first Registry-compatible release identity is
`0.2.0`; no artifact hashes or Registry entries are claimed until a real
release has been built and published.

## Build

The adapter uses only the public Go SDK and does not import Integrated Recorder
Core packages:

```sh
go mod tidy
go test ./...
go build -trimpath -ldflags='-X github.com/integrated-recorder/source.owncast/internal/owncast.version=0.2.0' \
  -o integrated-recorder-adapter-owncast ./cmd/integrated-recorder-adapter-owncast
```

The development descriptor reports `0.2.0-dev`. Release builds set the
descriptor version from the exact `v<version>` Git tag.

## Authoring model

The executable imports `adapter` and `protocol` from
[`integrated-recorder/adapter-sdk-go`](https://github.com/integrated-recorder/adapter-sdk-go).
The SDK owns framing, request ID echo, dispatch, structured errors, and
shutdown. This plugin implements the `Adapter` descriptor plus the optional
`Resolver`, `Watcher`, and `MetadataProvider` interfaces. A new Go adapter
starts with:

```sh
go mod init example.com/my-adapter
go get github.com/integrated-recorder/adapter-sdk-go@v0.2.1
```

See the SDK README for the minimal implementation pattern. No Core package or
Core module dependency is needed.

## Core conformance

Use the Core-owned black-box conformance executable. This invocation pins the
Core contract to commit
`3cf2c90280873f98f5120f67e39516c5af8abc04`:

```sh
git clone https://github.com/integrated-recorder/core.git core
git -C core checkout 3cf2c90280873f98f5120f67e39516c5af8abc04
(cd core && go build -o ../adapter-conformance ./cmd/adapter-conformance)
./adapter-conformance --binary ./integrated-recorder-adapter-owncast --json
```

The runner starts the executable as a subprocess and checks Protocol v1
framing, descriptor identity/validity and stability, structured errors,
stdout cleanliness, shutdown, and bounded exit. It does not prove that an
adapter is safe or correct for every Owncast server.

## Owncast behavior

Input is an Owncast instance base URL. Resolve removes any query or fragment
and appends `/hls/stream.m3u8`. Watch and metadata use `/api/status` on the same
instance path. A valid `online: false` response is an offline observation;
network, HTTP, and malformed response failures are errors. Live results carry
the HLS media fast path, title, `lastConnectTime` session reference, and parsed
start time. A known empty `streamTitle` is preserved as an empty metadata title.
401/403 produces the safe `authentication_required` protocol error.

Status requests use HTTP or HTTPS only, reject credentials and non-public
network targets, pin connections to validated public DNS addresses, validate
redirect targets, disable environment proxy inheritance, and bound redirects,
timeouts, and response bodies. The plugin does not accept arbitrary headers or
cookies. Integrated Recorder Core independently applies its media acquisition
request policy to the returned manifest URL.

The executable is native trusted code, not a sandbox. Protocol validation and
public-network checks do not make a plugin safe or malware-free.

## Release artifacts

Pushing a `v<version>` tag runs the release workflow from that exact tag commit
and publishes Linux binaries named:

```text
integrated-recorder-adapter-owncast_linux_amd64
integrated-recorder-adapter-owncast_linux_arm64
```

Registry metadata must be computed from the published assets: exact URL, file
size, SHA-256, source commit, descriptor version, and Protocol v1 identity.
Those values must not be guessed or copied from a local build. Registry approval
is a separate reviewed change in `integrated-recorder/plugin-registry`.

## License

This project preserves the Core repository's AGPL-3.0 license for the moved
Integrated Recorder Owncast implementation. The plugin does not bundle Owncast
Project artwork. The Owncast project has not endorsed this plugin.
