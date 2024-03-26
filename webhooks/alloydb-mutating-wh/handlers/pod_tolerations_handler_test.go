package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/api/admission/v1beta1"
)

const failed = "\u2717"

// Init the route & tolerations for the unit tests.
func init() {
	Routes()
	setDefaultTolerations()
}

func TestServe(t *testing.T) {
	tests := []struct {
		id          int
		name        string
		body        io.Reader
		userAgent   string
		method      string
		contentType string
		admit       AdmitFunc
		wantStatus  int
		wantResp    *v1beta1.AdmissionResponse
	}{
		{
			name: "Valid Request",
			id:   0,
			body: func() io.Reader {
				podBytes := []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"containers": [{"name": "fake-container"}]}}`)
				ar := &v1beta1.AdmissionReview{
					Request: &v1beta1.AdmissionRequest{
						UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
						Kind: metav1.GroupVersionKind{
							Group:   "",
							Version: "v1",
							Kind:    "Pod",
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
						Namespace: "fake-ns",
						Operation: "CREATE",
						Object: runtime.RawExtension{
							Raw: podBytes,
						},
					},
				}
				body, _ := json.Marshal(ar)
				return strings.NewReader(string(body))

			}(),
			method:      http.MethodPost,
			contentType: "application/json",
			admit:       mutatePod,
			wantStatus:  http.StatusOK,
			wantResp: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Patch:   []byte(`[{"op":"replace","path":"/spec/tolerations","value":[{"key":"cloud.google.com/alloydb-host","operator":"Exists","effect":"NoSchedule"}]}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
		{
			name: "Valid Request Invalid Content Type",
			id:   1,
			body: func() io.Reader {
				podBytes := []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"containers": [{"name": "fake-container"}]}}`)
				ar := &v1beta1.AdmissionReview{
					Request: &v1beta1.AdmissionRequest{
						UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
						Kind: metav1.GroupVersionKind{
							Group:   "",
							Version: "v1",
							Kind:    "Pod",
						},
						Resource: metav1.GroupVersionResource{
							Group:    "",
							Version:  "v1",
							Resource: "pods",
						},
						Namespace: "fake-ns",
						Operation: "CREATE",
						Object: runtime.RawExtension{
							Raw: podBytes,
						},
					},
				}
				body, _ := json.Marshal(ar)
				return strings.NewReader(string(body))

			}(),
			method:      http.MethodPost,
			contentType: "text/plain",
			admit:       mutatePod,
			wantStatus:  http.StatusBadRequest,
			wantResp:    nil,
		},
		{
			name:        "Invalid JSON Request Body",
			id:          2,
			body:        strings.NewReader(`{"request":`),
			contentType: "application/json",
			method:      http.MethodPost,
			admit:       mutatePod,
			wantStatus:  http.StatusBadRequest,
			wantResp:    nil,
		},
		{
			name:        "Empty Request Body",
			id:          3,
			body:        strings.NewReader(""),
			contentType: "application/json",
			method:      http.MethodPost,
			admit:       mutatePod,
			wantStatus:  http.StatusBadRequest,
			wantResp:    nil,
		},
		{
			name:        "Kubelet Probes",
			id:          4,
			body:        nil,
			contentType: "application/json",
			method:      http.MethodGet,
			userAgent:   "Kubelet",
			admit:       mutatePod,
			wantStatus:  http.StatusOK,
			wantResp:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/mutate", tt.body)
			req.Header.Set("Content-Type", tt.contentType)
			req.Header.Set("User-Agent", tt.userAgent)

			rr := httptest.NewRecorder()
			serve(rr, req, tt.admit)

			resp := rr.Result()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("\t%s\tTest ID=%d::Got status code %d, want %d", failed, tt.id, resp.StatusCode, tt.wantStatus)
			}

			if tt.wantResp != nil {
				gotResp := &v1beta1.AdmissionReview{}
				if err := json.NewDecoder(resp.Body).Decode(gotResp); err != nil {
					t.Errorf("\t%s\tTest ID=%d::Could not decode response: %v", failed, tt.id, err)
				}
				if !reflect.DeepEqual(gotResp.Response, tt.wantResp) {
					t.Errorf("\t%s\tTest ID=%d::Got response %+v, want %+v", failed, tt.id, gotResp.Response, tt.wantResp)
				}
			}
		})
	}
}

func TestMutatePod(t *testing.T) {
	tests := []struct {
		id   int
		name string
		tols []corev1.Toleration
		ar   *v1beta1.AdmissionReview
		want *v1beta1.AdmissionResponse
	}{
		{
			name: "Valid Pod No Tolerations",
			id:   0,
			tols: tolerations,
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"containers": [{"name": "fake-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Patch:   []byte(`[{"op":"replace","path":"/spec/tolerations","value":[{"key":"cloud.google.com/alloydb-host","operator":"Exists","effect":"NoSchedule"}]}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
		{
			name: "Pod With Existing Tolerations",
			id:   1,
			tols: tolerations,
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "test-pod"}, "spec": {"tolerations": [{"key": "key1", "operator": "Equal", "value": "value1", "effect": "NoSchedule"}], "containers": [{"name": "test-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Patch:   []byte(`[{"op":"replace","path":"/spec/tolerations","value":[{"key":"cloud.google.com/alloydb-host","operator":"Exists","effect":"NoSchedule"},{"key":"key1","operator":"Equal","value":"value1","effect":"NoSchedule"}]}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
		{
			name: "Invalid Kind",
			id:   2,
			tols: tolerations,
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "InvalidKind", "metadata": {"name": "test-pod"}, "spec": {"containers": [{"name": "test-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: false,
				Result: &metav1.Status{
					Message: "Invalid Kind for the request, only pods are supported for mutation",
				},
			},
		},
		{
			name: "No Defined Tolerations",
			id:   3,
			tols: make([]corev1.Toleration, 0),
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"containers": [{"name": "fake-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Result: &metav1.Status{
					Status: "Success",
				},
			},
		},
		{
			name: "Existing Same Toleration",
			id:   4,
			tols: tolerations,
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"tolerations": [{"key": "cloud.google.com/alloydb-host", "operator": "Exists", "effect": "NoSchedule"}], "containers": [{"name": "fake-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Patch:   []byte(`[{"op":"replace","path":"/spec/tolerations","value":[{"key":"cloud.google.com/alloydb-host","operator":"Exists","effect":"NoSchedule"}]}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
		{
			name: "Existing One Same And One Unique Toleration",
			id:   5,
			tols: tolerations,
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"tolerations": [{"key": "cloud.google.com/alloydb-host", "operator": "Exists", "effect": "NoSchedule"}, {"key": "key1", "operator": "Exists", "effect": "NoSchedule"}], "containers": [{"name": "fake-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Patch:   []byte(`[{"op":"replace","path":"/spec/tolerations","value":[{"key":"cloud.google.com/alloydb-host","operator":"Exists","effect":"NoSchedule"},{"key":"key1","operator":"Exists","effect":"NoSchedule"}]}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mutatePod(tt.ar, tt.tols)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("\t%s\tgot response %+v, want %+v", failed, got, tt.want)
			}
		})
	}
}

func TestConstructPatch(t *testing.T) {
	tests := []struct {
		name        string
		tolerations []corev1.Toleration
		want        []byte
	}{
		{
			name: "Construct Patch",
			tolerations: []corev1.Toleration{
				{
					Key:      "key1",
					Operator: corev1.TolerationOpEqual,
					Value:    "value1",
				},
			},
			want: []byte(`[{"op":"replace","path":"/spec/tolerations","value":[{"key":"key1","operator":"Equal","value":"value1"}]}]`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := constructPatch(tt.tolerations)
			if err != nil {
				t.Errorf("\t%s\tconstructPatch() error = %v", failed, err)
			}
			if string(got) != string(tt.want) {
				t.Errorf("\t%s\tconstructPatch() = %v, want %v", failed, string(got), string(tt.want))
			}
		})
	}
}

func setDefaultTolerations() {
	tolerations = []corev1.Toleration{
		{
			Key:      "cloud.google.com/alloydb-host",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}
}
