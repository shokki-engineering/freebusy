<!-- Copyright 2026 Srikanth K. -->
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Errors

Every error freebusy returns says what went wrong and what to do about it, in
its message, and carries a **reason**: a stable, machine-readable name for the
cause. Branch on the reason, never on the message. Messages are written for
people and may be reworded in any release; reasons are part of the API and
change only with a major version.

## The shape of an error

Over gRPC an error is a `google.rpc.Status`: a code, a message and details.
Over REST the same status comes wrapped in `error`, with the HTTP status as
`code` and the gRPC code's name as `status` (AIP-193):

```json
{
  "error": {
    "code": 400,
    "status": "FAILED_PRECONDITION",
    "message": "Hold organizationalUnits/riverside/holds/h1 is expired, so it no longer holds the resources. Take a new hold with CreateHold (POST /v1/organizationalUnits/riverside/holds) and create the reservation from it; if settling takes long, ExtendHold before expire_time.",
    "details": [
      { "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "HOLD_NOT_ACTIVE", "domain": "freebusy.dev" },
      { "@type": "type.googleapis.com/google.rpc.PreconditionFailure",
        "violations": [{ "type": "HOLD_NOT_ACTIVE",
                         "subject": "organizationalUnits/riverside/holds/h1",
                         "description": "…the same sentences as the message" }] }
    ]
  }
}
```

- **`ErrorInfo`** is always there (except on `INTERNAL`, below): `reason` from
  the tables here, `domain` `freebusy.dev`.
- **`BadRequest`** names the request field at fault, when one is.
- **`PreconditionFailure`** names the resource whose state is in the way, on
  `FAILED_PRECONDITION`.
- **`ResourceInfo`** names the resource that is missing or in the way,
  otherwise.

An **`INTERNAL`** error is a fault in freebusy, not in the request. It has no
reason; its message ends with the trace ID to report. Retry it; if it keeps
failing, report the trace.

## Codes and what to do

| gRPC code | HTTP | What it means | Retry? |
|---|---|---|---|
| `INVALID_ARGUMENT` | 400 | The request is wrong. | Not as it is: fix the field the reason names. |
| `NOT_FOUND` | 404 | Something the request names does not exist, or is deleted. | Not until it exists. |
| `ALREADY_EXISTS` | 409 | The id is taken. | With another id. |
| `FAILED_PRECONDITION` | 400 | The state of a resource forbids the call. | Not until that state changes; the message says how. |
| `ABORTED` | 409 | A concurrent change got there first. | Yes: re-read, then retry. |
| `UNIMPLEMENTED` | 501 | freebusy does not do this. | No. |
| `INTERNAL` | 500 | A fault in freebusy. | Yes, then report the trace. |
| `UNAVAILABLE` | 503 | The server is starting, stopping, or unreachable. | Yes, with backoff. |

## Reasons

A reason is listed once, under the area that returns it. A few are returned
with two codes, depending on the call; both are given.

### Any call

| Reason | Code | Means | Do |
|---|---|---|---|
| `INVALID_REQUEST_FIELDS` | `INVALID_ARGUMENT` | The request breaks a field rule of the API: a missing required field, a value out of range, a malformed name. `BadRequest` lists each field; each violation's `reason` is the rule's id. | Fix each listed field. |
| `REQUIRED_FIELD` | `INVALID_ARGUMENT` | A field the call needs is empty. | Set it. |
| `INVALID_ID` | `INVALID_ARGUMENT` | A `*_id` is not a valid id: lower-case letters, digits and hyphens, starting with a letter, at most 63 characters (AIP-122). | Fix it, or leave it empty to have one assigned. |
| `MALFORMED_NAME` | `INVALID_ARGUMENT` | A resource name is not shaped like the collection it should be in. | Send the full name the create call returned. |
| `NOTHING_TO_UPDATE` | `INVALID_ARGUMENT` | An update sets no field and names none in `update_mask`. | Set the fields to change and name them in `update_mask`. |
| `UNKNOWN_MASK_PATH` | `INVALID_ARGUMENT` | `update_mask` names a field the resource does not have, or one this update cannot change. | Name fields by their proto names; the message lists what can change. |
| `NOT_FOUND` | `NOT_FOUND` | The resource named does not exist, or is deleted. | Check the name; list what exists. |
| `ALREADY_EXISTS` | `ALREADY_EXISTS` | A create's id is taken, or an `Undelete…` names something not deleted. | Choose another id, or leave it empty; read the existing one. |
| `CONCURRENT_MODIFICATION` | `ABORTED` | The `etag` sent is no longer current, or another writer won the race for the same row. | Read the resource again, reapply the change, retry with the new etag. |
| `INVALID_ARGUMENT` | `INVALID_ARGUMENT` | The request is wrong in a way no more specific reason names. | Read the message. |
| `INVALID_STATE` | `FAILED_PRECONDITION` | The resource's state forbids the call, in a way no more specific reason names. | Read the message. |
| `UNIMPLEMENTED` | `UNIMPLEMENTED` | The call, or this use of it, is not implemented. | Read the message. |
| `REQUEST_IN_FLIGHT` | `ABORTED` | A call with the same `request_id` is still running. | Wait a moment and retry with the same `request_id`; once the first finishes, the retry returns its result. |
| `REQUEST_ABANDONED` | `ABORTED` | An earlier call with the same `request_id` stopped before finishing; its claim is now cleared. | Retry with the same `request_id`: it runs once. |

