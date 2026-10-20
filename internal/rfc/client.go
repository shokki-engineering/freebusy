// Package rfc is freebusy's client onto the RFC schema services.
//
// The catalogue freebusy books against is not freebusy's. A bookable thing is an
// RFC 9073 VRESOURCE and the calendar it is booked on is an RFC 5545 VCALENDAR,
// both served by protobuf-rfc's own services. freebusy references them by name —
// AIP-215 says to refer to a resource by name rather than embed it — and reads
// them through here when it needs a fact about one.
//
// Everything in this package is optional. A nil *Client is valid and every
// method on it returns "not found", because the engine has to keep working when
// the catalogue is unreachable: refusing a booking is a better failure than
// crashing, and the caller already has conservative defaults for the facts this
// supplies.
package rfc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/oh-tarnished/freebusy/protobuf/generated/go/protobuf/rfc5545/calendar/v1/calendarpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/protobuf/rfc9073/resource/v1/resourcepbv1"
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

// ErrUnavailable reports that no RFC catalogue is configured.
//
// A distinct error rather than a nil-check at every call site: "there is no
// catalogue" and "the catalogue said no" are different facts, and only the
// second should ever surface to a caller as a failed booking.
var ErrUnavailable = errors.New("rfc: no catalogue configured")

// Profile is the booking-relevant data freebusy reads off a resource's
// STRUCTURED-DATA. Fields are optional: a resource that never declared one is
// normal, not an error.
type Profile struct {
	// MaxOccupancy is the most guests one unit of this resource takes. Zero
	// means unbounded, matching party.Fits.
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

// Client reads the RFC services. The zero value is unusable; use Dial.
type Client struct {
	conn      *grpc.ClientConn
	resources resourcepbv1.ResourcesClient
	calendars calendarpbv1.CalendarsClient
}

// Dial connects to the RFC services at endpoint.
//
// One connection serves both: protobuf-rfc's services are separate proto
// packages but are expected to be served by one process, the way freebusy serves
// its own several services on one port. Split them later by taking two
// endpoints; nothing outside this constructor would change.
func Dial(ctx context.Context, endpoint string) (*Client, error) {
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial rfc services at %q: %w", endpoint, err)
	}
	return &Client{
		conn:      conn,
		resources: resourcepbv1.NewResourcesClient(conn),
		calendars: calendarpbv1.NewCalendarsClient(conn),
	}, nil
}

// Close releases the connection. Safe on a nil Client.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Resource fetches one VRESOURCE by name, e.g. "resources/bay-l2-014".
func (c *Client) Resource(ctx context.Context, name string) (*resourcepbv1.Resource, error) {
	if c == nil {
		return nil, ErrUnavailable
	}
	return c.resources.GetResource(ctx, &resourcepbv1.GetResourceRequest{Name: name})
}

// TimeZone returns the IANA zone a resource's nights are counted in, by reading
// the calendar the resource is booked on.
//
// Two calls rather than one: the zone lives on the RFC 5545 calendar, not on the
// resource, and section 7.3 gives a VRESOURCE no timezone of its own. An empty
// string means the resource names no calendar or the calendar names no zone,
// which callers read as UTC.
func (c *Client) TimeZone(ctx context.Context, res *resourcepbv1.Resource) (string, error) {
	if c == nil {
		return "", ErrUnavailable
	}
	calName := res.GetCalendar()
	if calName == "" {
		return "", nil
	}
	cal, err := c.calendars.GetCalendar(ctx, &calendarpbv1.GetCalendarRequest{Name: calName})
	if err != nil {
		return "", err
	}
	return cal.GetTimeZone().GetId(), nil
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
