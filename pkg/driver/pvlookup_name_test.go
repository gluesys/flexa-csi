package driver

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestLookupPVAttributesFindsFormattedVolumeByPVName(t *testing.T) {
	const pvName = "pvc-1a2b3c4d-0000-1111-2222-333344445555"
	const volumeID = "gluesys05_" + pvName
	client := fake.NewSimpleClientset(&corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: pvName},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{VolumeHandle: volumeID, VolumeAttributes: map[string]string{"fs": "lustre"}},
		}},
	})
	attrs, found, err := lookupPVAttributes(context.Background(), client, volumeID)
	if err != nil || !found || attrs["fs"] != "lustre" {
		t.Fatalf("got %v, %v, %v", attrs, found, err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "list" {
			t.Fatalf("unexpected PV list: %v", a.(k8stesting.ListAction))
		}
	}
}
