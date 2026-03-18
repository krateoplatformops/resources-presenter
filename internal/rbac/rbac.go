package rbac

import (
	"context"

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

	ucts := buildUserCanTargets(targets)

	// Batch RBAC check: groups by namespace, uses SelfSubjectRulesReview
	// with fallback to per-target SelfSubjectAccessReview.
	decisions := rbac.UserCan(ctx, ucts)

	return filterByDecisions(targets, ucts, decisions)
}

// buildUserCanTargets converts resource targets to the plumbing RBAC format.
// Verb is always "get". Version is intentionally excluded: Kubernetes RBAC
// operates at the Group+Resource level, not Group+Version+Resource.
// Object-level permissions are currently not supported, so Name is also not involved.
func buildUserCanTargets(targets []sql.ResourceTarget) []rbac.UserCanTarget {
	ucts := make([]rbac.UserCanTarget, len(targets))
	for i, t := range targets {
		ucts[i] = rbac.UserCanTarget{
			Verb:          "get",
			GroupResource: schema.GroupResource{Group: t.Group, Resource: t.Resource},
			Namespace:     t.Namespace,
		}
	}
	return ucts
}

// filterByDecisions returns only the targets whose corresponding UserCanTarget
// was allowed by the RBAC decisions map.
func filterByDecisions(targets []sql.ResourceTarget, ucts []rbac.UserCanTarget, decisions map[rbac.UserCanTarget]bool) []sql.ResourceTarget {
	var allowed []sql.ResourceTarget
	for i, t := range targets {
		if decisions[ucts[i]] {
			allowed = append(allowed, t)
		}
	}
	return allowed
}
