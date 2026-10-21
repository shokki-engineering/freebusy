// The webhook suite: a receiver on localhost subscribed to one site's
// reservations, sent what changes there, signed, on both backends.
//
//	FREEBUSY_TEST_POSTGRES_DSN=... FREEBUSY_TEST_GRAPHQL_URL=... go test ./internal/e2e/ -run Webhook -v
package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/oh-tarnished/freebusy/internal/service/rfcsvc"
	"github.com/oh-tarnished/freebusy/internal/service/webhooksvc"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/webhook/v1/webhookpbv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestWebhook_Gorm(t *testing.T) {
	gdb := openTestGorm(t)
	webhookFlow(t, &database.Connection{PgSQLConn: gdb, Provider: database.ProviderGorm})
}

func TestWebhook_Hasura(t *testing.T) {
	svc := connectTestGraphQL(t)
	webhookFlow(t, &database.Connection{Hasura: svc, Provider: database.ProviderHasura})
}

// receiver is an endpoint that checks each delivery's Standard Webhooks
// signature and answers with the next scripted status.
type receiver struct {
	mu       sync.Mutex
	secret   string
	statuses []int
	got      []map[string]any
	ids      []string
	sigs     []int
	bad      []string
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	key, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.secret, "whsec_"))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(req.Header.Get("webhook-id") + "." + req.Header.Get("webhook-timestamp") + "."))
	mac.Write(body)
	sigs := strings.Fields(req.Header.Get("webhook-signature"))
	if want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)); !slices.Contains(sigs, want) {
		r.bad = append(r.bad, req.Header.Get("webhook-signature"))
	}
	r.sigs = append(r.sigs, len(sigs))
	var event map[string]any
	_ = json.Unmarshal(body, &event)
	r.got = append(r.got, event)
	r.ids = append(r.ids, req.Header.Get("webhook-id"))
	code := http.StatusOK
	if len(r.statuses) > 0 {
		code, r.statuses = r.statuses[0], r.statuses[1:]
	}
	w.WriteHeader(code)
}

