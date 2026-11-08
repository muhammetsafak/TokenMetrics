# Contributing to TokenMetrics

Thank you for your interest in contributing. This guide explains how to propose changes.

## Before You Start

- Small fixes (typos, broken links, obvious bugs) can be sent directly as a pull request.
- For a new feature or a change in behavior, open an issue first so the approach can be discussed before you write code.
- Opening a pull request does not guarantee that it will be accepted.
- Do not report security vulnerabilities in issues or pull requests; follow the [security policy](SECURITY.md).

## Development Setup

Requirements:

- Go (the version in `go.mod`)
- Node.js 22 and npm
- Wails CLI v2: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1`
- Linux only: `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`

Clone the repository and install dependencies:

```bash
git clone https://github.com/muhammetsafak/TokenMetrics.git
cd TokenMetrics
go mod download
cd frontend && npm ci && npm run build && cd ..
```

`main.go` embeds `frontend/dist`, so the frontend must be built once before `go vet` or `go test` on a fresh clone.

Run the app in development mode with live reload, or build a production binary into `build/bin/`:

```bash
wails dev
```

```bash
wails build
```

On Linux with webkit2gtk 4.1, add `-tags webkit2_41` to the Wails and Go commands.

The app and tray icons are generated from code. After changing `cmd/icongen`, regenerate them with `go generate` and commit the result.

## Running Tests

These are the same commands that CI runs. Run both before opening a pull request.

```bash
test -z "$(gofmt -l $(git ls-files '*.go'))" && go vet ./... && (cd frontend && npm run check)
```

```bash
go test ./...
```

## Commit Messages

This project follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```text
<type>(<scope>)!: <description>

<body>
```

- **Types:** `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.
- **Scope** is optional, lowercase kebab-case, and names a module or area: `feat(tray): ...`.
- **Description** is in English, imperative mood, starts with a lowercase letter, has no trailing period and is at most 72 characters.
- **Body** is optional and explains why the change was made. Wrap lines at 72 characters.
- Mark a breaking change only with `!` after the type or scope, and describe it in the body.
- Do not add footers or trailers (`BREAKING CHANGE:`, `Refs:`, `Closes #12`, `Co-Authored-By:`, `Signed-off-by:` or any tool attribution). Link issues in the pull request description instead.
- Keep one logical change per commit.

Example:

```text
fix(codex): skip rollout lines without a response id
```

## Pull Requests

1. Create a branch from `main` named `<type>/<short-kebab-description>`, for example `feat/gemini-api` or `fix/123-empty-session`.
2. Write the pull request title in Conventional Commits format. Pull requests are squash merged, so the title becomes the commit message on `main`.
3. If the change affects users, add an entry under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
4. Fill in the pull request template and link the related issue with `Closes #<number>`.
5. Make sure CI passes. A maintainer reviews the pull request and may ask for changes before merging.

Releases are published by pushing a `vX.Y.Z` tag; `.github/workflows/release.yml` builds the archives and publishes the GitHub Release with the matching `CHANGELOG.md` section as its notes.

## Code of Conduct

This project is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold it.
