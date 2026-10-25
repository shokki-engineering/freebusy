// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

// hooks.go is freebusy's business logic around the generated standard
// methods: a site belongs to a live organization, a resource to a live site,
// a calendar's children to a live calendar, and nothing is deleted out from
// under what it holds.

import (
	"context"
	"fmt"

	organizational_unitv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc4519/organization/v1/organizationv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc4519/organizational_unit/v1/organizational_unitv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/calendar/v1/calendarv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc5545/event/v1/eventv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc7953/availability/v1/availabilityv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/protobuf/rfc9073/resource/v1/resourcev1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
)

type orgHooks struct {
	organizationv1svc.NopOrganizationsHooks
	repos Repositories
}

// BeforeDeleteOrganization refuses an organization that still has sites.
func (h orgHooks) BeforeDeleteOrganization(ctx context.Context, name string, _ bool) error {
	sites, _, err := h.repos.OrganizationalUnits.List(ctx, repox.ListInput{
		PageSize: 1, Where: []filterx.Condition{{Field: "organization", Op: filterx.OpEq, Value: name}},
	})
	if err != nil || len(sites) == 0 {
		return err
	}
	return servicex.Blocked("ORGANIZATION_HAS_SITES", hints.TypeOf(name), name,
		fmt.Sprintf("organization %q still has organizational units, %s among them, and deleting it would leave them serving nothing", name, sites[0].GetName()),
		"Delete each with "+hints.Call("DeleteOrganizationalUnit", map[string]string{"name": sites[0].GetName()})+
			", or move it to another organization with "+hints.Call("UpdateOrganizationalUnit", map[string]string{"organizational_unit.name": sites[0].GetName()})+
			"; list them with "+hints.Call("ListOrganizationalUnits", nil)+" and filter organization = \""+name+"\"")
}

type siteHooks struct {
	organizational_unitv1svc.NopOrganizationalUnitsHooks
	repos Repositories
	sites *Sites
}

// ValidateOrganizationalUnit checks the organization a site names when the
// write sets it: required, shaped like an organization name, and live.
func (h siteHooks) ValidateOrganizationalUnit(ctx context.Context, _ servicex.Op, m *organizational_unitv1.OrganizationalUnit, paths []string) error {
	if !servicex.Touches(paths, "organization") {
		return nil
	}
	const field = "organizational_unit.organization"
	org := m.GetOrganization()
	if org == "" {
		return servicex.BadField("ORGANIZATION_REQUIRED", field,
			"an organizational unit belongs to an organization, and this request names none",
			"Create the organization first with "+hints.Call("CreateOrganization", nil)+
				", then send its name here, e.g. organization: \"organizations/acme\"; "+
				"to use one that exists, list them with "+hints.Call("ListOrganizations", nil))
	}
	if err := checkName(field, org, organizationsCollection, "organizations/acme"); err != nil {
		return err
	}
	return inField(h.sites.Organization(ctx, org), field)
}

// BeforeDeleteOrganizationalUnit refuses a site that still has resources.
func (h siteHooks) BeforeDeleteOrganizationalUnit(ctx context.Context, name string, _ bool) error {
	held, _, err := h.repos.Resources.List(ctx, repox.ListInput{
		PageSize: 1, Where: []filterx.Condition{{Field: "organizational_unit", Op: filterx.OpEq, Value: name}},
	})
	if err != nil || len(held) == 0 {
		return err
	}
	return servicex.Blocked("SITE_HAS_RESOURCES", hints.TypeOf(name), name,
		fmt.Sprintf("organizational unit %q still has resources, %s among them, and deleting it would leave them unbookable", name, held[0].GetName()),
		"Delete each with "+hints.Call("DeleteResource", map[string]string{"name": held[0].GetName()})+
			", or move it to another site with "+hints.Call("UpdateResource", map[string]string{"resource.name": held[0].GetName()})+
			"; list them with "+hints.Call("ListResources", nil)+" and filter organizational_unit = \""+name+"\"")
}

// BeforeUndeleteOrganizationalUnit refuses to restore a site under an
// organization that is not live.
func (h siteHooks) BeforeUndeleteOrganizationalUnit(ctx context.Context, name string) error {
	ou, err := h.repos.OrganizationalUnits.Get(ctx, name)
	if err != nil {
		return err
	}
	org := ou.GetOrganization()
	if org == "" || h.sites.Organization(ctx, org) == nil {
		return nil
	}
	return servicex.Blocked("ORGANIZATION_DELETED", hints.TypeOf(name), name,
		fmt.Sprintf("organizational unit %s belongs to %s, which is not live, and a restored site would still answer nothing", name, org),
		"Restore the organization first with "+hints.Call("UndeleteOrganization", map[string]string{"name": org})+
			", or move the site to a live organization once restored")
}

type resourceHooks struct {
	resourcev1svc.NopResourcesHooks
	sites *Sites
}

// ValidateResource checks the site a resource names when the write sets it.
func (h resourceHooks) ValidateResource(ctx context.Context, _ servicex.Op, m *resourcev1.Resource, paths []string) error {
	if !servicex.Touches(paths, "organizational_unit") {
		return nil
	}
	const field = "resource.organizational_unit"
	site := m.GetOrganizationalUnit()
	if site == "" {
		return servicex.BadField("SITE_REQUIRED", field,
			"a resource is booked at a site, and this request names none",
			"Send the organizational unit it belongs to, e.g. organizational_unit: \"organizationalUnits/riverside\"; "+
				"list sites with "+hints.Call("ListOrganizationalUnits", nil)+
				", or create one (under an organization) with "+hints.Call("CreateOrganizationalUnit", nil))
	}
	if err := checkName(field, site, organizationalUnitsCollection, "organizationalUnits/riverside"); err != nil {
		return err
	}
	return inField(h.sites.Site(ctx, site), field)
}

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
