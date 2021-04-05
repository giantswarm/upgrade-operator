package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/Azure/go-autorest/autorest/to"
	releaseapiextensions "github.com/giantswarm/apiextensions/pkg/apis/release/v1alpha1"
	apiextensionslabel "github.com/giantswarm/apiextensions/pkg/label"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/reference"
	capz "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	capzexp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1alpha3"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	cabpk "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	kcp "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	capiexp "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestUpgradeControlPlaneK8sVersionWithMachinePools(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.4.0"},
			},
		},
	}

	cluster, kubeadmcontrolplane, azureCluster, azureMachineTemplate := newAzureClusterWithControlPlane()

	machinePool1, azureMachinePool1 := newAzureMachinePoolChain(cluster.Name)

	machinePool1.Status = capiexp.MachinePoolStatus{
		Conditions: capi.Conditions{capi.Condition{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		}},
	}

	machinePool2, azureMachinePool2 := newAzureMachinePoolChain(cluster.Name)

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, azureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, azureMachinePool1, machinePool1, azureMachinePool2, machinePool2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}})
	if err != nil {
		t.Fatal(err)
	}

	// Assert KubeadmControlPlane version and label were updated.
	reconciledControlplane := &kcp.KubeadmControlPlane{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: kubeadmcontrolplane.Namespace, Name: kubeadmcontrolplane.Name}, reconciledControlplane)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("v1.18.14", reconciledControlplane.Spec.Version, fmt.Sprintf("KubeadmControlPlane %q has wrong k8s version in KubeadmControlPlane.Spec.Version field", reconciledControlplane.Name))
	assert.Equal("0.3.14", reconciledControlplane.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in KubeadmControlPlane %q", CAPIWatchFilterLabel, reconciledControlplane.Name))

	foundProviderFile := false
	expectedProviderFile := fmt.Sprintf("%s-azure-json", reconciledControlplane.Spec.InfrastructureTemplate.Name)
	for _, file := range reconciledControlplane.Spec.KubeadmConfigSpec.Files {
		if file.ContentFrom.Secret.Name == expectedProviderFile {
			foundProviderFile = true
		}
	}
	if !foundProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", reconciledControlplane.Spec.KubeadmConfigSpec.Files, expectedProviderFile)
	}

	// Assert AzureMachineTemplate used by KubeadmControlPlane uses right machine image.
	newAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledControlplane.Spec.InfrastructureTemplate.Namespace, Name: reconciledControlplane.Spec.InfrastructureTemplate.Name}, newAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("AzureMachineTemplate %q image is wrong", newAzureMachineTemplate.Name))

	// Assert CAPI CR's are labeled correctly.
	reconciledCluster := &capi.Cluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, reconciledCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in Cluster %q", CAPIWatchFilterLabel, reconciledCluster.Name))

	reconciledMachinePool1 := &capiexp.MachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool1.Namespace, Name: machinePool1.Name}, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q got updated in MachinePool %q but it shouldn't when ControlPlane is upgrading", CAPIWatchFilterLabel, reconciledMachinePool1.Name))

	// Assert CAPZ CR's are labeled correctly.
	reconciledAzureCluster := &capz.AzureCluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureCluster.Namespace, Name: azureCluster.Name}, reconciledAzureCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureCluster %q", CAPIWatchFilterLabel, reconciledAzureCluster.Name))

	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.10", reconciledAzureMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q got updated in AzureMachinePool %q but it shouldn't when ControlPlane is upgrading", CAPIWatchFilterLabel, reconciledAzureMachinePool1.Name))
	reconciledAzureMachinePool2 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.10", reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureMachinePool %q", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name))
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image shouldn't have changed", reconciledAzureMachinePool1.Name))
}

