module github.com/giantswarm/upgrade-operator

go 1.13

require (
	github.com/Azure/go-autorest/autorest/to v0.4.0
	github.com/giantswarm/apiextensions v0.4.20
	github.com/go-logr/logr v0.1.0
	github.com/onsi/ginkgo v1.14.2
	github.com/onsi/gomega v1.10.3
	github.com/pkg/errors v0.9.1
	k8s.io/api v0.17.14
	k8s.io/apimachinery v0.17.14
	k8s.io/client-go v0.17.14
	sigs.k8s.io/cluster-api v0.3.14
	sigs.k8s.io/cluster-api-provider-azure v0.4.12
	sigs.k8s.io/controller-runtime v0.5.14
)
