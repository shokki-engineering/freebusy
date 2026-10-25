<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Getting started

From nothing to a confirmed booking over the REST API, with `curl`. The same
calls exist over gRPC (`:50051`) and as MCP tools (`http://localhost:8081/mcp`);
the names match.

You need a running freebusy. The quickest is the Compose stack:

```sh
cd deploy/compose
cp .env.example .env          # set POSTGRES_PASSWORD and WEBHOOK_SECRET_KEY
docker compose up -d
curl http://localhost:8080/health
```

The examples book a padel court at a club. Every request and response below
was run against that stack; responses are trimmed to the fields that matter.
Dates are in November 2026: pick dates in the future when you try it.

```sh
B=http://localhost:8080
```

## 1. An organization and a site

Everything belongs to an **organization** (the operator) and is booked at a
**site** (an organizational unit: a club, a car park, a clinic).

```sh
curl -s -X POST "$B/v1/organizations?organizationId=acme" \
  -H 'Content-Type: application/json' \
  -d '{"displayNames": ["Acme Sports"]}'
```

```json
{ "name": "organizations/acme", "displayNames": ["Acme Sports"], "etag": "01M5HD4DP1QS…" }
```

```sh
curl -s -X POST "$B/v1/organizationalUnits?organizationalUnitId=riverside" \
  -H 'Content-Type: application/json' \
  -d '{"displayNames": ["Riverside Club"], "organization": "organizations/acme"}'
```

```json
{ "name": "organizationalUnits/riverside", "organization": "organizations/acme" }
```

Leave the `…Id` query parameter out to have an id assigned.

## 2. Something to book

A **resource** is the bookable thing. Its **calendar** is its timeline, in its
own time zone; **availability** on the calendar says when it is offered.

```sh
curl -s -X POST "$B/v1/calendars?calendarId=court-1" \
  -H 'Content-Type: application/json' \
  -d '{"displayName": "Court 1", "timeZone": {"id": "Europe/London"}}'

curl -s -X POST "$B/v1/resources?resourceId=court-1" \
  -H 'Content-Type: application/json' \
  -d '{"displayName": "Court 1", "resourceTypes": ["padel-court"],
       "organizationalUnit": "organizationalUnits/riverside",
       "calendar": "calendars/court-1"}'
```

Open every day from 08:00 for 14 hours, London time, from 2 November:

```sh
curl -s -X POST "$B/v1/calendars/court-1/availabilities" \
  -H 'Content-Type: application/json' \
  -d '{"start": {"dateTime": {"year": 2026, "month": 11, "day": 2, "timeZone": {"id": "Europe/London"}}},
       "availablePeriods": [{
         "start": {"dateTime": {"year": 2026, "month": 11, "day": 2, "hours": 8, "timeZone": {"id": "Europe/London"}}},
         "duration": "50400s",
         "recurrenceRule": "FREQ=DAILY"}]}'
```

Closures, maintenance and bookings made elsewhere are **events** on the same
calendar (`POST /v1/calendars/court-1/events`). A calendar app can write all of
this over CalDAV instead.

## 3. Ask what is free

Across the organization, by type:

```sh
curl -s -X POST "$B/v1/organizations/acme:searchAvailability" \
  -H 'Content-Type: application/json' \
  -d '{"window": {"startTime": "2026-11-03T18:00:00Z", "endTime": "2026-11-03T19:00:00Z"},
       "resourceTypes": ["padel-court"], "quantity": 1}'
```

```json
{ "matches": [{ "resource": "resources/court-1",
                "organizationalUnit": "organizationalUnits/riverside",
                "resourceTypes": ["padel-court"] }] }
```

At one site, slot by slot:

```sh
curl -s -X POST "$B/v1/organizationalUnits/riverside:computeAvailability" \
  -H 'Content-Type: application/json' \
  -d '{"window": {"startTime": "2026-11-03T16:00:00Z", "endTime": "2026-11-03T20:00:00Z"},
       "granularity": "AVAILABILITY_GRANULARITY_SLOT", "slotDuration": "3600s"}'
```

```json
{ "buckets": [
    { "window": { "startTime": "2026-11-03T16:00:00Z", "endTime": "2026-11-03T17:00:00Z" },
      "freeCount": 1, "totalCount": 1 }, … ] }
```

One exact window, with the reasons when the answer is no:

```sh
curl -s -X POST "$B/v1/organizationalUnits/riverside:checkAvailability" \
  -H 'Content-Type: application/json' \
  -d '{"window": {"startTime": "2026-11-03T18:00:00Z", "endTime": "2026-11-03T19:00:00Z"}}'
```

```json
{ "bookable": true, "freeCount": 1, "candidateResources": ["resources/court-1"] }
```

## 4. Hear about it: a webhook

Subscribe before booking, so the confirmation reaches you. The endpoint must be
public https.

```sh
curl -s -X POST "$B/v1/webhooks?webhookId=bookings" \
  -H 'Content-Type: application/json' \
  -d '{"uri": "https://example.com/hooks/freebusy",
       "eventTypes": ["freebusy.reservation.confirmed", "freebusy.reservation.cancelled"],
       "organizationalUnit": "organizationalUnits/riverside"}'
```

