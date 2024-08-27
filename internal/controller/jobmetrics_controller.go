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
	"errors"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	rgravlinv1 "github.com/rgravlin/networktest-operator/api/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"strings"
	"time"
)

const (
	LabelSetController      = "rgravlin.github.com/controller"
	LabelSetNetworkTestName = "rgravlin.github.com/networkTestName"
	JobErrorGetJobNotExist  = "CronJob does not exist"
)

// JobMetricReconciler reconciles a JobMetric object
type JobMetricReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HttpClient *http.Client
	PromGauges map[string]prometheus.Gauge
}

// +kubebuilder:rbac:groups=rgravlin.github.com,resources=networktests,verbs=get;list
// +kubebuilder:rbac:groups=rgravlin.github.com,resources=networktests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rgravlin.github.com.kubebuilder.io,resources=networktests,verbs=get;list
// +kubebuilder:rbac:groups=rgravlin.github.com.kubebuilder.io,resources=networktests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs/status,verbs=get
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.4/pkg/reconcile
func (r *JobMetricReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	job := &batchv1.Job{}
	if err := r.Get(ctx, req.NamespacedName, job); err != nil {
		logger.Error(err, JobErrorGetJobNotExist)
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// find the latest job
	latestJob, err := r.getLatestJobFromJob(context.TODO(), *job)
	if err != nil {
		return ctrl.Result{}, errors.New("error retrieving latest job")
	}

	// update gauge
	if err := r.updateJobMetric(latestJob); err != nil {
		return ctrl.Result{}, fmt.Errorf("%s: %v", "could not update job metric", err)
	}

	// TODO: fix this
	// update NetworkTest status
	if err := r.updateNetworkStatus(context.TODO(), latestJob); err != nil {
		return ctrl.Result{}, fmt.Errorf("%s: %v", "could not update networktest status", err)
	}

	logger.V(2).Info("successfully reconciled job")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *JobMetricReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}, builder.WithPredicates(getNetworkTestJobPredicates())).
		Complete(r)
}

func (r *JobMetricReconciler) updateJobMetric(job batchv1.Job) error {
	// retrieve network name from label
	networkTestName, ok := job.Labels[LabelSetNetworkTestName]
	if !ok {
		return errors.New("networkTestName label not found")
	}

	// create the gauge to write metrics to
	gauge := r.newGauge(networkTestName)

	// ensure the gauge is registered for the job
	err := metrics.Registry.Register(gauge)
	if exists, ok := err.(prometheus.AlreadyRegisteredError); ok {
		gauge = exists.ExistingCollector.(prometheus.Gauge)
	} else {
		return err
	}

	if job.Status.Failed == 1 {
		gauge.Set(1)
	}

	if job.Status.Succeeded == 1 {
		gauge.Set(0)
	}

	return nil
}

func (r *JobMetricReconciler) updateNetworkStatus(ctx context.Context, job batchv1.Job) error {
	networkTest, err := r.getNetworkTestForJob(ctx, job)
	if err != nil {
		return err
	}

	if job.Status.Succeeded == 1 {
		if networkTest.Status.LastSuccess == nil || job.Status.CompletionTime.After(networkTest.Status.LastSuccess.Time) {
			networkTest.Status.LastSuccess = &metav1.Time{Time: job.Status.CompletionTime.Time}
		}
		if err := r.Client.Status().Update(ctx, networkTest); err != nil {
			return err
		}
	}

	return nil
}

func (r *JobMetricReconciler) getNetworkTestForJob(ctx context.Context, job batchv1.Job) (*rgravlinv1.NetworkTest, error) {
	networkTest := &rgravlinv1.NetworkTest{}
	var networkTestName string
	if _, ok := job.Labels[LabelSetNetworkTestName]; ok {
		networkTestName = job.Labels[LabelSetNetworkTestName]
	}
	// get the NetworkTest and return it
	namespacedName := types.NamespacedName{
		Name:      networkTestName,
		Namespace: job.Namespace,
	}

	if err := r.Get(ctx, namespacedName, networkTest); err != nil {
		return networkTest, err
	}

	return networkTest, nil
}

func (r *JobMetricReconciler) getLatestJobFromJob(ctx context.Context, job batchv1.Job) (batchv1.Job, error) {
	var jobs batchv1.JobList
	var latest string
	var latestJob batchv1.Job

	var label labels.Selector
	// we want to look up all jobs belonging to this network test
	if _, ok := job.Labels[LabelSetNetworkTestName]; ok {
		label = labels.SelectorFromSet(map[string]string{
			LabelSetNetworkTestName: job.Labels[LabelSetNetworkTestName],
		})
	}
	listOpts := client.ListOptions{
		LabelSelector: label,
	}
	if err := r.List(ctx, &jobs, &listOpts); err != nil {
		return latestJob, err
	}

	// find the latest timestamp
	var latestTime time.Time
	for _, j := range jobs.Items {
		if j.CreationTimestamp.After(latestTime) {
			latestTime = j.CreationTimestamp.Time
			latest = j.Name
		}
	}

	// get the job and return it
	namespacedName := types.NamespacedName{
		Name:      latest,
		Namespace: job.Namespace,
	}
	if err := r.Get(ctx, namespacedName, &latestJob); err != nil {
		return latestJob, err
	}

	return latestJob, nil
}

func (r *JobMetricReconciler) newGauge(name string) prometheus.Gauge {
	name = strings.Replace(name, "-", "_", -1)
	newGauge := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: name,
			Help: fmt.Sprintf("Status of job %s", name),
		},
	)

	if _, ok := r.PromGauges[name]; !ok {
		r.PromGauges[name] = newGauge
		return r.PromGauges[name]
	}

	return r.PromGauges[name]
}

func getNetworkTestJobPredicates() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if _, ok := e.ObjectOld.GetLabels()[LabelSetController]; ok {
				return true
			}
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}
}
