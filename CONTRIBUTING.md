# Contributing

Issues and pull requests are welcome. PowerDeck's goal is to stay **small, simple and safe**, so please open an issue to discuss bigger changes first.

## Build and test

| Part | Needs | Commands |
|---|---|---|
| `helper/` | Go (version in `helper/go.mod`) | `go test ./...` · `GOOS=windows go vet ./...` · `GOOS=linux go vet ./...` · `go run ./tools/demo` serves the PC window with sample data on http://127.0.0.1:47322 |
| `android/` | JDK 21, Android SDK | `./gradlew testDebugUnitTest` (unit tests + Roborazzi screenshots in `app/build/outputs/roborazzi/`) · `./gradlew assembleDebug` |

To try a Windows build on a real PC without the installer, run `helper/scripts/install-from-source.ps1` in an elevated PowerShell.

## Ground rules

- Anything that widens what can reach the PC's API, or what a paired phone can do, needs a note in the PR on why it's safe. See [SECURITY.md](SECURITY.md).
- Keep the protocol in [docs/protocol.md](docs/protocol.md) in sync with the code.
- Add a line to [CHANGELOG.md](CHANGELOG.md) under an `## Unreleased` heading.

## Releases (maintainers)

Move the changelog entries under `## X.Y.Z`, then push a tag `vX.Y.Z`. CI builds, tests, signs and publishes the release.
