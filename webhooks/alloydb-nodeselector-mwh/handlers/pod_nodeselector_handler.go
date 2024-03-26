package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	log "k8s.io/klog/v2"
)

type AdmitFunc func(*v1beta1.AdmissionReview, map[string]string) *v1beta1.AdmissionResponse

var nodelSelectors map[string]string

func Routes() {
	http.HandleFunc("/mutate", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, mutatePod)
	})
	log.Info("handlers.Routes():Registered the handler for the path /mutate")

}

func BuildSelectors() {
	path := os.Getenv("SELECTORS_CONFIG_PATH")
	fileName := os.Getenv("SELECTORS_CONFIG_FILE")
	filePath := filepath.Join(path, fileName)
	configFile, err := os.Open(filePath)
	if err != nil {
		log.Fatalf(fmt.Sprintf("handlers.BuildSelectors():Error opening node selectors config file %s", os.Getenv("SELECTORS_CONFIG_PATH")), err)
	}
	defer configFile.Close()
	data, err := io.ReadAll(configFile)
	if err != nil {
		log.Fatalf("handlers.BuildSelectors():Error reading the node selectors data from the file:: %v", err)
	}
	if err = json.Unmarshal(data, &nodelSelectors); err != nil {
		log.Fatalf("handlers.BuildSelectors():Error unmarshalling the node selectors data from the file:: %v", err)
	}
	log.Info("handlers.BuildSelectors():Initialized the node selectors to be configured for the pod")

}

func serve(w http.ResponseWriter, r *http.Request, admit AdmitFunc) {

	if r.Method != http.MethodPost {
		if r.Header.Get("User-Agent") == "Kubelet" {
			w.WriteHeader(http.StatusOK)
			return
		}
		log.Errorf("handlers.serve():Received a %s request instead of POST", r.Method)
		http.Error(w, fmt.Sprintf("Only POST requests are accepted, received: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
	var body []byte
	if r.Body != nil {
		if data, err := io.ReadAll(r.Body); err != nil {
			log.Errorf("handlers.serve():Error occured while reading from the request %v", err)
			http.Error(w, fmt.Sprintf("Could not read the request body or the request body is empty: %v", err), http.StatusBadRequest)
			return
		} else {
			body = data
		}
	} else {
		http.Error(w, "Empty request received from the client", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		log.Errorf("handlers.serve():Error occurred, content-type must be set to application/json but received %s", contentType)
		http.Error(w, fmt.Sprintf("Invalid Content-Type header received: %s", contentType), http.StatusBadRequest)
		return
	}
	addmissionReview := v1beta1.AdmissionReview{}
	if err := json.Unmarshal(body, &addmissionReview); err != nil {
		log.Errorf("handlers.serve():Could not unmarshall the AdmissionReview object from the request:: %v", err)
		http.Error(w, fmt.Sprintf("Could not unmarshall AdmissionReview from the request body is empty:: %v", err), http.StatusBadRequest)
		return
	}
	log.Infof("handlers.serve():Received a valid AdmissionReview for mutating the pod UID = %s", addmissionReview.Request.UID)
	admissionResponse := admit(&addmissionReview, nodelSelectors)
	addmissionReview.Response = admissionResponse
	resp, err := json.Marshal(addmissionReview)
	if err != nil {
		log.Errorf("handlers.serve():Error marshalling the AdmissionReview object:: %v", err)
		http.Error(w, fmt.Sprintf("Error marshalling the AdmissionReview object:: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(resp); err != nil {
		log.Errorf("handlers.serve():Error writing JSON response back to the client:: %v", err)
		http.Error(w, fmt.Sprintf("Error writing the JSON back to the client:: %v", err), http.StatusInternalServerError)
		return
	}

}

func mutatePod(ar *v1beta1.AdmissionReview, selectors map[string]string) *v1beta1.AdmissionResponse {

	log.Info("handlers.mutatePod():Starting to add AlloyDB specific node selectors to the pod")
	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	if err := json.Unmarshal(raw, &pod); err != nil {
		return &v1beta1.AdmissionResponse{
			UID:     ar.Request.UID,
			Allowed: false,
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	if pod.TypeMeta.Kind != "Pod" {
		return &v1beta1.AdmissionResponse{
			UID:     ar.Request.UID,
			Allowed: false,
			Result: &metav1.Status{
				Message: "Invalid Kind for the request, only pods are supported for mutation",
			},
		}
	}
	if len(selectors) == 0 {
		return &v1beta1.AdmissionResponse{
			UID:     ar.Request.UID,
			Allowed: true,
			Result: &metav1.Status{
				Status: "Success",
			},
		}

	}
	existing := pod.Spec.NodeSelector
	combined := mergeMaps(existing, selectors)
	patch, err := constructPatch(combined)
	if err != nil {
		log.Errorf("handlers.mutatePod():Could not create a patch for adding node selectors to the pod:: %v", err)
		return &v1beta1.AdmissionResponse{
			UID:     ar.Request.UID,
			Allowed: false,
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}
	log.Info("handlers.mutatePod():Added the AlloyDB Omni nodepool specific node selectors to the pod & returning the patch")
	return &v1beta1.AdmissionResponse{
		UID:     ar.Request.UID,
		Allowed: true,
		Patch:   patch,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

func mergeMaps(existing, new map[string]string) map[string]string {

	result := make(map[string]string)
	for k, val := range existing {
		result[k] = val
	}
	for k, val := range new {
		if _, ok := result[k]; !ok { // Don't overwrite but only add unique keys
			result[k] = val
		}
	}
	return result

}

func constructPatch(combined map[string]string) ([]byte, error) {

	patch := []interface{}{
		map[string]interface{}{
			"op":    "replace",
			"path":  "/spec/nodeSelector",
			"value": combined,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return patchBytes, nil

}
