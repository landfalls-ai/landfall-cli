.PHONY: build test lint lint-v2

build:
	go build -o landfall ./cmd/landfall

test:
	go test ./...

# Matches CI, which pins golangci-lint v1.64.8 and reads .golangci.yml.
lint:
	golangci-lint run ./...

# Use this when your locally installed golangci-lint is v2.
#
# .golangci.yml is in v1 schema because that is what CI runs, and a v2 binary
# refuses to load it ("unsupported version of the configuration"). The result is
# a blind spot: `make lint` fails to run at all locally, so lint errors are
# discovered only in CI. That is not hypothetical — it happened, on an unused
# test helper a local run would have caught in a second.
#
# --no-config sidesteps the schema mismatch; the linter set mirrors
# .golangci.yml's. Because the exclusions live in that config, this target
# reports 5 hits the real lint does not. That is the known baseline:
#
#   3x ST1005   internal/cli/connect_aws.go  — excluded on purpose; the error
#               text is byte-identical to the Node CLI's and must stay so
#   2x errcheck internal/hooks/socket.go     — (net.Conn).Close, in the real
#               config's exclude-functions list
#
# Anything BEYOND those five is yours.
lint-v2:
	golangci-lint run --no-config --default=none \
		-E unused,govet,ineffassign,staticcheck,errcheck ./...
