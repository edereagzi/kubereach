# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.0] - 2026-10-09

### Added

- ⌘K (Ctrl+K on Linux and Windows) opens a Command palette. It finds Clusters, views, objects and Commands.
- ⌘[ and ⌘] (Alt+← and Alt+→ on Linux and Windows) and the mouse's back and forward buttons go back and forward through the Clusters, views and details that you opened.
- ⌘F (Ctrl+Shift+F on Linux and Windows) finds text in a Terminal or Shell.
- New keys for views, rows, the open detail, the panel, the YAML editor and logs. Press ? to see all of them.

### Changed

- ⌘K opens the Command palette. It no longer goes to the Cluster filter.
- ⌘R refreshes the data and keeps every Terminal, Shell and log stream open. It no longer reloads the window.
- On Linux and Windows, a Terminal or Shell gets the Ctrl keys. Add Shift to use an app key, for example Ctrl+Shift+C to copy and Ctrl+Shift+W to close the tab.
- The keyboard shortcuts list shows the keys of your platform.

### Fixed

- Keys no longer act behind an open dialog or menu.
- Keys keep working after a select box was used.
- The keyboard shortcuts list scrolls when it is taller than the window.
- Esc in the Cluster filter or the log filter no longer closes the open detail.

### Security

- Kubereach is built with Go 1.26.9 and golang.org/x/net v0.61.0, which fix known vulnerabilities.

## [0.8.1] - 2026-10-08

### Fixed

- On Windows, the window recovers when its web view crashes, instead of staying blank.

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
