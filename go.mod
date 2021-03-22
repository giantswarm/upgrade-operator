module github.com/giantswarm/upgrade-operator

go 1.13

require (
	github.com/Azure/go-autorest/autorest/to v0.4.0
	github.com/giantswarm/apiextensions v0.4.20
	github.com/go-logr/logr v0.3.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.6.1
	k8s.io/api v0.20.2
	k8s.io/apimachinery v0.20.2
	k8s.io/apiserver v0.20.1
	k8s.io/client-go v0.20.2
	sigs.k8s.io/cluster-api v0.3.14
	sigs.k8s.io/cluster-api-provider-azure v0.4.12
	sigs.k8s.io/controller-runtime v0.8.3
)

// Fix CVEs attached to controller-runtime v0.5.14
replace (
	github.com/coreos/etcd v3.3.10+incompatible => github.com/coreos/etcd v3.3.25+incompatible
	github.com/gorilla/websocket v1.4.0 => github.com/gorilla/websocket v1.4.2
)
