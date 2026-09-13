# Contributing

Contributions are welcome through GitHub issues and pull requests.

## Development

1. Fork and clone `https://github.com/HeartBtz/pxl`.
2. Create a focused branch from the latest default branch.
3. Follow the source setup in `README.md`.
4. Add or update tests for behavior changes.
5. Run the checks below before opening a pull request.

```bash
gofmt -w .
go test -race ./...
go vet ./...
node --test web/*.test.cjs
```

Keep changes small, avoid committing generated binaries or `.env`, and explain
user-visible or security-relevant behavior in the pull request. Integration tests
that need PostgreSQL use `PXL_TEST_DATABASE_URL` and otherwise skip.

Report vulnerabilities privately as described in `SECURITY.md`.

## License

By submitting a contribution, you license it under the Apache License 2.0.
