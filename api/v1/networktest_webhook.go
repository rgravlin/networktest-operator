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

package v1

import (
	"fmt"
	"net"
	"regexp"
	"slices"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	validationutils "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var networktestlog = logf.Log.WithName("networktest-resource")

const validHostNameMaxLength = 255

var validHostNameRFC1123 = regexp.MustCompile(`^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]*[a-zA-Z0-9])\.)*([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9\-]*[A-Za-z0-9])$`)

// SetupWebhookWithManager will set up the manager to manage the webhooks
func (r *NetworkTest) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-rgravlin-github-com-v1-networktest,mutating=true,failurePolicy=fail,sideEffects=None,groups=rgravlin.github.com,resources=networktests,verbs=create;update,versions=v1,name=mnetworktest.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &NetworkTest{}

const (
	NetworkTestDefaultSchedule = "*/5 * * * *"
	NetworkTestDefaultTimeout  = 10
)

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *NetworkTest) Default() {
	networktestlog.Info("default", "name", r.Name)

	if r.Spec.Schedule == "" {
		r.Spec.Schedule = NetworkTestDefaultSchedule
	}

	if r.Spec.Timeout == 0 {
		r.Spec.Timeout = NetworkTestDefaultTimeout
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-rgravlin-github-com-v1-networktest,mutating=false,failurePolicy=fail,sideEffects=None,groups=rgravlin.github.com,resources=networktests,verbs=create;update,versions=v1,name=vnetworktest.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &NetworkTest{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *NetworkTest) ValidateCreate() (admission.Warnings, error) {
	networktestlog.Info("validate create", "name", r.Name)
	return nil, r.ValidateNetworkTest()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *NetworkTest) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	networktestlog.Info("validate update", "name", r.Name)
	return nil, r.ValidateNetworkTest()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *NetworkTest) ValidateDelete() (admission.Warnings, error) {
	networktestlog.Info("validate delete", "name", r.Name)
	return nil, nil
}

func (r *NetworkTest) validateNetworkTestName() *field.Error {
	if len(r.ObjectMeta.Name) > validationutils.DNS1035LabelMaxLength-11-9 {
		// The job name length is 63 character like all Kubernetes objects
		// (which must fit in a DNS subdomain). The cronjob controller appends
		// a 11-character suffix to the cronjob (`-$TIMESTAMP`) when creating
		// a job. The job name length limit is 63 characters. Therefore, NetworkTest
		// names must have length <= 63-11=52. If we don't validate this here,
		// then job creation will fail later.
		//
		// Adding 9 more for prefix-name maximum `nt-j-`
		// length <= 63-11-5=47
		return field.Invalid(field.NewPath("metadata").Child("name"), r.Name, "must be no more than 47 characters")
	}
	return nil
}

func (r *NetworkTest) validateNetworkTestType() *field.Error {
	if slices.Contains(TestTypes, r.Spec.Type) {
		return nil
	}
	return field.Invalid(field.NewPath("spec").Child("type"), r.Name, fmt.Sprintf("%s: %v", "test type is invalid, valid tests are", TestTypes))
}

func (r *NetworkTest) validateNetworkTestTimeout() *field.Error {
	if r.Spec.Timeout <= 0 || (r.Spec.Timeout > 60 && !r.Spec.TimeoutUnsafe) {
		return field.Invalid(field.NewPath("spec").Child("timeout"), r.Name, "invalid timeout value, must be above 0 and below 60 unless timeoutUnsafe is true")
	}
	return nil
}

func (r *NetworkTest) validateNetworkTestHost() *field.Error {
	validHost := false
	validIP := false

	if err := isValidHostname(r.Spec.Host); err == nil {
		validHost = true
	}

	if isValidIP(r.Spec.Host) {
		validIP = true
	}

	matchesBoth := validHost && validIP
	if matchesBoth {
		return field.Invalid(field.NewPath("spec").Child("host"), r.Name, "invalid host. should not be an IP and a hostname, raise github issue please")
	}

	isValidData := validHost || validIP
	if !isValidData {
		return field.Invalid(field.NewPath("spec").Child("host"), r.Name, "invalid host. not a hostname or IP address")
	}

	return nil
}

func (r *NetworkTest) validateNetworkTestSchedule() *field.Error {
	return validateScheduleFormat(
		r.Spec.Schedule,
		field.NewPath("spec").Child("schedule"))
}

func (r *NetworkTest) ValidateNetworkTest() error {
	var allErrs field.ErrorList

	if err := r.validateNetworkTestName(); err != nil {
		allErrs = append(allErrs, err)
	}

	if err := r.validateNetworkTestHost(); err != nil {
		allErrs = append(allErrs, err)
	}

	if err := r.validateNetworkTestType(); err != nil {
		allErrs = append(allErrs, err)
	}

	if err := r.validateNetworkTestSchedule(); err != nil {
		allErrs = append(allErrs, err)
	}

	if err := r.validateNetworkTestTimeout(); err != nil {
		allErrs = append(allErrs, err)
	}

	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: "rgravlin.github.com", Kind: "NetworkTest"},
		r.Name, allErrs)
}

func validateScheduleFormat(schedule string, fldPath *field.Path) *field.Error {
	if _, err := cron.ParseStandard(schedule); err != nil {
		return field.Invalid(fldPath, schedule, err.Error())
	}
	return nil
}

func isValidIP(ip string) bool {
	if ip := net.ParseIP(ip); ip == nil {
		return false
	}
	return true
}

func isValidHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("hostname cannot be empty")
	} else if len(hostname) > validHostNameMaxLength {
		return fmt.Errorf("name exceeded the maximum length of %d characters", validHostNameMaxLength)
	} else if !validHostNameRFC1123.MatchString(hostname) {
		return fmt.Errorf("%s is not RFC1123 compliant", hostname)
	}
	return nil
}
