---
hide:
  - toc
---

<section class="ispx-home" markdown>
<div class="ispx-home__copy" markdown>

<h1 class="ispx-sr-only">radblaster</h1>

<p class="ispx-kicker">Stress test your RADIUS infrastructure without a BNG in the way. radblaster generates the traffic an access node would generate, at the rate it would generate it, and models whole subscriber sessions rather than packets.</p>

<div class="ispx-actions" markdown>
[Get started](guides/quickstart.md){ .ispx-btn .ispx-btn--primary }
[View on GitHub](https://github.com/ispx-limited/radblaster){ .ispx-btn .ispx-btn--ghost }
</div>

<div class="ispx-signal__chips">
  <span class="ispx-chip">Whole subscriber sessions</span>
  <span class="ispx-chip">Vendor attributes from your own dictionaries</span>
  <span class="ispx-chip">Answers CoA and Disconnect</span>
</div>

</div>
</section>

## Built for testing an ISP's AAA platform

<div class="ispx-card-grid" markdown>

<div class="ispx-card" markdown>
### Sessions, not packets
A subscriber authenticates, comes online, sends interim updates for as long as
it is held, then tears down with an Accounting-Stop carrying its final counters.
`Class` and `Framed-IP-Address` from the Access-Accept are carried into every
accounting record, as a real access node would.
</div>

<div class="ispx-card" markdown>
### Your own attributes
Point radblaster at the dictionary files your RADIUS server already uses and
send vendor attributes by name. Nothing needs redefining, and an attribute can
still be written by number when there is no dictionary to hand.
</div>

<div class="ispx-card" markdown>
### Load the server can push back on
radblaster listens for CoA-Request and Disconnect-Request and answers them as an
access node would, so a bulk policy change or a mass disconnect can be tested
from the server's side of the exchange.
</div>

<div class="ispx-card" markdown>
### Results you can act on
Failures are counted by cause. A timeout, a reply that will not verify and a
policy rejection need different remedies, so they are never added together.
</div>

<div class="ispx-card" markdown>
### Honest about itself
Latency is reported both from the moment a request was due and from the moment
it went out. When those diverge the generator is falling behind, and the report
says so rather than charging the delay to the server.
</div>

<div class="ispx-card" markdown>
### One binary
A single file with the web interface built in. No runtime to install, no access
network, no BNG.
</div>

</div>