### Organizations and sites

| Reason | Code | Means | Do |
|---|---|---|---|
| `ORGANIZATION_REQUIRED` | `INVALID_ARGUMENT` | A site (organizational unit) must belong to an organization, and the request names none. | Create the organization first and send its name. |
| `ORGANIZATION_NOT_FOUND` | `NOT_FOUND` | The organization named does not exist. | Check the name, or `ListOrganizations`. |
| `ORGANIZATION_DELETED` | `NOT_FOUND`, or `FAILED_PRECONDITION` on `UndeleteOrganizationalUnit` | The organization is deleted; a site under it cannot serve or be restored. | `UndeleteOrganization` first, or use a live one. |
| `ORGANIZATION_HAS_SITES` | `FAILED_PRECONDITION` | An organization with sites cannot be deleted. | Delete or move its sites first. |
| `SITE_REQUIRED` | `INVALID_ARGUMENT` | A resource is booked at a site, and the request names none. | Send its `organizational_unit`. |
| `SITE_NOT_FOUND` | `NOT_FOUND` | The site named does not exist. | Check the name, or `ListOrganizationalUnits`. |
| `SITE_DELETED` | `NOT_FOUND` | The site is deleted. | `UndeleteOrganizationalUnit`, or use a live site. |
| `SITE_WITHOUT_ORGANIZATION` | `FAILED_PRECONDITION` | The site belongs to no organization, so nothing is served there. | Set its `organization`. |
| `SITE_HAS_RESOURCES` | `FAILED_PRECONDITION` | A site with resources cannot be deleted. | Delete or move its resources first. |

### Calendars, events and availability

| Reason | Code | Means | Do |
|---|---|---|---|
| `CALENDAR_NOT_FOUND` | `NOT_FOUND` | The calendar named does not exist. | Check the parent, or `ListCalendars`. |
| `CALENDAR_DELETED` | `FAILED_PRECONDITION` | Nothing can be added to a deleted calendar. | `UndeleteCalendar`, or use a live one. |
| `CALENDAR_NOT_EMPTY` | `FAILED_PRECONDITION` | The calendar still has events or availability. | Send `force=true`, or delete them first. |
| `NAME_NOT_UNDER_PARENT` | `INVALID_ARGUMENT` | `BatchGetEvents` was asked for an event of another calendar. | Ask each calendar for its own events. |
| `NOT_RECURRING` | `FAILED_PRECONDITION` | `ExpandEvent` on an event without a recurrence rule. | `GetEvent` reads it as it is. |
| `INVALID_EVENT` | `FAILED_PRECONDITION` | An event on the calendar cannot be read as time (for example, no start), so the calendar's time cannot be computed. | Fix the event the message names. |
| `UNSUPPORTED_RECURRENCE` | `UNIMPLEMENTED` | An event recurs by a rule part freebusy does not expand: BYSECOND, BYYEARDAY, BYWEEKNO or RSCALE. | Express the rule without them; the message lists what is supported. |
| `WINDOW_INCOMPLETE` | `INVALID_ARGUMENT` | A window needs both `start_time` and `end_time`. | Send both, RFC 3339. |
| `WINDOW_BACKWARDS` | `INVALID_ARGUMENT` | `window.end_time` is not after `window.start_time`. | Swap them, or check the offsets. |
| `WINDOW_TOO_LONG` | `INVALID_ARGUMENT` | The window is longer than one call answers. | Split it into consecutive windows. |

### Availability