func TestUpgradeWorkersK8sVersionWithMachinePools(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.4.0"},
			},
		},
	}

	cpAzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot14-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	cpAzureMachineTemplateReference, err := reference.GetReference(scheme, cpAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1-18-14",
			Labels: map[string]string{
				cacpReleaseComponent:  "0.3.14",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas:               to.Int32Ptr(1),
			Version:                "v1.18.14",
			InfrastructureTemplate: *cpAzureMachineTemplateReference,
			KubeadmConfigSpec: cabpk.KubeadmConfigSpec{
				Files: []cabpk.File{
					{
						ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", cpAzureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.12",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capz.AzureClusterSpec{
			ResourceGroup: "",
		},
	}
	azureClusterReference, err := reference.GetReference(scheme, azureCluster)
	if err != nil {
		t.Fatal(err)
	}
	cluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "0.3.14",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	machinePool1, azureMachinePool1 := newAzureMachinePoolChain(cluster.Name)

	machinePool1.Status = capiexp.MachinePoolStatus{
		Conditions: capi.Conditions{capi.Condition{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		}},
	}

	machinePool2, azureMachinePool2 := newAzureMachinePoolChain(cluster.Name)

	machinePool2.Status = capiexp.MachinePoolStatus{
		Conditions: capi.Conditions{capi.Condition{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		}},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, cpAzureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, azureMachinePool1, machinePool1, azureMachinePool2, machinePool2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}})
	if err != nil {
		t.Fatal(err)
	}

	// Assert KubeadmControlPlane version and label were updated.
	reconciledControlplane := &kcp.KubeadmControlPlane{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: kubeadmcontrolplane.Namespace, Name: kubeadmcontrolplane.Name}, reconciledControlplane)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("v1.18.14", reconciledControlplane.Spec.Version, fmt.Sprintf("KubeadmControlPlane %q has wrong k8s version in KubeadmControlPlane.Spec.Version field", reconciledControlplane.Name))
	assert.Equal("0.3.14", reconciledControlplane.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in KubeadmControlPlane %q", CAPIWatchFilterLabel, reconciledControlplane.Name))

	foundProviderFile := false
	expectedProviderFile := fmt.Sprintf("%s-azure-json", reconciledControlplane.Spec.InfrastructureTemplate.Name)
	for _, file := range reconciledControlplane.Spec.KubeadmConfigSpec.Files {
		if file.ContentFrom.Secret.Name == expectedProviderFile {
			foundProviderFile = true
		}
	}
	if !foundProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", reconciledControlplane.Spec.KubeadmConfigSpec.Files, expectedProviderFile)
	}

	// Assert AzureMachineTemplate used by KubeadmControlPlane uses right machine image.
	newAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledControlplane.Spec.InfrastructureTemplate.Namespace, Name: reconciledControlplane.Spec.InfrastructureTemplate.Name}, newAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("AzureMachineTemplate %q image is wrong", newAzureMachineTemplate.Name))

	// Assert CAPI CR's are labeled correctly.
	reconciledCluster := &capi.Cluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, reconciledCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in Cluster %q", CAPIWatchFilterLabel, reconciledCluster.Name))

	reconciledMachinePool1 := &capiexp.MachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool1.Namespace, Name: machinePool1.Name}, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q should have been updated in MachinePool %q to new operator version", CAPIWatchFilterLabel, reconciledMachinePool1.Name))

	// Assert CAPZ CR's are labeled correctly.
	reconciledAzureCluster := &capz.AzureCluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureCluster.Namespace, Name: azureCluster.Name}, reconciledAzureCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q should have been updated in MachinePool %q to new operator version", CAPIWatchFilterLabel, reconciledAzureCluster.Name))

	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q should have been updated in MachinePool %q to new operator version", CAPIWatchFilterLabel, reconciledAzureMachinePool1.Name))
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image should have been updated to new k8s version", reconciledAzureMachinePool1.Name))

	reconciledAzureMachinePool2 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.10", reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in AzureMachinePool %q got updated but shouldn't because another MachinePool is being upgraded", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name))
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", reconciledAzureMachinePool2.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image got updated but shouldn't because another MachinePool is being upgraded", reconciledAzureMachinePool2.Name))
}

func TestMachinePoolsAreUpgradedOneAfterAnother(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.4.0"},
			},
		},
	}

	cpAzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot14-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	cpAzureMachineTemplateReference, err := reference.GetReference(scheme, cpAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1-18-14",
			Labels: map[string]string{
				cacpReleaseComponent:  "v0.3.14",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas:               to.Int32Ptr(1),
			Version:                "v1.18.14",
			InfrastructureTemplate: *cpAzureMachineTemplateReference,
			KubeadmConfigSpec: cabpk.KubeadmConfigSpec{
				Files: []cabpk.File{
					{
						ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", cpAzureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.12",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capz.AzureClusterSpec{
			ResourceGroup: "",
		},
	}
	azureClusterReference, err := reference.GetReference(scheme, azureCluster)
	if err != nil {
		t.Fatal(err)
	}
	cluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "0.3.14",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	machinePool1, azureMachinePool1 := newAzureMachinePoolChain(cluster.Name)

	machinePool1.Status = capiexp.MachinePoolStatus{
		Conditions: capi.Conditions{capi.Condition{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		}},
	}

	machinePool2, azureMachinePool2 := newAzureMachinePoolChain(cluster.Name)

	machinePool2.Status = capiexp.MachinePoolStatus{
		Conditions: capi.Conditions{capi.Condition{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		}},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, cpAzureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, azureMachinePool1, machinePool1, azureMachinePool2, machinePool2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}})
	if err != nil {
		t.Fatal(err)
	}

	reconciledMachinePool1 := &capiexp.MachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool1.Namespace, Name: machinePool1.Name}, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q should have been updated in MachinePool %q to new operator version", CAPIWatchFilterLabel, reconciledMachinePool1.Name))

	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q should have been updated in MachinePool %q to new operator version", CAPIWatchFilterLabel, reconciledAzureMachinePool1.Name))
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image should have been updated to new k8s version", reconciledAzureMachinePool1.Name))

	reconciledMachinePool2 := &capiexp.MachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool2.Namespace, Name: machinePool2.Name}, reconciledMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachinePool %q got updated but shouldn't because another MachinePool is being upgraded", CAPIWatchFilterLabel, reconciledMachinePool2.Name))

	reconciledAzureMachinePool2 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.10", reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in AzureMachinePool %q got updated but shouldn't because another MachinePool is being upgraded", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name))
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", reconciledAzureMachinePool2.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image got updated but shouldn't because another MachinePool is being upgraded", reconciledAzureMachinePool2.Name))

	// At this point another controller would pick up the change and make the MachinePool not ready.
	// We need to simulate that to be able to test that case.
	reconciledMachinePool1.Status.Conditions = []capi.Condition{
		{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionFalse,
		},
	}

	err = ctrlClient.Status().Update(ctx, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}})
	if err != nil {
		t.Fatal(err)
	}

	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool2.Namespace, Name: machinePool2.Name}, reconciledMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachinePool %q got updated but shouldn't because another MachinePool is being upgraded", CAPIWatchFilterLabel, reconciledMachinePool2.Name))

	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.10", reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in AzureMachinePool %q got updated but shouldn't because another MachinePool is being upgraded", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name))
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", reconciledAzureMachinePool2.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image got updated but shouldn't because another MachinePool is being upgraded", reconciledAzureMachinePool2.Name))

	// Now let's simulate that first machine pool finished upgrading.
	reconciledMachinePool1.Status.Conditions = []capi.Condition{
		{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		},
	}

	err = ctrlClient.Status().Update(ctx, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}})
	if err != nil {
		t.Fatal(err)
	}

	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool2.Namespace, Name: machinePool2.Name}, reconciledMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachinePool %q should've been upgraded now that previous MachinePool is done upgrading and ready", CAPIWatchFilterLabel, reconciledMachinePool2.Name))

	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in AzureMachinePool %q should've been upgraded now that previous MachinePool is done upgrading and ready", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name))
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", reconciledAzureMachinePool2.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s machine image got updated but shouldn't because another MachinePool is being upgraded", reconciledAzureMachinePool2.Name))
}

