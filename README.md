# radblaster

**Stress test your RADIUS infrastructure without a BNG in the way.**

Testing whether your AAA stack survives a BNG reload normally means owning a
BNG. radblaster speaks RADIUS directly, so you can put realistic subscriber
load on your authentication and accounting servers with one binary and no
access network.

```sh
radblaster auth -server 10.0.0.1:1812 -user subscriber@example.net -password secret
```

## Status

Early. The RADIUS protocol layer is implemented and tested, and the `auth`
command sends a single Access-Request and reports the reply. **The load engine
is not built yet**, so today this is a correctness tool rather than a stress
tool.

What works:

- Access-Request with PAP or CHAP authentication
- Message-Authenticator on every request, and verification of it on replies
- Response Authenticator verification, so a reply signed with the wrong secret
  is reported as such rather than as a timeout
- Retransmission with the identical encoded packet, so a server can recognise
  the duplicate
- Attribute decoding for the standard RFC attribute set

What does not work yet:

- Sustained load, concurrency, and rate control
- Accounting sessions (Start, Interim-Update, Stop)
- CoA and Disconnect
- Latency percentiles and reporting
- The web interface

## Why it exists

An ISP's AAA platform is asked to do its hardest work at the worst possible
moment: a BNG reboots and every subscriber on it re-authenticates at once. That
behaviour is difficult to rehearse, because reproducing it usually means
reproducing the access network that causes it.

radblaster generates the RADIUS traffic a BNG would generate, at the rate a BNG
would generate it, without the BNG. It models subscriber sessions rather than
individual packets, so an Access-Request is followed by the accounting records
a real session would produce.

## Building

Requires Go 1.24 or later.

```sh
make build      # builds bin/radblaster
make test       # go test ./...
make lint       # golangci-lint run
```

## Usage

### Single authentication

```sh
radblaster auth -server 10.0.0.1:1812 \
                -user subscriber@example.net \
                -password secret
```

Use `-chap` to authenticate with CHAP instead of PAP. Note that a server
storing hashed passwords can only support PAP, because CHAP requires the server
to hold the cleartext.

The shared secret is read from `RADBLASTER_SECRET` when `-secret` is not given,
which keeps it out of the process list and your shell history.

Run `radblaster auth -h` for the full flag list.

## A warning about pointing this at production

This tool exists to generate load. Aimed at a production AAA platform outside a
maintenance window, it will affect real subscribers. Confirm the target address
before every run, and treat a RADIUS server as you would any other piece of
live network infrastructure.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
