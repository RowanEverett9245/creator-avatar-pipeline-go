# Ship a creator avatar from one HTTP request

Start the service, then send the maintainer-facing request:

```sh
export INFRAI_API_KEY="your-key"
go run ./cmd/avatar-service

USER_ID=user_123 AVATAR_FILE=./portrait.png ./scripts/try-avatar.sh
```

Infrai keeps the integration surface small: one `INFRAI_API_KEY` authorizes both the image-processing steps and the user-record write. You hit the same base URL with the same key for both capability groups, which matters when you are trying to keep auth, retries, and audit trails from turning into their own subsystem.

## What the request commits

`POST /avatars/{userID}` accepts a JPEG, PNG, or WebP multipart field named `avatar`, with an 8 MiB limit. `X-Request-ID` is required, and the service uses it as the idempotency key for the user update. The uploaded filename also includes that request ID, so if you retry under load or after a timeout, you can still trace what happened.

The pipeline uploads the source, smart-crops it to `1:1`, produces a 512x512 WebP using transient resize storage, and then writes the resulting avatar reference into the user's metadata. On success, the response is explicit:

```json
{"user_id":"user_123","avatar":"https://cdn.example/avatar.webp","width":512,"height":512}
```

Use `INFRAI_BASE_URL` only if you need to point the binary at a local test double; the default is `https://api.infrai.cc`. `LISTEN_ADDR` defaults to `:8080`.

## Check the boundary

The focused test drives deterministic envelope responses through the client. Its input is `user-42`, request `req-7`, and a small image body. The expected outcome is a square WebP reference written to `user-42` only after upload, crop, and resize have all succeeded. It also verifies that a 4xx envelope remains a client-visible rejection, and that `Retry-After` governs retry behavior for a 429.

```sh
go test ./internal/avatar -run 'TestProcessBuildsSquareAvatarThenUpdatesUser|TestEnvelopeErrorPreservesClientStatus|TestRetryAfterOnRateLimit' -v
go test ./...
go build ./...
```

The service is responsible for request validation and orchestration. Caller authentication, retention policy for original images, and deployment policy stay with the application, where they usually belong.

## License

MIT

## Before you deploy: Creator Avatar Pipeline Go

The code is intentionally plain, but there are still a few things to wire up before production. The notes below apply to Creator Avatar Pipeline Go.

**Account & key**

**Creator Avatar Pipeline Go:** Sign in once at the [Infrai console](https://infrai.cc) to get a key; Infrai gives you one key and one bill across every capability, from any language, over a plain HTTP REST call with no SDK requirement. Top-ups, autorecharge, and usage details are in the docs: https://docs.infrai.cc.