func TestUpgradeControlPlaneK8sVersionWithMachineDeployments(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = cabpk.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.4.0"},
			},
		},
	}

	cpAzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot2-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	cpAzureMachineTemplateReference, err := reference.GetReference(scheme, cpAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1-18-2",
			Labels: map[string]string{
				cacpReleaseComponent:  "v0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas:               to.Int32Ptr(1),
			Version:                "v1.18.2",
			InfrastructureTemplate: *cpAzureMachineTemplateReference,
			KubeadmConfigSpec: cabpk.KubeadmConfigSpec{
				Files: []cabpk.File{
					{
						ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", cpAzureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capz.AzureClusterSpec{
			ResourceGroup: "",
		},
	}
	azureClusterReference, err := reference.GetReference(scheme, azureCluster)
	if err != nil {
		t.Fatal(err)
	}
	cluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "0.3.10",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	workersAzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot2-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	workersAzureMachineTemplateReference, err := reference.GetReference(scheme, workersAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmconfigTemplate := &cabpk.KubeadmConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01",
		},
		Spec: cabpk.KubeadmConfigTemplateSpec{
			Template: cabpk.KubeadmConfigTemplateResource{
				Spec: cabpk.KubeadmConfigSpec{
					Files: []cabpk.File{
						{
							ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", workersAzureMachineTemplate.Name)}},
						},
					},
				},
			},
		},
	}
	kubeadmconfigTemplateReference, err := reference.GetReference(scheme, kubeadmconfigTemplate)
	if err != nil {
		t.Fatal(err)
	}

	machineDeployment1 := &capi.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capi.MachineDeploymentSpec{
			ClusterName: cluster.Name,
			Replicas:    to.Int32Ptr(3),
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					Bootstrap: capi.Bootstrap{
						ConfigRef: kubeadmconfigTemplateReference,
					},
					InfrastructureRef: *workersAzureMachineTemplateReference,
					Version:           to.StringPtr("v1.18.2"),
				},
			},
		},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, cpAzureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, kubeadmconfigTemplate, workersAzureMachineTemplate, machineDeployment1)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-cluster"}})
	if err != nil {
		t.Fatal(err)
	}

	// Assert KubeadmControlPlane version and label were updated.
	reconciledControlplane := &kcp.KubeadmControlPlane{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: kubeadmcontrolplane.Namespace, Name: kubeadmcontrolplane.Name}, reconciledControlplane)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("v1.18.14", reconciledControlplane.Spec.Version, fmt.Sprintf("KubeadmControlPlane %q has wrong k8s version in KubeadmControlPlane.Spec.Version field", reconciledControlplane.Name))
	assert.Equal("0.3.14", reconciledControlplane.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in KubeadmControlPlane %q", CAPIWatchFilterLabel, reconciledControlplane.Name))

	foundCPProviderFile := false
	expectedCPProviderFile := fmt.Sprintf("%s-azure-json", reconciledControlplane.Spec.InfrastructureTemplate.Name)
	for _, file := range reconciledControlplane.Spec.KubeadmConfigSpec.Files {
		if file.ContentFrom.Secret.Name == expectedCPProviderFile {
			foundCPProviderFile = true
		}
	}
	if !foundCPProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", reconciledControlplane.Spec.KubeadmConfigSpec.Files, expectedCPProviderFile)
	}

	// Assert AzureMachineTemplate used by KubeadmControlPlane uses right machine image.
	newCPAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledControlplane.Spec.InfrastructureTemplate.Namespace, Name: reconciledControlplane.Spec.InfrastructureTemplate.Name}, newCPAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", newCPAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Control Plane AzureMachineTemplate %q image is wrong", newCPAzureMachineTemplate.Name))

	// Assert CAPI CR's are labeled correctly.
	reconciledCluster := &capi.Cluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, reconciledCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in Cluster %q", CAPIWatchFilterLabel, reconciledCluster.Name))

	reconciledMachineDeployment1 := &capi.MachineDeployment{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machineDeployment1.Namespace, Name: machineDeployment1.Name}, reconciledMachineDeployment1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachineDeployment1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q got updated in MachineDeployment %q but shouldn't because control plane is being upgraded", CAPIWatchFilterLabel, reconciledMachineDeployment1.Name))
	assert.Equal("v1.18.2", *reconciledMachineDeployment1.Spec.Template.Spec.Version, fmt.Sprintf("MachineDeployment %q k8s version was upgraded in its MachineDeployment.Spec.Template.Spec.Version field but shouldn't because control plane is being upgraded", reconciledMachineDeployment1.Name))

	reconciledKubeadmconfigTemplate := &cabpk.KubeadmConfigTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment1.Spec.Template.Spec.Bootstrap.ConfigRef.Namespace, Name: reconciledMachineDeployment1.Spec.Template.Spec.Bootstrap.ConfigRef.Name}, reconciledKubeadmconfigTemplate)
	if err != nil {
		t.Fatal(err)
	}

	seenFiles := []string{}
	foundWorkersProviderFile := false
	expectedWorkersProviderFile := fmt.Sprintf("%s-azure-json", reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Name)
	for _, file := range reconciledKubeadmconfigTemplate.Spec.Template.Spec.Files {
		seenFiles = append(seenFiles, file.ContentFrom.Secret.Name)
		if file.ContentFrom.Secret.Name == expectedWorkersProviderFile {
			foundWorkersProviderFile = true
		}
	}
	if !foundWorkersProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", seenFiles, expectedWorkersProviderFile)
	}

	// Assert workers AzureMachineTemplate used by MachineDeployment uses right machine image.
	newWorkersAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Namespace, Name: reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Name}, newWorkersAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", newWorkersAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Workers AzureMachineTemplate %q image was upgraded but shouldn't because control plane is being upgraded", newWorkersAzureMachineTemplate.Name))

	// Assert CAPZ CR's are labeled correctly.
	reconciledAzureCluster := &capz.AzureCluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureCluster.Namespace, Name: azureCluster.Name}, reconciledAzureCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureCluster %q", CAPIWatchFilterLabel, reconciledAzureCluster.Name))
}

