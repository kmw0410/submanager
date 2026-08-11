# 2026-08-12
- Expanded the built-in subscription presets, improved skipped-payment status presentation, stabilized asset URLs, and added repository/container ignore rules.
- Added a GitHub-hosted multi-architecture GHCR publish workflow with latest and date-based image tags, plus the image pull command in README.md.
- Commits: `2e56915 feat: refine subscription dashboard`, `aab17eb ci: publish images to GHCR`; both were pushed to `origin/main`.
- Validation: `gofmt -d`, `go test ./...`, `go vet ./...`, `go build ./...`, and `docker compose config` completed successfully; Docker image build was skipped at the user's request.
