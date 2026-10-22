// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command loadtest measures a running freebusy server through its gRPC API,
// the way a booking integration uses it, and prints latency percentiles per
// scenario. It is the baseline any performance work is judged against: run it
// before and after, on the same machine and data.
//
// It seeds its own tenant -- an organization, sites and resources named after
// the run -- and soft-deletes it afterwards (the purger removes it for good).
//
//	contention  many clients race CreateHold for the one resource at a site;
//	            exactly one may win each window, which it checks
//	check       CheckAvailability at a site of -per-site resources
//	search      SearchAvailability across the whole organization
//	list        ListReservations, page by page, at a site with reservations
//
//	go run ./tools/loadtest -addr localhost:50051 -sites 10 -per-site 100
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organization/v1/organizationv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organizational_unit/v1/organizational_unitv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc9073/resource/v1/resourcev1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	addr        = flag.String("addr", "localhost:50051", "the server's gRPC address")
	sites       = flag.Int("sites", 10, "sites to seed")
	perSite     = flag.Int("per-site", 100, "resources per site")
	concurrency = flag.Int("concurrency", 32, "clients running at once")
	requests    = flag.Int("requests", 2000, "requests per scenario")
	racers      = flag.Int("racers", 16, "clients racing for each contended window")
	windows     = flag.Int("windows", 50, "contended windows")
	keep        = flag.Bool("keep", false, "leave the seeded tenant in place")
	useTLS      = flag.Bool("tls", true, "dial with TLS (the dev profile serves it)")
	caFile      = flag.String("ca", "", "PEM file of the CA to trust instead of the system roots, e.g. \"$(mkcert -CAROOT)/rootCA.pem\"")
)

// clients are the services the scenarios call.
type clients struct {
	orgs         organizationv1grpc.OrganizationsClient
	units        organizational_unitv1grpc.OrganizationalUnitsClient
	resources    resourcev1grpc.ResourcesClient
	availability allocationpbv1.AllocationServiceClient
	holds        allocationpbv1.HoldServiceClient
	reservations reservationpbv1.ReservationServiceClient
}

// tenant is what the run seeded.
type tenant struct {
	org       string
	sites     []string
	resources []string
	// arena is a site with one resource, for contention.
	arena string
}

// result is one scenario's measurements.
type result struct {
	name      string
	latencies []time.Duration
	errors    int64
	elapsed   time.Duration
	note      string
}

func main() {
	flag.Parse()
	creds := insecure.NewCredentials()
	if *useTLS {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if *caFile != "" {
			pem, err := os.ReadFile(*caFile)
			if err != nil {
				log.Fatal(err)
			}
			cfg.RootCAs = x509.NewCertPool()
			if !cfg.RootCAs.AppendCertsFromPEM(pem) {
				log.Fatalf("%s holds no PEM certificate", *caFile)
			}
		}
		creds = credentials.NewTLS(cfg)
	}
	cc, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Fatal(err)
	}
	defer cc.Close()
	c := clients{
		orgs:         organizationv1grpc.NewOrganizationsClient(cc),
		units:        organizational_unitv1grpc.NewOrganizationalUnitsClient(cc),
		resources:    resourcev1grpc.NewResourcesClient(cc),
		availability: allocationpbv1.NewAllocationServiceClient(cc),
		holds:        allocationpbv1.NewHoldServiceClient(cc),
		reservations: reservationpbv1.NewReservationServiceClient(cc),
	}
	ctx := context.Background()
	run := fmt.Sprintf("load-%d", time.Now().Unix())

	start := time.Now()
	t, err := seed(ctx, c, run)
	if err != nil {
		log.Fatalf("seed: %v", err)
	}
	log.Printf("seeded %d sites, %d resources in %v", len(t.sites), len(t.resources), time.Since(start).Round(time.Millisecond))
	if !*keep {
		defer teardown(context.Background(), c, t)
	}

	// Windows a year out, an hour each, so a run never meets another's.
	base := time.Now().UTC().AddDate(1, 0, 0).Truncate(time.Hour)
	var results []result
	won, r := contention(ctx, c, t, base)
	results = append(results, r)
	results = append(results, scenario("check", func(i int) error {
		w := window(base.Add(time.Duration(rand.IntN(24*30)) * time.Hour))
		_, err := c.availability.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{
			Parent: t.sites[i%len(t.sites)], Window: w,
		})
		return err
	}))
	results = append(results, scenario("search", func(int) error {
		w := window(base.Add(time.Duration(rand.IntN(24*30)) * time.Hour))
		_, err := c.availability.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{
			Parent: t.org, Window: w,
		})
		return err
	}))
	results = append(results, paging(ctx, c, t, won))
	report(results)
}

// seed creates the run's organization, its sites, and their resources.
func seed(ctx context.Context, c clients, run string) (*tenant, error) {
	org, err := c.orgs.CreateOrganization(ctx, &organizationv1.CreateOrganizationRequest{
		OrganizationId: run, Organization: &organizationv1.Organization{DisplayNames: []string{"load test " + run}},
	})
	if err != nil {
		return nil, err
	}
	t := &tenant{org: org.GetName()}
	site := func(id string) (string, error) {
		ou, err := c.units.CreateOrganizationalUnit(ctx, &organizational_unitv1.CreateOrganizationalUnitRequest{
			OrganizationalUnitId: id,
			OrganizationalUnit:   &organizational_unitv1.OrganizationalUnit{Organization: t.org, DisplayNames: []string{id}},
		})
		return ou.GetName(), err
	}
	for i := range *sites {
		name, err := site(fmt.Sprintf("%s-s%d", run, i))
		if err != nil {
			return t, err
		}
		t.sites = append(t.sites, name)
	}
	if t.arena, err = site(run + "-arena"); err != nil {
		return t, err
	}

	type job struct{ id, site string }
	jobs := make(chan job)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for range *concurrency {
		wg.Go(func() {
			for j := range jobs {
				r, err := c.resources.CreateResource(ctx, &resourcev1.CreateResourceRequest{
					ResourceId: j.id,
					Resource:   &resourcev1.Resource{OrganizationalUnit: j.site, ResourceTypes: []string{"load"}},
				})
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if err == nil {
					t.resources = append(t.resources, r.GetName())
				}
				mu.Unlock()
			}
		})
	}
	for i, s := range t.sites {
		for k := range *perSite {
			jobs <- job{fmt.Sprintf("%s-s%d-r%d", run, i, k), s}
		}
	}
	jobs <- job{run + "-arena-r0", t.arena}
	close(jobs)
	wg.Wait()
	return t, firstErr
}

