To run this utility, you must have an envoy repo already cloned and **checked out to the release branch** you want to generate the change logs.
The release branch name should look something like "v1.33.0"

```
go run main.go <path_to_your_envoy_repo> > v1.33.0.md
```

Starting with v1.39, envoy replaced `changelogs/current.yaml` with one file per entry under
`changelogs/current/<section>/<area>__<slug>.rst`. The tool detects which layout the branch uses.
For the new layout, each entry links every commit that touched its file (following renames).
Entries that were moved out of `current.yaml` by the migration commit (#45093) also link the
commits from the old `current.yaml`, so they still point at the PR that introduced them.
