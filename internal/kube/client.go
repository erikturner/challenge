package kube

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// Client defines the Kubernetes operations used by the HTTP API.
type Client interface {
	// WaitForSync waits for the Deployment informer cache to synchronize.
	WaitForSync(ctx context.Context) error

	// ListDeployments returns Deployments from the informer cache.
	ListDeployments(ctx context.Context) ([]appsv1.Deployment, error)

	// GetReplicas returns the cached desired replica count for a Deployment.
	GetReplicas(ctx context.Context, name string) (int32, error)

	// SetReplicas updates the desired replica count for a Deployment.
	SetReplicas(ctx context.Context, name string, replicas int32) (int32, error)

	// Ready reports whether the cache is synchronized and the Kubernetes API is reachable.
	Ready(ctx context.Context) error
}

type client struct {
	clientset kubernetes.Interface
	namespace string
	informer  cache.SharedIndexInformer
	lister    appslisters.DeploymentLister
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
	const resyncPeriod = 0
	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		resyncPeriod,
		informers.WithNamespace(namespace),
	)

	deploymentInformer := factory.Apps().V1().Deployments()

	informer := deploymentInformer.Informer()
	lister := deploymentInformer.Lister()

	factory.Start(ctx.Done())

	return &client{clientset: clientset, namespace: namespace, informer: informer, lister: lister}, nil
}

func (c *client) WaitForSync(ctx context.Context) error {
	if !cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced) {
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
	deployment, err := c.lister.Deployments(c.namespace).Get(name)
	if err != nil {
		return 0, err
	}

	if deployment.Spec.Replicas == nil {
		return 1, nil
	}
	return *deployment.Spec.Replicas, nil
}

func (c *client) SetReplicas(ctx context.Context, name string, replicas int32) (int32, error) {
	var updatedReplicas int32
	var conflictBackoff = wait.Backoff{
		Steps:    5,
		Duration: 50 * time.Millisecond,
		Factor:   1.0,
		Jitter:   0.1,
	}

	err := retry.RetryOnConflict(conflictBackoff, func() error {
		// Retrieve the Deployment from the informer-backed cache to avoid
		// an additional Kubernetes API read before updating its scale.
		// The cached resource version is used for optimistic concurrency.
		deployment, err := c.lister.Deployments(c.namespace).Get(name)
		if err != nil {
			return err
		}

		scale := &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:            deployment.Name,
				Namespace:       deployment.Namespace,
				ResourceVersion: deployment.ResourceVersion,
			},
			Spec: autoscalingv1.ScaleSpec{
				Replicas: replicas,
			},
		}

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
