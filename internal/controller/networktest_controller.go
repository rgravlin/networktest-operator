/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-test/deep"
	"github.com/pkg/errors"
	rgravlinv1 "github.com/rgravlin/networktest-operator/api/v1"
	batchv1 "k8s.io/api/batch/v1"
	v2 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	NetworkTestName                    = "networktest-operator"
	NetworkTestErrorCronJobNotExist    = "CronJob does not exist"
	NetworkTestCronJobPrefix           = "nt-j"
	NetworkTestRunnerImageName         = "networktest-runner"
	NetworkTestDefaultHTTPTimeoutParam = "--connect-timeout"
	NetworkTestDefaultDNSTimeoutParam  = "-timeout="
	NetworkTestFinalizer               = "rgravlin.github.com/finalizer"
	LabelSetController                 = "rgravlin.github.com/controller"
	LabelSetNetworkTestName            = "rgravlin.github.com/networkTestName"
	DefaultRequeueTime                 = 30 * time.Second
)

// NetworkTestReconciler reconciles a NetworkTest object
type NetworkTestReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HttpClient *http.Client
}

// +kubebuilder:rbac:groups=rgravlin.github.com,resources=networktests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rgravlin.github.com,resources=networktests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rgravlin.github.com,resources=networktests/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.4/pkg/reconcile
func (r *NetworkTestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	networkTest := &rgravlinv1.NetworkTest{}
	if err := r.Get(ctx, req.NamespacedName, networkTest); err != nil {
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// process finalizers
	// examine DeletionTimestamp to determine if object is under deletion
	if !networkTest.IsBeingDeleted() {
		if err := r.registerFinalizer(networkTest, context.TODO()); err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	} else {
		if err := r.handleFinalizer(networkTest, context.TODO()); err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// create CronJob spec from NetworkTest object
	cronJobSpec := newCronJobSpecForNetworkTest(*networkTest)

	// stamp controller reference
	if err := ctrl.SetControllerReference(networkTest, cronJobSpec, r.Scheme); err != nil {
		if err := r.updateNetworkTestStatusError(networkTest, *cronJobSpec, err); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, err
	}

	// retrieve CronJob from system should it exist
	foundCronJob, err := r.getCronJobForNetworkTest(*networkTest)
	if err != nil {
		if err.Error() == NetworkTestErrorCronJobNotExist {
			// the CronJob does not exist so we should create it
			_, err := r.createCronJobFromSpec(cronJobSpec)
			// there is an error creating the CronJob and we should requeue
			if err != nil {
				if err := r.updateNetworkTestStatusError(networkTest, *cronJobSpec, err); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, err
			}
		} else {
			// there was an error retrieving the CronJob
			if err := r.updateNetworkTestStatusError(networkTest, *cronJobSpec, err); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, err
		}
	}

	// validate that the specifications are equal, else we need to update the CronJob
	if foundCronJob != nil {
		if diff := deep.Equal(cronJobSpec.Spec, foundCronJob.Spec); diff != nil {
			logger.V(1).Info("CronJob spec has drifted from controller specification, reconciling", "diff", diff)
			err := r.Client.Update(context.Background(), cronJobSpec)
			if err != nil {
				if err := r.updateNetworkTestStatusError(networkTest, *cronJobSpec, err); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, err
			}
			logger.V(1).Info("Successfully updated CronJob", "CronJob", cronJobSpec.ObjectMeta.Name)
		}
	}

	// update CR status
	if err := r.updateNetworkTestStatusOk(networkTest, *cronJobSpec, "CronJob successfully synced"); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NetworkTestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rgravlinv1.NetworkTest{}).
		Owns(&batchv1.CronJob{}).
		Complete(r)
}

func (r *NetworkTestReconciler) registerFinalizer(networkTest *rgravlinv1.NetworkTest, ctx context.Context) error {
	// The object is not being deleted, so if it does not have our finalizer,
	// then lets add the finalizer and update the object. This is equivalent
	// to registering our finalizer.
	if !controllerutil.ContainsFinalizer(networkTest, NetworkTestFinalizer) {
		controllerutil.AddFinalizer(networkTest, NetworkTestFinalizer)
		if err := r.Update(ctx, networkTest); err != nil {
			return err
		}
	}
	return nil
}

func (r *NetworkTestReconciler) handleFinalizer(networkTest *rgravlinv1.NetworkTest, ctx context.Context) error {
	if controllerutil.ContainsFinalizer(networkTest, NetworkTestFinalizer) {
		// our finalizer is present, so lets handle any external dependency
		if err := r.deleteExternalResources(*networkTest); err != nil {
			// if fail to delete the external dependency here, return with error
			// so that it can be retried.
			return err
		}

		// remove our finalizer from the list and update it.
		controllerutil.RemoveFinalizer(networkTest, NetworkTestFinalizer)
		if err := r.Update(ctx, networkTest); err != nil {
			return err
		}
	}
	return nil
}

func (r *NetworkTestReconciler) updateNetworkTestStatusOk(networkTest *rgravlinv1.NetworkTest, cronJob batchv1.CronJob, msg string) error {
	needsUpdate := false
	// update CR status
	if networkTest.Status.CronJobName != cronJob.ObjectMeta.Name {
		networkTest.Status.CronJobName = cronJob.ObjectMeta.Name
		needsUpdate = true
	}

	// update the sync string
	if len(msg) != 0 {
		if networkTest.Status.CronSync != msg {
			networkTest.Status.CronSync = msg
			needsUpdate = true
		}
	}

	if needsUpdate {
		err := r.Status().Update(context.Background(), networkTest)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *NetworkTestReconciler) updateNetworkTestStatusError(networkTest *rgravlinv1.NetworkTest, cronJob batchv1.CronJob, syncErr error) error {
	needsUpdate := false
	// update CR status
	if networkTest.Status.CronJobName != cronJob.ObjectMeta.Name {
		networkTest.Status.CronJobName = cronJob.ObjectMeta.Name
		needsUpdate = true
	}

	// update the sync error
	if syncErr != nil {
		syncError := fmt.Sprintf("CronJob get has failed with error: %v", syncErr)
		if networkTest.Status.CronSync != syncError {
			networkTest.Status.CronSync = syncError
			needsUpdate = true
		}
	}

	if needsUpdate {
		err := r.Status().Update(context.Background(), networkTest)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *NetworkTestReconciler) createCronJobFromSpec(cronJob *batchv1.CronJob) (*batchv1.CronJob, error) {
	if err := r.Create(context.Background(), cronJob); err != nil {
		return cronJob, err
	}

	// successful creation of cronjob
	return cronJob, nil
}

func (r *NetworkTestReconciler) getCronJobForNetworkTest(networkTest rgravlinv1.NetworkTest) (*batchv1.CronJob, error) {
	cronJob := &batchv1.CronJob{}
	cronJobNamespacedName := types.NamespacedName{
		Name:      getCronJobNameForNetworkTest(networkTest.ObjectMeta.Name),
		Namespace: networkTest.ObjectMeta.Namespace,
	}

	if err := r.Get(context.Background(), cronJobNamespacedName, cronJob); err != nil {
		cronJobIsFound := client.IgnoreNotFound(err)
		if cronJobIsFound == nil {
			return cronJob, errors.New(NetworkTestErrorCronJobNotExist)
		} else {
			return cronJob, err
		}
	}
	return cronJob, nil
}

func (r *NetworkTestReconciler) deleteCronJobForNetworkTest(networkTest rgravlinv1.NetworkTest) error {
	cronJob, err := r.getCronJobForNetworkTest(networkTest)
	if err != nil {
		if err.Error() == NetworkTestErrorCronJobNotExist {
			return nil
		}
		return err
	}

	err = r.Client.Delete(context.Background(), cronJob)
	if err != nil {
		return err
	}

	return nil
}

func (r *NetworkTestReconciler) deleteExternalResources(networkTest rgravlinv1.NetworkTest) error {
	// delete any external resources associated with the networkTest
	return r.deleteCronJobForNetworkTest(networkTest)
}

const (
	NetworkTestDefaultActiveDeadlineSeconds  = 60
	NetworkTestDefaultJobHistoryLimit        = int32(3)
	NetworkTestDefaultFailedHistoryLimit     = int32(3)
	NetworkTestDefaultSuspended              = false
	NetworkTestDefaultTerminationGracePeriod = int64(30)
	NetworkTestDefaultScheduler              = "default-scheduler"
	NetworkTestDefaultTerminationLog         = "/dev/termination-log"
	NetworkTestDefaultContainerName          = NetworkTestName
	NetworkTestDefaultRegistry               = "docker.io"
	NetworkTestDefaultRepo                   = "ryangravlin"
)

var NetworkTestDefaultRunnerImage = fmt.Sprintf("%s/%s/%s", NetworkTestDefaultRegistry, NetworkTestDefaultRepo, NetworkTestRunnerImageName)

func newCronJobSpecForNetworkTest(test rgravlinv1.NetworkTest) *batchv1.CronJob {
	var (
		// TODO: without a reasonable deadline a Pod without an image can prevent the job from completing
		//       with a ForbidConcurrent policy this is basically a blocking lock on future jobs
		//       Maybe implement a timeout + (N * time.Second) deadline to ensure cleanup
		// activeDeadlineSeconds    = NetworkTestDefaultActiveDeadlineSeconds
		containerName            = NetworkTestDefaultContainerName
		concurrencyPolicy        = batchv1.ForbidConcurrent
		defaultScheduler         = NetworkTestDefaultScheduler
		dnsPolicy                = v2.DNSClusterFirst
		failedHistoryLimit       = NetworkTestDefaultFailedHistoryLimit
		imagePullPolicy          = v2.PullIfNotPresent
		jobHistoryLimit          = NetworkTestDefaultJobHistoryLimit
		restartPolicy            = v2.RestartPolicyOnFailure
		runnerImage              = NetworkTestDefaultRunnerImage
		schedule                 = rgravlinv1.NetworkTestDefaultSchedule
		securityContext          = v2.PodSecurityContext{}
		suspend                  = NetworkTestDefaultSuspended
		terminationGracePeriod   = NetworkTestDefaultTerminationGracePeriod
		terminationLog           = NetworkTestDefaultTerminationLog
		terminationMessagePolicy = v2.TerminationMessageReadFile
		timeout                  = rgravlinv1.NetworkTestDefaultTimeout
	)

	labels := map[string]string{
		LabelSetController:      NetworkTestName,
		LabelSetNetworkTestName: test.ObjectMeta.Name,
	}

	for l, v := range test.ObjectMeta.Labels {
		labels[l] = v
	}

	for l, v := range test.Spec.Labels {
		labels[l] = v
	}

	if len(test.Spec.Schedule) != 0 {
		schedule = test.Spec.Schedule
	}

	if test.Spec.Suspend {
		suspend = true
	}

	if test.Spec.Timeout != rgravlinv1.NetworkTestDefaultTimeout && test.Spec.Timeout != 0 {
		timeout = test.Spec.Timeout
	}

	if len(test.Spec.Registry) != 0 || len(test.Spec.Repo) != 0 {
		runnerImage = getRunnerImage(test.Spec.Registry, test.Spec.Repo)
	}

	cmd := getCommandForNetworkTest(test, getTimeout(timeout))

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", "nt-j", test.Name),
			Namespace: test.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   schedule,
			ConcurrencyPolicy:          concurrencyPolicy,
			SuccessfulJobsHistoryLimit: &jobHistoryLimit,
			FailedJobsHistoryLimit:     &failedHistoryLimit,
			Suspend:                    &suspend,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: batchv1.JobSpec{
					Template: v2.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: labels,
						},
						Spec: v2.PodSpec{
							DNSPolicy:                     dnsPolicy,
							RestartPolicy:                 restartPolicy,
							SchedulerName:                 defaultScheduler,
							TerminationGracePeriodSeconds: &terminationGracePeriod,
							SecurityContext:               &securityContext,
							Containers: []v2.Container{
								{
									Name:                     containerName,
									Image:                    runnerImage,
									ImagePullPolicy:          imagePullPolicy,
									Command:                  cmd,
									TerminationMessagePath:   terminationLog,
									TerminationMessagePolicy: terminationMessagePolicy,
								},
							},
						},
					},
				},
			},
		},
	}
}

