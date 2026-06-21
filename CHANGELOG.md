# Changelog

All notable changes to this project are documented in this file.


## [0.3.0] - 2026-06-21

### Fixed
- fix(backup): multipart S3 uploads with dedicated timeout profile (#44)
- fixup: comment correction
- fix(deps): bump golang.org/x/crypto to v0.52.0 for SSH advisories (#23)

### Improved
- update README + website for the de-shell / new architecture
- update CHANGELOG.md for v0.2.2 (#6)

### Documentation
- document registry-gc workflow; bump backup-worker + web versions (#20)

### Other
- raise coverage to ~56% + logical remote/SSH interfaces (#58)
- shared services with consumer interfaces; decompose long activities
- build(deps): bump github.com/aws/aws-sdk-go-v2/service/s3
- build: unify worker images, add package docs, modernize linting
- build(deps): bump github.com/aws/aws-sdk-go-v2/feature/s3/manager
- build(deps): bump github.com/aws/aws-sdk-go-v2/credentials
- build: trim worker images
- de-shell workers to native APIs; shared SSH/SFTP + Docker clients
- shared worker runtime (shared.RunWorker)
- build(deps): bump golang.org/x/crypto from 0.52.0 to 0.53.0
- build(deps): bump github.com/aws/aws-sdk-go-v2 from 1.41.11 to 1.42.0
- build(deps): bump go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
- self-authenticating shared clients + CertAcquirer workflow
- build(deps): bump go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp
- build(deps): bump aws-sdk-go-v2, otel, temporal sdk, s3 (#38)
- Cleanup-worker maintenance workflows + shared Postgres/Nomad clients (#37)
- Rework backup to per-database parallel; migrate workflows to Temporal Schedules (#35)
- GH_ISSUE_17: refactor registry-gc to saga pattern (#18)
- GH_ISSUE_15: add registry garbage-collect workflow to cleanup-worker (#16)
- push tweak to Makefile

## [0.2.1] - 2026-03-18

### Added
- Add Hugo documentation site with interactive diagrams and repo scaffolding
- Add unit tests and fix lint issues
- Add repo scaffolding: CI, linting, license, root Makefile

### Improved
- Update README with badges, retry docs, logo, and repo scaffolding files

### Other
- logo tweak
- logo tweak
- Initial commit - backup, trivy scan, and node cleanup workers
