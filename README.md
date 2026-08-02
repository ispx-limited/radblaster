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

Documentation is at [docs.radblaster.ispx.co](https://docs.radblaster.ispx.co/).

## Status

Early but useful. The full subscriber session lifecycle works end to end
against a real RADIUS server, including server-initiated disconnects. On a
24 thread desktop, answered by a responder that does nothing but reply,
authentication alone sustains over two million requests per second with no
packets dropped, so what a run measures is the server rather than the
generator. There is a web interface for driving runs and watching them happen.

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
- **Retransmission**, repeating the exact bytes so a server can recognise the
  duplicate, with the retry count reported because retries otherwise hide loss
- A live status line while a run is going, showing the outstanding request
  count, which moves before any latency figure does
- **A web interface** for setting up a run, watching it live and reading the
  result, driven entirely through the same API the command line uses
- **Profiles**: an estate saved as a YAML file the interface edits, the command
  line runs, and you commit alongside whatever else describes that network
- Reproducible runs with `-seed`

What does not work yet:

- Comparing one run against another; each run stands alone
- A downloadable report file

## Why it exists

An ISP's AAA platform is asked to do its hardest work at the worst possible
moment: a BNG reboots and every subscriber on it re-authenticates at once. That
behaviour is difficult to rehearse, because reproducing it usually means
reproducing the access network that causes it.

radblaster generates the RADIUS traffic a BNG would generate, at the rate a BNG
would generate it, without the BNG. It models subscriber sessions rather than
individual packets, so an Access-Request is followed by the accounting records
a real session would produce.

## Install

Download a build for Linux or macOS from the
[releases page](https://github.com/ispx-limited/radblaster/releases), or take the
latest directly:

```sh
base=https://github.com/ispx-limited/radblaster/releases/latest/download
curl -fsSLO "$base/radblaster_linux_amd64.tar.gz"
curl -fsSLO "$base/checksums.txt"
sha256sum --ignore-missing -c checksums.txt
tar xzf radblaster_linux_amd64.tar.gz
./radblaster_linux_amd64/radblaster version
```

The checksum is verified before anything is unpacked, so a download that did
not arrive intact stops there. `--ignore-missing` is what lets one archive be
checked against a `checksums.txt` that covers all of them. On macOS the command
is `shasum -a 256 --ignore-missing -c checksums.txt`.

Replace `linux_amd64` with `linux_arm64`, `darwin_amd64` or `darwin_arm64` as
needed. There is no Windows build. Each archive holds a single binary with the
web interface built in, so nothing else needs installing.

## Building

Requires Go 1.25 or later.

```sh
make build      # builds bin/radblaster
make test       # go test ./...
make lint       # golangci-lint run
```

## Usage

### The web interface

```sh
radblaster serve
```

Then open http://127.0.0.1:8080. Set a run up in the form, start it, and watch
the rates, the latency percentiles and the outstanding request count move as it
goes. Everything the interface can do goes through the same HTTP API, so
nothing in the browser has a privileged path the command line lacks:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/run` | start a run |
| `DELETE` | `/api/run` | stop the run |
| `GET` | `/api/status` | the current picture |
| `GET` | `/api/events` | the same, streamed as server-sent events |

The interface binds to localhost by default. It can start a run against any
server it can reach and has no authentication of its own, so putting it on a
shared address hands that ability to anyone who can reach it. The shared secret
is accepted but never returned, so a browser pointed at a running instance
cannot read it back.

Loopback keeps the network out, but on its own it does not keep a browser out:
any page an operator visits can send requests to 127.0.0.1, and a name an
attacker controls can be pointed there so the browser treats their page as
same-origin. So the API also refuses anything cross-origin, requires
`application/json` on a request carrying a body, and answers only to an address
or to `localhost` rather than to a name. Behind a reverse proxy that addresses
it by name, pass `-allowed-host` to name it.

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

The default is two links per core, capped at 64, which is chosen for a server
that answers rather than for the highest number a generator can reach. A link
spends most of its time waiting for a reply, so raising `-links` well past the
default still buys throughput when the thing under test can take it: on a 24
thread desktop, going from the default to 256 links lifts authentication from
about 2.1 to 2.5 million requests per second. Each link costs two sockets'
worth of buffers, which is the reason for the cap rather than a limit of the
protocol.

A request that goes unanswered is repeated, as a real access node would, and
the wait doubles after each attempt. `-retries 0` turns that off when you want
to measure raw loss on the path rather than simulate a node. Either way the
retry count is reported separately, because retries hide loss in the success
rate: a run that succeeds 100% of the time after 20,000 retransmissions is
telling you something important about the path.

Latency stays measured from the original request even when a later attempt is
the one answered, since that is what a subscriber waited. A tail that jumps to
the retry interval is real, not an artefact.

Two latency columns are reported. The one measured from the scheduled send
includes any time the generator spent falling behind its own schedule, and is
the honest figure for what a subscriber would experience. The one measured from
the actual send is what the server took. When they diverge, the run could not
keep pace and the result understates the load the server was asked to handle.

### Profiles

An estate is worth describing once. Save one from the interface, or write the
file by hand, and run it from anywhere:

```sh
radblaster run -profile profiles/nokia-business.yaml
```

```yaml
# radblaster profile
name: nokia-business
server: 10.0.0.1:1812
acctServer: 10.0.0.1:1813
access: ipoe
username: sub-{n}@business.example.net
load:
    rate: 500
    duration: 10m
    shape: ramp
    rejectRate: 0.02
    retries: 2
session:
    hold: 30m
    interim: 300s
traffic:
    up: {min: 1000000, max: 20000000}
    down: {min: 5000000, max: 90000000}
nas:
    identifier: bng-lab-01
    portId: eth 1/1/{n}
dictionaries:
    - /usr/share/radius/dictionary
attributes:
    - name: Cisco-AVPair
      value: ip:qos-policy-in=voice
```

**A profile carries no shared secret and no password.** It is meant to be
committed, and a credential in a file that is meant to be committed is a
credential that will be. Supply them when the run starts, from
`RADBLASTER_SECRET` or from the interface.

Any flag given on the command line overrides the file, so one saved estate can
be aimed at a different server or run at a different rate without editing it:

```sh
radblaster run -profile profiles/nokia-business.yaml -server 10.0.1.1 -duration 5m
```

A profile names the dictionaries its attribute names resolve through, because
an estate that uses vendor attributes is not fully described without them.

### Vendor attributes

radblaster ships definitions for the RFC attributes only. For vendor
attributes it looks in the usual system locations, so on a machine that already
runs RADIUS the dictionaries are found without being asked for. Point
`-dictionary` at them explicitly to override that, or pass
`-no-auto-dictionary` to use only the built-in set:

```sh
radblaster run -server 10.0.0.1:1812 -password secret -count 1000 \
               -dictionary /path/to/your/dictionary \
               -attr 'Cisco-AVPair=ip:qos-policy-in=voice' \
               -attr 'ADSL-Forum-Agent-Circuit-Id=eth 1/1/{n}'
```

Both the friendly and the qualified name work (`Cisco-AVPair` or
`Cisco.AVPair`), because dictionaries differ on which they use. In the web
interface the name field suggests from whatever is loaded, and an attribute
with named values offers those too.

What was loaded is always reported, and saving a profile records the paths, so
a run that has to be repeatable does not depend on what happened to be
installed on the machine that ran it.

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
