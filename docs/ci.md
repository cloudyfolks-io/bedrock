# CI coverage

PRs cancel superseded runs. Fast validation and the complete envtest package
partition run independently; E2E no longer waits for envtest. Both partitions
together test every Go package once (including unit tests in envtest packages).

Every code PR runs kind. Auth/UI changes add authn; compute/network/tenancy/
images/objectstorage changes add tenant. Mixed changes union their suites.
Unknown, shared, dependency, installer, release and upgrade changes run everything.
Documentation-only changes still run validation and envtest.

Nightly and manual CI run all seven E2E variants, including standalone offline
bundle installation and both upgrade success/abort paths. Before publishing,
manually run `ci` on the final candidate and require all jobs, including envtest,
to pass. A selected PR run is not release evidence.

UI outputs are tested/built once and shared only inside the same workflow run,
named for its exact SHA. Versioned binaries and release manifests are not reused
across incompatible release configs or versions. Both upgrade variants consume
one exact-source archive containing the identical version A/B inputs; their
independent installation, upgrade, recovery and abort assertions are unchanged.
`release-from-build` and
`bundle-from-release` are explicit CI steps, not incremental build caches:
callers must build the matching binaries first and render after publishing the
image so pinned digests remain correct.