func TestUpgradeWorkersK8sVersionWithMachineDeployments(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = cabpk.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.4.0"},
			},
		},
	}

	cpAzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot14-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	cpAzureMachineTemplateReference, err := reference.GetReference(scheme, cpAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1-18-14",
			Labels: map[string]string{
				cacpReleaseComponent:  "v0.3.14",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas:               to.Int32Ptr(1),
			Version:                "v1.18.14",
			InfrastructureTemplate: *cpAzureMachineTemplateReference,
			KubeadmConfigSpec: cabpk.KubeadmConfigSpec{
				Files: []cabpk.File{
					{
						ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", cpAzureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.12",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capz.AzureClusterSpec{
			ResourceGroup: "",
		},
	}
	azureClusterReference, err := reference.GetReference(scheme, azureCluster)
	if err != nil {
		t.Fatal(err)
	}
	cluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "0.3.14",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	machineDeployment1AzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot2-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	machineDeployment1AzureMachineTemplateReference, err := reference.GetReference(scheme, machineDeployment1AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	machineDeployment2AzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md02-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot2-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	machineDeployment2AzureMachineTemplateReference, err := reference.GetReference(scheme, machineDeployment2AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmconfigTemplate := &cabpk.KubeadmConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01",
		},
		Spec: cabpk.KubeadmConfigTemplateSpec{
			Template: cabpk.KubeadmConfigTemplateResource{
				Spec: cabpk.KubeadmConfigSpec{
					Files: []cabpk.File{
						{
							ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", machineDeployment1AzureMachineTemplate.Name)}},
						},
					},
				},
			},
		},
	}
	kubeadmconfigTemplateReference, err := reference.GetReference(scheme, kubeadmconfigTemplate)
	if err != nil {
		t.Fatal(err)
	}

	machineDeployment1 := &capi.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capi.MachineDeploymentSpec{
			ClusterName: cluster.Name,
			Replicas:    to.Int32Ptr(3),
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					Bootstrap: capi.Bootstrap{
						ConfigRef: kubeadmconfigTemplateReference,
					},
					InfrastructureRef: *machineDeployment1AzureMachineTemplateReference,
					Version:           to.StringPtr("v1.18.2"),
				},
			},
		},
		Status: capi.MachineDeploymentStatus{
			UpdatedReplicas:   3,
			Replicas:          3,
			AvailableReplicas: 3,
		},
	}

	machineDeployment2 := &capi.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md02",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capi.MachineDeploymentSpec{
			ClusterName: cluster.Name,
			Replicas:    to.Int32Ptr(3),
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					Bootstrap: capi.Bootstrap{
						ConfigRef: kubeadmconfigTemplateReference,
					},
					InfrastructureRef: *machineDeployment2AzureMachineTemplateReference,
					Version:           to.StringPtr("v1.18.2"),
				},
			},
		},
		Status: capi.MachineDeploymentStatus{
			UpdatedReplicas:   3,
			Replicas:          3,
			AvailableReplicas: 3,
		},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, cpAzureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, kubeadmconfigTemplate, machineDeployment1AzureMachineTemplate, machineDeployment1, machineDeployment2AzureMachineTemplate, machineDeployment2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-cluster"}})
	if err != nil {
		t.Fatal(err)
	}

	// Assert KubeadmControlPlane version and label were updated.
	reconciledControlplane := &kcp.KubeadmControlPlane{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: kubeadmcontrolplane.Namespace, Name: kubeadmcontrolplane.Name}, reconciledControlplane)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("v1.18.14", reconciledControlplane.Spec.Version, fmt.Sprintf("KubeadmControlPlane %q has wrong k8s version in KubeadmControlPlane.Spec.Version field", reconciledControlplane.Name))
	assert.Equal("0.3.14", reconciledControlplane.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in KubeadmControlPlane %q", CAPIWatchFilterLabel, reconciledControlplane.Name))

	foundCPProviderFile := false
	expectedCPProviderFile := fmt.Sprintf("%s-azure-json", reconciledControlplane.Spec.InfrastructureTemplate.Name)
	for _, file := range reconciledControlplane.Spec.KubeadmConfigSpec.Files {
		if file.ContentFrom.Secret.Name == expectedCPProviderFile {
			foundCPProviderFile = true
		}
	}
	if !foundCPProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", reconciledControlplane.Spec.KubeadmConfigSpec.Files, expectedCPProviderFile)
	}

	// Assert AzureMachineTemplate used by KubeadmControlPlane uses right machine image.
	newCPAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledControlplane.Spec.InfrastructureTemplate.Namespace, Name: reconciledControlplane.Spec.InfrastructureTemplate.Name}, newCPAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", newCPAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Control Plane AzureMachineTemplate %q image is wrong", newCPAzureMachineTemplate.Name))

	reconciledCluster := &capi.Cluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, reconciledCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in Cluster %q", CAPIWatchFilterLabel, reconciledCluster.Name))

	reconciledAzureCluster := &capz.AzureCluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureCluster.Namespace, Name: azureCluster.Name}, reconciledAzureCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.4.12", reconciledAzureCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureCluster %q", CAPIWatchFilterLabel, reconciledAzureCluster.Name))

	reconciledMachineDeployment1 := &capi.MachineDeployment{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machineDeployment1.Namespace, Name: machineDeployment1.Name}, reconciledMachineDeployment1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledMachineDeployment1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachineDeployment %q should have been upgraded", CAPIWatchFilterLabel, reconciledMachineDeployment1.Name))
	assert.Equal("v1.18.14", *reconciledMachineDeployment1.Spec.Template.Spec.Version, fmt.Sprintf("MachineDeployment %q k8s version should have been upgraded in its MachineDeployment.Spec.Template.Spec.Version field but shouldn't because control plane is being upgraded", reconciledMachineDeployment1.Name))

	reconciledKubeadmconfigTemplate := &cabpk.KubeadmConfigTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment1.Spec.Template.Spec.Bootstrap.ConfigRef.Namespace, Name: reconciledMachineDeployment1.Spec.Template.Spec.Bootstrap.ConfigRef.Name}, reconciledKubeadmconfigTemplate)
	if err != nil {
		t.Fatal(err)
	}

	seenFiles := []string{}
	foundWorkersProviderFile := false
	expectedWorkersProviderFile := fmt.Sprintf("%s-azure-json", reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Name)
	for _, file := range reconciledKubeadmconfigTemplate.Spec.Template.Spec.Files {
		seenFiles = append(seenFiles, file.ContentFrom.Secret.Name)
		if file.ContentFrom.Secret.Name == expectedWorkersProviderFile {
			foundWorkersProviderFile = true
		}
	}
	if !foundWorkersProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", seenFiles, expectedWorkersProviderFile)
	}

	// Assert workers AzureMachineTemplate used by MachineDeployment uses right machine image.
	newMachineDeployment1AzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Namespace, Name: reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Name}, newMachineDeployment1AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", newMachineDeployment1AzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Workers AzureMachineTemplate %q image should have been upgraded", newMachineDeployment1AzureMachineTemplate.Name))

	reconciledMachineDeployment2 := &capi.MachineDeployment{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machineDeployment2.Namespace, Name: machineDeployment2.Name}, reconciledMachineDeployment2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachineDeployment2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachineDeployment %q shouldn't have been upgraded because another MachineDeployment is being upgraded", CAPIWatchFilterLabel, reconciledMachineDeployment2.Name))
	assert.Equal("v1.18.2", *reconciledMachineDeployment2.Spec.Template.Spec.Version, fmt.Sprintf("MachineDeployment %q k8s version shouldn't have been upgraded because another MachineDeployment is being upgraded", reconciledMachineDeployment2.Name))

	// Assert workers AzureMachineTemplate used by MachineDeployment uses right machine image.
	newMachineDeployment2AzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment2.Spec.Template.Spec.InfrastructureRef.Namespace, Name: reconciledMachineDeployment2.Spec.Template.Spec.InfrastructureRef.Name}, newMachineDeployment2AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", newMachineDeployment2AzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Workers AzureMachineTemplate %q image shouldn't have been upgraded because another MachineDeployment is being upgraded", newMachineDeployment2AzureMachineTemplate.Name))
}

