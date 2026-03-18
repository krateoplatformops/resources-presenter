package rbac

import (
	"context"
	"log"

	"github.com/krateoplatformops/plumbing/kubeutil/rbac"
	"github.com/krateoplatformops/resources-presenter/internal/sql"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RbacAuthorizer implements handlers.Authorizer using plumbing's batch rbac.UserCan.
type RbacAuthorizer struct{}

// FilterAllowed checks RBAC permissions for each target and returns only the
// targets the current user is allowed to GET.
//
// The user's Kubernetes endpoint is resolved from the request context
// (set by the use.UserConfig middleware). If the endpoint cannot be resolved,
// all targets are denied (empty slice returned).
//
// Version is intentionally excluded from the RBAC check: Kubernetes RBAC
// operates at the Group+Resource level, not Group+Version+Resource.
func (RbacAuthorizer) FilterAllowed(ctx context.Context, targets []sql.ResourceTarget) []sql.ResourceTarget {
	if len(targets) == 0 {
		return nil
	}

	// verb=get, no version needed by Kubernetes RBAC
	ucts := make([]rbac.UserCanTarget, len(targets))
	for i, t := range targets {
		ucts[i] = rbac.UserCanTarget{
			Verb:          "get",
			GroupResource: schema.GroupResource{Group: t.Group, Resource: t.Resource},
			Namespace:     t.Namespace,
		}
	}

	// Batch RBAC check: groups by namespace, uses SelfSubjectRulesReview
	// with fallback to per-target SelfSubjectAccessReview.
	decisions := rbac.UserCan(ctx, ucts)

	var allowed []sql.ResourceTarget
	for i, t := range targets {
		if decisions[ucts[i]] {
			log.Printf("RBAC ALLOWED: (group=%s, resource=%s, namespace=%s)\n", t.Group, t.Resource, t.Namespace)
			allowed = append(allowed, t)
		} else {
			log.Printf("RBAC DENIED: (group=%s, resource=%s, namespace=%s)\n", t.Group, t.Resource, t.Namespace)
		}
	}

	// log allowed targets just for testing
	log.Print("==========BEGIN ALLOWED LIST ===========\n")
	for _, t := range allowed {
		log.Printf("FINAL FILTERED ALLOWED: (group=%s, resource=%s, namespace=%s)\n", t.Group, t.Resource, t.Namespace)
	}
	log.Print("==========END ALLOWED LIST ===========\n")

	return allowed
}
