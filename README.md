# radblaster

**Stress test your RADIUS infrastructure without a BNG in the way.**

Testing whether your AAA stack survives a BNG reload normally means owning a
BNG. radblaster speaks RADIUS directly, so you can put realistic subscriber
load on your authentication and accounting servers with one binary and no
access network.

```sh
radblaster run -server 10.0.0.1:1812 -acct-server 10.0.0.1:1813 \
               -password secret -rate 500 -count 50000 -hold 30m
```

## Status

Early but useful. The full subscriber session lifecycle works end to end
against a real RADIUS server, including server-initiated disconnects, and on a
desktop machine authentication alone sustains over 100,000 requests per second.
The web interface is not built yet.

What works:

- **Whole subscriber sessions**, not just packets: an Access-Request, then an
  Accounting-Start, interim updates for as long as the session is online, and
  an Accounting-Stop with the final counters
- `Class` and `Framed-IP-Address` from the Access-Accept carried into every
  accounting record, as a real access node would
- `Acct-Interim-Interval` from the Access-Accept honoured per session, so the
  server controls how much accounting load it receives
- Octet counters that roll into `Acct-Input-Gigawords` and
  `Acct-Output-Gigawords` past four gibibytes, which is where accounting
  pipelines tend to break
- PAP and CHAP authentication
- Results broken out per exchange, so authentication staying healthy while
  accounting collapses is visible rather than averaged away
- Latency percentiles reported both from the scheduled send and from the actual
  send, so time lost to the generator falling behind cannot be mistaken for the
  server being slow
- Failures separated by cause: a timeout, a reply that fails verification, and a
  policy rejection are counted apart, because they need different remedies
- Notes explaining what limited the run, including the throughput ceiling
  implied by the number of links and the server's response time
- **Answers CoA-Request and Disconnect-Request** as an access node would, so a
  bulk policy change or a mass disconnect can be tested from the server side
- **Vendor-specific attributes**, resolved by name from the dictionary files
  your own RADIUS server already uses, or written by number with no dictionary
  at all
- Reproducible runs with `-seed`

What does not work yet:

- Retransmission during a load run; a request that goes unanswered is counted
  as lost rather than retried
- Live progress output; a run reports once it finishes
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

### Subscriber sessions

```sh
radblaster run -server 10.0.0.1:1812 \
               -acct-server 10.0.0.1:1813 \
               -user 'subscriber{n}@example.net' \
               -password secret \
               -rate 500 -count 50000 \
               -hold 30m -traffic-up 125000 -traffic-down 1250000
```

Each session authenticates, comes online, sends interim updates for as long as
`-hold`, then tears down with an Accounting-Stop carrying its final counters.
`-traffic-up` and `-traffic-down` are octets per second, which is what drives
those counters.

`{n}` in the username is replaced with the session number, so each session is a
different subscriber. The same substitution works in `-nas-port-id` and
`-calling-station-id`.

Leave out `-acct-server` and a session is just an authentication, which is the
right shape for a pure authentication stress test:

```sh
radblaster run -server 10.0.0.1:1812 -password secret -rate 5000 -duration 60s
```

Omit `-rate` to set sessions up as fast as the links allow, and use `-count`
instead of `-duration` to bound a run by sessions rather than time.

Concurrency comes from sockets, not threads. The RADIUS identifier is one octet
wide, so a single source port can only carry 256 requests at a time; `-links`
sets how many ports to open per endpoint. If a run is limited by this rather
than by the server, it says so and tells you the ceiling it hit.

Two latency columns are reported. The one measured from the scheduled send
includes any time the generator spent falling behind its own schedule, and is
the honest figure for what a subscriber would experience. The one measured from
the actual send is what the server took. When they diverge, the run could not
keep pace and the result understates the load the server was asked to handle.

### Vendor attributes

radblaster ships definitions for the RFC attributes only. For vendor
attributes, point it at the dictionary files your RADIUS server already uses,
so both ends resolve the same names to the same numbers and there is no version
skew between them:

```sh
radblaster run -server 10.0.0.1:1812 -password secret -count 1000 \
               -dictionary /path/to/your/dictionary \
               -attr 'Cisco-AVPair=ip:qos-policy-in=voice' \
               -attr 'ADSL-Forum-Agent-Circuit-Id=eth 1/1/{n}'
```

Both the friendly and the qualified name work (`Cisco-AVPair` or
`Cisco.AVPair`), because dictionaries differ on which they use.

A dictionary is a convenience, not a requirement. Any attribute can be written
by number, which means nothing has to be installed for a one-off test:

```sh
radblaster run ... -attr '26.9.1=ip:qos-policy-in=voice' -attr '26.4874.10=policy'
```

Values are templated the same way usernames are, so `{n}` becomes the session
number. Integer attributes also accept the value names their dictionary
defines, as in `-attr 'Service-Type=Framed-User'`.

### Change of authorisation and disconnect

Give the run an address to listen on and it presents itself as an access node
the server can push changes to:

```sh
radblaster run -server 10.0.0.1:1812 -acct-server 10.0.0.1:1813 \
               -password secret -count 5000 -hold 1h \
               -coa-listen :3799
```

A Disconnect-Request naming a live session is acknowledged and the subscriber
is torn down, with `Acct-Terminate-Cause` set to Admin-Reset so the accounting
record says the server ended it rather than the subscriber. A CoA-Request can
change the session's interim interval or its remaining time.

Requests for a session that has already gone are refused with
`Error-Cause = Session-Context-Not-Found`, and the count of those is the figure
that matters when testing a bulk change: it is how many subscribers did not
receive it.

A request that fails verification is dropped without a reply rather than
refused, because answering would confirm to an unauthenticated sender that the
address is live.

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