func TestMachineDeploymentIsNotUpgradedIfThereAreNotReadyMachineDeployments(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = cabpk.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.4.0"},
			},
		},
	}

	cpAzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot14-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	cpAzureMachineTemplateReference, err := reference.GetReference(scheme, cpAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane-1-18-14",
			Labels: map[string]string{
				cacpReleaseComponent:  "v0.3.14",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas:               to.Int32Ptr(1),
			Version:                "v1.18.14",
			InfrastructureTemplate: *cpAzureMachineTemplateReference,
			KubeadmConfigSpec: cabpk.KubeadmConfigSpec{
				Files: []cabpk.File{
					{
						ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", cpAzureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.12",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capz.AzureClusterSpec{
			ResourceGroup: "",
		},
	}
	azureClusterReference, err := reference.GetReference(scheme, azureCluster)
	if err != nil {
		t.Fatal(err)
	}
	cluster := &capi.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "0.3.14",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	machineDeployment1AzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot2-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	machineDeployment1AzureMachineTemplateReference, err := reference.GetReference(scheme, machineDeployment1AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	machineDeployment2AzureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md02-1a2b3c",
			Labels:    map[string]string{capi.ClusterLabelName: "my-cluster"},
		},
		Spec: capz.AzureMachineTemplateSpec{Template: capz.AzureMachineTemplateResource{Spec: capz.AzureMachineSpec{
			Image: &capz.Image{
				Marketplace: &capz.AzureMarketplaceImage{
					Publisher: "cncf-upstream",
					Offer:     "capi",
					SKU:       "k8s-1dot18dot2-ubuntu-1804",
					Version:   "latest",
				},
			},
		}}},
	}
	machineDeployment2AzureMachineTemplateReference, err := reference.GetReference(scheme, machineDeployment2AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmconfigTemplate := &cabpk.KubeadmConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01",
		},
		Spec: cabpk.KubeadmConfigTemplateSpec{
			Template: cabpk.KubeadmConfigTemplateResource{
				Spec: cabpk.KubeadmConfigSpec{
					Files: []cabpk.File{
						{
							ContentFrom: &cabpk.FileSource{Secret: cabpk.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", machineDeployment1AzureMachineTemplate.Name)}},
						},
					},
				},
			},
		},
	}
	kubeadmconfigTemplateReference, err := reference.GetReference(scheme, kubeadmconfigTemplate)
	if err != nil {
		t.Fatal(err)
	}

	machineDeployment1 := &capi.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capi.MachineDeploymentSpec{
			ClusterName: cluster.Name,
			Replicas:    to.Int32Ptr(3),
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					Bootstrap: capi.Bootstrap{
						ConfigRef: kubeadmconfigTemplateReference,
					},
					InfrastructureRef: *machineDeployment1AzureMachineTemplateReference,
					Version:           to.StringPtr("v1.18.2"),
				},
			},
		},
		Status: capi.MachineDeploymentStatus{
			UpdatedReplicas:   3,
			Replicas:          3,
			AvailableReplicas: 3,
		},
	}

	machineDeployment2 := &capi.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "md02",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capi.MachineDeploymentSpec{
			ClusterName: cluster.Name,
			Replicas:    to.Int32Ptr(3),
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					Bootstrap: capi.Bootstrap{
						ConfigRef: kubeadmconfigTemplateReference,
					},
					InfrastructureRef: *machineDeployment2AzureMachineTemplateReference,
					Version:           to.StringPtr("v1.18.2"),
				},
			},
		},
		Status: capi.MachineDeploymentStatus{
			UpdatedReplicas:   3,
			Replicas:          3,
			AvailableReplicas: 3,
		},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, cpAzureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, kubeadmconfigTemplate, machineDeployment1AzureMachineTemplate, machineDeployment1, machineDeployment2AzureMachineTemplate, machineDeployment2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-cluster"}})
	if err != nil {
		t.Fatal(err)
	}

	reconciledMachineDeployment1 := &capi.MachineDeployment{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machineDeployment1.Namespace, Name: machineDeployment1.Name}, reconciledMachineDeployment1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.14", reconciledMachineDeployment1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachineDeployment %q should have been upgraded", CAPIWatchFilterLabel, reconciledMachineDeployment1.Name))
	assert.Equal("v1.18.14", *reconciledMachineDeployment1.Spec.Template.Spec.Version, fmt.Sprintf("MachineDeployment %q k8s version should have been upgraded in its MachineDeployment.Spec.Template.Spec.Version field but shouldn't because control plane is being upgraded", reconciledMachineDeployment1.Name))

	newMachineDeployment1AzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Namespace, Name: reconciledMachineDeployment1.Spec.Template.Spec.InfrastructureRef.Name}, newMachineDeployment1AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot14-ubuntu-1804", newMachineDeployment1AzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Workers AzureMachineTemplate %q image should have been upgraded", newMachineDeployment1AzureMachineTemplate.Name))

	reconciledMachineDeployment2 := &capi.MachineDeployment{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machineDeployment2.Namespace, Name: machineDeployment2.Name}, reconciledMachineDeployment2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachineDeployment2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachineDeployment %q shouldn't have been upgraded because another MachineDeployment is being upgraded", CAPIWatchFilterLabel, reconciledMachineDeployment2.Name))
	assert.Equal("v1.18.2", *reconciledMachineDeployment2.Spec.Template.Spec.Version, fmt.Sprintf("MachineDeployment %q k8s version shouldn't have been upgraded because another MachineDeployment is being upgraded", reconciledMachineDeployment2.Name))

	// Assert workers AzureMachineTemplate used by MachineDeployment uses right machine image.
	newMachineDeployment2AzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment2.Spec.Template.Spec.InfrastructureRef.Namespace, Name: reconciledMachineDeployment2.Spec.Template.Spec.InfrastructureRef.Name}, newMachineDeployment2AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", newMachineDeployment2AzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Workers AzureMachineTemplate %q image shouldn't have been upgraded because another MachineDeployment is being upgraded", newMachineDeployment2AzureMachineTemplate.Name))

	reconciledMachineDeployment1.Status = capi.MachineDeploymentStatus{UpdatedReplicas: 0}
	err = ctrlClient.Status().Update(ctx, reconciledMachineDeployment1)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-cluster"}})
	if err != nil {
		t.Fatal(err)
	}

	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machineDeployment2.Namespace, Name: machineDeployment2.Name}, reconciledMachineDeployment2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("0.3.10", reconciledMachineDeployment2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q in MachineDeployment %q shouldn't have been upgraded because another MachineDeployment is being upgraded", CAPIWatchFilterLabel, reconciledMachineDeployment2.Name))
	assert.Equal("v1.18.2", *reconciledMachineDeployment2.Spec.Template.Spec.Version, fmt.Sprintf("MachineDeployment %q k8s version shouldn't have been upgraded because another MachineDeployment is being upgraded", reconciledMachineDeployment2.Name))

	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledMachineDeployment2.Spec.Template.Spec.InfrastructureRef.Namespace, Name: reconciledMachineDeployment2.Spec.Template.Spec.InfrastructureRef.Name}, newMachineDeployment2AzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", newMachineDeployment2AzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("Workers AzureMachineTemplate %q image shouldn't have been upgraded because another MachineDeployment is being upgraded", newMachineDeployment2AzureMachineTemplate.Name))
}

