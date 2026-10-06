# Integrated Recorder Owncast Source Plugin

[한국어](README.md) | **English**

A first-party Source Plugin that connects an Owncast instance's live status and HLS manifest to Integrated Recorder. It is a standalone Adapter Protocol v1 executable and is not bundled in the Core image.

> **Distribution status:** v0.2.0 is available through the [official Plugin Registry](https://integrated-recorder.github.io/plugin-registry/catalog-v3.json). Registry approval and CI verification establish distribution identity; they do not guarantee plugin safety or provide a sandbox.

## Role

This plugin maps Owncast instance URL and API semantics to the Adapter Protocol's `resolve`, `watch`, and `metadata` capabilities. Recording, segment storage, VOD, and archive integrity belong to [Core](https://github.com/integrated-recorder/core).

## Owncast behavior

The input is an Owncast instance base URL. Resolve removes its query and fragment and points to `/hls/stream.m3u8`. Watch and metadata query `/api/status` under the same instance path.

- A valid `online: false` response is an offline observation. Network, HTTP, and malformed-response failures remain errors.
- Live results include an HLS media source, title, `lastConnectTime` session reference, and parsed start time.
- A known empty `streamTitle` is preserved as an empty metadata title.
- HTTP 401/403 becomes the safe `authentication_required` protocol error.

## Network and security

Status requests allow HTTP or HTTPS, and reject credentialed URLs and non-public network targets. Connections are pinned to validated public DNS addresses, redirect targets are checked, environment proxy inheritance is disabled, and redirects, timeouts, and response bodies are bounded. Arbitrary header or cookie injection is not supported.

Core applies its own media-acquisition request policy to the returned manifest. The plugin is a native executable and is not sandboxed. See the [Adapter SDK](https://github.com/integrated-recorder/adapter-sdk-go) and [Plugin Trust Model](https://github.com/integrated-recorder/core/blob/main/docs/PLUGIN_TRUST_MODEL.md).

## Development and verification

This repository uses the public Go SDK and does not import Core internal packages.

```sh
go mod tidy
go test ./...
go test -race -count=1 ./...
go vet ./...
go build -trimpath -ldflags='-X github.com/integrated-recorder/source.owncast/internal/owncast.version=0.2.0' \
  -o integrated-recorder-adapter-owncast ./cmd/integrated-recorder-adapter-owncast
```

The development descriptor reports `0.2.0-dev`. Release builds set the descriptor version from the exact `v<version>` Git tag.

Check executable compatibility with the black-box `adapter-conformance` runner from the exact Core commit/tag being targeted. Select that commit/tag and run the tool from its Core checkout.

```sh
git clone https://github.com/integrated-recorder/core.git core
cd core
CORE_REF="<the exact Core release tag or commit being targeted>"
git checkout "$CORE_REF"
go run ./cmd/adapter-conformance --binary /absolute/path/to/integrated-recorder-adapter-owncast --json
```

Conformance checks framing, descriptor identity/stability, structured errors, stdout cleanliness, shutdown, and bounded exit. It does not establish safety or behavior for every Owncast instance.

## Release

Pushing a `v<version>` tag runs the release workflow at that exact tag commit and publishes Linux/amd64 and Linux/arm64 executable assets. The current official release is [v0.2.0](https://github.com/integrated-recorder/source.owncast/releases/tag/v0.2.0).

The Registry pins the published asset's actual URL, size, SHA-256, source commit, descriptor version, and Protocol v1 identity. Do not guess Registry metadata or substitute values from a local build.

## Ecosystem and license

- [Integrated Recorder Core](https://github.com/integrated-recorder/core)
- [Adapter SDK for Go](https://github.com/integrated-recorder/adapter-sdk-go)
- [Official Plugin Registry](https://github.com/integrated-recorder/plugin-registry)
- [source.soop](https://github.com/integrated-recorder/source.soop) — a separate integration under development

See [LICENSE](LICENSE) for this repository's license. The plugin does not include Owncast project artwork and does not imply endorsement by the Owncast project.
