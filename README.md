# mega-doub

Backup utility written in Go.

## Build

```bash
cd golang
version="$(git describe --tags --always --dirty)"
commit="$(git rev-parse --short HEAD)"
go build -o mega-doub -ldflags "-X main.version=${version} -X main.commit=${commit}" \
  ./cmd/mega-doub
```

<!-- cspell:ignore doub ldflags -->
