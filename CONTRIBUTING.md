# Contributing

Issues and pull requests are welcome. For a larger change, open an issue first so we can agree on the approach.

Before sending a pull request, run:

```sh
go test ./...
golangci-lint run
(cd frontend && pnpm typecheck)
```

Contributions are accepted under the [Apache-2.0](LICENSE) license; there is no CLA.
