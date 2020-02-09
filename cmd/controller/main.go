package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis"
	apibatchv1 "k8s.io/api/batch/v1"
	apicorev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	informerbatchv1 "k8s.io/client-go/informers/batch/v1"
	informercorev1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	batchv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	listerbatchv1 "k8s.io/client-go/listers/batch/v1"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

var jobSpec = &apibatchv1.Job{
	ObjectMeta: metav1.ObjectMeta{
		Name: "",
	},
	Spec: apibatchv1.JobSpec{
		Template: apicorev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app": "",
				},
			},
			Spec: apicorev1.PodSpec{
				RestartPolicy: apicorev1.RestartPolicyOnFailure,
				Containers: []apicorev1.Container{
					{
						Name:            "worker",
						Image:           "gcr.io/playground-237408/worker",
						ImagePullPolicy: apicorev1.PullAlways,
					},
				},
			},
		},
	},
}

func main() {
	redisdb := redis.NewClient(&redis.Options{
		Addr: os.Getenv("REDIS_ADDR"),
	})

	_, err := redisdb.Ping().Result()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("starting controller")

	kubeconfig := ""
	flag.StringVar(&kubeconfig, "kubeconfig", kubeconfig, "kubeconfig file")
	flag.Parse()
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	var (
		config *rest.Config
	)
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating client: %v", err)
		os.Exit(1)
	}
	client := kubernetes.NewForConfigOrDie(config)

	sharedInformers := informers.NewSharedInformerFactory(client, 10*time.Minute)
	controller := NewController(client, redisdb, sharedInformers.Batch().V1().Jobs(), sharedInformers.Core().V1().Pods())

	stop := make(chan struct{})
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	go func() {
		<-sigc
		fmt.Println("received kill")
		stop <- struct{}{}
		stop <- struct{}{}
	}()

	sharedInformers.Start(nil)
	controller.Run(stop)
}

type Work int

const (
	CreateJob Work = iota
	DeleteJob
)

type workItem struct {
	Name string
	Work Work
}

type Controller struct {
	jobGetter       batchv1.JobsGetter
	jobLister       listerbatchv1.JobLister
	jobListerSynced cache.InformerSynced

	podGetter       corev1.PodsGetter
	podLister       listercorev1.PodLister
	podListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface

	redisdb *redis.Client
	jobs    map[string]struct{}
}

func NewController(client *kubernetes.Clientset, redisdb *redis.Client, jobInformer informerbatchv1.JobInformer, podInformer informercorev1.PodInformer) *Controller {
	c := &Controller{
		jobGetter:       client.BatchV1(),
		jobLister:       jobInformer.Lister(),
		jobListerSynced: jobInformer.Informer().HasSynced,
		podGetter:       client.CoreV1(),
		podLister:       podInformer.Lister(),
		podListerSynced: podInformer.Informer().HasSynced,
		queue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "worker-controller"),
		redisdb:         redisdb,
		jobs:            map[string]struct{}{},
	}

	jobInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				log.Println("job added")
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				log.Println("job updated")
				job := newObj.(*apibatchv1.Job)
				if job.Status.Succeeded == 1 || job.Status.Failed == 1 {
					c.ScheduleJobDeletion(job.Name)
				}
			},
			DeleteFunc: func(obj interface{}) {
				log.Print("job deleted")
				job := obj.(*apibatchv1.Job)
				delete(c.jobs, job.Name)
			},
		},
	)

	return c
}

