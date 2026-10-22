// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/runtime/hints"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
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
	// committed after a later one was read is still seen. Default 2 minutes.
	Lag time.Duration
	// Client sends deliveries. Default: a client with a 10-second timeout
	// that follows no redirects and, unless AllowPrivateNetworks, connects
	// only to public addresses.
	Client *http.Client
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Sealer encrypts signing secrets at rest (sealing.go). Nil stores them
	// as they are.
	Sealer *Sealer
}

// Server serves WebhookService and runs delivery (see Round).
type Server struct {
	webhookpbv1.UnimplementedWebhookServiceServer
	repos Repositories
	opts  Options

	// recorded remembers the changes this server has recorded lately, so a
	// round's reread of the last Lag does not write them all again.
	mu       sync.Mutex
	recorded map[string]time.Time
}

// NewServer returns a Server over repos.
func NewServer(repos Repositories, opts Options) *Server {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 8
	}
	if opts.Lag <= 0 {
		opts.Lag = 2 * time.Minute
	}
	if opts.Retention <= 0 {
		opts.Retention = 30 * 24 * time.Hour
	}
	if opts.Client == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		if !opts.AllowPrivateNetworks {
			dialer.Control = publicOnly
		}
		opts.Client = &http.Client{
			Timeout: 10 * time.Second,
			// No proxy: one would make the connection, and the address check
			// would see the proxy instead of the endpoint.
			Transport:     &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Server{repos: repos, opts: opts, recorded: map[string]time.Time{}}
}

func (s *Server) CreateWebhook(ctx context.Context, req *webhookpbv1.CreateWebhookRequest) (*webhookpbv1.Webhook, error) {
	var out *webhookpbv1.Webhook
	err := rpc.Traced(ctx, service, "CreateWebhook", func(ctx context.Context) error {
		in := proto.Clone(req.GetWebhook()).(*webhookpbv1.Webhook)
		if err := s.check(in); err != nil {
			return rpc.ToStatusErr(err)
		}
		secret, err := newSecret()
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		in.Name = ""
		if id := req.GetWebhookId(); id != "" {
			in.Name = "webhooks/" + id
		}
		sealed, err := s.opts.Sealer.Seal(secret)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		in.State, in.Secret = webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE, sealed
		in.CreateTime, in.UpdateTime, in.Etag = nil, nil, ""
		in.PreviousSecret, in.RotateTime = "", nil
		if out, err = s.repos.Webhooks.Create(ctx, in); err != nil {
			return rpc.ToStatusErr(err)
		}
		// One of the two responses that carries the secret, in the clear.
		out.Secret = secret
		return nil
	})
	return out, err
}