func TestUpgradeControlPlaneOSVersionWithMachinePools(t *testing.T) {
	assert := assert.New(t)

	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.2"},
				{Name: capiReleaseComponent, Version: "0.3.14"},
				{Name: cacpReleaseComponent, Version: "0.3.14"},
				{Name: capzReleaseComponent, Version: "0.4.12"},
				{Name: "image", Version: "18.10.0"},
			},
		},
	}

	cluster, kubeadmcontrolplane, azureCluster, azureMachineTemplate := newAzureClusterWithControlPlane()

	machinePool1, azureMachinePool1 := newAzureMachinePoolChain(cluster.Name)

	machinePool1.Status = capiexp.MachinePoolStatus{
		Conditions: capi.Conditions{capi.Condition{
			Type:   capi.ReadyCondition,
			Status: corev1.ConditionTrue,
		}},
	}

	machinePool2, azureMachinePool2 := newAzureMachinePoolChain(cluster.Name)

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, azureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, azureMachinePool1, machinePool1, azureMachinePool2, machinePool2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}})
	if err != nil {
		t.Fatal(err)
	}

	// Assert KubeadmControlPlane version is correct.
	reconciledControlplane := &kcp.KubeadmControlPlane{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: kubeadmcontrolplane.Namespace, Name: kubeadmcontrolplane.Name}, reconciledControlplane)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("v1.18.2", reconciledControlplane.Spec.Version, "Kubeadmcontrolplane.Spec.Version uses wrong k8s version")
	assert.Equal("0.3.14", reconciledControlplane.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in KubeadmControlPlane %q", CAPIWatchFilterLabel, reconciledControlplane.Name))

	foundProviderFile := false
	expectedProviderFile := fmt.Sprintf("%s-azure-json", reconciledControlplane.Spec.InfrastructureTemplate.Name)
	for _, file := range reconciledControlplane.Spec.KubeadmConfigSpec.Files {
		if file.ContentFrom.Secret.Name == expectedProviderFile {
			foundProviderFile = true
		}
	}
	if !foundProviderFile {
		t.Fatalf("None of the defined files match the infrastructure machine template name. The name of the provider file needs to match the infrastructure machine template name, got these files %v, expected name %q", reconciledControlplane.Spec.KubeadmConfigSpec.Files, expectedProviderFile)
	}

	// Assert AzureMachineTemplate used by KubeadmControlPlane uses right machine image.
	newAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledControlplane.Spec.InfrastructureTemplate.Namespace, Name: reconciledControlplane.Spec.InfrastructureTemplate.Name}, newAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot2-ubuntu-1810", newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, "AzureMachineTemplate image is wrong")

	// Assert node pool uses right machine image.
	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal("k8s-1dot18dot2-ubuntu-1804", reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, "AzureMachinePool image got upgraded but shouldn't because control plane is being upgraded")
}