// contention races -racers clients for the arena's one resource in each of
// -windows windows, and fails the run if any window is sold twice. Each
// winner becomes a reservation, which the list scenario pages through.
func contention(ctx context.Context, c clients, t *tenant, base time.Time) (int, result) {
	r := result{name: "contention"}
	var mu sync.Mutex
	won := 0
	start := time.Now()
	for w := range *windows {
		win := window(base.AddDate(0, 2, 0).Add(time.Duration(w) * 2 * time.Hour))
		var wins atomic.Int64
		var wg sync.WaitGroup
		var seen sync.Map // status code -> one message, for a failed window
		for range *racers {
			wg.Go(func() {
				t0 := time.Now()
				h, err := c.holds.CreateHold(ctx, &allocationpbv1.CreateHoldRequest{Parent: t.arena, Hold: &allocationpbv1.Hold{Window: win}})
				d := time.Since(t0)
				mu.Lock()
				r.latencies = append(r.latencies, d)
				mu.Unlock()
				seen.LoadOrStore(status.Code(err), status.Convert(err).Message())
				switch status.Code(err) {
				case codes.OK:
					wins.Add(1)
					if _, err := c.reservations.CreateReservation(ctx, &reservationpbv1.CreateReservationRequest{
						Parent: t.arena, Reservation: &reservationpbv1.Reservation{Hold: h.GetName()},
					}); err != nil {
						atomic.AddInt64(&r.errors, 1)
					}
				case codes.FailedPrecondition, codes.Aborted:
					// Lost the race: the expected answer for all but one.
				default:
					atomic.AddInt64(&r.errors, 1)
				}
			})
		}
		wg.Wait()
		if n := wins.Load(); n != 1 {
			seen.Range(func(code, msg any) bool {
				log.Printf("  %v: %v", code, msg)
				return true
			})
			log.Fatalf("contention: window %d was won %d times, want exactly 1", w, n)
		}
		won++
	}
	r.elapsed = time.Since(start)
	r.note = fmt.Sprintf("%d windows x %d racers, one winner each", *windows, *racers)
	return won, r
}

// paging lists the arena's reservations page by page, again and again.
func paging(ctx context.Context, c clients, t *tenant, total int) result {
	const size = 10
	r := scenario("list", func(int) error {
		token := ""
		for {
			res, err := c.reservations.ListReservations(ctx, &reservationpbv1.ListReservationsRequest{
				Parent: t.arena, PageSize: size, PageToken: token,
			})
			if err != nil {
				return err
			}
			if token = res.GetNextPageToken(); token == "" {
				return nil
			}
		}
	})
	r.note = fmt.Sprintf("all %d reservations, %d a page", total, size)
	return r
}

// scenario runs fn -requests times on -concurrency clients.
func scenario(name string, fn func(i int) error) result {
	r := result{name: name, latencies: make([]time.Duration, *requests)}
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for range *concurrency {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= *requests {
					return
				}
				t0 := time.Now()
				if err := fn(i); err != nil {
					atomic.AddInt64(&r.errors, 1)
				}
				r.latencies[i] = time.Since(t0)
			}
		})
	}
	wg.Wait()
	r.elapsed = time.Since(start)
	return r
}

func window(start time.Time) *interval.Interval {
	return &interval.Interval{StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour))}
}

// teardown soft-deletes the tenant: resources, then sites, then the
// organization, as the API requires.
func teardown(ctx context.Context, c clients, t *tenant) {
	for _, r := range t.resources {
		_, _ = c.resources.DeleteResource(ctx, &resourcev1.DeleteResourceRequest{Name: r})
	}
	for _, s := range append(t.sites, t.arena) {
		_, _ = c.units.DeleteOrganizationalUnit(ctx, &organizational_unitv1.DeleteOrganizationalUnitRequest{Name: s})
	}
	if _, err := c.orgs.DeleteOrganization(ctx, &organizationv1.DeleteOrganizationRequest{Name: t.org}); err != nil {
		log.Printf("teardown: %v", err)
	}
}

func report(results []result) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "scenario\trequests\terrors\tp50\tp90\tp99\tmax\treq/s\t")
	for _, r := range results {
		l := slices.Clone(r.latencies)
		slices.Sort(l)
		pct := func(p float64) time.Duration {
			if len(l) == 0 {
				return 0
			}
			return l[min(len(l)-1, int(float64(len(l))*p))].Round(10 * time.Microsecond)
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%v\t%v\t%v\t%v\t%.0f\t\n", r.name, len(l), r.errors,
			pct(0.50), pct(0.90), pct(0.99), pct(1), float64(len(l))/r.elapsed.Seconds())
	}
	_ = w.Flush()
	for _, r := range results {
		if r.note != "" {
			fmt.Printf("%s: %s\n", r.name, r.note)
		}
	}
}