| Reason | Code | Means | Do |
|---|---|---|---|
| `INVALID_TIME_ZONE` | `INVALID_ARGUMENT` | `time_zone` is not an IANA zone name. | Use one such as `Europe/London`, or leave it empty for the site's own. |
| `TOO_MANY_BUCKETS` | `INVALID_ARGUMENT` | The window would be answered in too many buckets. | A shorter window, a longer `slot_duration`, or day granularity. |

### Holds

| Reason | Code | Means | Do |
|---|---|---|---|
| `CAPACITY_EXHAUSTED` | `FAILED_PRECONDITION` | `CreateHold`: fewer resources are free for the whole window than were asked for. The race for the last one is lost this way too. | Ask for fewer, or another window; `ComputeAvailability` shows what is free. |
| `INVALID_HOLD_EXTENSION` | `INVALID_ARGUMENT` | `ExtendHold`'s `duration` is zero, negative or above the maximum. | Extend by less, again before it expires if needed. |
| `HOLD_NOT_ACTIVE` | `FAILED_PRECONDITION` | The hold expired, was released, or became a reservation. | Take a new hold. |
| `HOLD_CONVERTED` | `FAILED_PRECONDITION` | `ReleaseHold` on a hold that became a reservation. | Cancel the reservation instead. |
| `HOLD_ACTIVE` | `FAILED_PRECONDITION` | `DeleteHold` on a hold that still holds its resources. | `ReleaseHold` first. |

### Reservations

| Reason | Code | Means | Do |
|---|---|---|---|
| `HOLD_NOT_FOUND` | `INVALID_ARGUMENT` | The reservation's `hold` does not exist. | Take a hold first with `CreateHold`. |
| `HOLD_AT_ANOTHER_SITE` | `INVALID_ARGUMENT` | The hold is at a different site from the reservation's parent. | Send the hold's own site as `parent`. |
| `HOLD_LAPSED` | `FAILED_PRECONDITION` | The hold lapsed before the reservation was confirmed; the reservation failed. | Take a new hold and create the reservation again; hold longer, or `ExtendHold`. |
| `RESERVATION_ID_TAKEN` | `ALREADY_EXISTS` | `reservation_id` names a reservation made from another hold. | Choose another id, or leave it empty. |
| `RESERVATION_FAILED` | `FAILED_PRECONDITION` | `CancelReservation` on a reservation that failed; it holds nothing. | Nothing to do. |
| `RESERVATION_MOVED` | `FAILED_PRECONDITION` | The reservation was rescheduled; its replacement holds the booking. | Act on the replacement the message names. |
| `RESERVATION_NOT_CONFIRMED` | `FAILED_PRECONDITION` | Only a confirmed reservation can be rescheduled. | Create a new reservation from the new hold. |
| `RESCHEDULE_IN_PROGRESS` | `FAILED_PRECONDITION` | The reservation is already being moved. | Wait for that move; `GetReservation` shows its state. |
| `SAME_HOLD` | `INVALID_ARGUMENT` | Rescheduling onto the hold the reservation was made from changes nothing. | Take a hold for the new window and reschedule onto it. |
| `REPLACEMENT_FAILED` | `FAILED_PRECONDITION` | The new slot could not be booked; the reservation keeps its original one. | Take a fresh hold and reschedule again. |

### Webhooks

| Reason | Code | Means | Do |
|---|---|---|---|
| `INVALID_URI` | `INVALID_ARGUMENT` | `uri` is not an absolute URL. | Send scheme, host and path. |
| `URI_HAS_CREDENTIALS` | `INVALID_ARGUMENT` | `uri` carries a username or password. | Remove them; verify the delivery signature instead. |
| `URI_NOT_HTTPS` | `INVALID_ARGUMENT` | `uri` is not https. | Serve the endpoint over https. |
| `URI_NOT_PUBLIC` | `INVALID_ARGUMENT` | `uri` resolves to a private or loopback address. | Expose it publicly. |
| `UNKNOWN_EVENT_TYPE` | `INVALID_ARGUMENT` | An entry in `event_types` is not an event freebusy sends. | Choose from the list in the message, or leave it empty for all. |
| `WEBHOOK_HAS_DELIVERIES` | `FAILED_PRECONDITION` | The webhook still has deliveries on record. | Send `force=true`, or `PauseWebhook` to keep the record. |

## Keeping this page true

`go run ./tools/errors` (part of `just lint`, so CI runs it) reads every
reason the server can return from its source and fails when one is missing
here, or when this page lists one nothing returns.