func buildDNSCommand(test rgravlinv1.NetworkTest, timeout string) []string {
	return []string{rgravlinv1.NetworkTestCommandDNS,
		getDNSTimeout(timeout),
		test.Spec.Host,
	}
}

func buildCurlCommand(test rgravlinv1.NetworkTest, timeout string) []string {
	return []string{rgravlinv1.NetworkTestCommandHTTP,
		NetworkTestDefaultHTTPTimeoutParam,
		timeout,
		fmt.Sprintf("%s://%s", test.Spec.Type, test.Spec.Host),
	}
}

func getCommandForNetworkTest(test rgravlinv1.NetworkTest, timeout string) []string {
	switch test.Spec.Type {
	case rgravlinv1.NetworkTestTypeDNS:
		return buildDNSCommand(test, timeout)
	case rgravlinv1.NetworkTestTypeHTTP:
	case rgravlinv1.NetworkTestTypeHTTPS:
	default:
		return buildCurlCommand(test, timeout)
	}
	return buildCurlCommand(test, timeout)
}

func getDNSTimeout(timeout string) string {
	return NetworkTestDefaultDNSTimeoutParam + timeout
}

func getTimeout(timeout int) string {
	return strconv.Itoa(timeout)
}

func getRunnerImage(registry string, repo string) string {
	finalRegistry := NetworkTestDefaultRegistry
	finalRepo := NetworkTestDefaultRepo

	if len(registry) != 0 {
		finalRegistry = registry
	}

	if len(repo) != 0 {
		finalRepo = repo
	}

	return fmt.Sprintf("%s/%s/%s", finalRegistry, finalRepo, NetworkTestRunnerImageName)
}

func getCronJobNameForNetworkTest(name string) string {
	return fmt.Sprintf("%s-%s", "nt-j", name)
}
