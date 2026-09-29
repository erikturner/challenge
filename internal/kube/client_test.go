package kube

import (
	"context"
	"errors"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
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

func TestSetReplicasRetriesOnConflict(t *testing.T) {
	replicas := int32(2)

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

	getAttempts := 0
	updateAttempts := 0

	clientset.PrependReactor("get", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}

		getAttempts++

		return true, &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "demo",
				Namespace:       "default",
				ResourceVersion: fmt.Sprintf("%d", getAttempts),
			},
			Spec: autoscalingv1.ScaleSpec{
				Replicas: 2,
			},
		}, nil
	})

	clientset.PrependReactor("update", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
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
	})

	got, err := client.SetReplicas(ctx, "demo", 5)
	if err != nil {
		t.Fatalf("SetReplicas: %v", err)
	}

	if got != 5 {
		t.Fatalf("replicas = %d, want 5", got)
	}

	if getAttempts != 2 {
		t.Fatalf("get attempts = %d, want 2", getAttempts)
	}

	if updateAttempts != 2 {
		t.Fatalf("update attempts = %d, want 2", updateAttempts)
	}
}
