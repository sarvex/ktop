package k8s

import (
	"context"
	"errors"
	"time"

	"github.com/vladimirvivien/ktop/views/model"
	"k8s.io/client-go/informers"
	appsV1Informers "k8s.io/client-go/informers/apps/v1"
	batchV1Informers "k8s.io/client-go/informers/batch/v1"
	coreV1Informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
)

type RefreshNodesFunc func(ctx context.Context, items []model.NodeModel) error
type RefreshPodsFunc func(ctx context.Context, items []model.PodModel) error
type RefreshSummaryFunc func(ctx context.Context, items model.ClusterSummary) error

type Controller struct {
	client *Client

	nodeMetricsInformer *NodeMetricsInformer
	podMetricsInformer  *PodMetricsInformer
	namespaceInformer   coreV1Informers.NamespaceInformer
	nodeInformer        coreV1Informers.NodeInformer
	podInformer         coreV1Informers.PodInformer
	pvInformer          coreV1Informers.PersistentVolumeInformer
	pvcInformer         coreV1Informers.PersistentVolumeClaimInformer

	jobInformer     batchV1Informers.JobInformer
	cronJobInformer batchV1Informers.CronJobInformer

	deploymentInformer  appsV1Informers.DeploymentInformer
	daemonSetInformer   appsV1Informers.DaemonSetInformer
	replicaSetInformer  appsV1Informers.ReplicaSetInformer
	statefulSetInformer appsV1Informers.StatefulSetInformer

	nodeRefreshFunc    RefreshNodesFunc
	podRefreshFunc     RefreshPodsFunc
	summaryRefreshFunc RefreshSummaryFunc
}

func newController(client *Client) *Controller {
	ctrl := &Controller{client: client}
	return ctrl
}

func (c *Controller) SetNodeRefreshFunc(fn RefreshNodesFunc) *Controller {
	c.nodeRefreshFunc = fn
	return c
}
func (c *Controller) SetPodRefreshFunc(fn RefreshPodsFunc) *Controller {
	c.podRefreshFunc = fn
	return c
}

func (c *Controller) SetClusterSummaryRefreshFunc(fn RefreshSummaryFunc) *Controller {
	c.summaryRefreshFunc = fn
	return c
}

func (c *Controller) Start(ctx context.Context, resync time.Duration) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	// initialize

	if err := c.client.AssertMetricsAvailable(); err == nil {
		c.nodeMetricsInformer = NewNodeMetricsInformer(c.client.metricsClient, resync)
		nodeMetricsInformerHasSynced := c.nodeMetricsInformer.Informer().HasSynced

		c.podMetricsInformer = NewPodMetricsInformer(c.client.metricsClient, resync, c.client.namespace)
		podMetricsInformerHasSynced := c.podMetricsInformer.Informer().HasSynced

		go c.nodeMetricsInformer.Informer().Run(ctx.Done())
		go c.podMetricsInformer.Informer().Run(ctx.Done())

		if ok := cache.WaitForCacheSync(ctx.Done(), nodeMetricsInformerHasSynced, podMetricsInformerHasSynced); !ok {
			panic("metrics resources failed to sync [nodes, pods, containers]")
		}

	}

	// initialize informer factories
	var factory informers.SharedInformerFactory
	if c.client.namespace == AllNamespaces {
		factory = informers.NewSharedInformerFactory(c.client.kubeClient, resync)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(c.client.kubeClient, resync, informers.WithNamespace(c.client.namespace))
	}

	// NOTE: the followings captures each informer
	// and also calls Informer() method to register the cached type.
	// Call to Informer() must happen before factory.Star() or it hangs.

	// core/V1 informers
	coreInformers := factory.Core().V1()
	c.namespaceInformer = coreInformers.Namespaces()
	namespaceHasSynced := c.namespaceInformer.Informer().HasSynced
	c.nodeInformer = coreInformers.Nodes()
	nodeHasSynced := c.nodeInformer.Informer().HasSynced
	c.podInformer = coreInformers.Pods()
	podHasSynced := c.podInformer.Informer().HasSynced
	c.pvInformer = coreInformers.PersistentVolumes()
	pvHasSynced := c.pvInformer.Informer().HasSynced
	c.pvcInformer = coreInformers.PersistentVolumeClaims()
	pvcHasSynced := c.pvcInformer.Informer().HasSynced

	// Apps/v1 Informers
	appsInformers := factory.Apps().V1()
	c.deploymentInformer = appsInformers.Deployments()
	deploymentHasSynced := c.deploymentInformer.Informer().HasSynced
	c.daemonSetInformer = appsInformers.DaemonSets()
	daemonsetHasSynced := c.daemonSetInformer.Informer().HasSynced
	c.replicaSetInformer = appsInformers.ReplicaSets()
	replicasetHasSynced := c.replicaSetInformer.Informer().HasSynced
	c.statefulSetInformer = appsInformers.StatefulSets()
	statefulsetHasSynced := c.statefulSetInformer.Informer().HasSynced

	// Batch informers
	batchInformers := factory.Batch().V1()
	c.jobInformer = batchInformers.Jobs()
	jobHasSynced := c.jobInformer.Informer().HasSynced
	c.cronJobInformer = batchInformers.CronJobs()
	cronJobHasSynced := c.cronJobInformer.Informer().HasSynced

	factory.Start(ctx.Done())

	// wait immediately for core resources to syn
	// wait for core resources to sync
	if ok := cache.WaitForCacheSync(ctx.Done(),
		namespaceHasSynced,
		nodeHasSynced,
		podHasSynced,
	); !ok {
		panic("core resources failed to sync [namespaces, nodes, pods]")
	}

	// defer waiting for non-core resources to sync
	go func() {
		ok := cache.WaitForCacheSync(ctx.Done(),
			pvHasSynced,
			pvcHasSynced,
			deploymentHasSynced,
			daemonsetHasSynced,
			replicasetHasSynced,
			statefulsetHasSynced,
			jobHasSynced,
			cronJobHasSynced,
		)
		if !ok {
			panic("resource failed to sync")
		}
	}()

	c.setupSummaryHandler(ctx, c.summaryRefreshFunc)
	c.setupNodeHandler(ctx, c.nodeRefreshFunc)
	c.installPodsHandler(ctx, c.podRefreshFunc)

	return nil
}
