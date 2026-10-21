// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

import (
	"encoding/json"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBackoff(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(40); got != 6*time.Hour {
		t.Errorf("backoff(40) = %v, want the 6h cap", got)
	}
}

func TestEventTypes(t *testing.T) {
	types := EventTypes()
	for _, want := range []string{"freebusy.hold.expired", "freebusy.reservation.confirmed", "freebusy.reservation.rescheduled"} {
		if !slices.Contains(types, want) {
			t.Errorf("%s missing from %v", want, types)
		}
	}
	if slices.ContainsFunc(types, func(s string) bool { return s == "freebusy.hold.unspecified" }) {
		t.Error("an UNSPECIFIED state is offered as an event type")
	}
}

func TestWants(t *testing.T) {
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	c := change{name: "organizationalUnits/club/reservations/r1", kind: "reservation", state: "confirmed", updated: created.Add(time.Hour), site: "organizationalUnits/club"}
	w := func(types []string, ou string) *webhookpbv1.Webhook {
		return &webhookpbv1.Webhook{EventTypes: types, OrganizationalUnit: ou, CreateTime: timestamppb.New(created)}
	}
	cases := []struct {
		hook *webhookpbv1.Webhook
		want bool
	}{
		{w(nil, ""), true},
		{w([]string{"freebusy.reservation.confirmed"}, ""), true},
		{w([]string{"freebusy.reservation.cancelled"}, ""), false},
		{w(nil, "organizationalUnits/club"), true},
		{w(nil, "organizationalUnits/clu"), false},
		{&webhookpbv1.Webhook{CreateTime: timestamppb.New(created.Add(2 * time.Hour))}, false},
	}
	for i, tc := range cases {
		if got := wants(tc.hook, c); got != tc.want {
			t.Errorf("case %d: wants = %v, want %v", i, got, tc.want)
		}
	}
}

func TestPayloadIsACloudEvent(t *testing.T) {
	r := &reservationpbv1.Reservation{Name: "organizationalUnits/club/reservations/r1", State: reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED}
	c := change{record: r, name: r.GetName(), etag: "e1", kind: "reservation", state: "confirmed", updated: time.Unix(1_800_000_000, 0)}
	body, err := c.payload()
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["specversion"] != "1.0" || ev["id"] != c.eventID() || ev["type"] != "freebusy.reservation.confirmed" ||
		ev["subject"] != r.GetName() || ev["source"] != source || ev["time"] != "2027-01-15T08:00:00Z" {
		t.Fatalf("event %v", ev)
	}
	if data, _ := ev["data"].(map[string]any); data["state"] != "RESERVATION_STATE_CONFIRMED" {
		t.Fatalf("data %v", ev["data"])
	}
	other := c
	other.etag = "e2"
	if other.eventID() == c.eventID() {
		t.Fatal("two versions of a record share an event id")
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "100.64.0.1": false, "0.0.0.0": false, "fd00::1": false,
	} {
		if got := public(net.ParseIP(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestLifecycleAndSite(t *testing.T) {
	at := timestamppb.New(time.Unix(1_800_000_000, 0))
	later := timestamppb.New(time.Unix(1_800_000_060, 0))
	if got := lifecycle(at, at, nil); got != "created" {
		t.Errorf("unchanged since creation: %s", got)
	}
	if got := lifecycle(at, later, nil); got != "updated" {
		t.Errorf("changed: %s", got)
	}
	if got := lifecycle(at, later, later); got != "deleted" {
		t.Errorf("soft-deleted: %s", got)
	}
	if got := siteOf("organizationalUnits/club/holds/h1"); got != "organizationalUnits/club" {
		t.Errorf("siteOf = %q", got)
	}
}
