# CLIProxyAPI GitHub Copilot plugin

## Description

A native Go plugin that connects CLIProxyAPI to GitHub Copilot.
It runs inside the host, not as a separate HTTP proxy.
Supported platforms are Linux and macOS on AMD64/ARM64, FreeBSD on AMD64, and Windows on AMD64.

This is an independent integration, not an official GitHub product.
Copilot subscriptions, account policies, and usage limits still apply.
GitHub's internal Copilot endpoints are not a stable public inference API.

## Features

- GitHub device-code login and reuse of existing `copilot` credentials.
- Account-specific model discovery with a cache lifetime of 30-60 seconds.
- Configurable model prefix, allowlist, aliases, and exclusions.
- OpenAI Chat Completions, Responses, and Claude Messages, including streaming and tools.
- An embedded dashboard for connecting accounts, checking status, and refreshing models.
- In-memory Copilot tokens with one renewal retry after an upstream `401`, before output starts.
- Native library validation, isolated host integration tests, and plugin-store ZIP packaging.

## Build

Use Go 1.27.1 or newer, a working C compiler, and GNU Make (`gmake` on FreeBSD).
The plugin requires a plugin-enabled CLIProxyAPI host; v8.0.12 is the integration-test baseline.

```sh
make check
make build
```

A local build produces `dist/<goos>/<goarch>/github-copilot.so`, `github-copilot.dylib` on macOS, or `github-copilot.dll` on Windows.
The build checks the library's architecture, native format, OS ABI, and exported entrypoint.
The host and library must match in OS, architecture, and C runtime.
Linux release builds use Ubuntu 26.04; build on your deployment distribution if you need a different libc baseline, including Alpine/musl.

Local builds use `UNCONFIGURED` repository metadata.
Supply your real repository URL for a distributable build:

```sh
make build VERSION=0.1.0 REPOSITORY=https://github.com/YOUR_ACCOUNT/YOUR_REPOSITORY
```

### FreeBSD cross-build

On Linux with Clang, LLD, and the script's download/extraction tools installed:

```sh
make freebsd
```

This uses a checksum-pinned FreeBSD 14.4 sysroot and produces `dist/freebsd/amd64/github-copilot.so`.
It cross-compiles libraries and tests without running a FreeBSD VM or executing the FreeBSD tests.

### Integration tests

Supply an existing plugin-enabled CLIProxyAPI binary matching your platform:

```sh
make integration CPA_BINARY=/absolute/path/to/cli-proxy-api
```

CI runs build and unit checks; real-host integration tests must be run separately with `CPA_BINARY`.
Tests use an isolated host, mock GitHub/Copilot endpoints, and temporary credentials, not your existing deployment or account.
Additional dependency checks are available through `make audit`.
Review licensing before publishing; no distribution license has been selected for the newly written code.

## Install

1. Stop CLIProxyAPI and back up its configuration and auth directory.
2. Disable the old `cliproxyapi-copilot` plugin, if installed.
3. Copy the built library into `<plugins-dir>/<goos>/<goarch>/`, keeping its original filename.
4. Merge the configuration below into the existing host configuration.
5. Restart CLIProxyAPI and open **GitHub Copilot** from its plugin management menu.

Keep existing API keys, management authentication, providers, and credentials.
Do not run the old and new Copilot plugins together: both own the `copilot` credential provider.
Do not add an `openai-compatibility` provider or a synthetic Copilot API key.

## Configure settings

### Basic configuration

```yaml
plugins:
  enabled: true
  dir: ./plugins
  configs:
    github-copilot:
      enabled: true
      model_prefix: copilot
      models: []
      models_excluded: []
      model_cache_ttl_seconds: 60
```

See [examples/config.yaml](examples/config.yaml) for the complete configuration.

### Account connection

Open `/v0/resource/plugins/github-copilot/dashboard`, enter the host management key, and select **Connect GitHub account**.
Approve the device code in GitHub, then use **Check accounts** or **Refresh models** to verify the connection.
The dashboard does not persist the management key in browser storage, URLs, or cookies.

Existing credentials with `type: copilot` and `github_access_token` work without a new login.
The host stores GitHub credentials; short-lived Copilot tokens stay in memory.
No login is performed during build, installation, or startup.

### Model settings

| Setting | Default | Purpose |
| --- | --- | --- |
| `model_prefix` | `copilot` | Exposes IDs as `copilot/<upstream-id>`; blank uses the default |
| `models` | `[]` | Optional `{name, alias}` allowlist; empty discovers all eligible models |
| `models_excluded` | `[]` | Case-insensitive upstream model ID prefixes to exclude |
| `model_cache_ttl_seconds` | `60` | Catalog cache lifetime, from 30 to 60 seconds |

```yaml
model_prefix: copilot
models:
  - name: upstream-model-id
  - name: upstream-model-id
    alias: coding
```

Without an alias, the public ID is `<model_prefix>/<name>`.
An explicit alias replaces the entire public ID and must be unique.
Use the exact IDs returned by the host's `/v1/models`; do not add a duplicate credential prefix.
Settings changes update host listings asynchronously, so update client selections when IDs change.

Only models enabled for the selected account, with chat capabilities and a supported endpoint, are eligible.
An explicit disabled or unknown policy rejects a model; an omitted policy is accepted.
Allowlisting does not enable unavailable models or override exclusions.
Expired catalogs and failed explicit refreshes fail closed; there are no static fallback models.
A multi-account host may list a combined catalog, but each request is checked against its selected account.

### Authentication settings

| Setting | Default | Purpose |
| --- | --- | --- |
| `github_client_id` | `Iv1.b507a08c87ecfe98` | Public GitHub device-flow client ID |
| `github_scope` | `read:user` | GitHub authorization scope |
| `oauth_timeout_seconds` | `900` | Device-login timeout, from 60 to 1800 seconds |
| `token_expiry_buffer_seconds` | `300` | Token renewal buffer, from 30 to 900 seconds |

### Security and API limits

Endpoint overrides are trusted operator settings, not values to accept from user prompts.
HTTPS is required; `allow_insecure_base_urls` permits loopback HTTP only for tests.
Authentication and background discovery use the host's global upstream proxy; inference retains the host's request/account transport context.
Protect the auth directory and management API, and disable request logging for credential exchanges.
Native plugins run with the host's privileges.

Supported client routes are `POST /v1/chat/completions`, `POST /v1/responses`, and `POST /v1/messages`.
Same-protocol requests preserve native fields; cross-protocol conversion cannot preserve every provider-specific feature.
Claude token counting is a local estimate, not an authoritative Anthropic count.
Embeddings, completion-only models, and generic HTTP forwarding are not supported.
Incomplete streams fail, and delivered streams are never replayed by the plugin.
Set host `request-retry: 0` if you also need to disable host-level retries.
