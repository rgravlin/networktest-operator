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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	NetworkTestTypeHTTP    NetworkTestType = "http"
	NetworkTestTypeHTTPS   NetworkTestType = "https"
	NetworkTestTypeDNS     NetworkTestType = "dns"
	NetworkTestPrefixHTTP                  = "http://"
	NetworkTestPrefixHTTPS                 = "https://"
	NetworkTestCommandHTTP                 = "/bin/curl-wrapper.sh"
	NetworkTestCommandDNS                  = "dig"
)

var (
	TestTypes = []NetworkTestType{NetworkTestTypeHTTP, NetworkTestTypeHTTPS, NetworkTestTypeDNS}
)

type NetworkTestType string

// NetworkTestSpec defines the desired state of NetworkTest
type NetworkTestSpec struct {
	Host          string          `json:"host"`
	Image         string          `json:"image,omitempty"`
	Registry      string          `json:"registry,omitempty"`
	Schedule      string          `json:"schedule,omitempty"`
	Suspend       bool            `json:"suspend,omitempty"`
	Timeout       int             `json:"timeout,omitempty"`
	TimeoutUnsafe bool            `json:"timeoutUnsafe,omitempty"`
	Type          NetworkTestType `json:"type"`
}

// NetworkTestStatus defines the observed state of NetworkTest
type NetworkTestStatus struct {
	CronJobName string       `json:"cronJobName,omitempty"`
	CronSync    string       `json:"cronSync,omitempty"`
	Output      string       `json:"output,omitempty"`
	Error       bool         `json:"failed,omitempty"`
	LastSuccess *metav1.Time `json:"lastSuccess,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.cronSync`
// +kubebuilder:printcolumn:name="Cron Job",type=string,JSONPath=`.status.cronJobName`
// +kubebuilder:printcolumn:name="Last Success",type=string,JSONPath=`.status.lastSuccess`

// NetworkTest is the Schema for the networktests API
type NetworkTest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetworkTestSpec   `json:"spec,omitempty"`
	Status NetworkTestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetworkTestList contains a list of NetworkTest
type NetworkTestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkTest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkTest{}, &NetworkTestList{})
}
