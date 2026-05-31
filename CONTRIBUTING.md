# Contributing

Thanks for contributing to Assistant.

## Local Setup

```sh
git clone https://github.com/gratefulagents/assistant.git
cd assistant
go test ./...
```

See [docs/development.md](docs/development.md) for Go workspace notes when
developing against a local SDK checkout.

## Pull Requests

Before opening a pull request:

- Run `gofmt -w cmd internal`.
- Run `go test ./...`.
- Keep `cmd/assistant` as the thin executable entrypoint.
- Put implementation changes in `internal/assistant`.
- Update docs when flags, defaults, security behavior, config, or integrations
  change.
- Do not commit secrets, personal config files, generated binaries, or local
  `go.work` files.

## Code Style

Use normal Go style and simple package boundaries. Prefer small, testable
functions over new abstractions unless the abstraction removes real duplication
or clarifies a shared contract.

## Issues

Bug reports should include:

- Assistant version or commit.
- Go version.
- Operating system.
- Command and flags used.
- Expected behavior.
- Actual behavior and relevant logs.

Remove secrets and personal data from logs before posting.
