package kube

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// Client is the subset of Kubernetes operations used by the HTTP API.
type Client interface {
	WaitForSync(ctx context.Context) error
	ListDeployments(ctx context.Context) ([]appsv1.Deployment, error)
	GetReplicas(ctx context.Context, name string) (int32, error)
	SetReplicas(ctx context.Context, name string, replicas int32) (int32, error)
	Ready(ctx context.Context) error
}

type client struct {
	clientset kubernetes.Interface
	namespace string
	informer  cache.SharedIndexInformer
}

// New creates a Kubernetes client and starts a namespace-scoped Deployment
// informer. Reads are served from the informer cache after the initial sync.
func New(ctx context.Context, namespace string) (Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
		cfg, err = clientCfg.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes clientset: %w", err)
	}
	return newWithClientset(ctx, clientset, namespace)
}

func newWithClientset(ctx context.Context, clientset kubernetes.Interface, namespace string) (Client, error) {
	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		0,
		informers.WithNamespace(namespace),
	)
	informer := factory.Apps().V1().Deployments().Informer()
	factory.Start(ctx.Done())

	return &client{clientset: clientset, namespace: namespace, informer: informer}, nil
}

func (c *client) WaitForSync(ctx context.Context) error {
	syncCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if !cache.WaitForCacheSync(syncCtx.Done(), c.informer.HasSynced) {
		return fmt.Errorf("deployment informer cache failed to sync")
	}

	return nil
}

func (c *client) ListDeployments(context.Context) ([]appsv1.Deployment, error) {
	objects := c.informer.GetStore().List()
	deployments := make([]appsv1.Deployment, 0, len(objects))
	for _, object := range objects {
		deployment, ok := object.(*appsv1.Deployment)
		if !ok {
			continue
		}
		deployments = append(deployments, *deployment.DeepCopy())
	}
	return deployments, nil
}

func (c *client) GetReplicas(_ context.Context, name string) (int32, error) {
	key := c.namespace + "/" + name
	object, exists, err := c.informer.GetStore().GetByKey(key)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, apierrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, name)
	}
	deployment, ok := object.(*appsv1.Deployment)
	if !ok {
		return 0, fmt.Errorf("cached object %q is not a deployment", name)
	}
	if deployment.Spec.Replicas == nil {
		return 1, nil
	}
	return *deployment.Spec.Replicas, nil
}

func (c *client) SetReplicas(ctx context.Context, name string, replicas int32) (int32, error) {
	var updatedReplicas int32

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		scale, err := c.clientset.AppsV1().
			Deployments(c.namespace).
			GetScale(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		scale.Spec.Replicas = replicas

		updated, err := c.clientset.AppsV1().
			Deployments(c.namespace).
			UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		updatedReplicas = updated.Spec.Replicas
		return nil
	})

	if err != nil {
		return 0, err
	}

	return updatedReplicas, nil
}

func (c *client) Ready(ctx context.Context) error {
	if !c.informer.HasSynced() {
		return fmt.Errorf("deployment informer cache is not synced")
	}

	_, err := c.clientset.AppsV1().
		Deployments(c.namespace).
		List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("check kubernetes API connectivity: %w", err)
	}

	return nil
}
