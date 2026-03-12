package rbac

import (
	"context"

	"github.com/krateoplatformops/plumbing/kubeutil/rbac"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xcontext "github.com/krateoplatformops/plumbing/context"
)

// RbacAuthorizer implements handlers.Authorizer using plumbing's rbac.UserCan.
type RbacAuthorizer struct{}

func (RbacAuthorizer) CanGet(ctx context.Context, group, resource, namespace string) bool {
	// Extract the user endpoint from context (set by use.UserConfig middleware).
	ep, err := xcontext.UserConfig(ctx)
	if err != nil {
		return false
	}

	return rbac.UserCan(ctx, rbac.UserCanOptions{
		UserConfig:    ep,
		Verb:          "get",
		GroupResource: schema.GroupResource{Group: group, Resource: resource},
		Namespace:     namespace,
	})
}
