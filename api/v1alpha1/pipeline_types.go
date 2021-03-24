/*


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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type Processor struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type Kafka struct {
	URL   string `json:"url"`
	Topic string `json:"topic"`
}

type Input struct {
	Kafka Kafka `json:"kafka"`
}

type Output struct {
	Kafka Kafka `json:"kafka"`
}

type PipelineSpec struct {
	Input Input `json:"input"`
	// +patchStrategy=merge
	// +patchMergeKey=name
	Processors []Processor `json:"processors,omitempty"`
	Output     Output      `json:"output"`
}

type PipelineStatus struct {
}

// +kubebuilder:object:root=true

type Pipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PipelineSpec   `json:"spec,omitempty"`
	Status PipelineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type PipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pipeline{}, &PipelineList{})
}
