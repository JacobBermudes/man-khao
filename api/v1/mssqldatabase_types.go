package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SQLCowSpec defines the desired state of SQLCow
type SQLCowSpec struct {
	// Template is the name of the template database to snapshot (empty for no snapshot)
	Template string `json:"template,omitempty"`
	// User is the user identifier for organizing databases
	User string `json:"user"`
	// DatabaseName is the name of the new database to create
	DatabaseName string `json:"databaseName"`
}

// SQLCowStatus defines the observed state of SQLCow
type SQLCowStatus struct {
	// SnapshotName is the name of the created snapshot (if applicable)
	SnapshotName string `json:"snapshotName,omitempty"`
	// PVCName is the name of the created PVC
	PVCName string `json:"pvcName,omitempty"`
	// MountPath is the path where the database is mounted
	MountPath string `json:"mountPath,omitempty"`
	// Mounted indicates if the volume is mounted
	Mounted bool `json:"mounted,omitempty"`
	// DatabaseCreated indicates if the database is attached
	DatabaseCreated bool `json:"databaseCreated,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// SQLCow is the Schema for the sqlcows API
type SQLCow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SQLCowSpec   `json:"spec,omitempty"`
	Status SQLCowStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SQLCowList contains a list of SQLCow
type SQLCowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SQLCow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SQLCow{}, &SQLCowList{})
}
