# Reading the results

A run reports what happened per exchange, how long it took measured two
different ways, and what limited it. This page explains each part and why it is
split the way it is.

## The exchange table

```text
Exchange          Sent  OK    Rejected  Retried  Lost  Unverified  p50       p95       p99
Access-Request    2000  2000  0         0        0     0           627µs     1.09ms    1.139ms
Accounting-Start  2000  2000  0         0        0     0           20.578ms  21.103ms  21.103ms
Interim-Update    6000  6000  0         0        0     0           602µs     1.09ms    1.155ms
Accounting-Stop   2000  2000  0         0        0     0           635µs     1.122ms   1.188ms
```

One row per kind of request, never combined. Authentication staying healthy
while accounting collapses is a common failure and an aggregate hides it. In the
run above, Accounting-Start is thirty times slower than everything else because
that server writes each one to a detail file and waits for the write. A single
averaged latency figure would have buried that.

## Failures are counted by cause

The failure columns are deliberately separate, because they need different
remedies and adding them together produces a number nobody can act on.

| Column | What it means | Where to look |
|---|---|---|
| **Rejected** | the server answered, and said no | policy, credentials, realm routing |
| **Lost** | no answer arrived before the last attempt gave up | the server is saturated or unreachable |
| **Unverified** | replies arrived but would not verify | almost always a shared secret mismatch |
| **Retried** | attempts repeated, not requests | loss on the path, hidden by the success rate |
| **Blocked** | could not be offered, no identifier was free | the run's own concurrency, not the server |

A rejection means the server is healthy. A timeout means it may not be. Treating
those as the same event is how a load test produces a chart nobody trusts.

**Retried** deserves attention even when nothing was lost. A run that succeeds
100% of the time after 20,000 retransmissions is telling you something important
about the path, and the success rate alone would not.

## Two latency columns

```text
Access-Request latency  from scheduled send  from actual send
  p50                   627µs                67µs
  p90                   1.036ms              110µs
  p95                   1.073ms              117µs
  p99                   1.106ms              144µs
  p99.9                 1.151ms              177µs
  max                   1.151ms              177µs
  mean                  590µs                70µs
```

- **From the scheduled send** is measured from the moment the request was *due*
  to go out under the offered rate. This is what a subscriber experienced.
- **From the actual send** is measured from when it really left. This is what
  the server took.

When they agree, the generator is keeping pace and either number is the truth.
When they diverge, the run is falling behind its own schedule, and the
difference is queueing inside radblaster rather than latency at the server:

```text
  p50                   509.608ms            561µs
```

That is a server still answering in half a millisecond while the run is half a
second behind. Measuring only from the actual send would have hidden the
overload entirely; measuring only from the schedule would have blamed the server
for it. The report says which it is, in words:

```text
- Requests went out a median of 509.047ms later than scheduled, so the latency
  measured from the scheduled send (509.608ms) is mostly this run failing to
  keep pace rather than the server being slow (561µs from the actual send).
```

Latency stays measured from the original request even when a later attempt is
the one answered, because that is what the subscriber waited. A tail that jumps
to the retry interval is real, not an artefact.

## Sessions

```text
Sessions
  Started              2000
  Completed            2000
  Setup rate offered   200/s
  Setup rate achieved  200/s
```

**Started** counts subscribers that got online. **Completed** counts those that
also tore down cleanly. The achieved rate is measured over the window in which
subscribers were actually being established, not the whole run, because a run
that holds sessions online afterwards spends most of its time deliberately
setting nothing up.

## Notes

The report ends with what limited the run, in plain language. These are the ones
worth recognising.

**The run hit its own ceiling:**

```text
- 16044 requests could not be offered: 48 links carry at most 12288 requests at
  once. At a mean server response of 3.522ms that caps this run at about
  3489085/s regardless of the rate asked for. Raise the link count to lift the
  ceiling.
```

More `-links` will help. See [how it works](how-it-works.md).

**The server hit its ceiling:**

```text
- 590412 requests could not be offered, but that is a symptom rather than the
  problem: identifiers stayed in use because 81361 requests went unanswered. The
  server is the limit here, so more links would only push harder at something
  already dropping.
```

More links will not help. Both notes describe blocked requests; only the first
is about radblaster.

**The load asked for was not delivered:**

```text
- Achieved 139062 sessions/s against an offered 400000/s, so the server was
  tested at less load than asked for.
```

Whatever the run proves, it does not prove anything about 400,000 a second.

## An honest read of a saturated server

Putting it together, a server at its limit looks like this: latency from the
scheduled send climbs into seconds while latency from the actual send stays
flat, the outstanding count sits at the ceiling, requests start being lost, and
the achieved rate falls short of the offered one. The moment where those begin
is the capacity figure worth quoting, and a `ramp` finds it in one run.
