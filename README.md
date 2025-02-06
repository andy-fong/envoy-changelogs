This utility needs to be run inside the envoy directory as it execute git commands against the repo.

Check out the branch of the new release (eg v1.33.0) locally

In the envoy directory, run:
```
../envoy-changelogs/collect-changelogs changelogs/current.yaml > ../envoy-changelogs/v1.33.0.md
```
