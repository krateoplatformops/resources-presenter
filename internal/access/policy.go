package access

import "net/http"

// Policy represents an access control policy that restricts which resources
// a user can see. Today it is always nil (allow-all). In the future it will
// carry user identity and allowed namespaces/clusters/resource kinds.
//
// The repository layer accepts *Policy and skips access filtering when nil.
type Policy struct {
	// AllowedClusters restricts results to these clusters.
	// Empty means no restriction.
	AllowedClusters []string

	// AllowedNamespaces restricts results to these namespaces.
	// Empty means no restriction.
	AllowedNamespaces []string

	// AllowedKinds restricts results to these resource kinds.
	// Empty means no restriction.
	AllowedKinds []string
}

// TODO(auth): populate from verified JWT/session claims.
// PolicyFromRequest extracts an access policy from the HTTP request context.
// Today it always returns nil (allow-all).
func PolicyFromRequest(r *http.Request) *Policy {
	_ = r // will use r.Context() to extract claims in the future
	return nil
}
