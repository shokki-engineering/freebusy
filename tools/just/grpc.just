# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Exercise the gRPC API via reflection (server must be running; needs grpcurl).
# Invoked as `just grpc <recipe>`.
set working-directory := '../..'

grpc_port := env_var_or_default("FREEBUSY_SERVER__GRPC__PORT", "50051")

# List the gRPC services exposed via reflection.
[group('api')]
list:
    grpcurl -insecure localhost:{{ grpc_port }} list

# gRPC health check.
[group('api')]
health:
    grpcurl -insecure localhost:{{ grpc_port }} grpc.health.v1.Health/Check
