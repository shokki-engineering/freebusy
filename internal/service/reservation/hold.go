package reservation

import (
	"context"
	"strings"

	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
)

// HoldOf derives a reservation's hold from its name. The two share an id and a
// site by construction, which is what lets the stores fill `hold` on every
// read without mapping the reference column the generated converters skip.
func HoldOf(name string) string {
	parent, id, ok := strings.Cut(name, "/reservations/")
	if !ok {
		return ""
	}
	return parent + "/holds/" + id
}

// FillHold is an AfterRead hook for the generated repositories.
func FillHold(_ context.Context, r *reservationpbv1.Reservation) error {
	r.Hold = HoldOf(r.GetName())
	return nil
}

// SiteID is the organizational unit id in a reservation name,
// "organizationalUnits/{ou}/reservations/{id}".
func SiteID(name string) string {
	if parts := strings.Split(name, "/"); len(parts) == 4 {
		return parts[1]
	}
	return ""
}
