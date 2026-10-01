package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeKube struct {
	replicas int32
	err      error
}

func (f *fakeKube) ListDeployments(context.Context) ([]appsv1.Deployment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: appsv1.DeploymentSpec{Replicas: &f.replicas}}}, nil
}
func (f *fakeKube) WaitForSync(context.Context) error                  { return f.err }
func (f *fakeKube) GetReplicas(context.Context, string) (int32, error) { return f.replicas, f.err }
func (f *fakeKube) SetReplicas(_ context.Context, _ string, replicas int32) (int32, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.replicas = replicas
	return replicas, nil
}
func (f *fakeKube) Ready(context.Context) error { return f.err }

func TestGetDeploymentHappyPath(t *testing.T) {
	client := &fakeKube{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments", nil)
	w := httptest.NewRecorder()
	New(client, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestGetReplicasHappyPath(t *testing.T) {
	client := &fakeKube{replicas: 3}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/demo/replicas", nil)
	w := httptest.NewRecorder()
	New(client, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	if !strings.Contains(w.Body.String(), `"replicas":3`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestSetReplicasRejectsInvalidCount(t *testing.T) {
	client := &fakeKube{replicas: 1}
	r := httptest.NewRequest(http.MethodPut, "/api/v1/deployments/demo/replicas", strings.NewReader(`{"replicas":101}`))
	w := httptest.NewRecorder()
	New(client, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	if client.replicas != 1 {
		t.Fatalf("replicas changed to %d", client.replicas)
	}
}

func TestSetReplicasForbidden(t *testing.T) {
	client := &fakeKube{
		err: apierrors.NewForbidden(
			schema.GroupResource{
				Group:    "apps",
				Resource: "deployments/scale",
			},
			"demo",
			errors.New("access denied"),
		),
	}

	r := httptest.NewRequest(http.MethodPut, "/api/v1/deployments/demo/replicas", strings.NewReader(`{"replicas":5}`))
	w := httptest.NewRecorder()
	New(client, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestSetReplicasBadGateway(t *testing.T) {
	client := &fakeKube{err: errors.New("kubernetes API unavailable")}
	r := httptest.NewRequest(http.MethodPut, "/api/v1/deployments/demo/replicas", strings.NewReader(`{"replicas":5}`))
	w := httptest.NewRecorder()
	New(client, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestLivenessHappyPath(t *testing.T) {
	client := &fakeKube{err: errors.New("api down")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := httptest.NewRequest(http.MethodGet, "/livez", nil)
	w := httptest.NewRecorder()

	NewHealthHandler(client, logger).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestReadinessHappyPath(t *testing.T) {
	client := &fakeKube{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()

	NewHealthHandler(client, logger).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestReadinessUnhappyPath(t *testing.T) {
	client := &fakeKube{err: errors.New("api down")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()

	NewHealthHandler(client, logger).ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
