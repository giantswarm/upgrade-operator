/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	releaseapiextensions "github.com/giantswarm/apiextensions/pkg/apis/release/v1alpha1"
	"github.com/giantswarm/apiextensions/pkg/label"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/reference"
	capz "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/external"
	kcp "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	capiexp "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	capiconditions "sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

const (
	CAPIWatchFilterLabel = "cluster.x-k8s.io/watch-filter"
	capiReleaseComponent = "cluster-api-core"
	cacpReleaseComponent = "cluster-api-control-plane"
	capaReleaseComponent = "cluster-api-provider-aws"
	capzReleaseComponent = "cluster-api-provider-azure"
)

var (
	KindToReleaseComponent = map[string]string{
		"AzureCluster":     capzReleaseComponent,
		"AzureMachinePool": capzReleaseComponent,
		"AWSCluster":       capaReleaseComponent,
		"AWSMachinePool":   capaReleaseComponent,
	}
	KindToMachineImagePath = map[string]string{
		"AzureMachinePool": "spec.template.image.marketplace.sku",
		"AWSMachinePool":   "spec.template.image.marketplace.sku",
	}
)

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=webapp.giantswarm.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=webapp.giantswarm.io,resources=clusters/status,verbs=get;update;patch

func (r *ClusterReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	_ = r.Log.WithValues("cluster", req.NamespacedName)

	cluster := &capi.Cluster{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, cluster)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to get reconciled Cluster %q", req.Name)
	}

	controlplane := &kcp.KubeadmControlPlane{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Spec.ControlPlaneRef.Namespace, Name: cluster.Spec.ControlPlaneRef.Name}, controlplane)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to get KubeadmControlPlane %q referenced by Cluster %q", cluster.Spec.ControlPlaneRef.Name, req.Name)
	}

	cpMachineTemplate := &capz.AzureMachineTemplate{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: controlplane.Spec.InfrastructureTemplate.Namespace, Name: controlplane.Spec.InfrastructureTemplate.Name}, cpMachineTemplate)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to get AzureMachineTemplate %q referenced by KubeadmControlPlane %q", controlplane.Spec.InfrastructureTemplate.Name, controlplane.Name)
	}

	// Fetch Release to know the expected version of different components that will be used as labels.
	giantswarmRelease, err := getGiantSwarmRelease(ctx, r.Client, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	expectedCAPIVersion := getComponentVersion(giantswarmRelease, capiReleaseComponent)
	expectedCACPVersion := getComponentVersion(giantswarmRelease, cacpReleaseComponent)
	expectedImage := getComponentVersion(giantswarmRelease, "image")

	// Update Cluster label with CAPI release number.
	cluster.Labels[CAPIWatchFilterLabel] = expectedCAPIVersion
	err = r.Client.Update(ctx, cluster)
	if apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to update Cluster %q", cluster.Name)
	}

	// Update infrastructure Cluster label with provider release number.
	infraCluster, err := external.Get(ctx, r.Client, cluster.Spec.InfrastructureRef, cluster.Namespace)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to retrieve infra reference %q", cluster.Spec.InfrastructureRef.Name)
	}

	setCAPIWatchFilterLabel(infraCluster, giantswarmRelease)
	err = r.Client.Update(ctx, infraCluster)
	if apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to update infra reference %q", cluster.Spec.InfrastructureRef.Name)
	}

	// Create new MachineTemplate and update KubeadmControlPlane to use it, if updating k8s.
	// Update KubeadmControlPlane label with CACP release number.
	//controlPlaneNodesWillBeRolled := false
	expectedK8sVersion := getComponentVersion(giantswarmRelease, "kubernetes")
	if expectedK8sVersion != controlplane.Spec.Version {
		//controlPlaneNodesWillBeRolled = true
		controlplane.Spec.Version = expectedK8sVersion

		newAzureMachineTemplate := &capz.AzureMachineTemplate{
			ObjectMeta: v1.ObjectMeta{
				Namespace:   cpMachineTemplate.Namespace,
				Name:        MachineTemplateNameFromCluster(cluster, expectedK8sVersion),
				Labels:      cpMachineTemplate.Labels,
				Annotations: cpMachineTemplate.Annotations,
			},
			Spec: cpMachineTemplate.Spec,
		}
		newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU = expectedImage
		err = r.Client.Create(ctx, newAzureMachineTemplate)
		if err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to create AzureMachineTemplate %q based on AzureMachineTemplate %q used by KubeadmControlPlane %q", newAzureMachineTemplate.Name, cpMachineTemplate.Name, controlplane.Name)
		}
		newAzureMachineTemplateReference, err := reference.GetReference(r.Scheme, newAzureMachineTemplate)
		if err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to build ObjectReference for AzureMachineTemplate %q", newAzureMachineTemplate.Name)
		}
		controlplane.Spec.InfrastructureTemplate = *newAzureMachineTemplateReference
	}

	controlplane.Labels[CAPIWatchFilterLabel] = expectedCACPVersion
	err = r.Client.Update(ctx, controlplane)
	if apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to update KubeadmControlPlane %q", controlplane.Name)
	}

	// If control plane nodes will be rolled, let's return so that status and
	// conditions are applied correctly before continuing.
	//if controlPlaneNodesWillBeRolled {
	//	return ctrl.Result{}, nil
	//}

	// Maybe control plane was upgraded in previous reconciliation and it's not
	// done yet, or is just having issues. Let's wait.
	//if !control-plane-is-upgraded-and-ready {
	//	return ctrl.Result{}, nil
	//}

	err = r.upgradeWorkers(ctx, cluster, giantswarmRelease)
	if apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to upgrade Cluster %q workers", cluster.Name)
	}

	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capi.Cluster{}).
		Complete(r)
}

