# LimitDock

Go Windows shell app — renders AI tool quota bars as a native Windows appbar/overlay. Architecture details in vault/docs.

## Key Paths
- `cmd/limitdock/` — entry point, single-instance mutex, tray
- `internal/provider/` — quota aggregator (native readers: ClaudeCode, Codex, GeminiCLI, Cursor, Antigravity)
- `internal/ui/app.go` — rendering loop, appbar, docking, DPI handling
- `internal/quota/` — normalization (rate_limit_*, quota*, usage_five_hour, usage_seven_day*, plan_percent_used)
- `internal/settings/` — settings.json persistence

## Commands
- `go test ./...` — run all tests
- `go build ./cmd/limitdock` — build main binary
- `go build ./cmd/limitdock-release` — build release binary

## Critical Rules
- No CGO. Windows APIs via `golang.org/x/sys/windows` only.
- Quota normalization is narrow — filter out throughput, spend, request, token, cost metrics.
- First-reader-wins for duplicate snapshot keys. Registration order: ClaudeCodeReader, CodexReader, GeminiCLIReader, CursorReader, AntigravityReader.
- `settings.json` is gitignored — never commit it.
- Throughput/spend/request/cost rows are filtered before rendering (quota-only display).
