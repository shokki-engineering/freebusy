package rfc

import (
	"testing"

	resourcepbv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
)

// A resource carrying freebusy's STRUCTURED-DATA yields the profile in it.
func TestDecodeProfile(t *testing.T) {
	res := &resourcepbv1.Resource{
		Name: "resources/deluxe-king",
		StructuredData: []*resourcepbv1.StructuredData{
			{Schema: "https://example.com/other", Value: &resourcepbv1.StructuredData_Text{Text: `{"max_occupancy":99}`}},
			{Schema: ProfileSchemaURI, Value: &resourcepbv1.StructuredData_Text{Text: `{"max_occupancy":4}`}},
		},
	}
	p, err := DecodeProfile(res)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	if p.MaxOccupancy != 4 {
		t.Fatalf("MaxOccupancy = %d, want 4 (the entry matching our schema, not the first one)", p.MaxOccupancy)
	}
}

// A resource with no structured data at all is ordinary, not an error: most
// resources will never carry a booking profile.
func TestDecodeProfileAbsent(t *testing.T) {
	p, err := DecodeProfile(&resourcepbv1.Resource{Name: "resources/bay-1"})
	if err != nil {
		t.Fatalf("DecodeProfile on a bare resource: %v", err)
	}
	if p.MaxOccupancy != 0 {
		t.Fatalf("MaxOccupancy = %d, want 0 (unbounded)", p.MaxOccupancy)
	}
}

// Malformed JSON under our own schema URI is a real error — it means someone
// wrote a profile and got it wrong, which is worth reporting rather than
// silently reading as unbounded.
func TestDecodeProfileMalformed(t *testing.T) {
	res := &resourcepbv1.Resource{
		Name:           "resources/bay-2",
		StructuredData: []*resourcepbv1.StructuredData{{Schema: ProfileSchemaURI, Value: &resourcepbv1.StructuredData_Text{Text: `{"max_occupancy":`}}},
	}
	if _, err := DecodeProfile(res); err == nil {
		t.Fatal("DecodeProfile on malformed JSON = nil error, want a decode failure")
	}
}

// A uri-valued payload is deliberately not followed: fetching an arbitrary URL
// on the booking hot path is not something a decode should do silently.
func TestDecodeProfileIgnoresURIForm(t *testing.T) {
	res := &resourcepbv1.Resource{
		Name:           "resources/bay-3",
		StructuredData: []*resourcepbv1.StructuredData{{Schema: ProfileSchemaURI, Value: &resourcepbv1.StructuredData_Uri{Uri: "https://example.com/profile.json"}}},
	}
	p, err := DecodeProfile(res)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	if p.MaxOccupancy != 0 {
		t.Fatalf("MaxOccupancy = %d, want 0 — a uri payload must not be followed", p.MaxOccupancy)
	}
}
