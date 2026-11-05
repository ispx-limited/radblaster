---
hide:
  - toc
---

<section class="ispx-home" markdown>
<div class="ispx-home__copy" markdown>

<h1 class="ispx-sr-only">radblaster</h1>

<p class="ispx-kicker">Load test your RADIUS platform with subscribers, not packets.</p>

<div class="ispx-actions" markdown>
[Get started](guides/quickstart.md){ .ispx-btn .ispx-btn--primary }
[View on GitHub](https://github.com/ispx-limited/radblaster){ .ispx-btn .ispx-btn--ghost }
</div>

<div class="ispx-signal__chips">
  <span class="ispx-chip">Whole session lifecycle</span>
  <span class="ispx-chip">Two million requests a second</span>
  <span class="ispx-chip">Answers CoA and Disconnect</span>
</div>

</div>
</section>

## Built for what an access network does to your AAA platform

<div class="ispx-card-grid" markdown>

<div class="ispx-card" markdown>
### The whole lifecycle
Authenticate, start accounting, send interims on the interval the server asked
for, stop with final counters. `Class` and `Framed-IP-Address` carried the whole
way, as a real access node would.
</div>

<div class="ispx-card" markdown>
### The failure path, at scale
Rejection is a slower and different path than acceptance. Mix refused
subscribers into the load and see what a realm outage does to the ones still
valid.
</div>

<div class="ispx-card" markdown>
### CoA and Disconnect, answered
radblaster is an access node the server can push changes to, so a mass
disconnect is testable from the side that sends it.
</div>

<div class="ispx-card" markdown>
### Failures counted by cause
A timeout, an unverifiable reply and a policy rejection are three problems with
three fixes. They are never added together.
</div>

<div class="ispx-card" markdown>
### It says when it is the bottleneck
Latency is measured from when a request was due and from when it went out. When
those diverge, the report says the generator is behind, not your server.
</div>

<div class="ispx-card" markdown>
### Enough load to find the edge
Over two million authentications a second on a desktop, so the ceiling a run
finds is your server's.
</div>

</div>
