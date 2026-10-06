package kube

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestGetReplicasUsesInformerCache(t *testing.T) {
	replicas := int32(4)
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := newWithClientset(ctx, clientset, "default")
	if err != nil {
		t.Fatalf("newWithClientset: %v", err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		t.Fatalf("WaitForSync: %v", err)
	}

	clientset.ClearActions()
	got, err := client.GetReplicas(ctx, "demo")
	if err != nil {
		t.Fatalf("GetReplicas: %v", err)
	}
	if got != 4 {
		t.Fatalf("replicas = %d, want 4", got)
	}
	if actions := clientset.Actions(); len(actions) != 0 {
		t.Fatalf("cached read unexpectedly called Kubernetes API: %#v", actions)
	}
}

func TestReadyHappyPath(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := newWithClientset(ctx, clientset, "default")
	if err != nil {
		t.Fatalf("newWithClientset: %v", err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		t.Fatalf("WaitForSync: %v", err)
	}

	if err := client.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

func TestGetReplicasUsesCache(t *testing.T) {
	replicas := int32(3)

	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := newWithClientset(ctx, clientset, "default")
	if err != nil {
		t.Fatalf("newWithClientset: %v", err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		t.Fatalf("WaitForSync: %v", err)
	}

	clientset.ClearActions()

	got, err := client.GetReplicas(ctx, "demo")
	if err != nil {
		t.Fatalf("GetReplicas: %v", err)
	}

	if got != 3 {
		t.Fatalf("replicas = %d, want 3", got)
	}

	if actions := clientset.Actions(); len(actions) != 0 {
		t.Fatalf("GetReplicas unexpectedly called Kubernetes API: %#v", actions)
	}
}

func TestSetReplicasRetriesOnConflict(t *testing.T) {
	replicas := int32(2)

	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "demo",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := newWithClientset(ctx, clientset, "default")
	if err != nil {
		t.Fatalf("newWithClientset: %v", err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		t.Fatalf("WaitForSync: %v", err)
	}

	clientset.ClearActions()

	updateAttempts := 0

	clientset.PrependReactor(
		"update",
		"deployments",
		func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "scale" {
				return false, nil, nil
			}

			updateAction, ok := action.(ktesting.UpdateAction)
			if !ok {
				return false, nil, nil
			}

			updateAttempts++

			if updateAttempts == 1 {
				return true, nil, apierrors.NewConflict(
					schema.GroupResource{
						Group:    "apps",
						Resource: "deployments",
					},
					"demo",
					errors.New("simulated conflict"),
				)
			}

			return true, updateAction.GetObject(), nil
		},
	)

	got, err := client.SetReplicas(ctx, "demo", 5)
	if err != nil {
		t.Fatalf("SetReplicas: %v", err)
	}

	if got != 5 {
		t.Fatalf("replicas = %d, want 5", got)
	}

	if updateAttempts != 2 {
		t.Fatalf("update attempts = %d, want 2", updateAttempts)
	}
}

func TestSetReplicasUsesScaleSubresource(t *testing.T) {
	replicas := int32(2)

	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "demo",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := newWithClientset(ctx, clientset, "default")
	if err != nil {
		t.Fatalf("newWithClientset: %v", err)
	}

	if err := client.WaitForSync(ctx); err != nil {
		t.Fatalf("WaitForSync: %v", err)
	}

	clientset.ClearActions()

	clientset.PrependReactor(
		"update",
		"deployments",
		func(action ktesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "scale" {
				return false, nil, nil
			}

			updateAction, ok := action.(ktesting.UpdateAction)
			if !ok {
				return false, nil, nil
			}

			return true, updateAction.GetObject(), nil
		},
	)

	got, err := client.SetReplicas(ctx, "demo", 5)
	if err != nil {
		t.Fatalf("SetReplicas: %v", err)
	}

	if got != 5 {
		t.Fatalf("replicas = %d, want 5", got)
	}

	actions := clientset.Actions()

	if len(actions) != 1 {
		t.Fatalf("actions = %d, want 1: %#v", len(actions), actions)
	}

	action := actions[0]

	if action.GetVerb() != "update" {
		t.Fatalf("verb = %q, want update", action.GetVerb())
	}

	if action.GetResource().Resource != "deployments" {
		t.Fatalf("resource = %q, want deployments", action.GetResource().Resource)
	}

	if action.GetSubresource() != "scale" {
		t.Fatalf("subresource = %q, want scale", action.GetSubresource())
	}
}
