// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

// hooks.go is freebusy's business logic around the generated standard
// methods: a calendar's children belong to a live calendar, and a calendar is
// not deleted out from under what it holds.

import (
	"context"
	"fmt"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/calendar/v1/calendarv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/event/v1/eventv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc7953/availability/v1/availabilityv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
)

type calendarHooks struct {
	calendarv1svc.NopCalendarsHooks
	repos Repositories
}

// BeforeDeleteCalendar refuses a calendar with live events or availability,
// or with force soft-deletes them first.
func (h calendarHooks) BeforeDeleteCalendar(ctx context.Context, name string, force bool) error {
	for {
		events, _, err := h.repos.Events.List(ctx, name, repox.ListInput{PageSize: 1000})
		if err != nil {
			return err
		}
		avs, _, err := h.repos.Availabilities.List(ctx, name, repox.ListInput{PageSize: 1000})
		if err != nil {
			return err
		}
		if len(events) == 0 && len(avs) == 0 {
			return nil
		}
		if !force {
			return servicex.Blocked("CALENDAR_NOT_EMPTY", hints.TypeOf(name), name,
				fmt.Sprintf("calendar %s still has events or availability", name),
				"Send force=true to soft-delete them with it, or delete them first; UndeleteCalendar restores the calendar alone, so restore its events and availability one by one")
		}
		for _, e := range events {
			if err := h.repos.Events.Delete(ctx, e.GetName()); err != nil && !isNotFound(err) {
				return err
			}
		}
		for _, a := range avs {
			if err := h.repos.Availabilities.Delete(ctx, a.GetName()); err != nil && !isNotFound(err) {
				return err
			}
		}
	}
}

type availabilityHooks struct {
	availabilityv1svc.NopAvailabilitiesHooks
	repos Repositories
}

// ParentCheckAvailability refuses availability under a missing or deleted
// calendar.
func (h availabilityHooks) ParentCheckAvailability(ctx context.Context, op servicex.Op, parent string) error {
	if op == servicex.OpList {
		return nil
	}
	return liveParent(ctx, h.repos, parent)
}

type eventHooks struct {
	eventv1svc.NopEventsHooks
	repos Repositories
}

// ParentCheckEvent refuses events under a missing or deleted calendar.
func (h eventHooks) ParentCheckEvent(ctx context.Context, op servicex.Op, parent string) error {
	if op == servicex.OpList {
		return nil
	}
	return liveParent(ctx, h.repos, parent)
}
