// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

// servers.go assembles the generated AIP servers (protoc-gen-service) with
// freebusy's hooks: the standard methods, their checks and their errors are
// generated; what is here is the tenancy rule and the methods that are more
// than CRUD.

import (
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc4519/organization/v1/organizationv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc4519/organizational_unit/v1/organizational_unitv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/calendar/v1/calendarv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/event/v1/eventv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc7953/availability/v1/availabilityv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc9073/resource/v1/resourcev1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
)

// opts are every server's options: freebusy's translator and tracing.
func opts() []servicex.Option { return rpc.ServerOptions() }

// OrganizationServer serves RFC 4519 organizations.
type OrganizationServer struct {
	*organizationv1svc.OrganizationsServer
}

// NewOrganizationServer returns an OrganizationServer over repos.
func NewOrganizationServer(repos Repositories) *OrganizationServer {
	return &OrganizationServer{organizationv1svc.NewOrganizationsServer(
		organizationv1svc.OrganizationsRepositories{Organizations: repos.Organizations}, orgHooks{repos: repos}, opts()...)}
}

// OrganizationalUnitServer serves RFC 4519 organizational units: sites.
type OrganizationalUnitServer struct {
	*organizational_unitv1svc.OrganizationalUnitsServer
}

// NewOrganizationalUnitServer returns an OrganizationalUnitServer over repos.
func NewOrganizationalUnitServer(repos Repositories) *OrganizationalUnitServer {
	return &OrganizationalUnitServer{organizational_unitv1svc.NewOrganizationalUnitsServer(
		organizational_unitv1svc.OrganizationalUnitsRepositories{OrganizationalUnits: repos.OrganizationalUnits},
		siteHooks{repos: repos, sites: NewSites(repos)}, opts()...)}
}

// ResourceServer serves RFC 9073 resources.
type ResourceServer struct{ *resourcev1svc.ResourcesServer }

// NewResourceServer returns a ResourceServer over repos.
func NewResourceServer(repos Repositories) *ResourceServer {
	return &ResourceServer{resourcev1svc.NewResourcesServer(
		resourcev1svc.ResourcesRepositories{Resources: repos.Resources}, resourceHooks{sites: NewSites(repos)}, opts()...)}
}

// CalendarServer serves RFC 5545 calendars and answers free/busy for them.
type CalendarServer struct {
	*calendarv1svc.CalendarsServer
	repos    Repositories
	freeBusy FreeBusyQuerier
}

// NewCalendarServer returns a CalendarServer over repos, answering
// QueryFreeBusy from fb.
func NewCalendarServer(repos Repositories, fb FreeBusyQuerier) *CalendarServer {
	return &CalendarServer{
		CalendarsServer: calendarv1svc.NewCalendarsServer(
			calendarv1svc.CalendarsRepositories{Calendars: repos.Calendars}, calendarHooks{repos: repos}, opts()...),
		repos: repos, freeBusy: fb,
	}
}

// AvailabilityServer serves RFC 7953 availability.
type AvailabilityServer struct {
	*availabilityv1svc.AvailabilitiesServer
}

// NewAvailabilityServer returns an AvailabilityServer over repos.
func NewAvailabilityServer(repos Repositories) *AvailabilityServer {
	return &AvailabilityServer{availabilityv1svc.NewAvailabilitiesServer(
		availabilityv1svc.AvailabilitiesRepositories{Availabilities: repos.Availabilities}, availabilityHooks{repos: repos}, opts()...)}
}

// EventServer serves RFC 5545 events.
type EventServer struct {
	*eventv1svc.EventsServer
	repos Repositories
}

// NewEventServer returns an EventServer over repos.
func NewEventServer(repos Repositories) *EventServer {
	return &EventServer{EventsServer: eventv1svc.NewEventsServer(
		eventv1svc.EventsRepositories{Events: repos.Events}, eventHooks{repos: repos}, opts()...), repos: repos}
}
