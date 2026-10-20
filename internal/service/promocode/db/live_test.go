package db_test

// Live integration test for promo code creation on both backends.
//
//	FREEBUSY_TEST_DATABASE_URL="host=127.0.0.1 user=postgres password=postgrespassword dbname=freebusydb sslmode=disable" \
//	FREEBUSY_TEST_GRAPHQL_URL=http://localhost:3280/graphql \
//	  go test ./internal/service/promocode/db/ -run Live -v

import (
	"context"

	"github.com/oh-tarnished/freebusy/internal/database"
	"net/url"
	"os"
	"testing"

	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql"
	"github.com/oh-tarnished/freebusy/internal/service/promocode"
	"github.com/oh-tarnished/freebusy/internal/service/promocode/db"
	gormrepo "github.com/oh-tarnished/freebusy/internal/service/promocode/db/gorm"
	hasurarepo "github.com/oh-tarnished/freebusy/internal/service/promocode/db/hasura"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"github.com/oh-tarnished/runtime-go/ulid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func repos(t *testing.T) map[string]db.PromoCodeRepository {
	t.Helper()
	out := map[string]db.PromoCodeRepository{}
	if dsn := os.Getenv("FREEBUSY_TEST_DATABASE_URL"); dsn != "" {
		gdb, err := database.OpenGorm(dsn, &gorm.Config{Logger: logger.Discard})
		if err != nil {
			t.Fatal(err)
		}
		out["gorm"] = gormrepo.NewPromoCodeRepository(gdb)
	}
	if raw := os.Getenv("FREEBUSY_TEST_GRAPHQL_URL"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := freebusyql.Connect(u)
		if err != nil {
			t.Fatal(err)
		}
		out["hasura"] = hasurarepo.NewPromoCodeRepository(svc)
	}
	if len(out) == 0 {
		t.Skip("no live backend configured")
	}
	return out
}

func TestLivePromoCodeGeneration(t *testing.T) {
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			srv := promocode.NewServer(repo)
			ctx := context.Background()
			discount := &promocodepbv1.Discount{Amount: &promocodepbv1.Discount_PercentOff{PercentOff: 10}}

			gen, err := srv.CreatePromoCode(ctx, &promocodepbv1.CreatePromoCodeRequest{PromoCode: &promocodepbv1.PromoCode{Discount: discount}})
			if err != nil {
				t.Fatal(err)
			}
			if gen.GetCode() == "" || gen.GetCodeGeneration() != promocodepbv1.CodeGeneration_CODE_GENERATION_AUTO {
				t.Fatalf("empty code: got %q %v", gen.GetCode(), gen.GetCodeGeneration())
			}
			got, err := srv.GetPromoCode(ctx, &promocodepbv1.GetPromoCodeRequest{Name: gen.GetName()})
			if err != nil || got.GetCodeGeneration() != promocodepbv1.CodeGeneration_CODE_GENERATION_AUTO {
				t.Fatalf("read back: %v %v", got.GetCodeGeneration(), err)
			}

			vanity := "IT" + ulid.GenerateString()[:10]
			man, err := srv.CreatePromoCode(ctx, &promocodepbv1.CreatePromoCodeRequest{PromoCode: &promocodepbv1.PromoCode{Code: vanity, Discount: discount}})
			if err != nil {
				t.Fatal(err)
			}
			if man.GetCode() != vanity || man.GetCodeGeneration() != promocodepbv1.CodeGeneration_CODE_GENERATION_MANUAL {
				t.Fatalf("supplied code: %q %v", man.GetCode(), man.GetCodeGeneration())
			}
			if _, err := srv.CreatePromoCode(ctx, &promocodepbv1.CreatePromoCodeRequest{PromoCode: &promocodepbv1.PromoCode{Code: vanity, Discount: discount}}); status.Code(err) != codes.AlreadyExists {
				t.Fatalf("duplicate code: %v", err)
			}
		})
	}
}
