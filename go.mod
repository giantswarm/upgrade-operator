module github.com/giantswarm/upgrade-operator

go 1.13

require (
	github.com/Azure/go-autorest/autorest/to v0.4.0
	github.com/giantswarm/apiextensions v0.4.20
	github.com/go-logr/logr v1.2.0
	github.com/onsi/ginkgo v1.16.5
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.0
	k8s.io/api v0.23.0
	k8s.io/apimachinery v0.23.0
	k8s.io/apiserver v0.23.0
	k8s.io/client-go v0.23.0
	k8s.io/klog v1.0.0
	sigs.k8s.io/cluster-api v0.3.14
	sigs.k8s.io/cluster-api-provider-azure v0.4.12
	sigs.k8s.io/controller-runtime v0.11.1
)

// Fix CVEs attached to controller-runtime v0.5.14
replace (
	github.com/coreos/etcd v3.3.10+incompatible => github.com/coreos/etcd v3.3.25+incompatible
	github.com/gogo/protobuf v1.3.1 => github.com/gogo/protobuf v1.3.2
	github.com/gorilla/websocket v1.4.0 => github.com/gorilla/websocket v1.4.2
)
