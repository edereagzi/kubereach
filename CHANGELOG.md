# Changelog

What changed in each release, as the release page and Kubereach's update window show it.
Before tagging, rename "Unreleased" to the version, e.g. `## [0.8.1] - 2026-10-08

### Fixed

- On Windows, the window recovers when its web view crashes, instead of staying blank.

## [0.8.0] - 2026-10-10`; a tag with no section here is not released.

## [0.8.0] - 2026-10-08

### Added

- Kubereach updates itself. It looks for a new version at launch and once a day, and marks Settings when one is out; "Check for updates…" looks right away. The update downloads and installs only when you choose to.
- A Cluster reached directly that stops answering shows as unreachable within seconds, and comes back on its own once it answers again.
- A container's resource requests and limits read as a small table.
- A kubeconfig read from an SSH server can name certificate and key files on that server.

### Changed

- On Windows, the installer installs for the current user, without an administrator prompt. If you installed an earlier version with the installer, uninstall it before installing this one.

### Fixed

- Namespaces with many pods scroll smoothly in the overview.
- A Cluster that does not answer says so, instead of "Something went wrong".
- Nodes say when the Cluster has no metrics-server, instead of showing no usage.
