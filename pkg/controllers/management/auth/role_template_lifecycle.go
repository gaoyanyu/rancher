package auth

import (
	"fmt"

	"github.com/rancher/rancher/pkg/clustermanager"
	v3 "github.com/rancher/rancher/pkg/types/apis/management.cattle.io/v3"
	"github.com/rancher/rancher/pkg/types/config"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

const (
	roleTemplateLifecycleName = "mgmt-auth-roletemplate-lifecycle"
	prtbByRoleTemplateIndex   = "management.cattle.io/prtb-by-role-template"
)

type roleTemplateLifecycle struct {
	prtbIndexer    cache.Indexer
	prtbClient     v3.ProjectRoleTemplateBindingInterface
	clusters       v3.ClusterInterface
	clusterManager *clustermanager.Manager
}

func newRoleTemplateLifecycle(management *config.ManagementContext, clusterManager *clustermanager.Manager) v3.RoleTemplateLifecycle {
	informer := management.Management.ProjectRoleTemplateBindings("").Controller().Informer()
	indexers := map[string]cache.IndexFunc{
		prtbByRoleTemplateIndex: prtbByRoleTemplate,
	}
	informer.AddIndexers(indexers)

	rtl := &roleTemplateLifecycle{
		prtbIndexer:    informer.GetIndexer(),
		prtbClient:     management.Management.ProjectRoleTemplateBindings(""),
		clusters:       management.Management.Clusters(""),
		clusterManager: clusterManager,
	}
	return rtl
}

func (rtl *roleTemplateLifecycle) Create(obj *v3.RoleTemplate) (runtime.Object, error) {
	if err := rtl.enqueuePrtbs(obj); err != nil {
		return nil, err
	}
	return nil, nil
}

func (rtl *roleTemplateLifecycle) Updated(obj *v3.RoleTemplate) (runtime.Object, error) {
	if err := rtl.enqueuePrtbs(obj); err != nil {
		return nil, err
	}
	return nil, nil
}

func (rtl *roleTemplateLifecycle) Remove(obj *v3.RoleTemplate) (runtime.Object, error) {
	clusters, err := rtl.clusters.List(metav1.ListOptions{})
	if err != nil {
		return obj, err
	}

	// Collect all the errors to delete as many user context cluster roles as possible
	var allErrors []error

	for _, cluster := range clusters.Items {
		userContext, err := rtl.clusterManager.UserContext(cluster.Name)
		if err != nil {
			// ClusterUnavailable error indicates the record can't talk to the downstream cluster
			if !IsClusterUnavailable(err) {
				allErrors = append(allErrors, err)
			}
			continue
		}

		b, err := userContext.RBAC.ClusterRoles("").Controller().Lister().Get("", obj.Name)
		if err != nil {
			// User context clusterRole doesn't exist
			if !apierrors.IsNotFound(err) {
				allErrors = append(allErrors, err)
			}
			continue
		}

		err = userContext.RBAC.ClusterRoles("").Delete(b.Name, &metav1.DeleteOptions{})
		if err != nil {
			// User context clusterRole doesn't exist
			if !apierrors.IsNotFound(err) {
				allErrors = append(allErrors, err)
			}
			continue
		}

	}

	if len(allErrors) > 0 {
		return obj, fmt.Errorf("errors deleting dowstream clusterRole: %v", allErrors)
	}
	return obj, nil

}

// enqueue any prtb's linked to this roleTemplate in order to re-sync it via reconcileBindings
func (rtl *roleTemplateLifecycle) enqueuePrtbs(updatedRT *v3.RoleTemplate) error {
	prtbs, err := rtl.prtbIndexer.ByIndex(prtbByRoleTemplateIndex, updatedRT.Name)
	if err != nil {
		return err
	}
	for _, x := range prtbs {
		if prtb, ok := x.(*v3.ProjectRoleTemplateBinding); ok {
			rtl.prtbClient.Controller().Enqueue(prtb.Namespace, prtb.Name)
		}
	}
	return nil
}

func prtbByRoleTemplate(obj interface{}) ([]string, error) {
	prtb, ok := obj.(*v3.ProjectRoleTemplateBinding)
	if !ok {
		return []string{}, nil
	}
	return []string{prtb.RoleTemplateName}, nil
}