func (c *Controller) Run(stop <-chan struct{}) {
	var wg sync.WaitGroup

	defer func() {
		log.Print("shutting down queue")
		c.queue.ShutDown()

		log.Print("shutting down workers")
		wg.Wait()

		log.Print("workers are all done")
	}()

	log.Print("waiting for cache sync")
	if !cache.WaitForCacheSync(stop, c.jobListerSynced) {
		log.Print("timed out wating for cache sync")
	}
	log.Print("caches are synced")

	jobs, err := c.jobLister.Jobs("default").List(labels.Everything())
	if err != nil {
		log.Println(err)
	}
	for _, job := range jobs {
		c.jobs[job.Name] = struct{}{}
	}

	go func() {
		// runWorker will loop until "something bad" happens. wait.Until will
		// then rekick the worker after one second.
		wait.Until(c.runWorker, time.Second, stop)
		wg.Done()
	}()

	go func() {
		pubsub := c.redisdb.PSubscribe("informer")
		for {
			select {
			case msg := <-pubsub.Channel():
				if _, ok := c.jobs[msg.Payload]; ok {
					continue
				}
				c.ScheduleJobStart(msg.Payload)
			}
		}
	}()

	log.Print("waiting for stop signal")
	<-stop
	log.Print("received stop signal")
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) ScheduleJobStart(name string) {
	c.queue.Add(workItem{
		Name: name,
		Work: CreateJob,
	})
}

func (c *Controller) ScheduleJobDeletion(name string) {
	c.queue.Add(workItem{
		Name: name,
		Work: DeleteJob,
	})
}

func (c *Controller) processNextWorkItem() bool {
	// pull the next work item from queue.  It should be a key we use to lookup
	// something in a cache
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	// you always have to indicate to the queue that you've completed a piece of
	// work
	defer c.queue.Done(key)

	wi := key.(workItem)
	var err error
	switch wi.Work {
	case CreateJob:
		err = c.createJob(wi.Name)
	case DeleteJob:
		err = c.deleteJob(wi.Name)
	}

	// do your work on the key.  This method will contains your "do stuff" logic
	if err == nil {
		// if you had no error, tell the queue to stop tracking history for your
		// key. This will reset things like failure counts for per-item rate
		// limiting
		c.queue.Forget(key)
		return true
	}

	// there was a failure so be sure to report it.  This method allows for
	// pluggable error handling which can be used for things like
	// cluster-monitoring
	runtime.HandleError(fmt.Errorf("doc failed with: %v", err))

	// since we failed, we should requeue the item to work on later.  This
	// method will add a backoff to avoid hotlooping on particular items
	// (they're probably still not going to work right away) and overall
	// controller protection (everything I've done is broken, this controller
	// needs to calm down or it can starve other useful work) cases.
	c.queue.AddRateLimited(key)

	return true
}

func (c *Controller) createJob(name string) error {
	log.Printf("Starting createJob")

	_, err := c.jobGetter.Jobs("default").Get(name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if err == nil {
		log.Print("job already exists")
		return nil
	}

	jobSpec.Spec.Template.ObjectMeta.Labels["app"] = name
	jobSpec.ObjectMeta.Name = name
	jobSpec.Spec.Template.Spec.Containers[0].Env = []apicorev1.EnvVar{
		apicorev1.EnvVar{Name: "REDIS_TOPIC", Value: name},
		apicorev1.EnvVar{Name: "REDIS_ADDR", Value: "redis:6379"},
	}
	result, err := c.jobGetter.Jobs("default").Create(jobSpec)
	if err != nil {
		return err
	}
	log.Printf("Created job %q", result.GetObjectMeta().GetName())
	c.jobs[name] = struct{}{}

	log.Print("Finishing createJob")
	return nil
}

func (c *Controller) deleteJob(name string) error {
	log.Printf("Starting deleteJob")
	defer func() { log.Printf("Finishing deleteJob") }()

	job, err := c.jobGetter.Jobs("default").Get(name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	err = c.jobGetter.Jobs("default").Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	delete(c.jobs, name)

	labelSelector, err := metav1.ParseToLabelSelector("controller-uid=" + string(job.UID))
	if err != nil {
		return err
	}
	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return err
	}
	pods, err := c.podLister.List(selector)
	if err != nil {
		return err
	}

	for _, pod := range pods {
		if err := c.podGetter.Pods("default").Delete(pod.Name, &metav1.DeleteOptions{}); err != nil {
			log.Println(err)
		}
	}

	return nil
}
