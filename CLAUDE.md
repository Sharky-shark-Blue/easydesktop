# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

EasyDesktop is a Windows-focused desktop-path switcher with two implementations in the same repo:

- **Rust app** (`src/main.rs`, crate `easydesktop` from `Cargo.toml`): primary GUI app built with `eframe/egui`, with CLI fallback mode in the same binary.
- **Go app** (`main.go`, module `easydesktop` from `go.mod`): Windows console/CLI implementation that can launch a GUI helper executable.
- **Go WebView helper** (`ui/main_ui.go`): WebView2-based hotkey/tray UI that invokes `easydesktop.exe` for actual switching.

Both stacks operate on the same desktop-switching domain and share persisted history at `%LocalAppData%/EasyDesktop/history.json`.

## Common commands

Run commands from repository root.

### Rust (src/main.rs)

- Build:
  - `cargo build`
- Typecheck quickly:
  - `cargo check`
- Run:
  - `cargo run`
  - `cargo run -- --help`
  - `cargo run -- -r` (CLI restore mode implemented in Rust entrypoint)

### Go CLI (main.go)

- Build executable:
  - `go build -o easydesktop-go.exe .`
- Run directly:
  - `go run . --help`
  - `go run . --gui`
  - `go run . -r`

### Go WebView helper (ui/main_ui.go)

- Build helper executable:
  - `go build -o dist/easydesktop-ui.exe ./ui`

## Testing

No `_test.go` files or Rust `#[test]` blocks are currently present.

When adding tests:

- Go all tests: `go test ./...`
- Go single test: `go test ./... -run TestName`
- Rust all tests: `cargo test`
- Rust single test: `cargo test test_name`

## Architecture notes

### Desktop switching behavior

- Core behavior is to update Windows Desktop known-folder path (registry + shell notification behavior is implemented in both codepaths).
- The tool switches Desktop mapping only; it does not migrate existing files.

### Rust path (`src/main.rs`)

- `main` first checks for CLI mode (`run_cli_mode`); otherwise starts an always-on-top palette UI (`eframe`).
- App state is represented by `EasyDesktopApp` and `SavedState` (history/pinned), with search/filter/navigation state in-memory.
- Windows-specific integration is gated by `#[cfg(windows)]` and includes global hotkey registration (`global-hotkey`) and registry updates (`winreg`).

### Go path (`main.go` + `ui/main_ui.go`)

- `main.go` handles command parsing, interactive recent-picker CLI mode, history CRUD, and GUI launcher resolution.
- `launchGUI` looks for `easydesktop-ui.exe` in executable-adjacent locations (root or `dist/`).
- `ui/main_ui.go` owns system-tray + global hotkey (`Ctrl+E`) behavior and WebView2 window lifecycle.
- WebView callbacks (`goSwitch`, `goLoadEntries`, `goPin`, `goDelete`, `goHide`) bridge UI actions to local history operations and process invocation.

## Platform constraints

- This repository is Windows-centric: desktop switching, hotkeys, tray integration, and WebView2 integration are all Windows APIs/dependencies.
- Non-Windows environments can still parse/build some code paths but cannot exercise full runtime behavior.
