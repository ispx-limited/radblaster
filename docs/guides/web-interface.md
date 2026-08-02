# Web interface

```sh
radblaster serve
```

Then open <http://127.0.0.1:8080>. Set a run up in the form, start it, and watch
the rates, the latency percentiles and the outstanding request count move while
it goes.

## What it shows

The dashboard carries the same numbers the command line reports, updated live:

- **Sessions, online and awaiting reply.** The outstanding request count is the
  one to watch. It moves before any latency figure does, because a server that
  has begun to struggle holds requests before it starts answering them slowly.
- **Offered, answered and lost per second**, so a divergence between what was
  asked for and what came back is visible as it happens rather than in the
  summary.
- **Latency from the scheduled send and from the actual send**, plotted
  together. When the two separate, the generator is behind and the gap is not
  the server's fault. See [reading the results](../results.md).
- **A row per exchange**, so authentication and accounting are never averaged
  together.
- **Notes** explaining what limited the run, in the same words the command line
  uses.

## Options

```sh
radblaster serve -addr 127.0.0.1:9000 \
                 -profiles ./profiles \
                 -dictionary /usr/share/freeradius/dictionary
```

`-dictionary` gives the attribute picker something to search, and `-profiles`
sets where the Profile tab reads and writes. See
[dictionaries](dictionaries.md) and [profiles](profiles.md).

## The API

Everything the interface can do goes through the same HTTP API, so nothing in
the browser has a privileged path the command line lacks:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/run` | start a run |
| `DELETE` | `/api/run` | stop the run |
| `GET` | `/api/status` | the current picture |
| `GET` | `/api/events` | the same, streamed as server-sent events |
| `GET` | `/api/dictionary` | what dictionaries loaded |
| `GET` | `/api/attributes?q=` | search attributes by name |
| `GET` | `/api/profiles` | list saved profiles |
| `GET`, `PUT`, `DELETE` | `/api/profiles/{name}` | read, save, remove one |

That makes the interface scriptable. Driving a run from CI through `POST
/api/run` and reading `/api/events` is a supported way to use it.

## Security

!!! warning "It binds to localhost, and it should stay there"
    The interface can start a run against any server it can reach and has no
    authentication of its own. Exposing it on a shared address hands that
    ability to anyone who can reach it, and what it hands them is a tool that
    generates millions of requests a second.

    If it must be reachable from elsewhere, put it behind something that
    authenticates, and treat the ability to reach it as equivalent to shell
    access on that machine.

The shared secret is accepted but never returned by the API, so a browser
pointed at a running instance cannot read it back out.

### Why loopback is not on its own enough

Binding to loopback keeps the network out. It does not keep a browser out: a
page on any site an operator visits can send requests to `127.0.0.1`, and a
name an attacker controls can be pointed at loopback so that the browser treats
their page as same-origin with the interface. Left open, that is enough for a
site to start a run against a production AAA platform from the operator's own
machine, without ever reading a reply.

So the API also:

- refuses any request whose `Origin` is not its own
- requires `application/json` on a request carrying a body, which is what makes
  a browser ask permission before sending one at all
- answers only to an address or to `localhost`, never to a name, because a name
  is the thing that can be repointed

Behind a reverse proxy that addresses it by name, name it with `-allowed-host`:

```sh
radblaster serve -addr 127.0.0.1:8080 -allowed-host radblaster.internal
```

None of this is authentication. It stops a browser being used as the way in; it
does not stop anyone who can reach the port directly. The warning above still
holds.
