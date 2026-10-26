// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"slices"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The event types a webhook may ask for are the ones freebusy has always
// sent: every hold and reservation state, freebusy.reservation.updated, and
// the inventory's lifecycle.
func TestEventTypes(t *testing.T) {
	want := []string{"freebusy.reservation.updated"}
	for _, s := range []string{"active", "released", "expired", "converted"} {
		want = append(want, "freebusy.hold."+s)
	}
	for _, s := range []string{"pending", "confirmed", "cancelling", "cancelled", "failed", "rescheduling", "rescheduled"} {
		want = append(want, "freebusy.reservation."+s)
	}
	for _, k := range []string{"resource", "calendar", "event", "availability"} {
		for _, l := range []string{"created", "updated", "deleted"} {
			want = append(want, "freebusy."+k+"."+l)
		}
	}
	slices.Sort(want)
	if got := EventTypes(); !slices.Equal(got, want) {
		t.Fatalf("EventTypes() = %v\nwant %v", got, want)
	}
}

// A reservation's event is its state, except after UpdateReservation, whose
// write says so in last_change: then it is "updated", whatever the times say.
func TestReservationChange(t *testing.T) {
	at := func(ms int) *timestamppb.Timestamp {
		return timestamppb.New(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC).Add(time.Duration(ms) * time.Millisecond))
	}
	confirmed := reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED
	stateChange, details := reservationpbv1.Reservation_CHANGE_STATE, reservationpbv1.Reservation_CHANGE_DETAILS
	spec := reservationFeed(nil)
	if spec.ID != "reservations" || !slices.Contains(spec.ReadMask, "last_change") {
		t.Fatalf("feed %q reads %v", spec.ID, spec.ReadMask)
	}
	for _, c := range []struct {
		state  reservationpbv1.ReservationState
		last   reservationpbv1.Reservation_Change
		update *timestamppb.Timestamp
		want   string
	}{
		{confirmed, stateChange, at(40), "freebusy.reservation.confirmed"},
		{confirmed, details, at(10), "freebusy.reservation.updated"},
		{confirmed, details, at(5000), "freebusy.reservation.updated"},
		{confirmed, reservationpbv1.Reservation_CHANGE_UNSPECIFIED, at(5000), "freebusy.reservation.confirmed"},
		{reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED, stateChange, at(5000), "freebusy.reservation.cancelled"},
		{reservationpbv1.ReservationState_RESERVATION_STATE_RESCHEDULED, stateChange, at(5000), "freebusy.reservation.rescheduled"},
	} {
		r := &reservationpbv1.Reservation{Name: "reservations/r1", State: c.state, LastChange: c.last, ConfirmTime: at(0), UpdateTime: c.update}
		got := spec.Change(r)
		if got.Type != c.want || got.Scope != "" {
			t.Errorf("Change(%v, %v) = %q scope %q, want %q unscoped", c.state, c.last, got.Type, got.Scope, c.want)
		}
	}
}
