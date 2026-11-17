package doctor

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	utilversion "k8s.io/apimachinery/pkg/util/version"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cluster"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/version"
)

var (
	minCPU = resource.MustParse("2")
	minMem = resource.MustParse("8Gi")
	minK8s = utilversion.MustParseGeneric("1.21.0")
)

const (
	helmCertManager = "helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace"
	helmOmni        = "helm upgrade -i alloydb-omni oci://${_LOCATION}-docker.pkg.dev/${PROJECT_ID}/${_REPO}/alloydb-omni-argocd-applications"
)

var certManagerCRDs = []string{
	"certificates.cert-manager.io",
	"issuers.cert-manager.io",
	"clusterissuers.cert-manager.io",
}

// Evaluate turns a snapshot into an ordered report. It does not call the API.
func Evaluate(s Snapshot) Report {
	if s.APIError != "" {
		return Report{Results: []Result{{
			Name:   "kubernetes-api",
			Status: Fail,
			Detail: s.APIError,
			Next:   "check kubeconfig, --context, and that the API server is reachable",
		}}}.finish()
	}

	var results []Result
	results = append(results, Result{
		Name:   "kubernetes-api",
		Status: Pass,
		Detail: fmt.Sprintf("context %s reachable", quotedContext(s.Context)),
	})
	results = append(results, versionResult(s.ServerGit))
	results = append(results, rbacResult(s))
	results = append(results, certCRDResult(s))
	results = append(results, certWebhookResult(s))
	results = append(results, omniCRDResult(s))
	results = append(results, operatorResult(s))
	results = append(results, webhookResult(s))
	results = append(results, storageClassResult(s))
	results = append(results, nodeResults(s)...)
	results = append(results, Result{
		Name:   "cgroup-v2",
		Status: Skip,
		Detail: "not exposed by the API; on a node, stat -fc %T /sys/fs/cgroup should be cgroup2fs",
	})
	results = append(results, versionsResult(s))
	return Report{Results: results}.finish()
}

func quotedContext(ctx string) string {
	if ctx == "" {
		return "(in-cluster)"
	}
	return fmt.Sprintf("%q", ctx)
}

func versionResult(git string) Result {
	v, err := utilversion.ParseGeneric(git)
	if err != nil {
		return Result{
			Name:   "kubernetes-version",
			Status: Fail,
			Detail: fmt.Sprintf("cannot parse server version %q", git),
			Next:   "confirm the cluster is Kubernetes 1.21 or later",
		}
	}
	if !v.AtLeast(minK8s) {
		return Result{
			Name:   "kubernetes-version",
			Status: Fail,
			Detail: fmt.Sprintf("server %s is older than 1.21", git),
			Next:   "upgrade the cluster to Kubernetes 1.21 or later",
		}
	}
	return Result{Name: "kubernetes-version", Status: Pass, Detail: "server " + git}
}

func rbacResult(s Snapshot) Result {
	if s.RBACError != "" {
		return Result{
			Name:   "rbac",
			Status: Fail,
			Detail: s.RBACError,
			Next:   "grant this user permission to create SelfSubjectAccessReviews",
		}
	}
	if len(s.RBACDenied) > 0 {
		return Result{
			Name:   "rbac",
			Status: Fail,
			Detail: "missing permission: " + strings.Join(s.RBACDenied, ", "),
			Next:   "grant get/list on dbclusters, pods, secrets, and events, plus get on nodes, CRDs, and webhook configurations",
		}
	}
	if !s.PortForwardAllowed {
		return Result{
			Name:   "rbac",
			Status: Warn,
			Detail: "required reads are allowed. create on pods/portforward is denied, so adbo connect cannot open a tunnel",
			Next:   "grant create on pods/portforward in this namespace",
		}
	}
	return Result{Name: "rbac", Status: Pass, Detail: "allowed"}
}

