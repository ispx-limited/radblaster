# Quickstart

From download to a load run with results, in about five minutes.

## Install

Download a build for Linux or macOS from the
[releases page](https://github.com/ispx-limited/radblaster/releases), or take
the latest directly:

```sh
curl -fsSL https://github.com/ispx-limited/radblaster/releases/latest/download/radblaster_linux_amd64.tar.gz | tar xz
cd radblaster_linux_amd64
./radblaster version
```

Replace `linux_amd64` with `linux_arm64`, `darwin_amd64` or `darwin_arm64` as
needed. There is no Windows build. The archive holds a single binary with the
web interface built in, and `checksums.txt` on the release verifies the
download.

## Something to point it at

If you already have a test RADIUS server, use it and skip this step. You need
its address, its shared secret, and a subscriber it will accept.

Otherwise a throwaway server takes a moment. Write a user and a client
definition:

```sh
mkdir -p /tmp/radblaster-demo && cd /tmp/radblaster-demo

cat > authorize <<'EOF'
subscriber@example.net Cleartext-Password := "DingDong"
    Framed-IP-Address = 192.0.2.10,
    Session-Timeout = 3600,
    Acct-Interim-Interval = 15,
    Class = 0x0102030405,
    Framed-Protocol = PPP,
    Service-Type = Framed-User
EOF

cat > clients.conf <<'EOF'
client testing {
    ipaddr = 0.0.0.0/0
    secret = testing123
    require_message_authenticator = no
}
EOF
```

Then start it on ports that will not collide with anything already listening:

```sh
docker run -d --name radblaster-demo \
  -p 11812:1812/udp -p 11813:1813/udp \
  -v "$PWD/authorize":/etc/raddb/mods-config/files/authorize:ro \
  -v "$PWD/clients.conf":/etc/raddb/clients.conf:ro \
  freeradius/freeradius-server:latest
```

!!! warning "A test server, not a pattern to copy"
    That client definition accepts any source address with a short shared
    secret. It is appropriate for something running on your own machine for ten
    minutes, and appropriate nowhere else.

## One authentication first

Before offering load, prove the credentials and the secret are right:

```sh
radblaster auth -server 127.0.0.1:11812 -secret testing123 \
                -user subscriber@example.net -password DingDong
```

```text
Access-Accept in 632µs
  Message-Authenticator        0x45b51499513b4d457d3f571f0c9669d5
  Framed-IP-Address            192.0.2.10
  Session-Timeout              3600
  Acct-Interim-Interval        15
  Class                        0x0102030405
  Framed-Protocol              PPP (1)
  Service-Type                 Framed-User (2)
```

Every attribute the server returned is printed. If instead you see
`replies received but none verified`, the shared secret does not match. That is
reported separately from a timeout because the two have nothing in common and
need different fixes.

## A load run

Now offer sustained load. This sets subscribers up at 200 a second until 2000
have been established, holding each online for a minute:

```sh
radblaster run -server 127.0.0.1:11812 -acct-server 127.0.0.1:11813 \
               -secret testing123 \
               -user subscriber@example.net -password DingDong \
               -rate 200 -count 2000 -hold 1m \
               -traffic-up 125000 -traffic-down 1250000
```

Each subscriber authenticates, sends an Accounting-Start, sends interim updates
for as long as it is held, then tears down with an Accounting-Stop carrying its
final counters. `-traffic-up` and `-traffic-down` are octets per second, which
is the rate those counters climb at.

The result is broken out per exchange:

```text
Exchange          Sent  OK    Rejected  Retried  Lost  Unverified  p50       p95       p99
Access-Request    2000  2000  0         0        0     0           627µs     1.09ms    1.139ms
Accounting-Start  2000  2000  0         0        0     0           20.578ms  21.103ms  21.103ms
Interim-Update    6000  6000  0         0        0     0           602µs     1.09ms    1.155ms
Accounting-Stop   2000  2000  0         0        0     0           635µs     1.122ms   1.188ms
```

Three interim updates per subscriber, because the server asked for one every
fifteen seconds in its Access-Accept and each subscriber was held for a minute.
radblaster honours that per session, so the server decides how much accounting
load it receives.

Two things in that table are worth noticing. Accounting-Start is thirty times
slower than everything else, because this server writes each one to a detail
file and waits for the write. And authentication staying healthy while
accounting collapses is a real and common failure, which is why the two are
never averaged together. See [reading the results](../results.md) for the rest
of the report.

## Try the web interface

```sh
radblaster serve
```

Open <http://127.0.0.1:8080>, set a run up in the form, and watch the rates and
latency move while it goes. See [web interface](web-interface.md).

## Clean up

```sh
docker rm -f radblaster-demo
```

## Where to go next

- [Subscriber sessions](sessions.md) for the settings that shape a realistic run
- [Dictionaries](dictionaries.md) to send your vendor's attributes by name
- [Profiles](profiles.md) to save an estate as a file you can commit
- [How it works](../how-it-works.md) for what the numbers rest on
