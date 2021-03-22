package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/Azure/go-autorest/autorest/to"
	releaseapiextensions "github.com/giantswarm/apiextensions/pkg/apis/release/v1alpha1"
	apiextensionslabel "github.com/giantswarm/apiextensions/pkg/label"
	"github.com/stretchr/testify/assert"
	v12 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/reference"
	capz "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	capzexp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1alpha3"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"
	kcp "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	capiexp "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestUpgradeK8sVersion(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: v1.ObjectMeta{
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

	azureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cp-template",
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
	azureMachineTemplateReference, err := reference.GetReference(scheme, azureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: v1.ObjectMeta{
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
			InfrastructureTemplate: *azureMachineTemplateReference,
			KubeadmConfigSpec: v1alpha3.KubeadmConfigSpec{
				Files: []v1alpha3.File{
					{
						ContentFrom: &v1alpha3.FileSource{Secret: v1alpha3.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", azureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: v12.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.4.10",
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
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "v0.3.10",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	azureMachinePool1 := &capzexp.AzureMachinePool{
		TypeMeta: v1.TypeMeta{
			Kind:       "AzureMachinePool",
			APIVersion: "exp.infrastructure.cluster.x-k8s.io/v1alpha3",
		},
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.4.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capzexp.AzureMachinePoolSpec{
			Template: capzexp.AzureMachineTemplate{
				Image: &capz.Image{
					Marketplace: &capz.AzureMarketplaceImage{
						Publisher: "cncf-upstream",
						Offer:     "capi",
						SKU:       "k8s-1dot18dot2-ubuntu-1804",
						Version:   "latest",
					},
				},
			},
		},
	}

	machinePool1 := &capiexp.MachinePool{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capiexp.MachinePoolSpec{
			ClusterName: cluster.Name,
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					InfrastructureRef: v12.ObjectReference{
						Kind:       azureMachinePool1.Kind,
						Name:       azureMachinePool1.Name,
						APIVersion: azureMachinePool1.APIVersion,
					},
					Version: to.StringPtr("v1.18.2"),
				},
			},
		},
		Status: capiexp.MachinePoolStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: v12.ConditionTrue,
			}},
		},
	}

	azureMachinePool2 := &capzexp.AzureMachinePool{
		TypeMeta: v1.TypeMeta{
			Kind:       "AzureMachinePool",
			APIVersion: "exp.infrastructure.cluster.x-k8s.io/v1alpha3",
		},
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np02",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.4.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capzexp.AzureMachinePoolSpec{
			Template: capzexp.AzureMachineTemplate{
				Image: &capz.Image{
					Marketplace: &capz.AzureMarketplaceImage{
						Publisher: "cncf-upstream",
						Offer:     "capi",
						SKU:       "k8s-1dot18dot2-ubuntu-1804",
						Version:   "latest",
					},
				},
			},
		},
	}

	machinePool2 := &capiexp.MachinePool{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np02",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capiexp.MachinePoolSpec{
			ClusterName: cluster.Name,
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					InfrastructureRef: v12.ObjectReference{
						Kind:       azureMachinePool2.Kind,
						Name:       azureMachinePool2.Name,
						APIVersion: azureMachinePool2.APIVersion,
					},
					Version: to.StringPtr("v1.18.2"),
				},
			},
		},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, azureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, azureMachinePool1, machinePool1, azureMachinePool2, machinePool2)

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
	assert.Equal(t, "v1.18.14", reconciledControlplane.Spec.Version, fmt.Sprintf("KubeadmControlPlane %q has wrong k8s version in KubeadmControlPlane.Spec.Version field", reconciledControlplane.Name))
	assert.Equal(t, "v0.3.14", reconciledControlplane.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in KubeadmControlPlane %q", CAPIWatchFilterLabel, reconciledControlplane.Name))

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
	assert.Equal(t, "k8s-1dot18dot14-ubuntu-1804", newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, fmt.Sprintf("AzureMachineTemplate %q image is wrong", newAzureMachineTemplate.Name))

	// Assert CAPI CR's are labeled correctly.
	reconciledCluster := &capi.Cluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, reconciledCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "v0.3.14", reconciledCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in Cluster %q", CAPIWatchFilterLabel, reconciledCluster.Name))

	reconciledMachinePool1 := &capiexp.MachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool1.Namespace, Name: machinePool1.Name}, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "v0.3.14", reconciledMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in MachinePool %q", CAPIWatchFilterLabel, reconciledMachinePool1.Name))

	// Assert CAPZ CR's are labeled correctly.
	reconciledAzureCluster := &capz.AzureCluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureCluster.Namespace, Name: azureCluster.Name}, reconciledAzureCluster)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "v0.4.12", reconciledAzureCluster.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureCluster %q", CAPIWatchFilterLabel, reconciledAzureCluster.Name))
	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "v0.4.12", reconciledAzureMachinePool1.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureMachinePool %q", CAPIWatchFilterLabel, reconciledAzureMachinePool1.Name))
	reconciledAzureMachinePool2 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "v0.4.10", reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], fmt.Sprintf("Label %q is wrong in AzureMachinePool %q", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name))
	assert.Equal(t, "k8s-1dot18dot14-ubuntu-1804", reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, fmt.Sprintf("AzureMachinePool %s is wrong", reconciledAzureMachinePool1.Name))
}

