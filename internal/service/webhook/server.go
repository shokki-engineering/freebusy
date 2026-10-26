// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

// server.go is WebhookService on the server protoc-gen-service generates:
// the standard methods, their checks and errors are generated; what is here
// is what makes a webhook more than a record -- the endpoint and event types
// it may name, the signing secret minted for it and returned only twice, and
// the deliveries a delete takes with it -- and the custom methods.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/webhook/v1/webhookpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"github.com/the-protobuf-project/runtime-go/events"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"github.com/the-protobuf-project/runtime-go/telemetry"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const service = "WebhookService"

// Options tune delivery. The zero value of each field is its default.
type Options struct {
	// AllowInsecureEndpoints accepts http:// endpoints on loopback hosts,
	// for development against a local receiver.
	AllowInsecureEndpoints bool
	// AllowPrivateNetworks lets endpoints reach loopback, private and
	// link-local addresses -- a receiver on this machine or inside this
	// network. Off, the default client refuses to connect to them.
	AllowPrivateNetworks bool
	// Retention is how long finished deliveries are kept. Default 30 days.
	Retention time.Duration
	// MaxAttempts before a delivery is FAILED. Default 8: with the backoff,
	// about a day and a half of trying.
	MaxAttempts int
	// Lag is how far back each dispatch round rereads, so a write that
	// committed after a later one was read is still seen. Default 30 seconds.
	Lag time.Duration
	// HookSettle is how far behind its read of the webhooks a delivery round
	// stops taking changes. Default 1 second; a test driving rounds by hand
	// right after a write sets it tiny.
	HookSettle time.Duration
	// Client sends deliveries. Default events.NewClient(AllowPrivateNetworks).
	Client *http.Client
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Sealer encrypts signing secrets at rest ([webhooks] secret_key). Nil
	// stores them as they are.
	Sealer *events.Sealer
	// Meter receives delivery's metrics (freebusy.webhook.*). Nil drops them.
	Meter telemetry.Meter
}

// Server serves WebhookService and runs delivery (see Round).
type Server struct {
	*webhookpbv1svc.WebhookServiceServer
	repos      Repositories
	opts       Options
	dispatcher *events.Dispatcher
}

// NewServer returns a Server over repos.
func NewServer(repos Repositories, opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Server{repos: repos, opts: opts}
	s.WebhookServiceServer = webhookpbv1svc.NewWebhookServiceServer(webhookpbv1svc.WebhookServiceRepositories{
		Webhooks: repos.Webhooks, Deliveries: repos.Deliveries,
	}, hooks{s}, rpc.ServerOptions()...)
	s.dispatcher = events.NewDispatcher(&webhookpbv1svc.EventStore{
		Webhooks: repos.Webhooks, Deliveries: repos.Deliveries, Cursors: repos.Cursors, PageSize: 1000,
	}, feeds(repos), events.Options{
		Source:               "//freebusy.shokki.engineering",
		UserAgent:            "freebusy-webhooks/1.0",
		MetricPrefix:         "freebusy.webhook",
		AllowPrivateNetworks: opts.AllowPrivateNetworks,
		Retention:            opts.Retention,
		MaxAttempts:          opts.MaxAttempts,
		Lag:                  opts.Lag,
		HookSettle:           opts.HookSettle,
		Client:               opts.Client,
		Now:                  opts.Now,
		Sealer:               opts.Sealer,
		Meter:                opts.Meter,
	})
	return s
}

// Round records every new change for the webhooks that want it, sends every
// delivery that is due, and prunes old finished ones (events.Dispatcher).
func (s *Server) Round(ctx context.Context) error { return s.dispatcher.Round(ctx) }

// Start runs Round every interval until ctx ends.
func (s *Server) Start(ctx context.Context, interval time.Duration, logErr func(error)) {
	s.dispatcher.Start(ctx, interval, logErr)
}

// secretKey carries a minted secret from CreateWebhook to its hooks.
type secretKey struct{}

// CreateWebhook creates the webhook with a fresh signing secret, which this
// response -- and RotateWebhookSecret's -- alone carries in the clear.
func (s *Server) CreateWebhook(ctx context.Context, req *webhookpbv1.CreateWebhookRequest) (*webhookpbv1.Webhook, error) {
	secret, err := events.NewSecret()
	if err != nil {
		return nil, rpc.ToStatusErr(err)
	}
	out, err := s.WebhookServiceServer.CreateWebhook(context.WithValue(ctx, secretKey{}, secret), req)
	if out != nil {
		out.Secret, out.PreviousSecret = secret, ""
	}
	return out, err
}

func (s *Server) GetWebhook(ctx context.Context, req *webhookpbv1.GetWebhookRequest) (*webhookpbv1.Webhook, error) {
	out, err := s.WebhookServiceServer.GetWebhook(ctx, req)
	return redact(out), err
}

