package reviewer

import (
	"fmt"
	"strings"
)

// makeCacheKey creates a cache key from components.
func makeCacheKey(parts ...any) string {
	strParts := make([]string, len(parts))
	for i, part := range parts {
		strParts[i] = fmt.Sprint(part)
	}
	return strings.Join(strParts, ":")
}
