# Contributing to radblaster

Bug reports, protocol corrections and code are all welcome. Corrections
especially: a load generator that encodes a packet slightly wrong produces
results that look plausible and are not, so if you have a capture showing
radblaster putting something on the wire that your equipment would not, that is
one of the most useful things you can send.

## Getting started

1. **Open an issue** describing what you saw and what you expected. For a
   protocol bug, a packet capture is worth more than a description.
2. **Discuss first if the change is large.** Anything touching the wire format,
   the session model or the statistics engine is worth agreeing on in the issue
   before you write it.
3. **Open a pull request** linking the issue with `Closes #<N>`.

## Local checks

Run what CI runs before pushing:

```sh
make fmt      # gofmt -s -w .
make vet      # go vet ./...
make lint     # golangci-lint run
make test     # go test ./...
make build    # builds bin/radblaster with version metadata
```

Go 1.24 or later and `golangci-lint` are the only prerequisites.

## Commits and branches

- Branch: `<type>/<short-description>`, for example `fix/accounting-authenticator`
- Commit subject: `area: imperative summary`, lowercase area, no trailing full
  stop, for example `radius: verify message authenticator on replies`
- The body explains why, not what the diff already shows

## Testing expectations

- Every bug fix ships with a regression test that fails without the fix.
- Tests are deterministic. They must not depend on a live RADIUS server, on
  wall-clock timing, or on ordering luck. The protocol tests drive a controlled
  responder in-process for exactly this reason.
- Cryptographic code is tested against an independent restatement of the
  relevant RFC rather than against its own output, so a mistake in the
  implementation cannot silently become the expected value.

## Design guardrails

Two constraints are load-bearing and a change that breaks either will be
questioned:

- **The send path does not allocate.** Encoding reuses its buffer and hash
  state. There is a test asserting zero allocations per encode; if your change
  makes it fail, that is a finding rather than a test to update.
- **A failure must name its cause.** "No reply" and "a reply arrived that did
  not verify" are different diagnoses with different remedies, and collapsing
  distinct failures into one counter makes results unactionable. New failure
  modes get their own error, not a shared one.
