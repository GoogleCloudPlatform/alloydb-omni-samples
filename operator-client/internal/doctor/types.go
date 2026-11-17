// Package doctor checks whether AlloyDB Omni and its prerequisites are installed.
package doctor

import "k8s.io/apimachinery/pkg/api/resource"

const (
	// Pass means the check succeeded.
	Pass = "PASS"
	// Warn means the install can still be usable, but something needs attention.
	Warn = "WARN"
	// Fail means a prerequisite is missing or broken. Any Fail makes doctor exit 1.
	Fail = "FAIL"
	// Skip means the check could not be performed from the Kubernetes API.
	Skip = "SKIP"

	// UpstreamNote is printed because doctor does not query Google for the latest release.
	UpstreamNote = "upstream releases were not checked; this compares installed components with their declared specs"
)

// Result is one check.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Next   string `json:"next,omitempty"`
}

// Report is the full doctor output.
type Report struct {
	Results      []Result `json:"results"`
	Installation string   `json:"installation"`
	Next         string   `json:"next,omitempty"`
	Note         string   `json:"note"`
}

func (r Report) finish() Report {
	r.Note = UpstreamNote
	r.Installation = "ok"
	for _, item := range r.Results {
		if item.Status == Fail {
			r.Installation = "not ready"
			r.Next = item.Next
			break
		}
	}
	return r
}

// Failed is true when installation is not ready.
func (r Report) Failed() bool { return r.Installation == "not ready" }

// DepFact is a Deployment the checker cares about.
// Forbidden is set when the API returned 403. That is "could not check",
// which is distinct from Found=false (the object is really absent).
type DepFact struct {
	Name      string
	Found     bool
	Ready     bool
	Image     string
	Error     string
	Forbidden string
}

// HookFact is one webhook entry inside a webhook configuration.
type HookFact struct {
	CA                 bool
	Service            string
	Namespace          string
	Endpoints          bool
	EndpointsError     string
	EndpointsForbidden string
	ServiceMissing     bool
}

// WebhookFact is one admission webhook configuration.
type WebhookFact struct {
	Name      string
	Found     bool
	Error     string
	Forbidden string
	Hooks     []HookFact
}

// NodeFact is the schedulable capacity doctor can see.
type NodeFact struct {
	Name   string
	Arch   string
	Kernel string
	CPU    resource.Quantity
	Memory resource.Quantity
}

// ClusterFact is spec versus observed versions for one DBCluster.
type ClusterFact struct {
	Namespace     string
	Name          string
	SpecDB        string
	RunningDB     string
	SpecAgents    string
	RunningAgents string
}

// Snapshot is everything collect could read. evaluate is a pure function of this.
type Snapshot struct {
	Context       string
	Namespace     string
	AllNamespaces bool
	ServerGit     string
	APIError      string

	RBACError          string
	RBACDenied         []string
	PortForwardAllowed bool

	CertCRDs                      map[string]bool
	CertCRDError                  string
	CertCRDForbidden              string
	CertController                DepFact
	CertWebhook                   DepFact
	CertWebhookEndpoints          bool
	CertWebhookEndpointsError     string
	CertWebhookEndpointsForbidden string
	CertImagePullPod              string
	CertImagePullReason           string

	OmniCRDPresent          bool
	OmniCRDEstablished      bool
	OmniCRDError            string
	OmniCRDForbidden        string
	OperatorNamespace       string
	Operator                []DepFact
	OperatorImagePullPod    string
	OperatorImagePullReason string
	Webhooks                []WebhookFact

	DefaultStorageClass   string
	StorageClassError     string
	StorageClassForbidden string

	Nodes          []NodeFact
	NodesError     string
	NodesForbidden bool

	Clusters          []ClusterFact
	ClustersError     string
	ClustersForbidden string
}