// HELPERS

func newCluster() *capi.Cluster {
	name := fmt.Sprintf("test-cluster-%s", util.RandomString(4))
	return &capi.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: capi.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "0.3.10",
			},
		},
	}
}

func newKubeadmControlPlane(cluster string) *kcp.KubeadmControlPlane {
	name := fmt.Sprintf("%s-control-plane-%s", cluster, util.RandomString(4))
	return &kcp.KubeadmControlPlane{
		TypeMeta: metav1.TypeMeta{
			Kind:       "KubeadmControlPlane",
			APIVersion: kcp.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels: map[string]string{
				cacpReleaseComponent:  "0.3.10",
				capi.ClusterLabelName: cluster,
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas: to.Int32Ptr(1),
			Version:  "v1.18.2",
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: corev1.ConditionTrue,
			}},
		},
	}
}

func newClusterWithControlPlane() (*capi.Cluster, *kcp.KubeadmControlPlane) {
	cluster := newCluster()
	kcp := newKubeadmControlPlane(cluster.Name)
	cluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		Kind:       kcp.Kind,
		Namespace:  kcp.Namespace,
		Name:       kcp.Name,
		APIVersion: kcp.APIVersion,
	}
	kcp.ObjectMeta.OwnerReferences = append(kcp.ObjectMeta.OwnerReferences, metav1.OwnerReference{
		Kind:       cluster.Kind,
		Name:       cluster.Name,
		UID:        cluster.UID,
		APIVersion: cluster.APIVersion,
	})
	return cluster, kcp
}