func (s *Server) ListWebhooks(ctx context.Context, req *webhookpbv1.ListWebhooksRequest) (*webhookpbv1.ListWebhooksResponse, error) {
	out, err := s.WebhookServiceServer.ListWebhooks(ctx, req)
	for _, w := range out.GetWebhooks() {
		redact(w)
	}
	return out, err
}

func (s *Server) UpdateWebhook(ctx context.Context, req *webhookpbv1.UpdateWebhookRequest) (*webhookpbv1.Webhook, error) {
	out, err := s.WebhookServiceServer.UpdateWebhook(ctx, req)
	return redact(out), err
}

// ListDeliveries lists a webhook's deliveries, newest first unless order_by
// says otherwise.
func (s *Server) ListDeliveries(ctx context.Context, req *webhookpbv1.ListDeliveriesRequest) (*webhookpbv1.ListDeliveriesResponse, error) {
	if req.GetOrderBy() == "" {
		req = proto.Clone(req).(*webhookpbv1.ListDeliveriesRequest)
		req.OrderBy = "create_time desc"
	}
	return s.WebhookServiceServer.ListDeliveries(ctx, req)
}

func (s *Server) PauseWebhook(ctx context.Context, req *webhookpbv1.PauseWebhookRequest) (*webhookpbv1.Webhook, error) {
	return s.setState(ctx, "PauseWebhook", req.GetName(), webhookpbv1.WebhookState_WEBHOOK_STATE_PAUSED)
}

func (s *Server) ResumeWebhook(ctx context.Context, req *webhookpbv1.ResumeWebhookRequest) (*webhookpbv1.Webhook, error) {
	return s.setState(ctx, "ResumeWebhook", req.GetName(), webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE)
}

// setState moves a webhook to state; already there, it changes nothing.
func (s *Server) setState(ctx context.Context, method, name string, state webhookpbv1.WebhookState) (*webhookpbv1.Webhook, error) {
	var out *webhookpbv1.Webhook
	err := s.Options().Run(ctx, service, method, func(ctx context.Context) (err error) {
		if out, err = s.repos.Webhooks.Get(ctx, name); err != nil || active(out) == (state == webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE) {
			return err
		}
		out.State = state
		out, err = s.repos.Webhooks.UpdateOutputOnly(ctx, out, []string{"state"})
		return err
	})
	return redact(out), err
}

// RotateWebhookSecret mints a new signing secret; the one it replaces goes on
// signing for a day (events.Options.RotationGrace).
func (s *Server) RotateWebhookSecret(ctx context.Context, req *webhookpbv1.RotateWebhookSecretRequest) (*webhookpbv1.Webhook, error) {
	var out *webhookpbv1.Webhook
	err := s.Options().Run(ctx, service, "RotateWebhookSecret", func(ctx context.Context) (err error) {
		if out, err = s.repos.Webhooks.Get(ctx, req.GetName()); err != nil {
			return err
		}
		secret, err := events.NewSecret()
		if err != nil {
			return err
		}
		// The replaced secret is resealed under the current key, so one stored
		// in the clear, or under an older key, is not kept that way.
		prev, err := s.opts.Sealer.Open(out.GetSecret())
		if err != nil {
			return err
		}
		if out.PreviousSecret, err = s.opts.Sealer.Seal(prev); err != nil {
			return err
		}
		if out.Secret, err = s.opts.Sealer.Seal(secret); err != nil {
			return err
		}
		out.RotateTime = timestamppb.New(s.opts.Now())
		if out, err = s.repos.Webhooks.UpdateOutputOnly(ctx, out, []string{"secret", "previous_secret", "rotate_time"}); err != nil {
			return err
		}
		out.Secret = secret
		return nil
	})
	if out != nil {
		out.PreviousSecret = ""
	}
	return out, err
}

// RetryDelivery sends a delivery again from its first attempt, now.
func (s *Server) RetryDelivery(ctx context.Context, req *webhookpbv1.RetryDeliveryRequest) (*webhookpbv1.Delivery, error) {
	var out *webhookpbv1.Delivery
	err := s.Options().Run(ctx, service, "RetryDelivery", func(ctx context.Context) (err error) {
		if out, err = s.repos.Deliveries.Get(ctx, req.GetName()); err != nil {
			return err
		}
		out.State = webhookpbv1.DeliveryState_DELIVERY_STATE_PENDING
		out.AttemptCount, out.NextAttemptTime = 0, timestamppb.New(s.opts.Now())
		out, err = s.repos.Deliveries.UpdateOutputOnly(ctx, out, []string{"state", "attempt_count", "next_attempt_time"})
		return err
	})
	return out, err
}

// hooks are WebhookService's business logic around the generated standard
// methods.
type hooks struct {
	s *Server
}

