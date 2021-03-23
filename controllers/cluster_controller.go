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
	"github.com/Azure/go-autorest/autorest/to"
	corev1 "k8s.io/api/core/v1"
	cabpk "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	"strings"

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
		"AWSMachinePool":       "spec.awslaunchtemplate.imagelookupformat",
		"AWSMachineTemplate":   "spec.template.spec.imagelookupformat",
	}
	KindToK8sProviderFile = map[string]string{
		"AzureMachineTemplate": "%s-azure-json",
		"AWSMachineTemplate":   "%s-aws-json",
	}
	MachineImagesByK8sVersion = map[string]map[string]string{
		"v1.18.2": {
			"v18.4.0":  "k8s-1dot18dot2-ubuntu-1804",
			"v18.10.0": "k8s-1dot18dot2-ubuntu-1810",
		},
		"v1.18.14": {
			"v18.4.0":  "k8s-1dot18dot14-ubuntu-1804",
			"v18.10.0": "k8s-1dot18dot14-ubuntu-1810",
		},
		"v1.18.15": {
			"v18.4.0":  "k8s-1dot18dot15-ubuntu-1804",
			"v18.10.0": "k8s-1dot18dot15-ubuntu-1810",
		},
		"v1.18.16": {
			"v18.4.0":  "k8s-1dot18dot16-ubuntu-1804",
			"v18.10.0": "k8s-1dot18dot16-ubuntu-1810",
			"v20.4.0":  "k8s-1dot18dot16-ubuntu-2004",
		},
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
	logger := r.Log.WithValues("cluster", req.NamespacedName)

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

	// Fetch Release to know the expected version of different components that will be used.
	giantswarmRelease, err := getGiantSwarmRelease(ctx, r.Client, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	_, err = r.upgradeControlPlane(ctx, cluster, kcp, giantswarmRelease)
	if apierrors.IsConflict(err) {
		logger.Info("We received a conflict while saving objects in the k8s API. Let's try again on the next reconciliation")
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to upgrade Cluster %q control plane", cluster.Name)
	}

	// If control plane nodes will be rolled, let's return so that status and
	// conditions are applied correctly before continuing.
	//if controlPlaneNodesWillBeRolled {
	//	return ctrl.Result{}, nil
	//}

	// Maybe control plane was upgraded in previous reconciliation and it's not
	// done yet, or is just having issues. Let's wait.
	if !isReady(kcp) {
		logger.Info("Control plane is not ready, let's wait before upgrading the workers")
		return ctrl.Result{}, nil
	}

	err = r.upgradeWorkers(ctx, cluster, giantswarmRelease)
	if apierrors.IsConflict(err) {
		logger.Info("We received a conflict while saving objects in the k8s API. Let's try again on the next reconciliation")
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

func (r *ClusterReconciler) CloneTemplate(ctx context.Context, templateReference *corev1.ObjectReference, ownerNamespace, ownerName string) (*unstructured.Unstructured, error) {
	infrastructureMachineTemplate, err := external.Get(ctx, r.Client, templateReference, ownerNamespace)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get infrastructure template %q referenced by %s/%s", templateReference.Name, ownerNamespace, ownerName)
	}

	to := &unstructured.Unstructured{Object: infrastructureMachineTemplate.Object}
	to.SetName(names.SimpleNameGenerator.GenerateName(ownerName + "-"))
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

// getSortedClusterMachineDeployments returns the MachineDeployments that belong to the given Cluster.
// TODO: Allow users to sort the MachineDeployments by some priority label on them.
func (r *ClusterReconciler) getSortedClusterMachineDeployments(ctx context.Context, cluster *capi.Cluster) ([]capi.MachineDeployment, error) {
	machineDeploymentList := &capi.MachineDeploymentList{}
	err := r.Client.List(ctx, machineDeploymentList, client.MatchingLabels{capi.ClusterLabelName: cluster.Name})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list MachineDeployments labeled %q", cluster.Name)
	}

	return machineDeploymentList.Items, nil
}

func (r *ClusterReconciler) upgradeControlPlane(ctx context.Context, cluster *capi.Cluster, kcp *capikcp.KubeadmControlPlane, giantswarmRelease *releaseapiextensions.Release) (bool, error) {
	logger := r.Log.WithValues("cluster", cluster.Name, "kubeadmcontrolplane", kcp.Name)

	controlPlaneNodesWillBeRolled := false

	// Fetch infrastructure machine template for the control plane.
	infraMachineTemplate, err := external.Get(ctx, r.Client, &kcp.Spec.InfrastructureTemplate, cluster.Namespace)
	if err != nil {
		return false, errors.Wrapf(err, "failed to retrieve infra machine template %q", kcp.Spec.InfrastructureTemplate.Name)
	}

	expectedCAPIVersion := getComponentVersion(giantswarmRelease, capiReleaseComponent)
	expectedCACPVersion := getComponentVersion(giantswarmRelease, cacpReleaseComponent)

	// Update Cluster label with CAPI release number.
	cluster.Labels[CAPIWatchFilterLabel] = expectedCAPIVersion
	err = r.Client.Update(ctx, cluster)
	if err != nil {
		return false, errors.Wrapf(err, "failed to update Cluster %q", cluster.Name)
	}

	// Update infrastructure Cluster label with provider release number.
	infraCluster, err := external.Get(ctx, r.Client, cluster.Spec.InfrastructureRef, cluster.Namespace)
	if err != nil {
		return false, errors.Wrapf(err, "failed to retrieve infra cluster %q", cluster.Spec.InfrastructureRef.Name)
	}

	setCAPIWatchFilterLabel(infraCluster, giantswarmRelease)
	err = r.Client.Update(ctx, infraCluster)
	if err != nil {
		return false, errors.Wrapf(err, "failed to update infra reference %q", cluster.Spec.InfrastructureRef.Name)
	}

	// Create new MachineTemplate and update KubeadmControlPlane to use it, if updating k8s.
	// Update KubeadmControlPlane label with CACP release number.
	expectedK8sVersion := getComponentVersion(giantswarmRelease, "kubernetes")
	expectedOSVersion := getComponentVersion(giantswarmRelease, "image")
	expectedMachineImage := MachineImagesByK8sVersion[expectedK8sVersion][expectedOSVersion]
	currentMachineImage, _, err := unstructured.NestedString(infraMachineTemplate.Object, strings.Split(KindToMachineImagePath[kcp.Spec.InfrastructureTemplate.Kind], ".")...)
	if err != nil {
		return false, errors.Wrapf(err, "failed to read current machine image from infrastructure machine template %q", infraMachineTemplate.GetName())
	}

	if expectedMachineImage != currentMachineImage || expectedK8sVersion != kcp.Spec.Version {
		controlPlaneNodesWillBeRolled = true

		logger.Info("Cluster needs to be upgraded and control planes will be rolled", "currentK8sVersion", kcp.Spec.Version, "expectedK8sVersion", expectedK8sVersion, "currentMachineImage", currentMachineImage, "expectedMachineImage", expectedMachineImage)
		logger.Info("Cloning infrastructure template and changing its machine image")

		// Clone infrastructure machine template to new object.
		newInfrastructureMachineTemplate, err := r.CloneTemplate(ctx, &kcp.Spec.InfrastructureTemplate, kcp.Namespace, kcp.Name)
		if err != nil {
			return false, errors.Wrapf(err, "failed to build infrastructure template from %q", kcp.Spec.InfrastructureTemplate.Name)
		}

		// Update machine template machine image to upgrade k8s or the OS.
		if err := unstructured.SetNestedField(newInfrastructureMachineTemplate.Object, expectedMachineImage, strings.Split(KindToMachineImagePath[kcp.Spec.InfrastructureTemplate.Kind], ".")...); err != nil {
			return false, errors.Wrapf(err, "failed to set infrastructure template %q image to %q", kcp.Spec.InfrastructureTemplate.Name, expectedMachineImage)
		}

		// Create cloned object.
		if err := r.Client.Create(ctx, newInfrastructureMachineTemplate); err != nil {
			return false, errors.Wrapf(err, "failed to clone infrastructure template from %q to %q used by KubeadmControlPlane %q", kcp.Spec.InfrastructureTemplate.Name, newInfrastructureMachineTemplate.GetName(), kcp.Name)
		}

		// When upgrading the k8s cluster we need to update
		// * Name of the secret used as source for the k8s provider config file
		// * Reference to the infrastructure machine template.
		// * K8s version in the control plane object.
		for _, file := range kcp.Spec.KubeadmConfigSpec.Files {
			if file.ContentFrom.Secret.Name == fmt.Sprintf(KindToK8sProviderFile[newInfrastructureMachineTemplate.GetKind()], kcp.Spec.InfrastructureTemplate.Name) {
				file.ContentFrom.Secret.Name = fmt.Sprintf(KindToK8sProviderFile[newInfrastructureMachineTemplate.GetKind()], newInfrastructureMachineTemplate.GetName())
			}
		}
		kcp.Spec.InfrastructureTemplate.Name = newInfrastructureMachineTemplate.GetName()
		kcp.Spec.Version = expectedK8sVersion
	}

	kcp.Labels[CAPIWatchFilterLabel] = expectedCACPVersion
	err = r.Client.Update(ctx, kcp)
	if err != nil {
		return false, errors.Wrapf(err, "failed to update KubeadmControlPlane %q", kcp.Name)
	}

	return controlPlaneNodesWillBeRolled, nil
}

// upgradeMachinePool checks the machine image and k8s version used and will
// update the infra Machine Pool so it uses the right versions.
func (r *ClusterReconciler) upgradeMachinePool(ctx context.Context, cluster *capi.Cluster, machinePool *capiexp.MachinePool, giantswarmRelease *releaseapiextensions.Release) (bool, error) {
	logger := r.Log.WithValues("cluster", cluster.Name, "machinepool", machinePool.Name)
	logger.Info("Fetching infrastructure MachinePool to check its k8s version and machine image")

	expectedCAPIVersion := getComponentVersion(giantswarmRelease, capiReleaseComponent)
	expectedK8sVersion := getComponentVersion(giantswarmRelease, "kubernetes")
	expectedOSVersion := getComponentVersion(giantswarmRelease, "image")
	expectedMachineImage := MachineImagesByK8sVersion[expectedK8sVersion][expectedOSVersion]

	infraMachinePool, err := external.Get(ctx, r.Client, &machinePool.Spec.Template.Spec.InfrastructureRef, machinePool.Namespace)
	if err != nil {
		return false, err
	}

	currentMachineImage, _, err := unstructured.NestedString(infraMachinePool.Object, strings.Split(KindToMachineImagePath[infraMachinePool.GetKind()], ".")...)
	if err != nil {
		return false, errors.Wrapf(err, "failed to read current machine image from infrastructure machine pool template %q", infraMachinePool.GetName())
	}

	// If any MachinePool is not ready we exit because we don't want to
	// trigger an upgrade while something is wrong or another upgrade is in
	// progress.
	if !isReady(machinePool) {
		logger.Info("The MachinePool is not ready. Let's wait before trying to continue upgrading workers")
		return false, nil
	} else if currentMachineImage != expectedMachineImage || *machinePool.Spec.Template.Spec.Version != expectedK8sVersion || machinePool.Labels[CAPIWatchFilterLabel] != expectedCAPIVersion {
		logger.Info("The MachinePool is out of date. Updating the MachinePool and its infrastructure Machine Pool", "expectedK8sVersion", expectedK8sVersion, "currentK8sVersion", *machinePool.Spec.Template.Spec.Version, "expectedMachineImage", expectedMachineImage, "currentMachineImage", currentMachineImage, "expectedWatchFilterLabel", expectedCAPIVersion, "currentWatchFilterLabel", machinePool.Labels[CAPIWatchFilterLabel])
		machinePool.Labels[CAPIWatchFilterLabel] = expectedCAPIVersion
		machinePool.Spec.Template.Spec.Version = &expectedK8sVersion
		err = r.Client.Update(ctx, machinePool)
		if err != nil {
			return false, err
		}

		//ready, err := external.IsReady(infraMachinePool)
		//if !ready {
		//	return ctrl.Result{}, nil
		//}

		// Update infrastructure machine pool machine image to upgrade k8s or the OS.
		if err := unstructured.SetNestedField(infraMachinePool.Object, expectedMachineImage, strings.Split(KindToMachineImagePath[infraMachinePool.GetKind()], ".")...); err != nil {
			return false, errors.Wrapf(err, "failed to set infra MachinePool %q image to %q", infraMachinePool.GetName(), expectedMachineImage)
		}

		// Update watch filter label also for infrastructure machine pool.
		setCAPIWatchFilterLabel(infraMachinePool, giantswarmRelease)
		err = r.Client.Update(ctx, infraMachinePool)
		if err != nil {
			return false, err
		}

		return true, nil
	}

	logger.Info("The MachinePool is up to date", "expectedK8sVersion", expectedK8sVersion, "currentK8sVersion", *machinePool.Spec.Template.Spec.Version, "expectedMachineImage", expectedMachineImage, "currentMachineImage", currentMachineImage, "expectedWatchFilterLabel", expectedCAPIVersion, "currentWatchFilterLabel", machinePool.Labels[CAPIWatchFilterLabel])

	return false, nil
}

// upgradeMachineDeployment checks the machine image and k8s version used and
// will create a new infra Machine Template with the right versions when
// current Machine Template is out of date.
func (r *ClusterReconciler) upgradeMachineDeployment(ctx context.Context, cluster *capi.Cluster, machineDeployment *capi.MachineDeployment, giantswarmRelease *releaseapiextensions.Release) (bool, error) {
	logger := r.Log.WithValues("cluster", cluster.Name, "machineDeployment", machineDeployment.Name)

	expectedCAPIVersion := getComponentVersion(giantswarmRelease, capiReleaseComponent)
	expectedK8sVersion := getComponentVersion(giantswarmRelease, "kubernetes")
	expectedOSVersion := getComponentVersion(giantswarmRelease, "image")
	expectedMachineImage := MachineImagesByK8sVersion[expectedK8sVersion][expectedOSVersion]

	infraMachineTemplateReference := &machineDeployment.Spec.Template.Spec.InfrastructureRef

	workerNodesWillBeRolled := false

	// Fetch infrastructure machine template for the machine deployment.
	infraMachineTemplate, err := external.Get(ctx, r.Client, infraMachineTemplateReference, cluster.Namespace)
	if err != nil {
		return false, errors.Wrapf(err, "failed to retrieve infra machine template %q", infraMachineTemplateReference.Name)
	}

	// Update MachineDeployment label with CAPI release number.
	logger.Info("Setting MachineDeployment watch filter label")
	machineDeployment.Labels[CAPIWatchFilterLabel] = expectedCAPIVersion

	// Create new MachineTemplate and update MachineDeployment to use it, if updating k8s.
	currentMachineImage, _, err := unstructured.NestedString(infraMachineTemplate.Object, strings.Split(KindToMachineImagePath[infraMachineTemplateReference.Kind], ".")...)
	if err != nil {
		return false, errors.Wrapf(err, "failed to read current machine image from infrastructure machine template %q", infraMachineTemplate.GetName())
	}

	if expectedMachineImage != currentMachineImage || expectedK8sVersion != *machineDeployment.Spec.Template.Spec.Version {
		workerNodesWillBeRolled = true

		logger.Info("MachineDeployment worker nodes need to be upgraded and nodes will be rolled", "currentK8sVersion", machineDeployment.Spec.Template.Spec.Version, "expectedK8sVersion", expectedK8sVersion, "currentMachineImage", currentMachineImage, "expectedMachineImage", expectedMachineImage)
		logger.Info("Cloning infrastructure template and changing its machine image")

		// Clone infrastructure machine template to new object.
		newInfrastructureMachineTemplate, err := r.CloneTemplate(ctx, infraMachineTemplateReference, machineDeployment.Namespace, machineDeployment.Name)
		if err != nil {
			return false, errors.Wrapf(err, "failed to build infrastructure template from %q", infraMachineTemplateReference.Name)
		}

		// Update machine template machine image to upgrade k8s or the OS.
		if err := unstructured.SetNestedField(newInfrastructureMachineTemplate.Object, expectedMachineImage, strings.Split(KindToMachineImagePath[infraMachineTemplateReference.Kind], ".")...); err != nil {
			return false, errors.Wrapf(err, "failed to set infrastructure template %q image to %q", infraMachineTemplateReference.Name, expectedMachineImage)
		}

		// Create cloned object.
		if err := r.Client.Create(ctx, newInfrastructureMachineTemplate); err != nil {
			return false, errors.Wrapf(err, "failed to clone infrastructure template from %q to %q used by KubeadmControlPlane %q", infraMachineTemplateReference.Name, newInfrastructureMachineTemplate.GetName(), machineDeployment.Name)
		}

		// When upgrading the k8s cluster we need to update
		// * Name of the secret used as source for the k8s provider config file
		// * Reference to the infrastructure machine template.
		// * K8s version in the control plane object.
		kubeadmconfigtemplate := &cabpk.KubeadmConfigTemplate{}
		err = r.Client.Get(ctx, client.ObjectKey{Namespace: machineDeployment.Spec.Template.Spec.Bootstrap.ConfigRef.Namespace, Name: machineDeployment.Spec.Template.Spec.Bootstrap.ConfigRef.Name}, kubeadmconfigtemplate)
		if err != nil {
			return false, errors.Wrapf(err, "failed to retrieve workers bootstrap KubeadmConfigTemplate %q", machineDeployment.Spec.Template.Spec.Bootstrap.ConfigRef.Name)
		}

		for _, file := range kubeadmconfigtemplate.Spec.Template.Spec.Files {
			if file.ContentFrom.Secret.Name == fmt.Sprintf(KindToK8sProviderFile[newInfrastructureMachineTemplate.GetKind()], machineDeployment.Spec.Template.Spec.InfrastructureRef.Name) {
				file.ContentFrom.Secret.Name = fmt.Sprintf(KindToK8sProviderFile[newInfrastructureMachineTemplate.GetKind()], newInfrastructureMachineTemplate.GetName())
			}
		}
		err = r.Client.Update(ctx, kubeadmconfigtemplate)
		if err != nil {
			return false, errors.Wrapf(err, "failed to update KubeadmConfigTemplate %q", kubeadmconfigtemplate.Name)
		}

		machineDeployment.Spec.Template.Spec.InfrastructureRef.Name = newInfrastructureMachineTemplate.GetName()
		machineDeployment.Spec.Template.Spec.Version = to.StringPtr(expectedK8sVersion)
	}

	err = r.Client.Update(ctx, machineDeployment)
	if err != nil {
		return false, errors.Wrapf(err, "failed to update MachineDeployment %q", machineDeployment.Name)
	}

	return workerNodesWillBeRolled, nil
}

// upgradeMachinePools will fetch the MachinePools for the given cluster and
// try to upgrade k8s and OS, setting the right labels.
func (r *ClusterReconciler) upgradeMachinePools(ctx context.Context, cluster *capi.Cluster, giantswarmRelease *releaseapiextensions.Release) error {
	clusterMachinePools, err := r.getSortedClusterMachinePools(ctx, cluster)
	if err != nil {
		return err
	}

	for _, machinePool := range clusterMachinePools {
		_, err = r.upgradeMachinePool(ctx, cluster, &machinePool, giantswarmRelease)
		if err != nil {
			return err
		}
	}

	return nil
}

// upgradeMachineDeployments will fetch the MachineDeployments for the given
// cluster and try to upgrade k8s and OS, setting the right labels.
func (r *ClusterReconciler) upgradeMachineDeployments(ctx context.Context, cluster *capi.Cluster, giantswarmRelease *releaseapiextensions.Release) error {
	clusterMachineDeployments, err := r.getSortedClusterMachineDeployments(ctx, cluster)
	if err != nil {
		return err
	}

	for _, machineDeployment := range clusterMachineDeployments {
		_, err = r.upgradeMachineDeployment(ctx, cluster, &machineDeployment, giantswarmRelease)
		if err != nil {
			return err
		}
	}

	return nil
}

// upgradeWorkers will upgrade both MachinePools and MachineDeployments.
// While it's technically possible for a cluster to have both types of node
// pools, we assume no cluster would have both at the same time.
func (r *ClusterReconciler) upgradeWorkers(ctx context.Context, cluster *capi.Cluster, giantswarmRelease *releaseapiextensions.Release) error {
	err := r.upgradeMachinePools(ctx, cluster, giantswarmRelease)
	if err != nil {
		return err
	}

	err = r.upgradeMachineDeployments(ctx, cluster, giantswarmRelease)
	if err != nil {
		return err
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
