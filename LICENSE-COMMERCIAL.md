# Commercial licence

freebusy is dual-licensed.

| You are | Your licence | Cost |
|---|---|---|
| Building open-source software whose licence is compatible with AGPL-3.0 | [AGPL-3.0-or-later](./LICENSE) | Free, forever |
| Building proprietary or closed-source software on freebusy's code | Commercial licence | Paid |
| Embedding freebusy in a product you distribute without source | Commercial licence | Paid |
| Offering freebusy, or a service built on it, over a network without publishing your source | Commercial licence | Paid |

The `.proto` definitions and the code generated from them — everything under
[`protobuf/`](./protobuf), including the Go client packages published to the
Buf Schema Registry — are **Apache-2.0**. Writing a proprietary application that
*talks to* a freebusy server over gRPC, REST, MCP, CalDAV or webhooks needs no
commercial licence. The distinction is building on, or modifying, the server
itself.

---

## Why it works this way

The AGPL does the sorting, so no one has to argue about what counts as "open
source" or "proprietary". If your project's licence is AGPL-compatible you
already comply and owe nothing. If your product is closed, the AGPL is
incompatible with how you ship, and a commercial licence is the way through.

The intent is straightforward: **free for people building in the open, paid for
people building on top of it commercially without giving anything back.**

## What the AGPL asks of you

Section 13 is the part most people have not read. If you modify freebusy and let
users interact with it over a network — which is how a booking engine is used —
you must offer those users the source of your modified version, even if you
never distribute a binary. That closes the gap the plain GPL leaves open for
hosted services, and it is deliberate.

Running the unmodified server, for your own bookings or your customers', and
calling it from your own proprietary applications, is ordinary use: the AGPL
asks nothing of the applications that only talk to it.

Note that **importing counts.** A Go program that imports freebusy's server
packages (anything outside `protobuf/`) is a derived work, and if that program
is proprietary it needs a commercial licence.

## What a commercial licence grants

- The right to modify, embed and distribute freebusy in closed-source products
- No obligation to publish your source under Section 13
- Terms negotiated per deployment: sites, instances, redistribution, support

## Getting one

Open an issue titled `commercial licence` or contact the maintainer through the
repository. Include roughly what you are building and how you intend to deploy
it — that is usually enough to size it.

---

**This file describes the offer. It is not itself a contract**, and it does not
grant rights. An executed commercial agreement does. Nothing here is legal
advice; if the licensing boundary matters to your business, have a lawyer read
both this and the AGPL before you rely on either.
