// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"os"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// DialReferences registers, for every configured reference service, a
// resolver over its AIP Get and a referrer checker over its
// store.v1.ReferenceService, dialing each target once. It returns nil when
// there is nothing to register, which leaves references unchecked.
func DialReferences(services []config.ReferenceServiceConfig) (*repox.References, error) {
	if len(services) == 0 {
		return nil, nil
	}
	plaintext := map[string]bool{}
	endpoints := make([]repox.ReferenceEndpoint, 0, len(services))
	for _, s := range services {
		if s.Type == "" || s.Get == "" {
			return nil, fmt.Errorf("reference_services: an entry needs type and get (got type %q, get %q)", s.Type, s.Get)
		}
		// ${NAME} is expanded here, so the target dialed is the one the
		// entry's plaintext setting is recorded under.
		target := os.ExpandEnv(s.Target)
		endpoints = append(endpoints, repox.ReferenceEndpoint{Type: s.Type, Get: s.Get, Target: target, Referrers: s.Referrers})
		plaintext[target] = plaintext[target] || s.Plaintext
	}
	refs := repox.NewReferences()
	err := repox.WireEndpoints(refs, endpoints, func(target string) (grpc.ClientConnInterface, error) {
		creds := credentials.NewTLS(nil)
		if plaintext[target] {
			creds = insecure.NewCredentials()
		}
		return grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	})
	if err != nil {
		return nil, fmt.Errorf("reference_services: %w", err)
	}
	return refs, nil
}
