# Contributing

Thanks for helping! This project follows a normal review-based workflow.

## Workflow

1. Open an issue first for larger changes or anything touching the security model.
2. Branch from `main`: `feat/…`, `fix/…`, `docs/…`, `chore/…`.
3. Use [Conventional Commits](https://www.conventionalcommits.org/)
   (`feat: add recreate action`, `fix(egress): block NAT64 prefix`).
4. Run `make check` locally (format, vet, lint, tests).
5. Open a pull request using the template. Every PR needs
   * green CI,
   * at least one review,
   * an updated `CHANGELOG.md` entry under *Unreleased* for user-visible changes,
   * updated docs/ADRs if behaviour or architecture changes.
6. Changes to `internal/broker/policy.go`, `internal/egress/` or the compose
   file's security settings additionally need an updated threat model entry.

## Development setup

Requirements: Go (see `go.mod`), Node.js 24+, Docker with Compose v2.

```bash
make ui          # vendor xterm.js into the embedded static files
make test        # unit tests
make images      # build both container images locally
make it          # integration tests against a local Docker engine
make dev-up      # run the stack from local images on http://localhost:8420
```

## Releasing

Releases are fully automatic:

1. Set the new version in `VERSION` and in the four image references in
   `docker-compose.yml` (CI checks that they match; the file must stay free of
   `${...}` interpolation, which ZimaOS cannot parse). Add a changelog entry.
2. Push to `main`. The release workflow notices that `vX.Y.Z` does not exist
   yet, runs the full test suite, publishes multi-arch images to GHCR
   (`dockerbox-core`, `dockerbox-agentbox`) with SBOM and provenance, and
   creates the tag and the GitHub release.

## Code style

* Go: `gofmt`, `go vet`, `staticcheck`; small packages, standard library first.
* Frontend: plain ES modules, no framework, no inline scripts (CSP).
* No new runtime dependency without an ADR.

## Privacy

Do not commit personal data: no real names, e-mail addresses, hostnames, IPs,
tokens or screenshots showing private information. CI runs a secret scanner.
