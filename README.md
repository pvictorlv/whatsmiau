# WhatsMiau

![logo-whatsmiau](logo.png)

WhatsMiau is a backend service for WhatsApp, built with Go. It uses the Whatsmeow library to connect to WhatsApp and provides an HTTP API to send and receive messages.

[Community Whatsapp (BR)](https://chat.whatsapp.com/FXMrTY552nOBFXU71Be8Zh)
## About The Project

This project provides a robust, scalable, and production-ready solution for integrating WhatsApp functionalities into your applications. It is extremely lightweight, consuming very little memory, making it ideal for resource-constrained environments.

It's designed to be compatible with the Evolution API, making it a flexible choice for developers familiar with that ecosystem.

## Features

- **Lightweight & Efficient:** Optimized for low memory consumption.
- **Production Ready:** Stable and reliable for use in production environments.
- **WhatsApp Integration:** Connects to WhatsApp to send and receive messages.
- **HTTP API:** Exposes an HTTP API for easy integration with other services.
- **Redis Support:** Uses Redis for session storage and caching.
- **SQLite Database:** Utilizes SQLite for persistent data storage.
- **Environment-based Configuration:** Easily configure the application using environment variables.
- **Structured Logging:** Implements structured logging with Zap for better monitoring and debugging.
- **Group & Community Management:** Full support for WhatsApp group and community operations.
- **Web Manager Dashboard:** Built-in web UI for managing instances, viewing QR codes, and monitoring status.
- **All Evolution API Message Types:** Compatible with all Evolution API message types for sending and receiving.
- **Message Reactions:** Support for sending and receiving emoji reactions.
- **Message Deletion:** Ability to delete messages for everyone.

## Getting Started

To get a local copy up and running follow these simple steps.

### Prerequisites

- Go 1.24 or higher
- Redis
- SQLite

### Installation

1. Clone the repo
   ```sh
   git clone https://github.com/verbeux-ai/whatsmiau.git
   ```
2. Install Go packages
   ```sh
   go mod tidy
   ```
3. Set up your environment variables by copying `.env.example` to `.env` and filling in the required values.
   ```sh
   cp .env.example .env
   ```
4. Run the application
   ```sh
   go run main.go
   ```

## Running with Docker

You can also run the application using Docker and Docker Compose.

1.  **Build and run the containers:**
    ```sh
    docker-compose up -d --build
    ```
2.  **View the logs:**
    ```sh
    docker-compose logs -f
    ```
3.  **Stop the containers:**
    ```sh
    docker-compose down
    ```

## Docker Image

Official Docker images are available on Docker Hub.

- **Latest stable release:** `impedr029/whatsmiau:vX.Y.Z` [(see versions)](https://github.com/verbeux-ai/whatsmiau/tags)
- **Development version:** `impedr029/whatsmiau:develop`

You can pull the latest stable image with (example):
```sh
docker pull impedr029/whatsmiau:vX.Y.Z
```

Or the development image with:
```sh
docker pull impedr029/whatsmiau:develop
```

## Configuration

The application is configured using environment variables. The following variables are available:

| Variable | Description | Default |
| --- | --- | --- |
| `PORT` | The port the server will run on. | `8080` |
| `DEBUG_MODE` | Enable or disable debug mode. | `false` |
| `DEBUG_WHATSMEOW` | Enable or disable debug mode for Whatsmeow. | `false` |
| `REDIS_URL` | The URL of the Redis server. | `localhost:6379` |
| `REDIS_PASSWORD` | The password for the Redis server. | `` |
| `REDIS_TLS` | Enable or disable TLS for Redis. | `false` |
| `API_KEY` | The API key to protect the service. | `` |
| `DIALECT_DB` | The database dialect to use (`sqlite3` or `postgres`). | `sqlite3` |
| `DB_URL` | The database connection URL. | `file:data.db?_foreign_keys=on` |
| `GCS_ENABLED` | Enable or disable Google Cloud Storage. | `false` |
| `GCS_BUCKET` | The GCS bucket name. | `whatsmiau` |
| `GCS_URL` | The GCS URL. | `https://storage.googleapis.com` |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to GCP service account JSON key. | `` |
| `GCL_APP_NAME` | The GCL application name. | `whatsmiau-br-1` |
| `GCL_ENABLED` | Enable or disable Google Cloud Logging. | `false` |
| `GCL_PROJECT_ID` | The GCL project ID. | `` |
| `EMITTER_BUFFER_SIZE` | The emitter buffer size. | `2048` |
| `EMITTER_WORKERS` | The number of emitter workers. | `50` |
| `HANDLER_SEMAPHORE_SIZE` | The handler semaphore size. | `512` |
| `PROXY_ADDRESSES` | A comma-separated list of proxy addresses. Example: `SOCKS5://user:pass@host:port,HTTP://host:port` | `` |
| `PROXY_STRATEGY` | The strategy to use when selecting a proxy from the list (`RANDOM`). | `RANDOM` |
| `PROXY_NO_MEDIA` | If set to `true`, media will not be sent through the proxy. | `false` |
| `PROXY_POOL_FILE` | Path to a file with the rotating proxy pool. See [Proxy pool](#proxy-pool). | `` |
| `PROXY_POOL_ROTATION` | How a proxy is picked from the pool (`round_robin`, `random` or `sticky`). | `round_robin` |
| `PROXY_POOL_COOLDOWN` | How long a proxy stays quarantined after a connection failure. | `5m` |
| `MANAGER_URL` | The public URL for the manager dashboard. | `` |
| `MEDIA_TRANSFER_TIMEOUT` | Ceiling for a single media transfer (download the source, upload it to WhatsApp). See [Large media](#large-media). | `10m` |
| `MEDIA_EVENT_TIMEOUT` | Budget to convert an incoming message, including downloading its attachment and pushing it to storage. | `10m` |
| `WEBHOOK_TIMEOUT` | Ceiling for one webhook delivery attempt. | `60s` |
| `WEBHOOK_BASE64_MAX_BYTES` | Largest attachment embedded as base64 in the webhook payload, used only when no storage is configured. `0` disables the cut. | `25165824` (24 MiB) |
| `S3_ENABLED` | Store media in an S3-compatible bucket (R2, MinIO, AWS). See [Media storage](#media-storage). | `false` |
| `S3_ENDPOINT` | Bucket host, without scheme. Example: `<account>.r2.cloudflarestorage.com` | `` |
| `S3_PORT` | Port. Only appended to the endpoint when it is not 80/443. | `443` |
| `S3_USE_SSL` | Whether to talk to the endpoint over TLS. | `true` |
| `S3_REGION` | Bucket region. R2 uses `auto`. | `` |
| `S3_BUCKET` | Bucket name. | `` |
| `S3_ACCESS_KEY` | Access key id. | `` |
| `S3_SECRET_KEY` | Secret access key. | `` |
| `S3_PUBLIC_URL` | Public base URL for the objects. Without it, media is served through presigned URLs, **which expire**. | `` |
| `S3_PREFIX` | Key prefix for this service inside the bucket. | `whatsmiau` |
| `S3_PRESIGN_EXPIRY` | Lifetime of a presigned URL. Only used when `S3_PUBLIC_URL` is empty. | `168h` |

## Media storage

With `S3_ENABLED` (or `GCS_ENABLED`), an incoming attachment goes from the
WhatsApp CDN to a temporary file to the bucket, all streaming, and the webhook
carries only `mediaUrl`. Nothing is base64-encoded and nothing is held whole in
memory on either side.

Without storage, the only way to hand the file to the consumer is to embed it in
the event as base64 — which inflates it by a third and passes it through memory
on both ends. That path still works and is bounded by
`WEBHOOK_BASE64_MAX_BYTES`; above the cap the event ships without `base64` and
the consumer fetches the file from `/chat/getBase64FromMediaMessage`.

The variable names match the evolution-api's on purpose, so the same `.env`
block configures both and they can share one bucket. `S3_PREFIX` is what keeps
their objects apart — the evolution-api writes under `evolution-api/`.

> ⚠ **Check the bucket's expiration rule against your prefix.** Lifecycle rules
> are usually scoped to a prefix. If yours only expires `evolution-api/`, media
> written under `whatsmiau/` is never deleted and the bucket grows forever.
> Either add the matching rule or point `S3_PREFIX` at the covered prefix.

> Prefer `S3_PUBLIC_URL` over presigned URLs. A presigned URL expires, and the
> URL is what stays recorded in the conversation history — media that has to
> keep opening months later cannot depend on a signature with a lifetime.

## Large media

Media never passes through memory as a whole: sends stream the source file to
disk and upload it from there, and receives stream the attachment from the CDN
to disk before it goes to storage. That is what keeps an 80 MB document from
turning into hundreds of megabytes of RAM during encryption.

`MEDIA_TRANSFER_TIMEOUT` and `MEDIA_EVENT_TIMEOUT` are ceilings, not waits. They
exist so a stalled peer cannot hang a transfer forever; a healthy transfer
finishes long before them. Raise them if you serve very large files over slow
links.

## Proxy pool

Instead of configuring a proxy per instance, you can point `PROXY_POOL_FILE` at a
file listing every proxy you own — typically one port per outbound IPv6 on a
3proxy VPS — and let the API hand one out to each instance. The file format is
the same one the Evolution API reads, so a single file can feed both.

JSON (`.json` extension), either a plain array or wrapped in `proxies`:

```json
[
  { "host": "186.194.48.234", "port": 30000, "protocol": "http", "username": "user", "password": "secret" },
  { "host": "186.194.48.234", "port": 30001, "protocol": "http", "username": "user", "password": "secret" }
]
```

Or one URL per line (any other extension), where `#` starts a comment and the
scheme defaults to `http`:

```
http://user:secret@186.194.48.234:30000
socks5://user:secret@[2804:abcd::1]:1080
186.194.48.234:30001
```

How it behaves:

- An instance that has its own proxy keeps it; the pool only serves instances without one.
- Each instance keeps the proxy it was given across reconnects, so its exit IP stays stable.
- A proxy that fails to connect is quarantined for `PROXY_POOL_COOLDOWN` and the instance moves to another one.
- The file is re-read whenever it changes, so proxies can be added or removed without restarting.
- If the pool is configured but empty (missing file, no valid entries), instances refuse to connect instead of falling back to a direct connection that would expose the server IP.

## Versioning

We use [SemVer](http://semver.org/) for versioning. For the versions available, see the [tags on this repository](https://github.com/verbeux-ai/whatsmiau/tags).

## Compatibility

This API is designed to be compatible with the Evolution API. This means that you can use clients and tools designed for the Evolution API with this project.

It exclusively supports webhooks in the Evolution API format, offering two distinct approaches for their implementation, providing flexibility for different use cases.

## Migration from Evolution API

WhatsMiau is designed to be a lightweight, drop-in replacement for the Evolution API. If you are running WhatsMiau on the same host and port as your previous Evolution API instance, migration is seamless.

Since WhatsMiau maintains compatibility with the Evolution API's routes, you only need to stop your Evolution API server and start the WhatsMiau server. No changes to your existing API calls are necessary.

### Example

For instance, if you were sending a text message using a `curl` command to an Evolution API server running on `localhost:8080`, the exact same command will work with WhatsMiau.

**Before (Evolution API):**
```bash
curl -X POST 'http://localhost:8080/message/sendText/my-instance' \
-H 'Content-Type: application/json' \
-H 'apikey: YOUR_API_KEY' \
-d ".{\"number\": \"1234567890\",\"textMessage\": {\"text\": \"Hello from Evolution API!\"}}"
```

**After (WhatsMiau):**

Simply point your application to the WhatsMiau server URL. The same request will be handled by WhatsMiau:
```bash
curl -X POST 'http://localhost:8080/v1/message/sendText/my-instance' \
-H 'Content-Type: application/json' \
-H 'apikey: YOUR_API_KEY' \
-d ".{\"number\": \"1234567890\",\"textMessage\": {\"text\": \"Hello from WhatsMiau!\"}}"
```

## API Documentation

The API is fully documented using Swagger/OpenAPI. Once the server is running, you can access the interactive documentation at:

```
http://localhost:8080/swagger/index.html
```

No API key is required to access the documentation page.

The Swagger UI allows you to explore all available routes, view request/response schemas, and test the API directly from your browser.

## Manager Dashboard

WhatsMiau includes a built-in web manager dashboard for managing your WhatsApp instances visually.

### Access

```
http://localhost:8080/manager/
```

### Features

- View all instances and their connection status
- Generate and display QR codes for authentication
- Pair with phone using pairing codes
- Monitor instance health and activity

### Authentication

If `API_KEY` is configured, the manager dashboard will require login. If no `API_KEY` is set, the dashboard is accessible without authentication (useful for local development).

## Supported Events

The application can send webhook events for the following actions:

| Event             | Description                                         |
|-------------------|-----------------------------------------------------|
| `MESSAGES_UPSERT` | Triggered when a new message is received.           |
| `MESSAGES_UPDATE` | Triggered when a message status changes (e.g., read). |
| `MESSAGES_DELETE` | Triggered when a message is deleted for everyone.   |
| `MESSAGES_SET` | Triggered with batches of history-sync messages, including on-demand recovery of a single lost message. |
| `MESSAGES_UNDECRYPTABLE` | Triggered when a message arrived but could not be decrypted, after whatsmeow exhausted its retries. |
| `CONTACTS_UPSERT` | Triggered when a contact is created or updated.     |
| `CONNECTION_UPDATE` | Triggered when connection state changes (connected, disconnected, failed). |
| `GROUP_PARTICIPANTS_UPDATE` | Triggered when participants join or leave a group. |
| `QRCODE_UPDATED` | Triggered when a new QR code or pairing code is generated. |
| `CALL` | Triggered when an incoming call is offered or terminated. |

### Message payload shape

`data.message` always carries the message in the Baileys/Evolution key spelling
(`imageMessage`, `protocolMessage`, `key.id`, `key.remoteJid`, ...).

Types with an explicit mapping are emitted first and enriched (`mediaUrl` and
`base64` for media). Every remaining protobuf field is then appended verbatim
from the WhatsApp protocol, so a message type this API does not model yet still
reaches the consumer instead of being reported as `unknown`. Where a mapping is
partial, the two are deep-merged and the mapped value wins.

`data.messageType` is the name of the first content field, matching how Baileys
consumers derive the type.

### Lost messages

A message that cannot be decrypted is reported through `MESSAGES_UNDECRYPTABLE`
and always logged at error level, even when the instance is not subscribed to
the event. whatsmeow additionally asks the phone to resend it; the resent
message arrives later as an on-demand `MESSAGES_SET`, which is delivered
regardless of the instance's `syncFullHistory` setting.


## Contributors

<a href="https://github.com/verbeux-ai/whatsmiau/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=verbeux-ai/whatsmiau" />
</a>

## Did you like project?
Donate: https://buy.stripe.com/8x28wI5vKfPbe9b8ih1VK0f