// getSortedClusterMachinePools returns the MachinePools that belong to the given Cluster.
// TODO: Allow users to sort the MachinePools by some priority label on them.
func (r *ClusterReconciler) getSortedClusterMachinePools(ctx context.Context, cluster *capi.Cluster) ([]capiexp.MachinePool, error) {
	machinePoolList := &capiexp.MachinePoolList{}
	err := r.Client.List(ctx, machinePoolList, client.MatchingLabels{capi.ClusterLabelName: cluster.Name})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list MachinePools labeled %q", cluster.Name)
	}

	return machinePoolList.Items, nil
}

func (r *ClusterReconciler) upgradeWorkers(ctx context.Context, cluster *capi.Cluster, giantswarmRelease *releaseapiextensions.Release) error {
	logger := r.Log.WithValues("cluster", cluster.Name)

	expectedCAPIVersion := getComponentVersion(giantswarmRelease, capiReleaseComponent)
	expectedImage := getComponentVersion(giantswarmRelease, "image")

	clusterMachinePools, err := r.getSortedClusterMachinePools(ctx, cluster)
	if err != nil {
		return err
	}

	for _, machinePool := range clusterMachinePools {
		// If any MachinePool is not ready we exit because we don't want to
		// trigger an upgrade while something is wrong or another upgrade is in
		// progress.
		if !isReady(&machinePool) {
			logger.Info("The MachinePool %q is not ready. Let's wait before trying to continue upgrading workers")
			return nil
		} else if isMachinePoolOutdated(machinePool, expectedCAPIVersion) {
			machinePool.Labels[CAPIWatchFilterLabel] = expectedCAPIVersion
			err = r.Client.Update(ctx, &machinePool)
			if err != nil {
				return err
			}

			infraMachinePool, err := external.Get(ctx, r.Client, &machinePool.Spec.Template.Spec.InfrastructureRef, machinePool.Namespace)
			if err != nil {
				return err
			}

			//ready, err := external.IsReady(infraMachinePool)
			//if !ready {
			//	return ctrl.Result{}, nil
			//}

			// Update node pool machine image to upgrade k8s or the OS.
			if err := unstructured.SetNestedField(infraMachinePool.Object, expectedImage, strings.Split(KindToMachineImagePath[machinePool.Spec.Template.Spec.InfrastructureRef.Kind], ".")...); err != nil {
				return errors.Wrapf(err, "failed to set infra MachinePool %q image to %q", machinePool.Spec.Template.Spec.InfrastructureRef.Name, expectedImage)
			}

			setCAPIWatchFilterLabel(infraMachinePool, giantswarmRelease)
			err = r.Client.Update(ctx, infraMachinePool)
			if err != nil {
				return err
			}

			return nil
		}
	}

	return nil
}

func isReady(obj capiconditions.Getter) bool {
	return capiconditions.IsTrue(obj, capi.ReadyCondition)
}

func isMachinePoolOutdated(machinePool capiexp.MachinePool, expectedVersion string) bool {
	return machinePool.Labels[CAPIWatchFilterLabel] != expectedVersion
}

func getGiantSwarmRelease(ctx context.Context, ctrlClient client.Client, cluster *capi.Cluster) (*releaseapiextensions.Release, error) {
	// Fetch Release to know the expected version of different components that will be used as labels.
	giantswarmReleaseNumber, ok := cluster.GetLabels()[label.ReleaseVersion]
	if !ok {
		return nil, errors.New(fmt.Sprintf("failed to get %q label from reconciled Cluster %q", label.ReleaseVersion, cluster.Name))
	}

	giantswarmRelease := &releaseapiextensions.Release{}
	err := ctrlClient.Get(ctx, client.ObjectKey{Name: giantswarmReleaseNumber}, giantswarmRelease)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get Release %q used by Cluster %q", giantswarmReleaseNumber, cluster.Name)
	}

	return giantswarmRelease, nil
}

func getComponentVersionForInfraReference(release *releaseapiextensions.Release, infraReference *unstructured.Unstructured) string {
	return getComponentVersion(release, KindToReleaseComponent[infraReference.GetKind()])
}

func getComponentVersion(release *releaseapiextensions.Release, componentName string) string {
	for _, component := range release.Spec.Components {
		if component.Name == componentName {
			return component.Version
		}
	}

	return ""
}

func setCAPIWatchFilterLabel(infraReference *unstructured.Unstructured, release *releaseapiextensions.Release) {
	infraReference.SetLabels(
		merge(
			infraReference.GetLabels(),
			map[string]string{CAPIWatchFilterLabel: getComponentVersionForInfraReference(release, infraReference)},
		),
	)
}

func MachineTemplateNameFromCluster(cluster *capi.Cluster, k8sVersion string) string {
	return fmt.Sprintf("%s-control-plane-%s", cluster.Name, strings.ReplaceAll(k8sVersion, ".", "-"))
}

// merge returns a new map with values from both input maps
// Second map would overwrite values from first map.
func merge(map1, map2 map[string]string) map[string]string {
	merged := map[string]string{}

	for k, v := range map1 {
		merged[k] = v
	}
	for k, v := range map2 {
		merged[k] = v
	}

	return merged
}
