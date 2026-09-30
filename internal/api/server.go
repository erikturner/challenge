package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/erikturner/challenge/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

type server struct {
	kube   kube.Client
	logger *slog.Logger
}

type replicaRequest struct {
	Replicas int32 `json:"replicas"`
}

type replicaResponse struct {
	Name     string `json:"name"`
	Replicas int32  `json:"replicas"`
}

type deploymentResponse struct {
	Name     string `json:"name"`
	Replicas int32  `json:"replicas"`
}

func New(client kube.Client, logger *slog.Logger) http.Handler {
	s := &server{kube: client, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deployments", s.listDeployments)
	mux.HandleFunc("GET /api/v1/deployments/{name}/replicas", s.getReplicas)
	mux.HandleFunc("PUT /api/v1/deployments/{name}/replicas", s.setReplicas)
	return logging(logger, mux)
}

// NewHealthHandler exposes liveness and readiness checks for kubelet probes on
// an internal-only container port. The Kubernetes Service does not expose this
// listener.
func NewHealthHandler(client kube.Client) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		
		if err := client.Ready(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "server not ready")
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return mux
}

func (s *server) listDeployments(w http.ResponseWriter, r *http.Request) {
	deployments, err := s.kube.ListDeployments(r.Context())
	if err != nil {
		s.writeKubeError(w, err)
		return
	}
	response := make([]deploymentResponse, 0, len(deployments))
	for _, d := range deployments {
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		response = append(response, deploymentResponse{Name: d.Name, Replicas: replicas})
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *server) getReplicas(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validName(name) {
		writeError(w, http.StatusBadRequest, "invalid deployment name")
		return
	}
	replicas, err := s.kube.GetReplicas(r.Context(), name)
	if err != nil {
		s.writeKubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, replicaResponse{Name: name, Replicas: replicas})
}

func (s *server) setReplicas(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validName(name) {
		writeError(w, http.StatusBadRequest, "invalid deployment name")
		return
	}

	var req replicaRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `request body must be JSON: {"replicas": <0-100>} `)
		return
	}
	if req.Replicas < 0 || req.Replicas > 100 {
		writeError(w, http.StatusBadRequest, "replicas must be between 0 and 100")
		return
	}

	replicas, err := s.kube.SetReplicas(r.Context(), name, req.Replicas)
	if err != nil {
		s.writeKubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, replicaResponse{Name: name, Replicas: replicas})
}

func (s *server) writeKubeError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "deployment not found")
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		writeError(w, http.StatusForbidden, "kubernetes access denied")
	default:
		s.logger.Error("kubernetes request failed", "error", err)
		writeError(w, http.StatusBadGateway, "kubernetes request failed")
	}
}

func validName(name string) bool {
	return len(validation.IsDNS1123Subdomain(name)) == 0
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message, "status": status})
}

func logging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
