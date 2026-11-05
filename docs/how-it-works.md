# How it works

What the numbers rest on, and the constraints that shaped the design.

## Concurrency comes from sockets, not threads

The RADIUS identifier is one octet. A request is matched to its reply by the
source port it went out on and that single octet, so one source port can have at
most 256 requests outstanding at any moment, however much work is pushed at it.

That is the fact the whole engine is built around. Throughput does not come from
running more goroutines; it comes from opening more source ports. `-links` sets
how many, per endpoint, and the ceiling on requests in flight is
`links x 256`.

```text
  link 1   source port 51001   256 identifiers  \
  link 2   source port 51002   256 identifiers  --->  RADIUS server
  link n   source port 510nn   256 identifiers  /
```

Each link owns its identifier space, its outstanding-request table, and the
subscribers assigned to it. A subscriber stays with one link for its whole life,
so nothing has to be coordinated between them.

The default is two links per core, capped at 64. A link spends most of its time
waiting for a reply rather than working, so one per core leaves the machine
idle. Raising `-links` well past the default still buys throughput when the
thing under test can take it.

## Identifiers are reused as late as possible

Identifiers are handed out shuffled and returned to the back of the queue, so
one is reused only after every other free identifier has been. That maximal
reuse distance is what stops a late reply, to a request already given up on,
being matched against a fresh request that happened to inherit its identifier.

A reply that fails verification never releases an identifier. Otherwise anyone
able to inject packets could free identifiers at will and cancel requests that
are still legitimately outstanding.

## Pacing works from cumulative demand

A naive pacer computes a gap between subscribers and sleeps for it. A gap is the
reciprocal of a rate, so a shape that starts near zero produces an enormous
first gap and then never reconsiders it. A ramp built that way fires a handful
of requests and stops.

radblaster instead integrates the offered rate forward into cumulative demand
and inverts it to find the moment each subscriber was due. That has no
singularity at zero, it handles a rate that varies over the run, and it produces
the schedule that latency is measured against.

## Latency is measured twice

Every request carries the moment it was *due* and the moment it actually left.
Measuring only from the actual send hides queueing inside the generator, and a
saturated run would report a healthy-looking server. Measuring only from the
schedule charges the generator's own backlog to the server. Both are reported,
and the difference between them is the honest answer to "was that us or them".

See [reading the results](results.md).

## What a session sends

```text
radblaster                                              RADIUS server
    |  Access-Request                                         |
    |-------------------------------------------------------->|
    |             Access-Accept (Class, Framed-IP-Address,     |
    |<------------ Acct-Interim-Interval)                      |
    |  Accounting-Request (Start)                              |
    |-------------------------------------------------------->|
    |<------------------------------- Accounting-Response      |
    |                                                          |
    |  Accounting-Request (Interim-Update)   while online      |
    |-------------------------------------------------------->|
    |<------------------------------- Accounting-Response      |
    |                                                          |
    |  Accounting-Request (Stop, final counters)               |
    |-------------------------------------------------------->|
    |<------------------------------- Accounting-Response      |
```

`Class` and `Framed-IP-Address` from the Access-Accept are carried into every
accounting record the subscriber sends, as a real access node would. That
exercises the whole chain, including any proxy in the path, rather than just the
front door.

Octet counters roll into `Acct-Input-Gigawords` and `Acct-Output-Gigawords` past
four gibibytes. Plenty of accounting pipelines mishandle that rollover, which is
exactly why a load tool should reproduce it rather than quietly staying under
the limit.

## Answering the server

With `-coa-listen`, radblaster is also a server: it accepts CoA-Request and
Disconnect-Request, resolves them against its own session table by
`Acct-Session-Id`, and answers as an access node would.

A change is applied by the worker that owns the subscriber rather than by the
listener, so a session is only ever mutated from one place. When the queue for a
worker is full the request is answered with Resources-Unavailable, which is a
truthful answer the server can act on, rather than an acknowledgement for work
that was dropped.

## Dictionaries cannot change the wire

Attribute names are never transmitted. A dictionary maps a name to a vendor
number and an attribute number, and only the numbers go out. Loading one changes
what you can type and nothing else, which is why radblaster reads your files
rather than shipping its own. See [dictionaries](guides/dictionaries.md).

## What the generator costs

On a 24 thread desktop, answered by a responder that does nothing but reply,
radblaster sustains over two million authentications a second with no packets
dropped, at roughly three microseconds of CPU per authentication on a dedicated
core. Throughput scales close to linearly to about four cores and then flattens,
because most of the remaining cost is the kernel's per-packet UDP path rather
than anything in userspace.

The point of those numbers is not the number. It is that the generator is far
enough ahead of any real RADIUS server that what a run measures is the server.

If it ever is the limit, the report says so rather than leaving you to infer it,
and raising `-links` is the lever.

## What it does not do

- Compare one run against another. Each run stands alone.
- Produce a downloadable report file.
- Run distributed across several machines. One process saturates a server well
  before it saturates itself.