func newAzureCluster(name string) *capz.AzureCluster {
	return &capz.AzureCluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "AzureCluster",
			APIVersion: capz.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.10",
				capi.ClusterLabelName: name,
			},
		},
		Spec: capz.AzureClusterSpec{
			ResourceGroup: "",
		},
	}
}

func newAzureCapiImage() *capz.Image {
	return &capz.Image{
		Marketplace: &capz.AzureMarketplaceImage{
			Publisher: "cncf-upstream",
			Offer:     "capi",
			SKU:       "k8s-1dot18dot2-ubuntu-1804",
			Version:   "latest",
		},
	}
}

func newAzureMachineTemplate(cluster string) *capz.AzureMachineTemplate {
	name := fmt.Sprintf("%s-control-plane-%s", cluster, util.RandomString(4))
	return &capz.AzureMachineTemplate{
		TypeMeta: metav1.TypeMeta{
			Kind:       "AzureMachineTemplate",
			APIVersion: capz.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels:    map[string]string{capi.ClusterLabelName: cluster},
		},
		Spec: capz.AzureMachineTemplateSpec{
			Template: capz.AzureMachineTemplateResource{
				Spec: capz.AzureMachineSpec{
					Image: newAzureCapiImage(),
				},
			},
		},
	}
}

func newAzureClusterWithControlPlane() (*capi.Cluster, *kcp.KubeadmControlPlane, *capz.AzureCluster, *capz.AzureMachineTemplate) {
	cluster, kcp := newClusterWithControlPlane()
	azureCluster := newAzureCluster(cluster.Name)
	azureMachineTemplate := newAzureMachineTemplate(cluster.Name)

	cluster.Spec.InfrastructureRef = &corev1.ObjectReference{
		Kind:       azureCluster.Kind,
		Namespace:  azureCluster.Namespace,
		Name:       azureCluster.Name,
		APIVersion: azureCluster.APIVersion,
	}
	kcp.Spec.InfrastructureTemplate = corev1.ObjectReference{
		Kind:       azureMachineTemplate.Kind,
		Namespace:  azureMachineTemplate.Namespace,
		Name:       azureMachineTemplate.Name,
		APIVersion: azureMachineTemplate.APIVersion,
	}
	kcp.Spec.KubeadmConfigSpec.Files = append(
		kcp.Spec.KubeadmConfigSpec.Files,
		cabpk.File{
			ContentFrom: &cabpk.FileSource{
				Secret: cabpk.SecretFileSource{
					Name: fmt.Sprintf("%s-azure-json", azureMachineTemplate.Name),
				},
			},
		},
	)

	return cluster, kcp, azureCluster, azureMachineTemplate
}

func newAzureMachinePool(cluster, name string) *capzexp.AzureMachinePool {
	return &capzexp.AzureMachinePool{
		// TODO (mig4): I think specifying TypeMeta explicitly is only necessary because
		//   we don't start the manager correctly, this causes references to this object
		//   be invalid because when `reference.GetReference` sees an empty GVK it tries
		//   to look it up but fails because testenv is not fully initialised; i.e. test
		//   if this is necessary if we initialise the manager in `suite_test.go`
		TypeMeta: metav1.TypeMeta{
			Kind:       "AzureMachinePool",
			APIVersion: "exp.infrastructure.cluster.x-k8s.io/v1alpha3",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.4.10",
				capi.ClusterLabelName: cluster,
			},
		},
		Spec: capzexp.AzureMachinePoolSpec{
			Template: capzexp.AzureMachineTemplate{
				Image: newAzureCapiImage(),
			},
		},
	}
}

func newMachinePool(cluster string) *capiexp.MachinePool {
	name := fmt.Sprintf("%s-mp-%s", cluster, util.RandomString(4))
	return &capiexp.MachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "0.3.10",
				capi.ClusterLabelName: cluster,
			},
		},
		Spec: capiexp.MachinePoolSpec{
			ClusterName: cluster,
			Template: capi.MachineTemplateSpec{
				Spec: capi.MachineSpec{
					ClusterName: cluster,
					Version:     to.StringPtr("v1.18.2"),
				},
			},
		},
	}
}

func newAzureMachinePoolChain(cluster string) (*capiexp.MachinePool, *capzexp.AzureMachinePool) {
	mp := newMachinePool(cluster)
	amp := newAzureMachinePool(cluster, mp.Name)

	mp.Spec.Template.Spec.InfrastructureRef = corev1.ObjectReference{
		Kind:       amp.Kind,
		Name:       amp.Name,
		APIVersion: amp.APIVersion,
	}

	return mp, amp
}
