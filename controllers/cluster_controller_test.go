package controllers

import (
	"context"
	"github.com/Azure/go-autorest/autorest/to"
	releaseapiextensions "github.com/giantswarm/apiextensions/pkg/apis/release/v1alpha1"
	apiextensionslabel "github.com/giantswarm/apiextensions/pkg/label"
	v12 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/reference"
	capz "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	capzexp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1alpha3"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	kcp "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	capiexp "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"testing"
)

func TestName(t *testing.T) {
	ctx := context.TODO()
	scheme := runtime.NewScheme()
	err := capi.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = capiexp.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = capz.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = capzexp.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = kcp.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = releaseapiextensions.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}

	release10dot0 := &releaseapiextensions.Release{
		ObjectMeta: v1.ObjectMeta{
			Name: "v10.0.0",
		},
		Spec: releaseapiextensions.ReleaseSpec{
			Components: []releaseapiextensions.ReleaseSpecComponent{
				{Name: "kubernetes", Version: "1.18.14"},
				{Name: capiReleaseComponent, Version: "v0.3.14"},
				{Name: cacpReleaseComponent, Version: "v0.3.14"},
				{Name: capzReleaseComponent, Version: "v0.4.12"},
				{Name: "image", Version: "k8s-1dot18dot14-ubuntu"},
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
	if reconciledControlplane.Spec.Version != "1.18.14" {
		t.Fatalf("Kubeadmcontrolplane.Spec.Version uses wrong k8s version, got %q, expected %q", reconciledControlplane.Spec.Version, "1.18.14")
	}
	if reconciledControlplane.Labels[CAPIWatchFilterLabel] != "v0.3.14" {
		t.Fatalf("Kubeadmcontrolplane label %q is wrong, got %q, expected %q", cacpReleaseComponent, reconciledControlplane.Labels[cacpReleaseComponent], "v0.3.14")
	}

	// Assert AzureMachineTemplate used by KubeadmControlPlane uses right machine image.
	newAzureMachineTemplate := &capz.AzureMachineTemplate{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: reconciledControlplane.Spec.InfrastructureTemplate.Namespace, Name: reconciledControlplane.Spec.InfrastructureTemplate.Name}, newAzureMachineTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU != "k8s-1dot18dot14-ubuntu" {
		t.Fatalf("AzureMachineTemplate %q image is wrong, got %q, expected %q", newAzureMachineTemplate.Name, newAzureMachineTemplate.Spec.Template.Spec.Image.Marketplace.SKU, "k8s-1dot18dot14-ubuntu")
	}

	// Assert CAPI CR's are labeled correctly.
	reconciledCluster := &capi.Cluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, reconciledCluster)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledCluster.Labels[CAPIWatchFilterLabel] != "v0.3.14" {
		t.Fatalf("Label %q is wrong in Cluster %q, got %q, expected %q", CAPIWatchFilterLabel, reconciledCluster.Name, reconciledCluster.Labels[CAPIWatchFilterLabel], "v0.3.14")
	}
	reconciledMachinePool1 := &capiexp.MachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: machinePool1.Namespace, Name: machinePool1.Name}, reconciledMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledMachinePool1.Labels[CAPIWatchFilterLabel] != "v0.3.14" {
		t.Fatalf("Label %q is wrong in MachinePool %q, got %q, expected %q", CAPIWatchFilterLabel, reconciledMachinePool1.Name, reconciledMachinePool1.Labels[CAPIWatchFilterLabel], "v0.3.14")
	}

	// Assert CAPZ CR's are labeled correctly.
	reconciledAzureCluster := &capz.AzureCluster{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureCluster.Namespace, Name: azureCluster.Name}, reconciledAzureCluster)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledAzureCluster.Labels[CAPIWatchFilterLabel] != "v0.4.12" {
		t.Fatalf("Label %q is wrong in AzureCluster %q, got %q, expected %q", CAPIWatchFilterLabel, reconciledAzureCluster.Name, reconciledAzureCluster.Labels[CAPIWatchFilterLabel], "v0.4.12")
	}
	reconciledAzureMachinePool1 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool1.Namespace, Name: azureMachinePool1.Name}, reconciledAzureMachinePool1)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledAzureMachinePool1.Labels[CAPIWatchFilterLabel] != "v0.4.12" {
		t.Fatalf("Label %q is wrong in AzureMachinePool %q, got %q, expected %q", CAPIWatchFilterLabel, reconciledAzureMachinePool1.Name, reconciledAzureMachinePool1.Labels[CAPIWatchFilterLabel], "v0.4.12")
	}
	reconciledAzureMachinePool2 := &capzexp.AzureMachinePool{}
	err = ctrlClient.Get(ctx, client.ObjectKey{Namespace: azureMachinePool2.Namespace, Name: azureMachinePool2.Name}, reconciledAzureMachinePool2)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel] != "v0.4.10" {
		t.Fatalf("Label %q is wrong in AzureMachinePool %q, got %q, expected %q", CAPIWatchFilterLabel, reconciledAzureMachinePool2.Name, reconciledAzureMachinePool2.Labels[CAPIWatchFilterLabel], "v0.4.10")
	}

	// Assert AzureMachinePool uses the right image.
	if reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU != "k8s-1dot18dot14-ubuntu" {
		t.Fatalf("AzureMachinePool %q image is wrong, got %q, expected %q", reconciledAzureMachinePool1.Name, reconciledAzureMachinePool1.Spec.Template.Image.Marketplace.SKU, "k8s-1dot18dot14-ubuntu")
	}
}
