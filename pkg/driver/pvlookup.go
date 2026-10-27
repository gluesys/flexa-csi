/*
Copyright 2025 Gluesys FlexA Inc.

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

package driver

import (
	"context"
	"regexp"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// embeddedPVName finds the provisioner PV name (pvc-<uid>) inside a volume ID.
var embeddedPVName = regexp.MustCompile(`pvc-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// lookupPVAttributes returns the CSI volume attributes of the PV whose
// volumeHandle is volumeID. found is false only when no such PV exists; an API
// failure is returned as err so callers never treat an outage as "deleted".
//
// Dynamically provisioned PVs are named after the volume ID, or contain the PV
// name when volumeNameFormat is set, so a GET usually answers; statically
// created PVs fall back to a list.
func lookupPVAttributes(ctx context.Context, client kubernetes.Interface, volumeID string) (map[string]string, bool, error) {
	names := []string{volumeID}
	if pvName := embeddedPVName.FindString(volumeID); pvName != "" && pvName != volumeID {
		names = append(names, pvName)
	}
	for _, name := range names {
		pv, err := client.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == volumeID {
				return pv.Spec.CSI.VolumeAttributes, true, nil
			}
		case !apierrors.IsNotFound(err):
			return nil, false, err
		}
	}

	pvs, err := client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false, err
	}
	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == volumeID {
			return pv.Spec.CSI.VolumeAttributes, true, nil
		}
	}
	return nil, false, nil
}
