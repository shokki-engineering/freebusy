// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"

	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// acrossSites, acrossCalendars and acrossWebhooks are AIP-159 wildcard
	// parents.
	acrossSites     = "organizationalUnits/-"
	acrossCalendars = "calendars/-"
	acrossWebhooks  = "webhooks/-"
	// feedPage and sendBatch bound one read of a feed and of due deliveries.
	feedPage  = 500
	sendBatch = 100
	// sendParallel bounds concurrent POSTs, so one slow endpoint does not
	// hold up the rest.
	sendParallel = 8
	// claimFor is how long a claimed delivery is left to its sender before
	// another server may attempt it: longer than any one attempt takes.
	claimFor = time.Minute
	// rotationGrace is how long a replaced secret goes on signing.
	rotationGrace = 24 * time.Hour
	// prunePage bounds the finished deliveries one round deletes.
	prunePage = 500
)

// Start runs Round every interval until ctx ends.
func (s *Server) Start(ctx context.Context, interval time.Duration, logErr func(error)) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if err := s.Round(ctx); err != nil && logErr != nil && ctx.Err() == nil {
				logErr(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Round records every new change for the webhooks that want it, then sends
// every delivery that is due. Safe to run on several servers at once: a
// change recorded twice collides on its delivery's derived id, and a delivery
// is claimed before it is sent.
func (s *Server) Round(ctx context.Context) error {
	if err := s.dispatch(ctx); err != nil {
		return fmt.Errorf("webhooks: dispatch: %w", err)
	}
	if err := s.send(ctx); err != nil {
		return fmt.Errorf("webhooks: send: %w", err)
	}
	if err := s.prune(ctx); err != nil {
		return fmt.Errorf("webhooks: prune: %w", err)
	}
	return nil
}

// prune deletes finished deliveries -- SUCCEEDED or FAILED -- last changed
// more than Retention ago, a page a round.
func (s *Server) prune(ctx context.Context) error {
	old, _, err := s.repos.Deliveries.List(ctx, acrossWebhooks, repox.ListInput{
		PageSize: prunePage,
		Where: []filterx.Condition{
			filterx.In("state", "SUCCEEDED", "FAILED"),
			{Field: "update_time", Op: filterx.OpLt, Value: s.opts.Now().Add(-s.opts.Retention).UTC().Format(time.RFC3339Nano)},
		},
	})
	if err != nil || len(old) == 0 {
		return err
	}
	b := s.repos.NewBatch()
	for _, d := range old {
		b.DeleteDelivery(d.GetName())
	}
	return b.Commit(ctx)
}

// feed is one kind of record whose changes are sent.
type feed struct {
	// id names the feed's cursor, feedCursors/{id}.
	id   string
	read func(ctx context.Context, in repox.ListInput) ([]change, string, error)
}

func (s *Server) feeds() []feed {
	return []feed{
		feedOf("holds", func(ctx context.Context, in repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
			return s.repos.Holds.List(ctx, acrossSites, in)
		}, func(h *allocationpbv1.Hold) change {
			return change{kind: "hold", state: stateName(h.GetState().String(), "HOLD_STATE_"), updated: h.GetUpdateTime().AsTime(), site: siteOf(h.GetName())}
		}),
		feedOf("reservations", func(ctx context.Context, in repox.ListInput) ([]*reservationpbv1.Reservation, string, error) {
			return s.repos.Reservations.List(ctx, acrossSites, in)
		}, func(r *reservationpbv1.Reservation) change {
			return change{kind: "reservation", state: stateName(r.GetState().String(), "RESERVATION_STATE_"), updated: r.GetUpdateTime().AsTime(), site: siteOf(r.GetName())}
		}),
		// Inventory is soft-deleted (AIP-164), so deleted records are read too:
		// a delete is a change like any other.
		feedOf("resources", func(ctx context.Context, in repox.ListInput) ([]*resourcev1.Resource, string, error) {
			in.ShowDeleted = true
			return s.repos.Resources.List(ctx, in)
		}, func(r *resourcev1.Resource) change {
			return change{kind: "resource", state: lifecycle(r.GetCreateTime(), r.GetUpdateTime(), r.GetDeleteTime()), updated: r.GetUpdateTime().AsTime(), site: r.GetOrganizationalUnit()}
		}),
		feedOf("calendars", func(ctx context.Context, in repox.ListInput) ([]*calendarv1.Calendar, string, error) {
			in.ShowDeleted = true
			return s.repos.Calendars.List(ctx, in)
		}, func(c *calendarv1.Calendar) change {
			return change{kind: "calendar", state: lifecycle(c.GetCreateTime(), c.GetUpdateTime(), c.GetDeleteTime()), updated: c.GetUpdateTime().AsTime()}
		}),
		feedOf("events", func(ctx context.Context, in repox.ListInput) ([]*eventv1.Event, string, error) {
			in.ShowDeleted = true
			return s.repos.Events.List(ctx, acrossCalendars, in)
		}, func(e *eventv1.Event) change {
			return change{kind: "event", state: lifecycle(e.GetCreateTime(), e.GetUpdateTime(), e.GetDeleteTime()), updated: e.GetUpdateTime().AsTime()}
		}),
		feedOf("availabilities", func(ctx context.Context, in repox.ListInput) ([]*availabilityv1.Availability, string, error) {
			in.ShowDeleted = true
			return s.repos.Availabilities.List(ctx, acrossCalendars, in)
		}, func(a *availabilityv1.Availability) change {
			return change{kind: "availability", state: lifecycle(a.GetCreateTime(), a.GetUpdateTime(), a.GetDeleteTime()), updated: a.GetUpdateTime().AsTime()}
		}),
	}
}

// record is what every feed's records have: a name and an etag.
type record interface {
	proto.Message
	GetName() string
	GetEtag() string
}

// feedOf is a feed over list, each record read into a change by read.
func feedOf[R record](id string, list func(context.Context, repox.ListInput) ([]R, string, error), read func(R) change) feed {
	return feed{id: id, read: func(ctx context.Context, in repox.ListInput) ([]change, string, error) {
		page, next, err := list(ctx, in)
		out := make([]change, len(page))
		for i, r := range page {
			c := read(r)
			c.record, c.name, c.etag = r, r.GetName(), r.GetEtag()
			out[i] = c
		}
		return out, next, err
	}}
}

// dispatch reads each feed from its cursor, less Lag, and records what it
// finds for the active webhooks that want it.
func (s *Server) dispatch(ctx context.Context) error {
	var hooks []*webhookpbv1.Webhook
	for token := ""; ; {
		page, next, err := s.repos.Webhooks.List(ctx, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return err
		}
		for _, w := range page {
			if active(w) {
				hooks = append(hooks, w)
			}
		}
		if token = next; token == "" {
			break
		}
	}
	for _, f := range s.feeds() {
		if err := s.dispatchFeed(ctx, f, hooks); err != nil {
			return fmt.Errorf("%s: %w", f.id, err)
		}
	}
	return nil
}

func (s *Server) dispatchFeed(ctx context.Context, f feed, hooks []*webhookpbv1.Webhook) error {
	cur, err := s.cursor(ctx, f.id)
	if err != nil {
		return err
	}
	pos := cur.GetPositionTime().AsTime()
	latest := pos
	since := pos.Add(-s.opts.Lag).UTC().Format(time.RFC3339Nano)
	for token := ""; ; {
		page, next, err := f.read(ctx, repox.ListInput{
			PageSize: feedPage, PageToken: token, OrderBy: "update_time",
			Where: []filterx.Condition{{Field: "update_time", Op: filterx.OpGte, Value: since}},
		})
		if err != nil {
			return err
		}
		for _, c := range page {
			if c.state == "unspecified" || s.seen(c) {
				continue
			}
			if err := s.record(ctx, c, hooks); err != nil {
				return err
			}
			if c.updated.After(latest) {
				latest = c.updated
			}
		}
		if token = next; token == "" {
			break
		}
	}
	s.forget(latest.Add(-s.opts.Lag))
	if !latest.After(pos) {
		return nil
	}
	cur.PositionTime = timestamppb.New(latest)
	_, err = s.repos.Cursors.Update(ctx, cur, []string{"position_time"})
	if errors.Is(err, types.ErrConflict) {
		return nil // another server moved it; its position is as good
	}
	return err
}

// cursor returns a feed's cursor, starting one at now: a feed nobody has read
// begins with the changes after its first read, not with all of history.
func (s *Server) cursor(ctx context.Context, id string) (*webhookpbv1.FeedCursor, error) {
	name := "feedCursors/" + id
	cur, err := s.repos.Cursors.Get(ctx, name)
	if !errors.Is(err, types.ErrNotFound) {
		return cur, err
	}
	cur, err = s.repos.Cursors.Create(ctx, &webhookpbv1.FeedCursor{Name: name, PositionTime: timestamppb.New(s.opts.Now())})
	if errors.Is(err, types.ErrAlreadyExists) {
		return s.repos.Cursors.Get(ctx, name)
	}
	return cur, err
}

// record writes c as a PENDING delivery for every webhook that wants it.
func (s *Server) record(ctx context.Context, c change, hooks []*webhookpbv1.Webhook) error {
	var payload string
	for _, w := range hooks {
		if !wants(w, c) {
			continue
		}
		if payload == "" {
			var err error
			if payload, err = c.payload(); err != nil {
				return err
			}
		}
		_, err := s.repos.Deliveries.Create(ctx, w.GetName(), &webhookpbv1.Delivery{
			Name:            deliveryName(w, c),
			EventId:         c.eventID(),
			EventType:       c.eventType(),
			Subject:         c.name,
			Payload:         payload,
			State:           webhookpbv1.DeliveryState_DELIVERY_STATE_PENDING,
			NextAttemptTime: timestamppb.New(s.opts.Now()),
		})
		if err != nil && !errors.Is(err, types.ErrAlreadyExists) {
			return err
		}
	}
	s.mu.Lock()
	s.recorded[c.eventID()] = c.updated
	s.mu.Unlock()
	return nil
}

// seen reports whether this server recorded c already.
func (s *Server) seen(c change) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.recorded[c.eventID()]
	return ok
}

// forget drops what was recorded before cutoff, which no reread reaches.
func (s *Server) forget(cutoff time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range s.recorded {
		if at.Before(cutoff) {
			delete(s.recorded, id)
		}
	}
}

// send attempts every PENDING delivery that is due.
func (s *Server) send(ctx context.Context) error {
	now := s.opts.Now()
	due, _, err := s.repos.Deliveries.List(ctx, acrossWebhooks, repox.ListInput{
		PageSize: sendBatch, OrderBy: "next_attempt_time",
		Where: []filterx.Condition{
			{Field: "state", Op: filterx.OpEq, Value: "PENDING"},
			{Field: "next_attempt_time", Op: filterx.OpLte, Value: now.UTC().Format(time.RFC3339Nano)},
		},
	})
	if err != nil || len(due) == 0 {
		return err
	}
	hooks := map[string]*webhookpbv1.Webhook{}
	for _, d := range due {
		name := webhookOf(d.GetName())
		if _, ok := hooks[name]; ok {
			continue
		}
		w, err := s.repos.Webhooks.Get(ctx, name)
		if err != nil && !errors.Is(err, types.ErrNotFound) {
			return err
		}
		hooks[name] = w
	}

	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, sendParallel)
		mu    sync.Mutex
		errs  []error
	)
	for _, d := range due {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := s.attempt(ctx, d, hooks[webhookOf(d.GetName())]); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// attempt makes one attempt at d. It claims d first -- pushing its next
// attempt out, guarded by its etag -- so of several servers reading it due,
// one sends it.
func (s *Server) attempt(ctx context.Context, d *webhookpbv1.Delivery, w *webhookpbv1.Webhook) error {
	now := s.opts.Now()
	if w != nil && !active(w) {
		return nil // paused: it waits
	}
	d = proto.Clone(d).(*webhookpbv1.Delivery)
	d.NextAttemptTime = timestamppb.New(now.Add(claimFor))
	d, err := s.repos.Deliveries.UpdateOutputOnly(ctx, d, []string{"next_attempt_time"})
	if errors.Is(err, types.ErrConflict) {
		return nil // another server claimed it
	}
	if err != nil {
		return err
	}

	code, sendErr := 0, errors.New("the webhook was deleted")
	if w != nil {
		code, sendErr = s.post(ctx, w, d)
	}
	d.AttemptCount++
	d.ResponseCode = int32(code)
	paths := []string{"state", "attempt_count", "response_code", "error_message", "next_attempt_time"}
	switch {
	case sendErr == nil:
		d.State, d.ErrorMessage, d.NextAttemptTime = webhookpbv1.DeliveryState_DELIVERY_STATE_SUCCEEDED, "", nil
		d.DeliverTime = timestamppb.New(s.opts.Now())
		paths = append(paths, "deliver_time")
		s.metrics.attempt(ctx, "succeeded")
		if d.GetCreateTime() != nil {
			s.metrics.delivered(ctx, d.GetDeliverTime().AsTime().Sub(d.GetCreateTime().AsTime()))
		}
	case w == nil || int(d.AttemptCount) >= s.opts.MaxAttempts:
		d.State, d.ErrorMessage, d.NextAttemptTime = webhookpbv1.DeliveryState_DELIVERY_STATE_FAILED, sendErr.Error(), nil
		s.metrics.attempt(ctx, "failed")
	default:
		d.State, d.ErrorMessage = webhookpbv1.DeliveryState_DELIVERY_STATE_PENDING, sendErr.Error()
		d.NextAttemptTime = timestamppb.New(s.opts.Now().Add(backoff(int(d.AttemptCount))))
		s.metrics.attempt(ctx, "retrying")
	}
	_, err = s.repos.Deliveries.UpdateOutputOnly(ctx, d, paths)
	return err
}

// backoff is the wait after the nth failed attempt: 30 seconds, doubling,
// at most 6 hours.
func backoff(n int) time.Duration {
	wait := 30 * time.Second
	for i := 1; i < n && wait < 6*time.Hour; i++ {
		wait *= 2
	}
	return min(wait, 6*time.Hour)
}

// post sends d to w's endpoint, signed per the Standard Webhooks
// specification: webhook-signature is "v1," and the base64 HMAC-SHA256 of
// "<webhook-id>.<webhook-timestamp>.<body>" under the webhook's key -- and,
// for a day after a rotation, a second such signature under the key it
// replaced, space-separated. Any 2xx is acceptance; a redirect is not.
func (s *Server) post(ctx context.Context, w *webhookpbv1.Webhook, d *webhookpbv1.Delivery) (int, error) {
	body := []byte(d.GetPayload())
	now := s.opts.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	secrets := []string{w.GetSecret()}
	if prev := w.GetPreviousSecret(); prev != "" && now.Before(w.GetRotateTime().AsTime().Add(rotationGrace)) {
		secrets = append(secrets, prev)
	}
	var sigs []string
	for _, stored := range secrets {
		secret, err := s.opts.Sealer.Open(stored)
		if err != nil {
			return 0, err
		}
		sig, err := sign(secret, d.GetEventId()+"."+ts+".", body)
		if err != nil {
			return 0, err
		}
		sigs = append(sigs, sig)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.GetUri(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json; charset=utf-8")
	req.Header.Set("User-Agent", "freebusy-webhooks/1.0")
	req.Header.Set("webhook-id", d.GetEventId())
	req.Header.Set("webhook-timestamp", ts)
	req.Header.Set("webhook-signature", strings.Join(sigs, " "))
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("the endpoint answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// sign is one Standard Webhooks signature, "v1,<base64 HMAC-SHA256>", of
// prefix and body under secret.
func sign(secret, prefix string, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("the webhook's signing secret is unreadable")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(prefix))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// publicOnly refuses connections to anything but a public address: loopback,
// private, link-local, carrier-grade NAT and unspecified addresses are where
// an endpoint's URL would reach into this server's own network. It runs at
// connect time, on the address DNS actually returned, so a name that
// resolves somewhere private is caught however it is spelled.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !public(ip) {
		return fmt.Errorf("refusing to connect to %s: not a public address", host)
	}
	return nil
}

// cgnat is RFC 6598's shared address space, private in all but name.
var cgnat = &net.IPNet{IP: net.IP{100, 64, 0, 0}, Mask: net.CIDRMask(10, 32)}

func public(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified() && !cgnat.Contains(ip)
}

// webhookOf is the webhook a delivery belongs to.
// "webhooks/w/deliveries/d" -> "webhooks/w".
func webhookOf(delivery string) string {
	if i := strings.Index(delivery, "/deliveries/"); i >= 0 {
		return delivery[:i]
	}
	return delivery
}