func (s *Server) GetWebhook(ctx context.Context, req *webhookpbv1.GetWebhookRequest) (*webhookpbv1.Webhook, error) {
	var out *webhookpbv1.Webhook
	err := rpc.Traced(ctx, service, "GetWebhook", func(ctx context.Context) (err error) {
		out, err = s.repos.Webhooks.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return redact(out), err
}

func (s *Server) ListWebhooks(ctx context.Context, req *webhookpbv1.ListWebhooksRequest) (*webhookpbv1.ListWebhooksResponse, error) {
	out := &webhookpbv1.ListWebhooksResponse{}
	err := rpc.Traced(ctx, service, "ListWebhooks", func(ctx context.Context) (err error) {
		out.Webhooks, out.NextPageToken, err = s.repos.Webhooks.List(ctx, repox.ListInput{PageSize: req.GetPageSize(), PageToken: req.GetPageToken()})
		return rpc.ToStatusErr(err)
	})
	for _, w := range out.Webhooks {
		redact(w)
	}
	return out, err
}

// updatable are the fields UpdateWebhook may write; state has its own
// methods, and the secret is the server's.
var updatable = []string{"display_name", "uri", "event_types", "organizational_unit"}

func (s *Server) UpdateWebhook(ctx context.Context, req *webhookpbv1.UpdateWebhookRequest) (*webhookpbv1.Webhook, error) {
	var out *webhookpbv1.Webhook
	err := rpc.Traced(ctx, service, "UpdateWebhook", func(ctx context.Context) error {
		in := req.GetWebhook()
		paths, err := updatePaths(in, req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		cur, err := s.repos.Webhooks.Get(ctx, in.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		// Check the webhook as it will be, not just the fields sent.
		next := proto.Clone(cur).(*webhookpbv1.Webhook)
		for _, p := range paths {
			fd := next.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(p))
			next.ProtoReflect().Set(fd, in.ProtoReflect().Get(fd))
		}
		if err := s.check(next); err != nil {
			return rpc.ToStatusErr(err)
		}
		out, err = s.repos.Webhooks.Update(ctx, in, paths)
		return rpc.ToStatusErr(err)
	})
	return redact(out), err
}

// updatePaths resolves an AIP-134 update mask against the updatable fields:
// absent, the fields the caller populated; "*", all of them.
func updatePaths(in *webhookpbv1.Webhook, mask *fieldmaskpb.FieldMask) ([]string, error) {
	switch {
	case mask == nil:
		var paths []string
		for _, p := range updatable {
			if in.ProtoReflect().Has(in.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(p))) {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			return nil, types.BadField("NOTHING_TO_UPDATE", "update_mask",
				"the request changes nothing: it sets none of the fields a webhook update can change",
				fmt.Sprintf("Set one of %v and name it in update_mask", updatable))
		}
		return paths, nil
	case len(mask.GetPaths()) == 1 && mask.GetPaths()[0] == "*":
		return updatable, nil
	}
	// A path may be the field's name or, as REST clients send it, its JSON
	// name (AIP-161): "displayName" is display_name.
	fields := in.ProtoReflect().Descriptor().Fields()
	paths := make([]string, 0, len(mask.GetPaths()))
	for _, p := range mask.GetPaths() {
		fd := fields.ByName(protoreflect.Name(p))
		if fd == nil {
			fd = fields.ByJSONName(p)
		}
		if fd == nil || !slices.Contains(updatable, string(fd.Name())) {
			return nil, types.BadField("UNKNOWN_MASK_PATH", "update_mask",
				fmt.Sprintf("update_mask names %q, which a webhook update cannot change", p),
				fmt.Sprintf("Updatable fields are %v; the secret changes with RotateWebhookSecret", updatable))
		}
		paths = append(paths, string(fd.Name()))
	}
	return paths, nil
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
	err := rpc.Traced(ctx, service, method, func(ctx context.Context) (err error) {
		if out, err = s.repos.Webhooks.Get(ctx, name); err != nil || active(out) == (state == webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE) {
			return rpc.ToStatusErr(err)
		}
		out.State = state
		out, err = s.repos.Webhooks.UpdateOutputOnly(ctx, out, []string{"state"})
		return rpc.ToStatusErr(err)
	})
	return redact(out), err
}

func (s *Server) DeleteWebhook(ctx context.Context, req *webhookpbv1.DeleteWebhookRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, service, "DeleteWebhook", func(ctx context.Context) error {
		cur, err := s.repos.Webhooks.Get(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		if e := req.GetEtag(); e != "" && e != cur.GetEtag() {
			return rpc.ToStatusErr(&types.Problem{
				Kind: types.ErrConflict, Reason: rpc.ReasonConcurrentModification,
				ResourceType: hints.TypeOf(cur.GetName()), ResourceName: cur.GetName(),
				What: fmt.Sprintf("webhook %s changed after you read it: etag %q is no longer current", cur.GetName(), e),
				Fix:  "Read it again with " + hints.Call("GetWebhook", map[string]string{"name": cur.GetName()}) + " and retry with the new etag",
			})
		}
		b := s.repos.NewBatch()
		for token := ""; ; {
			page, next, err := s.repos.Deliveries.List(ctx, cur.GetName(), repox.ListInput{PageSize: 1000, PageToken: token})
			if err != nil {
				return rpc.ToStatusErr(err)
			}
			if len(page) > 0 && !req.GetForce() {
				return rpc.ToStatusErr(types.Blocked("WEBHOOK_HAS_DELIVERIES", hints.TypeOf(cur.GetName()), cur.GetName(),
					fmt.Sprintf("webhook %s still has deliveries on record", cur.GetName()),
					"Send force=true to delete them with it, or pause it with "+
						hints.Call("PauseWebhook", map[string]string{"name": cur.GetName()})+" to stop sending and keep the record"))
			}
			for _, d := range page {
				b.DeleteDelivery(d.GetName())
			}
			if token = next; token == "" {
				break
			}
		}
		b.DeleteWebhook(cur.GetName())
		return rpc.ToStatusErr(b.Commit(ctx))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) RotateWebhookSecret(ctx context.Context, req *webhookpbv1.RotateWebhookSecretRequest) (*webhookpbv1.Webhook, error) {
	var out *webhookpbv1.Webhook
	err := rpc.Traced(ctx, service, "RotateWebhookSecret", func(ctx context.Context) (err error) {
		if out, err = s.repos.Webhooks.Get(ctx, req.GetName()); err != nil {
			return rpc.ToStatusErr(err)
		}
		secret, err := newSecret()
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		// The replaced secret is resealed under the current key, so one stored
		// in the clear, or under an older key, is not kept that way.
		prev, err := s.opts.Sealer.Open(out.GetSecret())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		if out.PreviousSecret, err = s.opts.Sealer.Seal(prev); err != nil {
			return rpc.ToStatusErr(err)
		}
		if out.Secret, err = s.opts.Sealer.Seal(secret); err != nil {
			return rpc.ToStatusErr(err)
		}
		out.RotateTime = timestamppb.New(s.opts.Now())
		if out, err = s.repos.Webhooks.UpdateOutputOnly(ctx, out, []string{"secret", "previous_secret", "rotate_time"}); err != nil {
			return rpc.ToStatusErr(err)
		}
		// One of the two responses that carries the secret, in the clear.
		out.Secret = secret
		return nil
	})
	if out != nil {
		out.PreviousSecret = ""
	}
	return out, err
}

func (s *Server) GetDelivery(ctx context.Context, req *webhookpbv1.GetDeliveryRequest) (*webhookpbv1.Delivery, error) {
	var out *webhookpbv1.Delivery
	err := rpc.Traced(ctx, service, "GetDelivery", func(ctx context.Context) (err error) {
		out, err = s.repos.Deliveries.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) ListDeliveries(ctx context.Context, req *webhookpbv1.ListDeliveriesRequest) (*webhookpbv1.ListDeliveriesResponse, error) {
	out := &webhookpbv1.ListDeliveriesResponse{}
	err := rpc.Traced(ctx, service, "ListDeliveries", func(ctx context.Context) (err error) {
		order := req.GetOrderBy()
		if order == "" {
			order = "create_time desc"
		}
		out.Deliveries, out.NextPageToken, err = s.repos.Deliveries.List(ctx, req.GetParent(), repox.ListInput{
			PageSize: req.GetPageSize(), PageToken: req.GetPageToken(), Filter: req.GetFilter(), OrderBy: order,
		})
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *Server) RetryDelivery(ctx context.Context, req *webhookpbv1.RetryDeliveryRequest) (*webhookpbv1.Delivery, error) {
	var out *webhookpbv1.Delivery
	err := rpc.Traced(ctx, service, "RetryDelivery", func(ctx context.Context) (err error) {
		if out, err = s.repos.Deliveries.Get(ctx, req.GetName()); err != nil {
			return rpc.ToStatusErr(err)
		}
		out.State = webhookpbv1.DeliveryState_DELIVERY_STATE_PENDING
		out.AttemptCount, out.NextAttemptTime = 0, timestamppb.New(s.opts.Now())
		out, err = s.repos.Deliveries.UpdateOutputOnly(ctx, out, []string{"state", "attempt_count", "next_attempt_time"})
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// check validates what a client chose: an endpoint this server will send
// to, and event types that exist.
func (s *Server) check(w *webhookpbv1.Webhook) error {
	u, err := url.Parse(w.GetUri())
	switch {
	case err != nil || u.Host == "":
		return types.BadField("INVALID_URI", "webhook.uri",
			fmt.Sprintf("uri %q is not an absolute URL", w.GetUri()),
			"Send the full endpoint, scheme and host included, e.g. https://example.com/hooks/freebusy")
	case u.User != nil:
		return types.BadField("URI_HAS_CREDENTIALS", "webhook.uri",
			"uri carries a username or password",
			"Remove them: every delivery is signed with the webhook's secret (Standard Webhooks), so verify that signature instead")
	case u.Scheme == "https":
	case u.Scheme == "http" && s.opts.AllowInsecureEndpoints && loopback(u.Hostname()):
	default:
		return types.BadField("URI_NOT_HTTPS", "webhook.uri",
			fmt.Sprintf("uri %q is not https", w.GetUri()),
			"Serve the endpoint over https; plain http is accepted only for localhost, and only when the server allows insecure endpoints (development)")
	}
	// A name is checked when it is dialled; an address can be refused now.
	if ip := net.ParseIP(u.Hostname()); ip != nil && !public(ip) && !s.opts.AllowPrivateNetworks {
		return types.BadField("URI_NOT_PUBLIC", "webhook.uri",
			fmt.Sprintf("uri points at %s, which is not a public address", u.Hostname()),
			"Deliveries go only to the public internet; expose the endpoint publicly, or allow private networks on the server (development)")
	}
	known := EventTypes()
	for _, t := range w.GetEventTypes() {
		if !slices.Contains(known, t) {
			return types.BadField("UNKNOWN_EVENT_TYPE", "webhook.event_types",
				fmt.Sprintf("event type %q does not exist", t),
				fmt.Sprintf("Choose from %v, or leave event_types empty to receive every type", known))
		}
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newSecret is a signing key in the Standard Webhooks form.
func newSecret() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(key), nil
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
