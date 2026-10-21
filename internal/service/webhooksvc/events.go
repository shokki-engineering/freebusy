// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// source is the CloudEvents source of every event: this service.
const source = "//freebusy.shokki.engineering"

// change is one record as a feed read it.
type change struct {
	record proto.Message
	name   string
	etag   string
	// kind and state make the event type: "freebusy.<kind>.<state>".
	kind    string
	state   string
	updated time.Time
	// site is the organizational unit the record belongs to, empty for a
	// record that belongs to none.
	site string
}

// siteOf is the site a hold or reservation name sits under.
// "organizationalUnits/o/holds/h" -> "organizationalUnits/o".
func siteOf(name string) string {
	if i := strings.Index(name, "/"); i >= 0 {
		if j := strings.Index(name[i+1:], "/"); j >= 0 {
			return name[:i+1+j]
		}
	}
	return ""
}

// lifecycles are the states of a record that has none of its own.
var lifecycles = []string{"created", "updated", "deleted"}

// lifecycle is what last happened to a record with no state of its own: a
// soft delete, else its creation when it has not changed since, else an
// update.
func lifecycle(create, update, del *timestamppb.Timestamp) string {
	switch {
	case del != nil:
		return "deleted"
	case create.AsTime().Equal(update.AsTime()):
		return "created"
	}
	return "updated"
}

// eventType is the CloudEvents type, e.g. "freebusy.reservation.confirmed".
func (c change) eventType() string { return "freebusy." + c.kind + "." + c.state }

// eventID identifies the change: a record's etag changes with every write,
// so (name, etag) names one version of one record.
func (c change) eventID() string { return digest(c.name, c.etag) }

// payload is the request body: a CloudEvents 1.0 structured-mode JSON
// document whose data is the record in its proto3 JSON form, as the REST
// gateway returns it.
func (c change) payload() (string, error) {
	data, err := protojson.Marshal(c.record)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(struct {
		SpecVersion     string          `json:"specversion"`
		ID              string          `json:"id"`
		Source          string          `json:"source"`
		Type            string          `json:"type"`
		Subject         string          `json:"subject"`
		Time            string          `json:"time"`
		DataContentType string          `json:"datacontenttype"`
		Data            json.RawMessage `json:"data"`
	}{"1.0", c.eventID(), source, c.eventType(), c.name, c.updated.UTC().Format(time.RFC3339Nano), "application/json", data})
	return string(body), err
}

// wants reports whether w is sent c: it asks for c's type (or every type) and
// c's site (or every site), and existed when c happened. A record of no site
// goes only to webhooks of every site.
func wants(w *webhookpbv1.Webhook, c change) bool {
	if types := w.GetEventTypes(); len(types) > 0 && !slices.Contains(types, c.eventType()) {
		return false
	}
	if ou := w.GetOrganizationalUnit(); ou != "" && c.site != ou {
		return false
	}
	return !c.updated.Before(w.GetCreateTime().AsTime())
}

// deliveryName is where c is recorded for w. The id is derived, so recording
// the same change twice collides on the primary key.
func deliveryName(w *webhookpbv1.Webhook, c change) string {
	return w.GetName() + "/deliveries/d" + digest(w.GetName(), c.eventID())[:31]
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// stateName is an enum value's name without its prefix, in lower case:
// HOLD_STATE_EXPIRED -> "expired".
func stateName(value, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(value, prefix))
}

// EventTypes lists every event type a webhook can ask for, read off the
// state enums so a new state is a new type without touching this file.
func EventTypes() []string {
	var out []string
	add := func(kind, prefix string, names map[int32]string) {
		for v, n := range names {
			if v != 0 {
				out = append(out, "freebusy."+kind+"."+stateName(n, prefix))
			}
		}
	}
	add("hold", "HOLD_STATE_", allocationpbv1.HoldState_name)
	add("reservation", "RESERVATION_STATE_", reservationpbv1.ReservationState_name)
	for _, kind := range []string{"resource", "calendar", "event", "availability"} {
		for _, l := range lifecycles {
			out = append(out, "freebusy."+kind+"."+l)
		}
	}
	slices.Sort(out)
	return out
}
