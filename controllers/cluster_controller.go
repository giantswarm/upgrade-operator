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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage/names"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/external"
	capikcp "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
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
		"AzureMachinePool":     "spec.template.image.marketplace.sku",
		"AzureMachineTemplate": "spec.template.spec.image.marketplace.sku",
		"AWSMachinePool":       "spec.template.image.marketplace.sku",
		"AWSMachineTemplate":   "spec.template.image.marketplace.sku",
	}
	MachineImagesByK8sVersion = map[string]string{
		"v1.18.2":  "k8s-1dot18dot2-ubuntu",
		"v1.18.14": "k8s-1dot18dot14-ubuntu",
		"v1.18.15": "k8s-1dot18dot15-ubuntu",
		"v1.18.16": "k8s-1dot18dot16-ubuntu",
	}
)

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.x-k8s.io;infrastructure.cluster.x-k8s.io,resources=clusters;clusters/status;awsclusters;awsclusters/status;awsmachinetemplates;azureclusters;azureclusters/status;azuremachinetemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=awsmachinetemplates;azuremachinetemplates,verbs=create
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kubeadmcontrolplanes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=exp.infrastructure.cluster.x-k8s.io;exp.cluster.x-k8s.io,resources=machinepools;machinepools/status;awsmachinepools;awsmachinepools/status;azuremachinepools;azuremachinepools/status,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=release.giantswarm.io,resources=releases,verbs=get;list;watch

func (r *ClusterReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("cluster", req.NamespacedName)

	cluster := &capi.Cluster{}
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, cluster)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to get reconciled Cluster %q", req.Name)
	}

	kcp := &capikcp.KubeadmControlPlane{}
	err = r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Spec.ControlPlaneRef.Namespace, Name: cluster.Spec.ControlPlaneRef.Name}, kcp)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to get KubeadmControlPlane %q referenced by Cluster %q", cluster.Spec.ControlPlaneRef.Name, req.Name)
	}

	// Fetch Release to know the expected version of different components that will be used as labels.
	giantswarmRelease, err := getGiantSwarmRelease(ctx, r.Client, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	expectedCAPIVersion := getComponentVersion(giantswarmRelease, capiReleaseComponent)
	expectedCACPVersion := getComponentVersion(giantswarmRelease, cacpReleaseComponent)

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
	if expectedK8sVersion != kcp.Spec.Version {
		log.Info("Current k8s version doesn't match expected version", "current", kcp.Spec.Version, "expected", expectedK8sVersion)
		log.Info("Cloning infrastructure template and changing its machine image")

		// Clone infrastructure machine template to new object.
		newInfrastructureMachineTemplate, err := r.CloneTemplate(ctx, kcp)
		if err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to build infrastructure template from %q", kcp.Spec.InfrastructureTemplate.Name)
		}

		// Update machine template machine image to upgrade k8s or the OS.
		if err := unstructured.SetNestedField(newInfrastructureMachineTemplate.Object, MachineImagesByK8sVersion[expectedK8sVersion], strings.Split(KindToMachineImagePath[kcp.Spec.InfrastructureTemplate.Kind], ".")...); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to set infrastructure template %q image to %q", kcp.Spec.InfrastructureTemplate.Name, MachineImagesByK8sVersion[expectedK8sVersion])
		}

		// Create cloned object.
		if err := r.Client.Create(ctx, newInfrastructureMachineTemplate); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to clone infrastructure template from %q to %q used by KubeadmControlPlane %q", kcp.Spec.InfrastructureTemplate.Name, newInfrastructureMachineTemplate.GetName(), kcp.Name)
		}

		//controlPlaneNodesWillBeRolled = true
		kcp.Spec.InfrastructureTemplate.Name = newInfrastructureMachineTemplate.GetName()
		kcp.Spec.Version = expectedK8sVersion
	}

	kcp.Labels[CAPIWatchFilterLabel] = expectedCACPVersion
	err = r.Client.Update(ctx, kcp)
	if apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to update KubeadmControlPlane %q", kcp.Name)
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

func (r *ClusterReconciler) CloneTemplate(ctx context.Context, kcp *capikcp.KubeadmControlPlane) (*unstructured.Unstructured, error) {
	infrastructureMachineTemplate, err := external.Get(ctx, r.Client, &kcp.Spec.InfrastructureTemplate, kcp.Namespace)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get infrastructure template %q referenced by KubeadmControlPlane %q", kcp.Spec.InfrastructureTemplate.Name, kcp.Name)
	}

	to := &unstructured.Unstructured{Object: infrastructureMachineTemplate.Object}
	to.SetName(names.SimpleNameGenerator.GenerateName(infrastructureMachineTemplate.GetName() + "-"))
	to.SetResourceVersion("")
	to.SetFinalizers(nil)
	to.SetUID("")
	to.SetSelfLink("")
	to.SetNamespace(infrastructureMachineTemplate.GetNamespace())
	to.SetAnnotations(infrastructureMachineTemplate.GetAnnotations())
	to.SetLabels(infrastructureMachineTemplate.GetLabels())
	to.SetOwnerReferences(infrastructureMachineTemplate.GetOwnerReferences())
	to.SetAPIVersion(infrastructureMachineTemplate.GetAPIVersion())
	to.SetKind(infrastructureMachineTemplate.GetKind())

	return to, nil
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
	expectedK8sVersion := getComponentVersion(giantswarmRelease, "kubernetes")

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
		} else if machinePool.Labels[CAPIWatchFilterLabel] != expectedCAPIVersion {
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
			if err := unstructured.SetNestedField(infraMachinePool.Object, MachineImagesByK8sVersion[expectedK8sVersion], strings.Split(KindToMachineImagePath[machinePool.Spec.Template.Spec.InfrastructureRef.Kind], ".")...); err != nil {
				return errors.Wrapf(err, "failed to set infra MachinePool %q image to %q", machinePool.Spec.Template.Spec.InfrastructureRef.Name, MachineImagesByK8sVersion[expectedK8sVersion])
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
			return fmt.Sprintf("v%s", component.Version)
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

func MachineTemplateNameFromCluster(cluster *capi.Cluster, k8sVersion, osVersion string) string {
	return fmt.Sprintf("%s-control-plane-k8s-%s-flatcar-%s", cluster.Name, strings.ReplaceAll(k8sVersion, ".", "dot"), strings.ReplaceAll(osVersion, ".", "dot"))
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
