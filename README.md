# gomall-cli

Enterprise-grade Go CLI scaffold.

## Architecture

- `cmd/`: command layer (routing, args, command tree)
- `internal/app`: runtime dependency container (config, logger, API client, session store)
- `internal/config`: unified config loading (defaults + env + file + flags)
- `internal/logging`: structured logging setup
- `internal/clierr`: typed error with stable exit codes
- `internal/gomallapi`: HTTP client + standard response envelope parsing
- `internal/auth`: login service
- `internal/session`: local auth session persistence

## Quick Start

```bash
go run . --help
go run . version
go run . hello sun
```

## Login And Session

```bash
# safer way: avoid password in shell history
read -rsp "Password: " GOMALL_PASSWORD && printf '\n'
printf '%s\n' "$GOMALL_PASSWORD" | go run . auth login -u <email> --password-stdin

go run . auth status
go run . auth logout

# model search (requires login first)
go run . model search --name qwen

# my created models (requires login first)
go run . model created
go run . model created --name qwen

# create model (requires login first)
go run . model create --name demomodel
go run . model create --name demomodel --visibility 5

# delete model by id (requires login first)
go run . model delete 2546

# model detail by author/name (requires login first)
go run . model detail gomall/test1
go run . model detail gomall/test1 --show-readme

# clone model repository (requires login first)
go run . model clone gomall/test1
go run . model clone gomall/test1 --into ./downloads
go run . model clone 1700
```

After login, CLI stores `token / expireTime / username / gitlabToken / gitlabId` in local session file.
`gitlabToken` is fetched from `/goMallApi/api/users/get_current_user` right after login, and `model clone` uses it for Git authentication.
After clone, CLI will automatically hydrate Git LFS pointer files with concurrent download + retry (exponential backoff), and show real-time progress (speed / remaining / ETA). If server supports HTTP Range, large files are downloaded in parallel chunks.
Session file is encrypted with AES-256-GCM.
Session key is always machine-derived.
Machine-derived secret supports macOS / Linux / Windows.
Default session file:

```text
~/.gomall-cli/session.json
```

## Global Flags

```bash
--config string
--env string
--log-level string
--log-format string
--api-base-url string
--api-timeout duration
--api-lfs-timeout duration
--api-lfs-idle-timeout duration
--api-lfs-chunk-size-mb int
--api-lfs-download-url-override string
--api-insecure
--api-user-agent string
--auth-login-path string
--auth-token-header string
--auth-session-file string
```

## Environment Variables

All env vars use `GOMALL_` prefix.

- `GOMALL_ENV`
- `GOMALL_LOG_LEVEL`
- `GOMALL_LOG_FORMAT`
- `GOMALL_API_BASE_URL`
- `GOMALL_API_TIMEOUT`
- `GOMALL_API_LFS_TIMEOUT`
- `GOMALL_API_LFS_IDLE_TIMEOUT`
- `GOMALL_API_LFS_CHUNK_SIZE_MB`
- `GOMALL_API_LFS_DOWNLOAD_URL_OVERRIDE`
- `GOMALL_API_INSECURE`
- `GOMALL_API_USER_AGENT`
- `GOMALL_AUTH_LOGIN_PATH`
- `GOMALL_AUTH_TOKEN_HEADER`
- `GOMALL_AUTH_SESSION_FILE`

## LFS Download URL Override

When LFS batch response returns internal download hosts (for example `http://10.x.x.x/lfs-objects/...`), you can force override to another host:

```yaml
api-lfs-download-url-override: "http://my.host.cn"
```

Then `download.href` will be rewritten as `http://my.host.cn/lfs-objects/...` while keeping original query params/signature.