func certCRDResult(s Snapshot) Result {
	if s.CertCRDForbidden != "" {
		return Result{Name: "cert-manager-crds", Status: Warn, Detail: s.CertCRDForbidden, Next: "grant get on customresourcedefinitions"}
	}
	if s.CertCRDError != "" {
		return Result{Name: "cert-manager-crds", Status: Fail, Detail: s.CertCRDError, Next: helmCertManager}
	}
	var missing []string
	for _, name := range certManagerCRDs {
		if !s.CertCRDs[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Result{
			Name:   "cert-manager-crds",
			Status: Fail,
			Detail: "missing " + strings.Join(missing, ", "),
			Next:   helmCertManager,
		}
	}
	return Result{Name: "cert-manager-crds", Status: Pass, Detail: "installed"}
}

func certWebhookResult(s Snapshot) Result {
	if s.CertImagePullPod != "" {
		return Result{
			Name:   "cert-manager-webhook",
			Status: Fail,
			Detail: fmt.Sprintf("pod %s is %s. AlloyDB admission calls fail with connection refused until this image pull succeeds", s.CertImagePullPod, s.CertImagePullReason),
			Next:   fmt.Sprintf("kubectl -n %s describe pod %s", cluster.CertManagerNamespace, s.CertImagePullPod),
		}
	}
	if problem, status, next := deploymentProblems([]DepFact{s.CertController, s.CertWebhook}, cluster.CertManagerNamespace); problem != "" {
		if status == Fail && strings.Contains(problem, "was not found") {
			next = "install cert-manager into the cert-manager namespace"
		}
		return Result{Name: "cert-manager-webhook", Status: status, Detail: problem, Next: next}
	}
	if s.CertWebhookEndpointsForbidden != "" {
		return Result{Name: "cert-manager-webhook", Status: Warn, Detail: s.CertWebhookEndpointsForbidden, Next: "grant list on endpointslices in namespace " + cluster.CertManagerNamespace}
	}
	if s.CertWebhookEndpointsError != "" {
		return Result{Name: "cert-manager-webhook", Status: Fail, Detail: s.CertWebhookEndpointsError, Next: "kubectl -n cert-manager get pods"}
	}
	if !s.CertWebhookEndpoints {
		return Result{
			Name:   "cert-manager-webhook",
			Status: Fail,
			Detail: "cert-manager-webhook has no ready addresses",
			Next:   "kubectl -n cert-manager get pods",
		}
	}
	return Result{
		Name:   "cert-manager-webhook",
		Status: Pass,
		Detail: fmt.Sprintf("ready (%s, %s)", shortImage(s.CertController.Image), shortImage(s.CertWebhook.Image)),
	}
}

func omniCRDResult(s Snapshot) Result {
	if s.OmniCRDForbidden != "" {
		return Result{Name: "alloydb-crds", Status: Warn, Detail: s.OmniCRDForbidden, Next: "grant get on customresourcedefinitions"}
	}
	if s.OmniCRDError != "" {
		return Result{Name: "alloydb-crds", Status: Fail, Detail: s.OmniCRDError, Next: helmOmni}
	}
	if !s.OmniCRDPresent {
		return Result{Name: "alloydb-crds", Status: Fail, Detail: "dbclusters.alloydbomni.dbadmin.goog is not installed", Next: helmOmni}
	}
	if !s.OmniCRDEstablished {
		return Result{Name: "alloydb-crds", Status: Fail, Detail: "dbclusters.alloydbomni.dbadmin.goog exists but is not Established", Next: "kubectl get crd dbclusters.alloydbomni.dbadmin.goog"}
	}
	return Result{Name: "alloydb-crds", Status: Pass, Detail: "Established"}
}

func operatorResult(s Snapshot) Result {
	if s.OperatorImagePullPod != "" {
		return Result{
			Name:   "alloydb-operator",
			Status: Fail,
			Detail: fmt.Sprintf("pod %s is %s", s.OperatorImagePullPod, s.OperatorImagePullReason),
			Next:   fmt.Sprintf("kubectl -n %s describe pod %s", operatorNS(s), s.OperatorImagePullPod),
		}
	}
	if len(s.Operator) == 0 {
		return Result{Name: "alloydb-operator", Status: Fail, Detail: "no operator deployments were checked", Next: "install the AlloyDB Omni operator into " + operatorNS(s)}
	}
	var images []string
	for _, d := range s.Operator {
		if d.Image != "" {
			images = append(images, shortImage(d.Image))
		}
	}
	if problem, status, next := deploymentProblems(s.Operator, operatorNS(s)); problem != "" {
		return Result{Name: "alloydb-operator", Status: status, Detail: problem, Next: next}
	}
	return Result{Name: "alloydb-operator", Status: Pass, Detail: strings.Join(images, ", ")}
}

// deploymentProblems reports missing, unready, or unreadable Deployments.
// A 403 is a warning. A Deployment that is actually absent stays a failure,
// and a wrong operator namespace points at how to find the right one.
func deploymentProblems(deps []DepFact, namespace string) (detail, status, next string) {
	var notes []string
	sawForbidden, sawMissing, sawBroken := false, false, false
	for _, d := range deps {
		switch {
		case d.Forbidden != "":
			sawForbidden = true
			notes = append(notes, d.Name+": "+d.Forbidden)
		case d.Error != "":
			sawBroken = true
			notes = append(notes, d.Name+": "+d.Error)
		case !d.Found:
			sawMissing = true
			notes = append(notes, d.Name+" was not found")
		case !d.Ready:
			sawBroken = true
			notes = append(notes, d.Name+" is not Available")
		}
	}
	if len(notes) == 0 {
		return "", Pass, ""
	}
	status = Fail
	next = "kubectl -n " + namespace + " get deploy,pods"
	switch {
	case sawMissing && !sawBroken && !sawForbidden:
		next = fmt.Sprintf("operator not found in %q; find it with: kubectl get deploy -A | grep controller-manager, then pass --operator-namespace <ns>", namespace)
	case sawForbidden && !sawBroken && !sawMissing:
		status = Warn
		next = fmt.Sprintf("grant get on deployments in namespace %q", namespace)
	}
	return strings.Join(notes, "; "), status, next
}

func webhookResult(s Snapshot) Result {
	if len(s.Webhooks) == 0 {
		return Result{Name: "alloydb-webhooks", Status: Fail, Detail: "no AlloyDB webhook configurations were checked", Next: "reinstall the AlloyDB Omni operator so cert-manager can inject the webhook CA"}
	}
	var problems []string
	sawForbidden, sawBroken := false, false
	for _, w := range s.Webhooks {
		switch {
		case w.Forbidden != "":
			sawForbidden = true
			problems = append(problems, w.Name+": "+w.Forbidden)
		case w.Error != "":
			sawBroken = true
			problems = append(problems, w.Name+": "+w.Error)
		case !w.Found:
			sawBroken = true
			problems = append(problems, w.Name+" was not found")
		default:
			if len(w.Hooks) == 0 {
				sawBroken = true
				problems = append(problems, w.Name+" has no webhooks")
			}
			for _, h := range w.Hooks {
				if !h.CA {
					sawBroken = true
					problems = append(problems, w.Name+" has an empty caBundle (cert-manager inject did not run)")
				}
				if h.Service == "" {
					sawBroken = true
					problems = append(problems, w.Name+" has no service reference")
					continue
				}
				where := h.Namespace + "/" + h.Service
				switch {
				case h.EndpointsForbidden != "":
					sawForbidden = true
					problems = append(problems, where+": "+h.EndpointsForbidden)
				case h.EndpointsError != "":
					sawBroken = true
					problems = append(problems, where+": "+h.EndpointsError)
				case !h.Endpoints:
					sawBroken = true
					problems = append(problems, where+" has no ready endpoints")
				}
			}
		}
	}
	if len(problems) > 0 {
		status := Fail
		next := "check cert-manager.io/inject-ca-from on the webhook configurations and the webhook Service endpoints"
		if sawForbidden && !sawBroken {
			status = Warn
			next = "grant get on validatingwebhookconfigurations and mutatingwebhookconfigurations"
		}
		return Result{
			Name:   "alloydb-webhooks",
			Status: status,
			Detail: strings.Join(problems, "; "),
			Next:   next,
		}
	}
	return Result{Name: "alloydb-webhooks", Status: Pass, Detail: "CA and ready addresses"}
}

func operatorNS(s Snapshot) string {
	if s.OperatorNamespace != "" {
		return s.OperatorNamespace
	}
	return cluster.OperatorNamespace
}

func storageClassResult(s Snapshot) Result {
	if s.StorageClassForbidden != "" {
		return Result{Name: "storageclass", Status: Warn, Detail: s.StorageClassForbidden, Next: "grant list on storageclasses"}
	}
	if s.StorageClassError != "" {
		return Result{Name: "storageclass", Status: Fail, Detail: s.StorageClassError, Next: "kubectl get storageclass"}
	}
	if s.DefaultStorageClass == "" {
		return Result{
			Name:   "storageclass",
			Status: Fail,
			Detail: "no StorageClass has storageclass.kubernetes.io/is-default-class=true",
			Next:   "kubectl get storageclass",
		}
	}
	return Result{Name: "storageclass", Status: Pass, Detail: s.DefaultStorageClass + " is the default"}
}

func nodeResults(s Snapshot) []Result {
	if s.NodesForbidden {
		return []Result{{
			Name:   "nodes",
			Status: Warn,
			Detail: "listing nodes returned 403; CPU, memory, architecture, and kernel were not checked",
			Next:   "grant get on nodes to check capacity",
		}}
	}
	if s.NodesError != "" {
		return []Result{{
			Name: "nodes", Status: Fail, Detail: s.NodesError,
			Next: "grant get on nodes, or confirm the API can list them",
		}}
	}
	if len(s.Nodes) == 0 {
		return []Result{
			{
				Name:   "nodes",
				Status: Warn,
				Detail: "no nodes yet. On GKE Autopilot this is normal until the first pod is scheduled",
			},
			{
				Name:   "node-resources",
				Status: Skip,
				Detail: "CPU, memory, architecture, and kernel checks were skipped because the cluster has no nodes",
			},
		}
	}
	var bad []string
	for _, n := range s.Nodes {
		var why []string
		if n.Arch != "amd64" {
			why = append(why, "architecture "+empty(n.Arch)+" (need amd64)")
		}
		if n.CPU.Cmp(minCPU) < 0 {
			why = append(why, "cpu "+n.CPU.String()+" (need >= 2)")
		}
		if n.Memory.Cmp(minMem) < 0 {
			why = append(why, "memory "+n.Memory.String()+" (need >= 8Gi)")
		}
		ok, err := kernelAtLeast(n.Kernel, 4, 18)
		if err != nil {
			why = append(why, err.Error())
		} else if !ok {
			why = append(why, "kernel "+n.Kernel+" (need >= 4.18)")
		}
		if len(why) > 0 {
			bad = append(bad, n.Name+": "+strings.Join(why, ", "))
		}
	}
	nodes := Result{Name: "nodes", Status: Pass, Detail: fmt.Sprintf("%d nodes", len(s.Nodes))}
	if len(bad) > 0 {
		return []Result{nodes, {
			Name:   "node-resources",
			Status: Fail,
			Detail: strings.Join(bad, "; "),
			Next:   "schedule database pods onto nodes with at least 2 amd64 CPUs, 8Gi RAM, and kernel 4.18 or later",
		}}
	}
	return []Result{nodes, {
		Name:   "node-resources",
		Status: Pass,
		Detail: fmt.Sprintf("%d nodes meet amd64, 2 CPU, 8Gi, and kernel >= 4.18", len(s.Nodes)),
	}}
}

func versionsResult(s Snapshot) Result {
	if s.OmniCRDForbidden != "" {
		return Result{Name: "versions", Status: Warn, Detail: s.OmniCRDForbidden + "; client " + version.Version}
	}
	if !s.OmniCRDPresent {
		return Result{Name: "versions", Status: Skip, Detail: "DBCluster CRD is not installed, so versions were not compared. client " + version.Version}
	}
	if s.ClustersForbidden != "" {
		return Result{Name: "versions", Status: Warn, Detail: s.ClustersForbidden + "; client " + version.Version}
	}
	if s.ClustersError != "" {
		return Result{Name: "versions", Status: Fail, Detail: s.ClustersError, Next: "kubectl get dbclusters"}
	}
	var notes []string
	status := Pass
	for _, d := range s.Operator {
		switch {
		case d.Forbidden != "":
			status = Warn
			notes = append(notes, d.Name+": "+d.Forbidden)
		case d.Error != "":
			status = Warn
			notes = append(notes, d.Name+": "+d.Error)
		case !d.Found:
			status = Warn
			notes = append(notes, d.Name+" was not found, so the operator version cannot be confirmed")
		case !d.Ready:
			status = Warn
			notes = append(notes, d.Name+" is not ready, so the operator version cannot be confirmed")
		}
	}
	if len(s.Clusters) == 0 {
		where := fmt.Sprintf("namespace %q", s.Namespace)
		if s.AllNamespaces {
			where = "any namespace"
		}
		notes = append(notes, "no DBClusters in "+where)
	}
	for _, cl := range s.Clusters {
		id := cl.Namespace + "/" + cl.Name
		if msg, warn := versionGap(id+" database", cl.SpecDB, cl.RunningDB); msg != "" {
			notes = append(notes, msg)
			if warn {
				status = Warn
			}
		}
		if msg, warn := versionGap(id+" control-plane agents", cl.SpecAgents, cl.RunningAgents); msg != "" {
			notes = append(notes, msg)
			if warn {
				status = Warn
			}
		}
	}
	if status == Pass {
		notes = append([]string{"specs match"}, notes...)
	}
	notes = append(notes, "client "+version.Version)
	return Result{Name: "versions", Status: status, Detail: strings.Join(notes, "; ")}
}

func versionGap(what, spec, running string) (string, bool) {
	if spec == "" {
		return "", false
	}
	if running == "" {
		return fmt.Sprintf("%s spec is %s but status is empty (cluster may still be starting)", what, spec), true
	}
	if spec != running {
		return fmt.Sprintf("%s spec %s, running %s (upgrade in progress or stuck)", what, spec, running), true
	}
	return "", false
}

// shortImage keeps the repository name and tag, or the first 12 digest characters.
// Full registry paths and full sha256 strings are omitted from PASS rows.
func shortImage(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	base := image
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	const marker = "@sha256:"
	if i := strings.Index(base, marker); i >= 0 {
		hex := base[i+len(marker):]
		if len(hex) > 12 {
			hex = hex[:12]
		}
		return base[:i] + marker + hex
	}
	return base
}

func kernelAtLeast(raw string, major, minor int) (bool, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return false, fmt.Errorf("cannot parse kernel %q", raw)
	}
	maj, err1 := strconv.Atoi(leadingInt(parts[0]))
	min, err2 := strconv.Atoi(leadingInt(parts[1]))
	if err1 != nil || err2 != nil || leadingInt(parts[0]) == "" {
		return false, fmt.Errorf("cannot parse kernel %q", raw)
	}
	if maj != major {
		return maj > major, nil
	}
	return min >= minor, nil
}

func leadingInt(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func empty(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}
