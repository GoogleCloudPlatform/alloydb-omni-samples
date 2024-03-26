package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"k8s.io/api/admission/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const failed = "\u2717"

func init() {
	Routes()
	setTestNodeSelectors()
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
				Patch:   []byte(`[{"op":"replace","path":"/spec/nodeSelector","value":{"disk":"ssd","node-type":"database"}}]`),
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
		id        int
		name      string
		ar        *v1beta1.AdmissionReview
		want      *v1beta1.AdmissionResponse
		selectors map[string]string
	}{
		{
			id:        0,
			name:      "Valid Pod No NodeSelector",
			selectors: nodelSelectors,
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
				Patch:   []byte(`[{"op":"replace","path":"/spec/nodeSelector","value":{"disk":"ssd","node-type":"database"}}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
		{
			id:        1,
			name:      "Pod With Existing NodeSelector",
			selectors: nodelSelectors,
			ar: &v1beta1.AdmissionReview{
				Request: &v1beta1.AdmissionRequest{
					UID: types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
					Object: runtime.RawExtension{
						Raw: []byte(`{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "fake-pod", "namespace": "fake-ns"}, "spec": {"nodeSelector": {"environment": "dev"}, "containers": [{"name": "test-container"}]}}`),
					},
				},
			},
			want: &v1beta1.AdmissionResponse{
				UID:     types.UID("70a7fc1a-a84b-4e9d-9e6e-500f45a4697b"),
				Allowed: true,
				Patch:   []byte(`[{"op":"replace","path":"/spec/nodeSelector","value":{"disk":"ssd","environment":"dev","node-type":"database"}}]`),
				PatchType: func() *v1beta1.PatchType {
					pt := v1beta1.PatchTypeJSONPatch
					return &pt
				}(),
			},
		},
		{
			id:        2,
			name:      "Invalid Kind",
			selectors: nodelSelectors,
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
			id:        3,
			name:      "No Defined Selectors",
			selectors: make(map[string]string),
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mutatePod(tt.ar, tt.selectors)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("\t%s\tGot response %+v, want %+v", failed, got, tt.want)
			}
		})
	}
}

func TestConstructPatch(t *testing.T) {
	tests := []struct {
		id         int
		name       string
		combined   map[string]string
		wantPatch  []byte
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "Empty NodeSelector",
			id:         0,
			combined:   map[string]string{},
			wantPatch:  []byte(`[{"op":"replace","path":"/spec/nodeSelector","value":{}}]`),
			wantErr:    false,
			wantErrMsg: "",
		},
		{
			name:       "Non-Empty NodeSelector",
			id:         1,
			combined:   map[string]string{"disk": "ssd", "node-type": "database"},
			wantPatch:  []byte(`[{"op":"replace","path":"/spec/nodeSelector","value":{"disk":"ssd","node-type":"database"}}]`),
			wantErr:    false,
			wantErrMsg: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPatch, err := constructPatch(tt.combined)
			if (err != nil) != tt.wantErr {
				t.Errorf("\t%s\tconstructPatch() error = %v, wantErr %v", failed, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				if gotPatch != nil {
					t.Errorf("\t%s\tconstructPatch() gotPatch = %v, want nil", failed, gotPatch)
				}
				if err.Error() != tt.wantErrMsg {
					t.Errorf("\t%s\tconstructPatch() error message = %v, want %v", failed, err.Error(), tt.wantErrMsg)
				}
				return
			}
			if !json.Valid(gotPatch) {
				t.Errorf("\t%s\tconstructPatch() gotPatch is not valid JSON", failed)
			}
			if string(gotPatch) != string(tt.wantPatch) {
				t.Errorf("\t%s\tconstructPatch() = %v, want %v", failed, gotPatch, tt.wantPatch)
			}
		})
	}
}

func setTestNodeSelectors() {

	nodelSelectors = map[string]string{
		"disk":      "ssd",
		"node-type": "database",
	}
}