func webhookFlow(t *testing.T, conn *database.Connection) {
	ctx := context.Background()
	repos := webhooksvc.NewRepositories(conn.Repox())
	opts := webhooksvc.Options{AllowInsecureEndpoints: true, AllowPrivateNetworks: true, Lag: 5 * time.Second}
	srv := webhooksvc.NewServer(repos, opts)
	reservations := reservation.New(conn.Repox()).Reservations

	rec := &receiver{statuses: []int{http.StatusServiceUnavailable}}
	endpoint := httptest.NewServer(rec)
	defer endpoint.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	site := "organizationalUnits/wh-" + suffix

	// Validation: https only (http to loopback in development), no
	// credentials in the URL, known event types.
	for _, bad := range []*webhookpbv1.Webhook{
		{Uri: "http://example.com/hook"},
		{Uri: "ftp://127.0.0.1/hook"},
		{Uri: "https://user:pw@example.com/hook"},
		{Uri: endpoint.URL, EventTypes: []string{"freebusy.reservation.teleported"}},
		{Uri: "https://10.0.0.7/hook"}, // a private address, refused below
	} {
		if bad.GetUri() == "https://10.0.0.7/hook" {
			strict := webhooksvc.NewServer(repos, webhooksvc.Options{})
			if _, err := strict.CreateWebhook(ctx, &webhookpbv1.CreateWebhookRequest{Webhook: bad}); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("CreateWebhook to a private address: %v", err)
			}
			continue
		}
		if _, err := srv.CreateWebhook(ctx, &webhookpbv1.CreateWebhookRequest{Webhook: bad}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("CreateWebhook(%v): %v, want InvalidArgument", bad, err)
		}
	}

	hook, err := srv.CreateWebhook(ctx, &webhookpbv1.CreateWebhookRequest{
		WebhookId: "wh-" + suffix,
		Webhook: &webhookpbv1.Webhook{
			DisplayName: "test receiver", Uri: endpoint.URL + "/hook", OrganizationalUnit: site,
			EventTypes: []string{"freebusy.reservation.confirmed", "freebusy.reservation.cancelled"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = srv.DeleteWebhook(ctx, &webhookpbv1.DeleteWebhookRequest{Name: hook.GetName(), Force: true})
	}()
	if !strings.HasPrefix(hook.GetSecret(), "whsec_") || hook.GetState() != webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE {
		t.Fatalf("created webhook: secret %q, state %v", hook.GetSecret(), hook.GetState())
	}
	rec.secret = hook.GetSecret()
	if got, _ := srv.GetWebhook(ctx, &webhookpbv1.GetWebhookRequest{Name: hook.GetName()}); got.GetSecret() != "" {
		t.Fatal("GetWebhook returned the secret")
	}
	if _, err := srv.UpdateWebhook(ctx, &webhookpbv1.UpdateWebhookRequest{
		Webhook: &webhookpbv1.Webhook{Name: hook.GetName(), Uri: "http://example.com/x"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"uri"}},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateWebhook to plain http: %v", err)
	}
	if _, err := srv.UpdateWebhook(ctx, &webhookpbv1.UpdateWebhookRequest{
		Webhook: &webhookpbv1.Webhook{Name: hook.GetName()}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"secret"}},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("UpdateWebhook of the secret: %v", err)
	}

	deliveries := func() []*webhookpbv1.Delivery {
		t.Helper()
		res, err := srv.ListDeliveries(ctx, &webhookpbv1.ListDeliveriesRequest{Parent: hook.GetName()})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetDeliveries()
	}
	round := func() {
		t.Helper()
		if err := srv.Round(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// A PENDING reservation is a type this webhook did not ask for.
	name := site + "/reservations/r" + suffix
	res, err := reservations.Create(ctx, site, &reservationpbv1.Reservation{
		Name: name, Hold: site + "/holds/h" + suffix, State: reservationpbv1.ReservationState_RESERVATION_STATE_PENDING,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reservations.Delete(ctx, name) }()
	round()
	if d := deliveries(); len(d) != 0 {
		t.Fatalf("a PENDING reservation was sent to a webhook asking for confirmed and cancelled: %v", d)
	}

	// Confirmed: recorded, and the first attempt meets a 503.
	res.State = reservationpbv1.ReservationState_RESERVATION_STATE_CONFIRMED
	if res, err = reservations.UpdateOutputOnly(ctx, res, []string{"state"}); err != nil {
		t.Fatal(err)
	}
	round()
	d := deliveries()
	if len(d) != 1 {
		t.Fatalf("%d deliveries after a confirmation, want 1", len(d))
	}
	if d[0].GetState() != webhookpbv1.DeliveryState_DELIVERY_STATE_PENDING || d[0].GetAttemptCount() != 1 || d[0].GetResponseCode() != 503 {
		t.Fatalf("after a 503: %v", d[0])
	}
	if d[0].GetNextAttemptTime().AsTime().Before(time.Now().Add(20 * time.Second)) {
		t.Fatalf("retry scheduled at %v, want backoff", d[0].GetNextAttemptTime().AsTime())
	}

	// Retried now, accepted.
	if _, err := srv.RetryDelivery(ctx, &webhookpbv1.RetryDeliveryRequest{Name: d[0].GetName()}); err != nil {
		t.Fatal(err)
	}
	round()
	d = deliveries()
	if len(d) != 1 || d[0].GetState() != webhookpbv1.DeliveryState_DELIVERY_STATE_SUCCEEDED || d[0].GetDeliverTime() == nil {
		t.Fatalf("after the retry: %v", d)
	}

	// What the endpoint saw: two signed attempts at one CloudEvent.
	rec.mu.Lock()
	if len(rec.bad) > 0 || len(rec.got) != 2 || rec.ids[0] != rec.ids[1] {
		t.Fatalf("receiver: %d bad signatures, %d requests, ids %v", len(rec.bad), len(rec.got), rec.ids)
	}
	event := rec.got[1]
	data, _ := event["data"].(map[string]any)
	if event["specversion"] != "1.0" || event["type"] != "freebusy.reservation.confirmed" || event["subject"] != name ||
		event["id"] != rec.ids[1] || data["state"] != "RESERVATION_STATE_CONFIRMED" {
		t.Fatalf("event: %v", event)
	}
	rec.mu.Unlock()

	// Rereading the same changes records nothing new -- on this server, or on
	// a second one that remembers nothing.
	round()
	if err := webhooksvc.NewServer(repos, opts).Round(ctx); err != nil {
		t.Fatal(err)
	}
	if d := deliveries(); len(d) != 1 {
		t.Fatalf("%d deliveries after rereads, want 1", len(d))
	}

	// Rotated: the new secret signs, and the old one alongside it for a day.
	rotated, err := srv.RotateWebhookSecret(ctx, &webhookpbv1.RotateWebhookSecretRequest{Name: hook.GetName()})
	if err != nil || rotated.GetSecret() == hook.GetSecret() || !strings.HasPrefix(rotated.GetSecret(), "whsec_") || rotated.GetPreviousSecret() != "" {
		t.Fatalf("rotate: %v, %v", rotated, err)
	}
	if _, err := srv.RetryDelivery(ctx, &webhookpbv1.RetryDeliveryRequest{Name: d[0].GetName()}); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	rec.secret = rotated.GetSecret() // the receiver now checks the new key
	rec.mu.Unlock()
	round()
	rec.mu.Lock()
	if n := len(rec.sigs); n != 3 || rec.sigs[2] != 2 || len(rec.bad) > 0 {
		t.Fatalf("after rotation: %d requests, signatures %v, %d bad", n, rec.sigs, len(rec.bad))
	}
	rec.mu.Unlock()

	// A server that keeps to public addresses will not reach the loopback
	// receiver, whatever the webhook says.
	if _, err := srv.RetryDelivery(ctx, &webhookpbv1.RetryDeliveryRequest{Name: d[0].GetName()}); err != nil {
		t.Fatal(err)
	}
	if err := webhooksvc.NewServer(repos, webhooksvc.Options{AllowInsecureEndpoints: true, Lag: 5 * time.Second}).Round(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.GetDelivery(ctx, &webhookpbv1.GetDeliveryRequest{Name: d[0].GetName()}); got.GetState() != webhookpbv1.DeliveryState_DELIVERY_STATE_PENDING ||
		!strings.Contains(got.GetErrorMessage(), "not a public address") {
		t.Fatalf("sent to loopback by a server that forbids it: %v", got)
	}
	rec.mu.Lock()
	if len(rec.got) != 3 {
		t.Fatalf("the receiver was reached: %d requests", len(rec.got))
	}
	rec.mu.Unlock()
	// Paused: a cancellation is not recorded.
	if _, err := srv.PauseWebhook(ctx, &webhookpbv1.PauseWebhookRequest{Name: hook.GetName()}); err != nil {
		t.Fatal(err)
	}
	res.State = reservationpbv1.ReservationState_RESERVATION_STATE_CANCELLED
	if _, err = reservations.UpdateOutputOnly(ctx, res, []string{"state"}); err != nil {
		t.Fatal(err)
	}
	round()
	if d := deliveries(); len(d) != 1 {
		t.Fatalf("%d deliveries while paused, want 1", len(d))
	}
	got, err := srv.ResumeWebhook(ctx, &webhookpbv1.ResumeWebhookRequest{Name: hook.GetName()})
	if err != nil || got.GetState() != webhookpbv1.WebhookState_WEBHOOK_STATE_ACTIVE {
		t.Fatalf("resume: %v, %v", got.GetState(), err)
	}

	// A webhook with deliveries goes only with force, and takes them along.
	if _, err := srv.DeleteWebhook(ctx, &webhookpbv1.DeleteWebhookRequest{Name: hook.GetName()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("delete without force: %v", err)
	}
	if _, err := srv.DeleteWebhook(ctx, &webhookpbv1.DeleteWebhookRequest{Name: hook.GetName(), Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.GetDelivery(ctx, &webhookpbv1.GetDeliveryRequest{Name: d[0].GetName()}); status.Code(err) != codes.NotFound {
		t.Fatalf("a delivery outlived its webhook: %v", err)
	}

	// Inventory: a webhook of every site is sent a resource's life.
	inv, err := srv.CreateWebhook(ctx, &webhookpbv1.CreateWebhookRequest{
		WebhookId: "inv-" + suffix,
		Webhook:   &webhookpbv1.Webhook{Uri: endpoint.URL + "/inventory", EventTypes: []string{"freebusy.resource.created", "freebusy.resource.updated", "freebusy.resource.deleted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = srv.DeleteWebhook(ctx, &webhookpbv1.DeleteWebhookRequest{Name: inv.GetName(), Force: true})
	}()
	resources := rfcsvc.NewRepositories(conn.Repox()).Resources
	court := &resourcev1.Resource{Name: "resources/wh-court-" + suffix, DisplayName: "Court"}
	if court, err = resources.Create(ctx, court); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resources.Purge(ctx, court.GetName()) }()
	invTypes := func() []string {
		t.Helper()
		res, err := srv.ListDeliveries(ctx, &webhookpbv1.ListDeliveriesRequest{Parent: inv.GetName(), OrderBy: "create_time"})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range res.GetDeliveries() {
			if d.GetSubject() == court.GetName() {
				out = append(out, d.GetEventType())
			}
		}
		return out
	}
	round()
	court.DisplayName = "Court one"
	if court, err = resources.Update(ctx, court, []string{"display_name"}); err != nil {
		t.Fatal(err)
	}
	round()
	if err := resources.Delete(ctx, court.GetName()); err != nil {
		t.Fatal(err)
	}
	round()
	if got, want := invTypes(), []string{"freebusy.resource.created", "freebusy.resource.updated", "freebusy.resource.deleted"}; !slices.Equal(got, want) {
		t.Fatalf("resource events %v, want %v", got, want)
	}

	// Finished deliveries older than the retention are pruned.
	short := webhooksvc.NewServer(repos, webhooksvc.Options{AllowInsecureEndpoints: true, AllowPrivateNetworks: true, Lag: 5 * time.Second,
		Retention: time.Nanosecond, Now: func() time.Time { return time.Now().Add(time.Minute) }})
	for range 5 { // a page a round
		if err := short.Round(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if left := invTypes(); len(left) != 0 {
		t.Fatalf("succeeded deliveries outlived the retention: %v", left)
	}
}
