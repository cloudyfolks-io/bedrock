# CI coverage

PRs cancel superseded runs. Fast validation and the complete envtest package
partition run independently; E2E no longer waits for envtest. Both partitions
together test every Go package once (including unit tests in envtest packages).
Envtest runs at most two packages concurrently: each starts its own etcd and
API server. This limits runner contention, not coverage, and adds no retries.

Code pull requests run the kind smoke test. Documentation-only pull requests
run validation and envtest.

Scheduled, manual and tagged release runs also test offline installation and
sign-in, tenant workloads, upgrade and upgrade rollback. The sign-in job runs
the offline installation smoke test on the same host and bundle. The release
workflow waits for all qualification jobs before it publishes artifacts.

UI outputs are tested/built once and shared only inside the same workflow run,
named for its exact SHA. Versioned binaries and release manifests are not reused
across incompatible release configs or versions. Both upgrade variants consume
one exact-source archive containing the identical version A/B inputs; their
independent installation, upgrade, recovery and abort assertions are unchanged.
`release-from-build` and
`bundle-from-release` are explicit CI steps, not incremental build caches:
callers must build the matching binaries first and render after publishing the
image so pinned digests remain correct.

Image preparation also renders `dist/release` before Docker builds, because
`Containerfile` embeds it. The configured Bedrock image is excluded from digest
resolution, so preparation does not require that image to exist in the registry.
Host-install E2E jobs use supported Ubuntu 24.04 explicitly; runner label
migrations must not silently bypass the installer's operating-system preflight.