```json
{ "name": "webhooks/bookings", "state": "WEBHOOK_STATE_ACTIVE",
  "secret": "whsec_Lyvu5ZTUncZX…" }
```

The `secret` is shown this once: keep it to verify each delivery's
`webhook-signature` ([Standard Webhooks](https://www.standardwebhooks.com/)).
Each delivery is a CloudEvent; the README's Webhooks section lists the types.

## 5. Hold

A **hold** claims resources for a window, for a short time (15 minutes by
default), while you take payment or fill in forms. Send an `idempotencyKey`:
retrying with the same key returns the same hold instead of taking another.

```sh
curl -s -X POST "$B/v1/organizationalUnits/riverside/holds" \
  -H 'Content-Type: application/json' \
  -d '{"window": {"startTime": "2026-11-03T18:00:00Z", "endTime": "2026-11-03T19:00:00Z"},
       "quantity": 1, "idempotencyKey": "checkout-7f3a"}'
```

```json
{ "name": "organizationalUnits/riverside/holds/c49331c0a350a309395876fa75",
  "resources": ["resources/court-1"], "state": "HOLD_STATE_ACTIVE",
  "expireTime": "2026-10-22T09:13:01.657092Z" }
```

freebusy chose the resource. Anyone else asking for that court then is told no,
and why:

```json
{ "error": { "code": 400, "status": "FAILED_PRECONDITION",
    "message": "Only 0 of the 1 resources asked for are free for that whole window. Ask for fewer, or another window -- ComputeAvailability (POST /v1/organizationalUnits/riverside:computeAvailability) shows what is free when, and CheckAvailability says why a window is not.",
    "details": [{ "@type": "type.googleapis.com/google.rpc.ErrorInfo",
                  "reason": "CAPACITY_EXHAUSTED", "domain": "freebusy.dev" }, …] } }
```

Branch on `reason`; [errors.md](errors.md) lists them all. Need longer?
`POST /v1/{hold}:extend`. Changed your mind? `POST /v1/{hold}:release`.

## 6. Reserve

When payment clears, turn the hold into a **reservation**. `customer` names who
it is for in your user service; `attributes` keeps whatever else you need.

```sh
curl -s -X POST "$B/v1/organizationalUnits/riverside/reservations" \
  -H 'Content-Type: application/json' \
  -d '{"hold": "organizationalUnits/riverside/holds/c49331c0a350a309395876fa75",
       "customer": "users/ana",
       "attributes": {"party_size": 4, "payment": "pi_3Q9"}}'
```

```json
{ "name": "organizationalUnits/riverside/reservations/c49331c0a350a309395876fa75",
  "resources": ["resources/court-1"],
  "startTime": "2026-11-03T18:00:00Z", "endTime": "2026-11-03T19:00:00Z",
  "state": "RESERVATION_STATE_CONFIRMED" }
```

The hold is now `HOLD_STATE_CONVERTED`. A hold backs at most one reservation,
so retrying this call is safe. Your webhook receives
`freebusy.reservation.confirmed`; what was sent, and how the endpoint answered,
is on record:

```sh
curl -s "$B/v1/webhooks/bookings/deliveries"
```

```json
{ "deliveries": [{ "eventType": "freebusy.reservation.confirmed",
                   "state": "DELIVERY_STATE_PENDING", "attemptCount": 1,
                   "responseCode": 405,
                   "errorMessage": "the endpoint answered 405 Method Not Allowed" }] }
```

(example.com does not accept deliveries, so this one waits to retry, with
backoff.)

## 7. Change it

Update who it is for, or what is kept with it. `updateMask` names the fields
to change -- `customer`, `attributes`, or both -- and is required; the time and
the court cannot change this way. Send the `etag` you read to be refused,
rather than overwrite, if someone changed it since:

```sh
curl -s -X PATCH "$B/v1/organizationalUnits/riverside/reservations/c49331c0a350a309395876fa75?updateMask=attributes" \
  -H 'Content-Type: application/json' \
  -d '{"attributes": {"party_size": 6, "payment": "pi_3Q9"}, "etag": "<etag>"}'
```

```json
{ "attributes": {"party_size": 6, "payment": "pi_3Q9"},
  "state": "RESERVATION_STATE_CONFIRMED" }
```

A webhook that asked for it receives `freebusy.reservation.updated`.

Cancel, which frees the court:

```sh
curl -s -X POST "$B/v1/organizationalUnits/riverside/reservations/c49331c0a350a309395876fa75:cancel" \
  -H 'Content-Type: application/json' -d '{"reason": "customer cancelled"}'
```

```json
{ "state": "RESERVATION_STATE_CANCELLED", "cancelReason": "customer cancelled" }
```

Or move it: take a hold on the new time first, then
`POST /v1/{reservation}:reschedule` with `{"hold": "<new hold>"}`. The new
time is secured before the old one is let go.

## Where next

- [errors.md](errors.md): every error reason, and what to do about it.
- `protobuf/generated/openapi/openapiv3-spec.yaml`: every REST path and field.
- `protobuf/freebusy/*/README.md`: the API reference, from the protos.
- The README: configuration, webhooks, CalDAV, running in production.
