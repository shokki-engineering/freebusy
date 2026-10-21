// Package webhooksvc serves WebhookService and keeps its promise: a
// dispatcher reads changes to holds and reservations into deliveries, and a
// sender POSTs each, signed, until its endpoint accepts it or attempts run out.
//
// Changes are read from the records, not announced by the writes that make
// them. Nothing that writes a hold or a reservation knows webhooks exist, and
// no change can be lost between a write and its announcement: a record's
// update_time and etag are the change log, and a soft delete is a change like
// any other.
package webhooksvc

import (
	"github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/webhook"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/availability"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/calendar"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/event"
	"github.com/oh-tarnished/freebusy/internal/database/repository/rfc/resource"
)

// Repositories are the generated repositories webhooks read and write.
type Repositories struct {
	Webhooks   webhook.WebhookRepository
	Deliveries webhook.DeliveryRepository
	Cursors    webhook.FeedCursorRepository
	// NewBatch starts a batch of webhook writes on the same backend.
	NewBatch func() webhook.Batch

	// The feeds: the records whose changes are sent.
	Holds          allocation.HoldRepository
	Reservations   reservation.ReservationRepository
	Resources      resource.ResourceRepository
	Calendars      calendar.CalendarRepository
	Events         event.EventRepository
	Availabilities availability.AvailabilityRepository
}

// NewRepositories opens every repository on conn, picking the backend the
// connection carries.
func NewRepositories(conn repox.Conn) Repositories {
	w := webhook.New(conn)
	return Repositories{
		Webhooks:       w.Webhooks,
		Deliveries:     w.Deliveries,
		Cursors:        w.FeedCursors,
		NewBatch:       func() webhook.Batch { return webhook.NewBatch(conn) },
		Holds:          allocation.New(conn).Holds,
		Reservations:   reservation.New(conn).Reservations,
		Resources:      resource.New(conn).Resources,
		Calendars:      calendar.New(conn).Calendars,
		Events:         event.New(conn).Events,
		Availabilities: availability.New(conn).Availabilities,
	}
}