var _ webhookpbv1svc.WebhookServiceHooks = hooks{}

// ValidateWebhook checks the webhook as it will be -- on an update, the
// stored one with the fields sent -- against the endpoints this server sends
// to and the event types that exist.
func (h hooks) ValidateWebhook(ctx context.Context, op servicex.Op, m *webhookpbv1.Webhook, paths []string) error {
	next := m
	if op == servicex.OpUpdate {
		cur, err := h.s.repos.Webhooks.Get(ctx, m.GetName())
		if err != nil {
			return err
		}
		next = proto.Clone(cur).(*webhookpbv1.Webhook)
		fields := next.ProtoReflect().Descriptor().Fields()
		for _, p := range paths {
			if fd := fields.ByName(protoreflect.Name(p)); fd != nil {
				next.ProtoReflect().Set(fd, m.ProtoReflect().Get(fd))
			}
		}
		if len(paths) == 0 { // "*": every field the update may change
			for _, p := range []string{"display_name", "uri", "event_types"} {
				fd := fields.ByName(protoreflect.Name(p))
				next.ProtoReflect().Set(fd, m.ProtoReflect().Get(fd))
			}
		}
	}
	return h.s.check(next)
}

// BeforeCreateWebhook stores the secret CreateWebhook minted, sealed, and
// starts the webhook active.
func (h hooks) BeforeCreateWebhook(ctx context.Context, _ string, m *webhookpbv1.Webhook) error {
	secret, _ := ctx.Value(secretKey{}).(string)
	if secret == "" {
		var err error
		if secret, err = events.NewSecret(); err != nil {
			return err
		}
	}
	sealed, err := h.s.opts.Sealer.Seal(secret)
	if err != nil {
		return err
	}
	m.State, m.Secret = webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE, sealed
	return nil
}

func (hooks) AfterCreateWebhook(context.Context, *webhookpbv1.Webhook) error { return nil }

func (hooks) BeforeUpdateWebhook(context.Context, *webhookpbv1.Webhook, []string) error { return nil }

func (hooks) AfterUpdateWebhook(context.Context, *webhookpbv1.Webhook) error { return nil }

// BeforeDeleteWebhook refuses a webhook with deliveries on record, or with
// force deletes them first.
func (h hooks) BeforeDeleteWebhook(ctx context.Context, name string, force bool) error {
	b := h.s.repos.NewBatch()
	n := 0
	for token := ""; ; {
		page, next, err := h.s.repos.Deliveries.List(ctx, name, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return err
		}
		if len(page) > 0 && !force {
			return servicex.Blocked("WEBHOOK_HAS_DELIVERIES", hints.TypeOf(name), name,
				fmt.Sprintf("webhook %s still has deliveries on record", name),
				"Send force=true to delete them with it, or pause it with "+
					hints.Call("PauseWebhook", map[string]string{"name": name})+" to stop sending and keep the record")
		}
		for _, d := range page {
			b.DeleteDelivery(d.GetName())
			n++
		}
		if token = next; token == "" {
			break
		}
	}
	if n == 0 {
		return nil
	}
	return b.Commit(ctx)
}

func (hooks) AfterDeleteWebhook(context.Context, string) error { return nil }

func (hooks) ParentCheckDelivery(context.Context, servicex.Op, string) error { return nil }

// check validates what a client chose: an endpoint this server will send
// to, and event types that exist.
func (s *Server) check(w *webhookpbv1.Webhook) error {
	err := events.CheckEndpoint(w.GetUri(), events.EndpointPolicy{
		AllowInsecure: s.opts.AllowInsecureEndpoints, AllowPrivateNetworks: s.opts.AllowPrivateNetworks,
	})
	if e, ok := err.(*events.EndpointError); ok {
		return servicex.BadField(e.Reason, "webhook.uri", e.What, e.Fix)
	}
	if err != nil {
		return err
	}
	known := EventTypes()
	for _, t := range w.GetEventTypes() {
		if !slices.Contains(known, t) {
			return servicex.BadField("UNKNOWN_EVENT_TYPE", "webhook.event_types",
				fmt.Sprintf("event type %q does not exist", t),
				fmt.Sprintf("Choose from %v, or leave event_types empty to receive every type", known))
		}
	}
	return nil
}

// redact clears the secrets, which only CreateWebhook and
// RotateWebhookSecret return -- and the previous one, never.
func redact(w *webhookpbv1.Webhook) *webhookpbv1.Webhook {
	if w != nil {
		w.Secret, w.PreviousSecret = "", ""
	}
	return w
}

// active reports whether w is sent changes; unset reads as ACTIVE.
func active(w *webhookpbv1.Webhook) bool {
	return w.GetState() != webhookpbv1.WebhookState_WEBHOOK_STATE_PAUSED
}
