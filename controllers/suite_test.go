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
	"os"
	"testing"

	releaseapiextensions "github.com/giantswarm/apiextensions/pkg/apis/release/v1alpha1"
	. "github.com/onsi/ginkgo"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog"
	"k8s.io/klog/klogr"
	capz "sigs.k8s.io/cluster-api-provider-azure/api/v1alpha3"
	capzexp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1alpha3"
	capi "sigs.k8s.io/cluster-api/api/v1alpha3"
	kcp "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	capiexp "sigs.k8s.io/cluster-api/exp/api/v1alpha3"
	ctrl "sigs.k8s.io/controller-runtime"
	// +kubebuilder:scaffold:imports
)

// TestMain is the entry point to all tests, it ensures some common setup is
// done before testing and invokes the actual tests via `M.Run()`.
func TestMain(m *testing.M) {
	testEnvSetup()
	os.Exit(m.Run())
}

func testEnvSetup() {
	klog.InitFlags(nil)
	klog.SetOutput(GinkgoWriter)
	ctrl.SetLogger(klogr.New())
	klog.Info("testEnvSetup")
	utilruntime.Must(capi.AddToScheme(scheme.Scheme))
	utilruntime.Must(capiexp.AddToScheme(scheme.Scheme))
	utilruntime.Must(releaseapiextensions.AddToScheme(scheme.Scheme))
	utilruntime.Must(capz.AddToScheme(scheme.Scheme))
	utilruntime.Must(capzexp.AddToScheme(scheme.Scheme))
	utilruntime.Must(kcp.AddToScheme(scheme.Scheme))
}
