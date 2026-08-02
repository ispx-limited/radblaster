# Profiles

A profile is an estate saved as a YAML file. The web interface edits it, the
command line runs it, and you commit it alongside whatever else describes that
network.

## Why they exist

The settings that describe a realistic estate are numerous and worth keeping:
the traffic mix, the NAS identity, the vendor attributes your platform expects,
the shape of the load. Retyping them for every run means they drift, and a
result you cannot reproduce is not evidence of anything.

## What one looks like

```yaml
name: demo-estate
description: 2000 PPPoE subscribers on the lab BNG, one minute online each

server: 127.0.0.1:11812
acctServer: 127.0.0.1:11813

access: pap
username: subscriber@example.net

load:
  rate: 200
  count: 2000
  shape: steady
  retries: 2
  timeout: 5s

session:
  hold: 1m

traffic:
  up:
    min: 125000
  down:
    min: 1250000

nas:
  identifier: lab-bng-01
  portType: 15
  callingStationId: "00:1a:2b:00:{n}"
```

Vendor attributes and the dictionaries they resolve against belong in the file
too:

```yaml
dictionaries:
  - /usr/share/freeradius/dictionary

attributes:
  - name: Cisco-AVPair
    value: subscriber:sla-profile=voice
  - name: Mikrotik-Rate-Limit
    value: 20M/100M
```

Ranges make an estate look like a spread of subscribers rather than one repeated:

```yaml
traffic:
  up:
    min: 125000
    max: 2000000
```

Each subscriber then draws its own rate from that range and keeps it for its
whole life.

## Running one

```sh
radblaster run -profile demo-estate.yaml -secret "$RADBLASTER_SECRET" -password secret
```

Any flag given on the command line overrides the file, so one saved estate can
be pointed at a different server or run at a different rate without editing it:

```sh
radblaster run -profile demo-estate.yaml -server 10.0.0.9:1812 -rate 1000 \
               -secret "$RADBLASTER_SECRET" -password secret
```

That is the shape of a regression test. The estate is committed and stable; what
varies is which server it is aimed at.

## Credentials are not in the file

!!! warning "A profile carries no secret and no password"
    Deliberately. A profile is meant to be committed, and the moment it can hold
    a shared secret somebody commits one. Supply them per run with `-secret` and
    `-password`, or set `RADBLASTER_SECRET` in the environment, which also keeps
    the secret out of the process list and your shell history.

The web interface follows the same rule: it accepts a secret to start a run and
never returns one, so a browser pointed at a running instance cannot read it
back.

## In the web interface

```sh
radblaster serve -profiles ./profiles
```

The Profile tab edits the current settings and saves them under that directory
as `<name>.yaml`. Files written there are exactly what the command line reads,
and files you write by hand appear in the interface. There is no separate
format and no import step.
