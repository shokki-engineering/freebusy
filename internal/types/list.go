// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package types

import (
	"encoding/base64"
	"strconv"
)

// Pagination defaults applied by PageBounds when the request omits or
// overshoots the page size.
const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

// PageBounds resolves the limit/offset window for a List call. It clamps the
// page size to [1, maxPageSize] (defaulting when unset) and decodes the opaque
// page token into an offset; a malformed token decodes to offset 0.
func PageBounds(pageSize int32, pageToken string) (limit, offset int) {
	limit = int(pageSize)
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}
	return limit, decodeOffset(pageToken)
}

// EncodeOffset produces the opaque page token addressing the row at the given
// absolute offset. Adapters call it with offset+limit after fetching one extra
// row to detect whether a further page exists.
func EncodeOffset(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeOffset(token string) int {
	if token == "" {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