func TestUpgradeOSVersion(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = capi.AddToScheme(scheme)
	_ = capiexp.AddToScheme(scheme)
	_ = capz.AddToScheme(scheme)
	_ = capzexp.AddToScheme(scheme)
	_ = kcp.AddToScheme(scheme)
	_ = releaseapiextensions.AddToScheme(scheme)

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: v1.ObjectMeta{
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

	azureMachineTemplate := &capz.AzureMachineTemplate{
		ObjectMeta: v1.ObjectMeta{
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
	azureMachineTemplateReference, err := reference.GetReference(scheme, azureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}

	kubeadmcontrolplane := &kcp.KubeadmControlPlane{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster-control-plane",
			Labels: map[string]string{
				cacpReleaseComponent:  "v0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: kcp.KubeadmControlPlaneSpec{
			Replicas:               to.Int32Ptr(1),
			Version:                "v1.18.2",
			InfrastructureTemplate: *azureMachineTemplateReference,
			KubeadmConfigSpec: v1alpha3.KubeadmConfigSpec{
				Files: []v1alpha3.File{
					{
						ContentFrom: &v1alpha3.FileSource{Secret: v1alpha3.SecretFileSource{Name: fmt.Sprintf("%s-azure-json", azureMachineTemplate.Name)}},
					},
				},
			},
		},
		Status: kcp.KubeadmControlPlaneStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: v12.ConditionTrue,
			}},
		},
	}

	kcpReference, err := reference.GetReference(scheme, kubeadmcontrolplane)
	if err != nil {
		t.Fatal(err)
	}

	azureCluster := &capz.AzureCluster{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.4.10",
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
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "my-cluster",
			Labels: map[string]string{
				apiextensionslabel.ReleaseVersion: "v10.0.0",
				CAPIWatchFilterLabel:              "v0.3.10",
			},
		},
		Spec: capi.ClusterSpec{
			ControlPlaneRef:   kcpReference,
			InfrastructureRef: azureClusterReference,
		},
	}

	azureMachinePool1 := &capzexp.AzureMachinePool{
		TypeMeta: v1.TypeMeta{
			Kind:       "AzureMachinePool",
			APIVersion: "exp.infrastructure.cluster.x-k8s.io/v1alpha3",
		},
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.4.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capzexp.AzureMachinePoolSpec{
			Template: capzexp.AzureMachineTemplate{
				Image: &capz.Image{
					Marketplace: &capz.AzureMarketplaceImage{
						Publisher: "cncf-upstream",
						Offer:     "capi",
						SKU:       "k8s-1dot18dot2-ubuntu-1804",
						Version:   "latest",
					},
				},
			},
		},
	}

	machinePool1 := &capiexp.MachinePool{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np01",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capiexp.MachinePoolSpec{
			ClusterName: cluster.Name,
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					InfrastructureRef: v12.ObjectReference{
						Kind:       azureMachinePool1.Kind,
						Name:       azureMachinePool1.Name,
						APIVersion: azureMachinePool1.APIVersion,
					},
					Version: to.StringPtr("v1.18.2"),
				},
			},
		},
		Status: capiexp.MachinePoolStatus{
			Conditions: capi.Conditions{capi.Condition{
				Type:   capi.ReadyCondition,
				Status: v12.ConditionTrue,
			}},
		},
	}

	azureMachinePool2 := &capzexp.AzureMachinePool{
		TypeMeta: v1.TypeMeta{
			Kind:       "AzureMachinePool",
			APIVersion: "exp.infrastructure.cluster.x-k8s.io/v1alpha3",
		},
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np02",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.4.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capzexp.AzureMachinePoolSpec{
			Template: capzexp.AzureMachineTemplate{
				Image: &capz.Image{
					Marketplace: &capz.AzureMarketplaceImage{
						Publisher: "cncf-upstream",
						Offer:     "capi",
						SKU:       "k8s-1dot18dot2-ubuntu-1804",
						Version:   "latest",
					},
				},
			},
		},
	}

	machinePool2 := &capiexp.MachinePool{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default",
			Name:      "np02",
			Labels: map[string]string{
				CAPIWatchFilterLabel:  "v0.3.10",
				capi.ClusterLabelName: "my-cluster",
			},
		},
		Spec: capiexp.MachinePoolSpec{
			ClusterName: cluster.Name,
			Template: capi.MachineTemplateSpec{
				ObjectMeta: capi.ObjectMeta{},
				Spec: capi.MachineSpec{
					ClusterName: cluster.Name,
					InfrastructureRef: v12.ObjectReference{
						Kind:       azureMachinePool2.Kind,
						Name:       azureMachinePool2.Name,
						APIVersion: azureMachinePool2.APIVersion,
					},
					Version: to.StringPtr("v1.18.2"),
				},
			},
		},
	}

	ctrlClient := fake.NewFakeClientWithScheme(scheme, release10dot0, azureMachineTemplate, kubeadmcontrolplane, azureCluster, cluster, azureMachinePool1, machinePool1, azureMachinePool2, machinePool2)

	reconciler := ClusterReconciler{
		Client: ctrlClient,
		Log:    ctrl.Log.WithName("controllers").WithName("Cluster"),
		Scheme: scheme,
	}

	_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "my-cluster"}})
	if err != nil {
		t.Fatal(err)
	}

	// Assert KubeadmControlPlane version is correct.
	reconciledControlplane := &kcp.KubeadmControlPlane{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: kubeadmcontrolplane.Namespace, Name: kubeadmcontrolplane.Name}, reconciledControlplane)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "v1.18.2", reconciledControlplane.Spec.Version, "Kubeadmcontrolplane.Spec.Version uses wrong k8s version")

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
	assert.Equal(t, "k8s-1dot18dot2-ubuntu-1810", newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, "AzureMachineTemplate image is wrong")

	// Assert node pool uses right machine image.
	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "k8s-1dot18dot2-ubuntu-1810", reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, "AzureMachinePool image is wrong")
}
