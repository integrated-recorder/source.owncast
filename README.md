# Integrated Recorder Owncast Source Plugin

**한국어** | [English](README.en.md)

Owncast instance의 live 상태와 HLS manifest를 Integrated Recorder에 연결하는 first-party Source Plugin입니다. Adapter Protocol v1 standalone executable이며 Core image에는 bundled되지 않습니다.

> **배포 상태:** v0.2.0을 [공식 Plugin Registry](https://integrated-recorder.github.io/plugin-registry/catalog-v3.json)에서 설치할 수 있습니다. Registry 승인과 CI 검증은 배포 identity를 확인하는 절차이며, plugin 안전성 보장이나 sandbox를 뜻하지 않습니다.

## 역할

이 plugin은 Owncast의 instance URL과 API 의미를 Adapter Protocol의 `resolve`, `watch`, `metadata` capability로 제공합니다. Recording, segment 보관, VOD, archive integrity는 [Core](https://github.com/integrated-recorder/core)의 책임입니다.

## Owncast 동작

입력은 Owncast instance base URL입니다. Resolve는 query/fragment를 제거하고 `/hls/stream.m3u8`을 가리킵니다. Watch와 metadata는 같은 instance path의 `/api/status`를 조회합니다.

- `online: false`는 정상적인 offline 상태입니다. 네트워크, HTTP, malformed response 오류는 offline으로 숨기지 않습니다.
- Live 결과에는 HLS media source, title, `lastConnectTime` session reference 및 해석된 시작 시간이 포함됩니다.
- 알려진 빈 `streamTitle`은 빈 metadata title로 유지합니다.
- HTTP 401/403은 안전한 `authentication_required` protocol error가 됩니다.

## 네트워크와 보안

상태 요청은 HTTP/HTTPS만 허용하고 credential URL과 public이 아닌 network target을 거부합니다. DNS 결과의 public address에 연결을 고정하고 redirect target도 검사합니다. 환경 proxy 상속을 끄고 redirect, timeout, response body 크기를 제한합니다. 임의 header나 cookie 주입은 지원하지 않습니다.

반환된 manifest에는 Core가 자체 media acquisition request policy를 적용합니다. Plugin은 native executable이며 sandbox가 없습니다. [Adapter SDK](https://github.com/integrated-recorder/adapter-sdk-go)와 [Plugin Trust Model](https://github.com/integrated-recorder/core/blob/main/docs/PLUGIN_TRUST_MODEL.md)을 참고하세요.

## 개발과 검증

이 저장소는 public Go SDK를 사용하며 Core internal package를 import하지 않습니다.

```sh
go mod tidy
go test ./...
go test -race -count=1 ./...
go vet ./...
go build -trimpath -ldflags='-X github.com/integrated-recorder/source.owncast/internal/owncast.version=0.2.0' \
  -o integrated-recorder-adapter-owncast ./cmd/integrated-recorder-adapter-owncast
```

개발 descriptor는 `0.2.0-dev`를 보고합니다. Release build는 exact `v<version>` tag에서 descriptor version을 설정합니다.

실제 executable 호환성은 대상 Core commit/tag의 black-box `adapter-conformance`로 확인하세요. 대상 commit을 직접 선택해 Core checkout에서 실행합니다.

```sh
git clone https://github.com/integrated-recorder/core.git core
cd core
CORE_REF="<the exact Core release tag or commit being targeted>"
git checkout "$CORE_REF"
go run ./cmd/adapter-conformance --binary /absolute/path/to/integrated-recorder-adapter-owncast --json
```

Conformance는 framing, descriptor identity/stability, structured error, stdout 정리, shutdown 및 bounded exit를 확인하지만 모든 Owncast instance에서 안전성이나 동작을 보증하지는 않습니다.

## Release

`v<version>` tag를 push하면 해당 tag commit에서 Linux/amd64 및 Linux/arm64 executable asset을 생성하는 release workflow가 실행됩니다. 현재 공식 release는 [v0.2.0](https://github.com/integrated-recorder/source.owncast/releases/tag/v0.2.0)입니다.

Registry는 release asset의 실제 URL, size, SHA-256, source commit, descriptor version과 Protocol v1 identity를 pin합니다. Registry metadata는 추정하거나 local build 값으로 대체하지 않습니다.

## 생태계와 라이선스

- [Integrated Recorder Core](https://github.com/integrated-recorder/core)
- [Adapter SDK for Go](https://github.com/integrated-recorder/adapter-sdk-go)
- [공식 Plugin Registry](https://github.com/integrated-recorder/plugin-registry)
- [source.soop](https://github.com/integrated-recorder/source.soop) — 별도 개발 중인 integration

이 저장소의 라이선스는 [LICENSE](LICENSE)를 참고하세요. 이 plugin은 Owncast project artwork를 포함하지 않으며 Owncast project의 endorsement를 의미하지 않습니다.
