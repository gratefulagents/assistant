# Development

## Repository Layout

The project follows the conventional Go command layout:

```text
cmd/assistant/          executable entrypoint
internal/assistant/     private application implementation
docs/                   user and maintainer docs
config.example.json     example assistant extension config
```

`cmd/assistant` should stay thin. Runtime behavior belongs in
`internal/assistant` unless there is a reason to expose a public Go package.

## Checks

Run before sending changes:

```sh
gofmt -w cmd internal
go test ./...
```

Useful extra checks:

```sh
go vet ./...
go test -race ./...
```

## Dependency Policy

`go.mod` must not contain local `replace` directives. If you need to develop
against a sibling checkout of `github.com/gratefulagents/sdk`, use an
uncommitted workspace outside the repository:

```sh
go work init .
go work use ../gratefulagents-sdk
```

The SDK dependency should be pinned to a public tagged release.

## Release Checklist

Before publishing a release:

- Confirm `go test ./...` passes from a clean clone.
- Confirm `go.mod` has no local `replace` directives.
- Confirm the pinned `github.com/gratefulagents/sdk` version is a public tag.
- Build the command with `go build ./cmd/assistant`.
- Review README, `docs/`, `SECURITY.md`, and `CHANGELOG.md`.
- Create a GitHub release with the same changelog entry.
