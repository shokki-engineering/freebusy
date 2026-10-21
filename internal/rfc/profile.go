// Package rfc reads freebusy's own data off RFC records.
//
// A bookable thing is an RFC 9073 VRESOURCE, and freebusy keeps no columns of
// its own on it. What the availability engine needs to know that the RFC does
// not model -- a minimum stay, a notice period -- rides in the resource's RFC
// 9073 section 6.6 STRUCTURED-DATA, under a schema URI freebusy owns. This
// package finds and decodes that payload.
package rfc

import (
	"encoding/json"
	"fmt"

	resourcepbv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
)

// ProfileSchemaURI identifies the STRUCTURED-DATA payload freebusy reads off a
// resource.
//
// RFC 9073 section 6.6 carries arbitrary machine-readable data with a SCHEMA
// parameter naming what it conforms to, which is the RFC's own extension point
// and the reason freebusy needs no field of its own on the resource. A resource
// may carry several structured-data entries; this URI is how freebusy finds the
// one that is its.
const ProfileSchemaURI = "https://freebusy.ohtarnished.dev/schema/booking-profile"

// Profile is the booking-relevant data freebusy reads off a resource's
// STRUCTURED-DATA. Fields are optional: a resource that never declared one is
// normal, not an error.
type Profile struct {
	// MaxOccupancy is the most people one unit of this resource takes, zero
	// for unbounded. The engine does not enforce it -- who is coming is a
	// question for the vertical booking on top -- but it is part of the
	// profile, so it is read.
	MaxOccupancy int32 `json:"max_occupancy"`

	// The booking policy the availability engine enforces, each an RFC 5545
	// DURATION value such as "PT1H" or "P30D". Empty imposes nothing.
	//
	// MinDuration and MaxDuration bound the window's length: a minimum stay, a
	// court's shortest slot. MinNotice is how far ahead a window must be booked
	// and MaxAdvance how far ahead bookings open.
	MinDuration string `json:"min_duration,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
	MinNotice   string `json:"min_notice,omitempty"`
	MaxAdvance  string `json:"max_advance,omitempty"`
}

// Profile decodes freebusy's STRUCTURED-DATA payload from a resource.
//
// Only an inline `text` payload is read. A `uri` form would mean fetching an
// arbitrary URL on the booking hot path, and a `binary` one has no agreed
// encoding for this schema; both are left unread rather than guessed at. A
// resource carrying neither returns the zero Profile and no error, because a
// resource without a booking profile is a perfectly ordinary resource.
func DecodeProfile(res *resourcepbv1.Resource) (Profile, error) {
	var p Profile
	for _, sd := range res.GetStructuredData() {
		if sd.GetSchema() != ProfileSchemaURI {
			continue
		}
		text := sd.GetText()
		if text == "" {
			continue
		}
		if err := json.Unmarshal([]byte(text), &p); err != nil {
			return Profile{}, fmt.Errorf("decode booking profile on %s: %w", res.GetName(), err)
		}
		return p, nil
	}
	return p, nil
}
