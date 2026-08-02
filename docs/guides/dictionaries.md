# Dictionaries

radblaster ships definitions for the RFC attributes only. Everything else comes
from the dictionary files your own RADIUS server already uses, so both ends
resolve the same names to the same numbers.

## Why it works this way

A dictionary is a local naming convention, not part of the protocol. Names are
never exchanged: what goes on the wire is a vendor number and an attribute
number. Loading a dictionary changes what you can type, and cannot change what
is transmitted.

That is why radblaster reads your files rather than shipping its own copy of
every vendor. A definition that has drifted from your server's is a bug you
would rather find in the tool's output than in a mismatch on the wire.

## Where it looks

Without any flags, radblaster looks in the usual places a RADIUS installation
puts its dictionaries and reports what it found:

```text
Loaded 6140 attributes across 193 vendors from:
  /usr/share/freeradius/dictionary
```

If it finds nothing, it says so, and vendor attributes can still be written by
number:

```text
Using the built-in RFC attributes only. Vendor attributes can be written by number.
```

Pass `-no-auto-dictionary` to stop it looking, which is what you want when a run
must be reproducible on a machine whose installed dictionaries differ.

## Pointing it at your own

```sh
radblaster run -dictionary /usr/share/freeradius/dictionary ...
```

The flag is repeatable and takes either a file or a directory. A dictionary file
that includes others is followed, so the top of a tree is usually all you need:
the example above resolves 193 vendors from one path.

Definitions load in order and a later one replaces an earlier one, so your own
attributes override the shipped set rather than colliding with it.

## Sending vendor attributes

```sh
radblaster run -attr 'Cisco-AVPair=subscriber:sla-profile=voice' \
               -attr 'Mikrotik-Rate-Limit=20M/100M' \
               -attr 'Huawei-Input-Peak-Rate=100000' \
               -dictionary /usr/share/freeradius/dictionary ...
```

`-attr` is repeatable, and every attribute it names goes on every request the
run sends. The value is encoded according to the type the dictionary gives, so
a string, an integer and an IP address each go on the wire correctly without
being told which is which.

`{n}` works here too, so an attribute can vary per subscriber:

```sh
radblaster run -attr 'Unisphere-Ingress-Policy-Name=policy-{n}' ...
```

## When there is no dictionary

Any attribute can be written by number:

```sh
radblaster run -attr '26.9.1=subscriber:sla-profile=voice' ...
```

That is `26.<vendor>.<type>`, where 26 is Vendor-Specific. The leading `26.` is
optional, so `9.1=...` means the same thing. The value is sent as a string when
its type is not known.

This is the escape hatch for an attribute your dictionaries do not define, or a
machine where they are not installed. The receiving server still decodes it by
name, because the numbers are what matter.

## If a name does not resolve

```text
radblaster: radius: unknown attribute "Cisco-AVPair"; load a dictionary or write it by number
```

The run stops before offering any load rather than quietly dropping the
attribute from every packet, because a test that silently omits the thing it was
meant to exercise is worse than one that refuses to start.

## In the web interface

The interface has an attribute picker that searches whatever dictionaries the
server was started with:

```sh
radblaster serve -dictionary /usr/share/freeradius/dictionary
```

Type a fragment of a name and it offers matches with their vendor and type, so
you do not have to remember whether your platform calls it `Unisphere-` or
something else.
