# Subscriber sessions

A run models subscribers, not packets. This page covers the settings that decide
what those subscribers look like and how they arrive.

## What a session does

With `-acct-server` given, each subscriber walks a full lifecycle:

1. An **Access-Request**, with the credential the access method implies.
2. An **Accounting-Start** once the server accepts it, echoing `Class` and
   `Framed-IP-Address` from the Access-Accept.
3. **Interim updates** for as long as the subscriber is held online, with
   counters that climb at the traffic rate it was given.
4. An **Accounting-Stop** with the final counters and a terminate cause.

Leave `-acct-server` out and a session is only an authentication, which is the
right shape for a pure authentication stress test.

## How many, how fast, how long

```sh
radblaster run -server 10.0.0.1:1812 -password secret \
               -rate 500 -count 50000 -hold 30m
```

| Flag | Effect |
|---|---|
| `-rate` | subscribers set up per second. Omit it to go as fast as the links allow |
| `-count` | stop after this many subscribers |
| `-duration` | stop after this long |
| `-hold` | how long each subscriber stays online before it tears down |
| `-interim` | update period, used only when the server does not state one |

A run needs either `-count` or `-duration`. `-hold` is what separates a churn
test from a soak: with no hold, subscribers stop as soon as accounting has
started, which is the shape of a BNG flapping. With a long hold they accumulate,
and the interesting number becomes how many can be online at once.

If the server returns `Acct-Interim-Interval` in its Access-Accept, radblaster
honours it per session and `-interim` is ignored. That is deliberate: the server
is telling you how much accounting load it wants, and a mixed estate where
different subscribers get different intervals behaves the way a real one does.

## Who the subscribers are

```sh
radblaster run -server 10.0.0.1:1812 \
               -user 'subscriber{n}@example.net' -password secret \
               -nas-id lab-bng-01 -nas-port-type 15 \
               -calling-station-id '00:1a:2b:00:{n}' \
               -count 5000
```

`{n}` is replaced with the session number, so each subscriber is distinct. The
same substitution works in `-nas-port-id` and `-calling-station-id`. Without it
every session uses the same name, which is what you want when the server only
knows one test account.

## Access methods

```sh
radblaster run -access chap ...
```

- **`pap`** sends the password obfuscated in `User-Password`. The default.
- **`chap`** sends a `CHAP-Password` digest instead. A server that stores hashed
  passwords cannot support it, because CHAP needs the cleartext at both ends.
- **`ipoe`** models a subscriber that does not authenticate at all. The access
  node has no credential to present, so it sends a fixed service password purely
  because an Access-Request has to carry one, and the server identifies the
  subscriber from the line. Set `-password` to whatever string your node is
  configured with.

## Shaping the load

```sh
radblaster run -shape ramp -rate 20000 -duration 5m ...
```

| Shape | What it offers |
|---|---|
| `steady` | the rate, flat, for the whole run |
| `ramp` | climbs from nothing to the rate across the run |
| `burst` | alternates between the rate and a fraction of it |
| `stampede` | everything at once |

A **ramp** is how you find capacity: the point where latency turns up is the
answer, and it is far more informative than a pass or fail at one rate. A ramp
needs `-duration` to climb across.

A **burst** catches a server that copes with load but not with change. Set the
cycle with `-burst-period` and the quiet half with `-burst-low 0.1`.

A **stampede** is the shape of a BNG reload, where every subscriber on a node
re-authenticates together.

## Subscribers the server will refuse

```sh
radblaster run -reject-rate 0.2 ...
```

A fifth of subscribers get a username no server should know, so a fifth of the
requests take the rejection path. This matters more than it sounds. Rejection is
often slower and more expensive than acceptance, and some servers hold a reject
deliberately before answering. A platform that is fine until a line card flaps
and thousands of stale credentials arrive at once will only show that under a
load which contains some.

The rejected subscribers are named obviously, so a rejection in a server log
reads as this tool asking for one rather than a real subscriber failing to get
online. Use `-reject-username` to control that.

## Retransmission

An unanswered request is repeated, as a real access node would, and the wait
doubles after each attempt. `-retries 0` turns that off when you want to measure
raw loss on the path rather than model a node.

Either way the retry count is reported separately, because retries hide loss in
the success rate. A run that succeeds 100% of the time after 20,000
retransmissions is telling you something important about the path.

A retransmission repeats the exact bytes already sent, so the identifier and
authenticator are unchanged and a server that has already answered can recognise
the duplicate rather than treating it as new work.

## Answering the server

```sh
radblaster run -coa-listen :3799 ...
```

radblaster then presents itself as an access node the server can push changes
to. A **Disconnect-Request** tears the subscriber down and sends a final
accounting record with a cause saying the server ended it. A **CoA-Request** can
change the interim interval or the session timeout, and the subscriber's
schedule is rewritten to match.

That lets a bulk policy change or a mass disconnect be tested from the server's
side of the exchange, which is the half that usually goes untested.

## Reproducibility

```sh
radblaster run -seed 42 ...
```

Everything that varies between subscribers is drawn from a seeded stream, so the
same seed produces the same estate. Without one, a fresh seed is drawn and
reported.
