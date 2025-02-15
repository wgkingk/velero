/*
Copyright 2021 the Velero contributors.

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

package filtering

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	. "github.com/vmware-tanzu/velero/test/e2e"
	. "github.com/vmware-tanzu/velero/test/e2e/test"
	. "github.com/vmware-tanzu/velero/test/e2e/util/k8s"
)

const FileName = "test-data.txt"

var yamlData = `version: v1
volumePolicies:
- conditions:
    capacity: "2Gi,3Gi"
  action:
    type: skip
- conditions:
    storageClass:
    - e2e-storage-class
  action:
    type: skip
`

type ResourcePoliciesCase struct {
	TestCase
	cmName, yamlConfig string
}

var ResourcePoliciesTest func() = TestFunc(&ResourcePoliciesCase{})

func (r *ResourcePoliciesCase) Init() error {
	rand.Seed(time.Now().UnixNano())
	UUIDgen, _ = uuid.NewRandom()
	r.yamlConfig = yamlData
	r.VeleroCfg = VeleroCfg
	r.Client = *r.VeleroCfg.ClientToInstallVelero
	r.VeleroCfg.UseVolumeSnapshots = false
	r.VeleroCfg.UseNodeAgent = true
	r.NamespacesTotal = 3
	r.NSBaseName = "resource-policies-" + UUIDgen.String()
	r.cmName = "cm-resource-policies-sc"
	r.NSIncluded = &[]string{}
	for nsNum := 0; nsNum < r.NamespacesTotal; nsNum++ {
		createNSName := fmt.Sprintf("%s-%00000d", r.NSBaseName, nsNum)
		*r.NSIncluded = append(*r.NSIncluded, createNSName)
	}

	r.BackupName = "backup-resource-policies-" + UUIDgen.String()
	r.RestoreName = "restore-resource-policies-" + UUIDgen.String()

	r.BackupArgs = []string{
		"create", "--namespace", VeleroCfg.VeleroNamespace, "backup", r.BackupName,
		"--resource-policies-configmap", r.cmName,
		"--include-namespaces", strings.Join(*r.NSIncluded, ","),
		"--default-volumes-to-fs-backup",
		"--snapshot-volumes=false", "--wait",
	}

	r.RestoreArgs = []string{
		"create", "--namespace", VeleroCfg.VeleroNamespace, "restore", r.RestoreName,
		"--from-backup", r.BackupName, "--wait",
	}

	r.TestMsg = &TestMSG{
		Desc:      "Skip backup of volume by resource policies",
		FailedMSG: "Failed to skip backup of volume by resource policies",
		Text:      fmt.Sprintf("Should backup PVs in namespace %s respect to resource policies rules", *r.NSIncluded),
	}
	return nil
}

func (r *ResourcePoliciesCase) CreateResources() error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	By(("Installing storage class..."), func() {
		Expect(r.installTestStorageClasses(fmt.Sprintf("testdata/storage-class/%s.yaml", VeleroCfg.CloudProvider))).To(Succeed(), "Failed to install storage class")
	})

	By(fmt.Sprintf("Create configmap %s in namespaces %s for workload\n", r.cmName, r.VeleroCfg.VeleroNamespace), func() {
		Expect(CreateConfigMapFromYAMLData(r.Client.ClientGo, r.yamlConfig, r.cmName, r.VeleroCfg.VeleroNamespace)).To(Succeed(), fmt.Sprintf("Failed to create configmap %s in namespaces %s for workload\n", r.cmName, r.VeleroCfg.VeleroNamespace))
	})

	By(fmt.Sprintf("Waiting for configmap %s in namespaces %s ready\n", r.cmName, r.VeleroCfg.VeleroNamespace), func() {
		Expect(WaitForConfigMapComplete(r.Client.ClientGo, r.VeleroCfg.VeleroNamespace, r.cmName)).To(Succeed(), fmt.Sprintf("Failed to wait configmap %s in namespaces %s ready\n", r.cmName, r.VeleroCfg.VeleroNamespace))
	})

	for nsNum := 0; nsNum < r.NamespacesTotal; nsNum++ {
		namespace := fmt.Sprintf("%s-%00000d", r.NSBaseName, nsNum)
		By(fmt.Sprintf("Create namespaces %s for workload\n", namespace), func() {
			Expect(CreateNamespace(ctx, r.Client, namespace)).To(Succeed(), fmt.Sprintf("Failed to create namespace %s", namespace))
		})

		volName := fmt.Sprintf("vol-%s-%00000d", r.NSBaseName, nsNum)
		volList := PrepareVolumeList([]string{volName})

		// Create PVC
		By(fmt.Sprintf("Creating pvc in namespaces ...%s\n", namespace), func() {
			Expect(r.createPVC(nsNum, namespace, volList)).To(Succeed(), fmt.Sprintf("Failed to create pvc in namespace %s", namespace))
		})

		// Create deployment
		By(fmt.Sprintf("Creating deployment in namespaces ...%s\n", namespace), func() {
			Expect(r.createDeploymentWithVolume(namespace, volList)).To(Succeed(), fmt.Sprintf("Failed to create deployment namespace %s", namespace))
		})

		//Write data into pods
		By(fmt.Sprintf("Writing data into pod in namespaces ...%s\n", namespace), func() {
			Expect(r.writeDataIntoPods(namespace, volName)).To(Succeed(), fmt.Sprintf("Failed to write data into pod in namespace %s", namespace))
		})
	}

	return nil
}

func (r *ResourcePoliciesCase) Verify() error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	for i, ns := range *r.NSIncluded {
		By(fmt.Sprintf("Verify pod data in namespace %s", ns), func() {
			By(fmt.Sprintf("Waiting for deployment %s in namespace %s ready", r.NSBaseName, ns), func() {
				Expect(WaitForReadyDeployment(r.Client.ClientGo, ns, r.NSBaseName)).To(Succeed(), fmt.Sprintf("Failed to waiting for deployment %s in namespace %s ready", r.NSBaseName, ns))
			})
			podList, err := ListPods(ctx, r.Client, ns)
			Expect(err).To(Succeed(), fmt.Sprintf("failed to list pods in namespace: %q with error %v", ns, err))

			volName := fmt.Sprintf("vol-%s-%00000d", r.NSBaseName, i)
			for _, pod := range podList.Items {
				for _, vol := range pod.Spec.Volumes {
					if vol.Name != volName {
						continue
					}
					content, err := ReadFileFromPodVolume(ctx, ns, pod.Name, "container-busybox", vol.Name, FileName)
					if i%2 == 0 {
						Expect(err).To(HaveOccurred(), "Expected file not found") // File should not exist
					} else {
						Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Fail to read file %s from volume %s of pod %s in namespace %s",
							FileName, vol.Name, pod.Name, ns))

						content = strings.Replace(content, "\n", "", -1)
						originContent := strings.Replace(fmt.Sprintf("ns-%s pod-%s volume-%s", ns, pod.Name, vol.Name), "\n", "", -1)

						Expect(content == originContent).To(BeTrue(), fmt.Sprintf("File %s does not exist in volume %s of pod %s in namespace %s",
							FileName, vol.Name, pod.Name, ns))
					}
				}
			}
		})

	}
	return nil
}

func (r *ResourcePoliciesCase) Clean() error {
	if err := r.deleteTestStorageClassList([]string{"e2e-storage-class", "e2e-storage-class-2"}); err != nil {
		return err
	}

	if err := DeleteConfigmap(r.Client.ClientGo, r.VeleroCfg.VeleroNamespace, r.cmName); err != nil {
		return err
	}

	return r.GetTestCase().Clean()
}

func (r *ResourcePoliciesCase) createPVC(index int, namespace string, volList []*v1.Volume) error {
	var err error
	for i := range volList {
		pvcName := fmt.Sprintf("pvc-%d", i)
		By(fmt.Sprintf("Creating PVC %s in namespaces ...%s\n", pvcName, namespace))
		if index%3 == 0 {
			pvcBuilder := NewPVC(namespace, pvcName).WithStorageClass("e2e-storage-class") // Testing sc should not backup
			err = CreatePvc(r.Client, pvcBuilder)
		} else if index%3 == 1 {
			pvcBuilder := NewPVC(namespace, pvcName).WithStorageClass("e2e-storage-class-2") // Testing sc should backup
			err = CreatePvc(r.Client, pvcBuilder)
		} else if index%3 == 2 {
			pvcBuilder := NewPVC(namespace, pvcName).WithStorageClass("e2e-storage-class-2").WithResourceStorage(resource.MustParse("2Gi")) // Testing capacity should not backup
			err = CreatePvc(r.Client, pvcBuilder)
		}
		if err != nil {
			return errors.Wrapf(err, "failed to create pvc %s in namespace %s", pvcName, namespace)
		}
	}
	return nil
}

func (r *ResourcePoliciesCase) createDeploymentWithVolume(namespace string, volList []*v1.Volume) error {
	deployment := NewDeployment(r.NSBaseName, namespace, 1, map[string]string{"resource-policies": "resource-policies"}, nil).WithVolume(volList).Result()
	deployment, err := CreateDeployment(r.Client.ClientGo, namespace, deployment)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to create deloyment %s the namespace %q", deployment.Name, namespace))
	}
	err = WaitForReadyDeployment(r.Client.ClientGo, namespace, deployment.Name)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to wait for deployment %s to be ready in namespace: %q", deployment.Name, namespace))
	}
	return nil
}

func (r *ResourcePoliciesCase) writeDataIntoPods(namespace, volName string) error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	podList, err := ListPods(ctx, r.Client, namespace)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("failed to list pods in namespace: %q with error %v", namespace, err))
	}
	for _, pod := range podList.Items {
		for _, vol := range pod.Spec.Volumes {
			if vol.Name != volName {
				continue
			}
			err := CreateFileToPod(ctx, namespace, pod.Name, "container-busybox", vol.Name, FileName, fmt.Sprintf("ns-%s pod-%s volume-%s", namespace, pod.Name, vol.Name))
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("failed to create file into pod %s in namespace: %q", pod.Name, namespace))
			}
		}
	}
	return nil
}

func (r *ResourcePoliciesCase) deleteTestStorageClassList(scList []string) error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	for _, v := range scList {
		if err := DeleteStorageClass(ctx, r.Client, v); err != nil {
			return err
		}
	}
	return nil
}

func (r *ResourcePoliciesCase) installTestStorageClasses(path string) error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	err := InstallStorageClass(ctx, path)
	if err != nil {
		return err
	}
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return errors.Wrapf(err, "failed to get %s when install storage class", path)
	}

	// replace sc to new value
	newContent := strings.ReplaceAll(string(content), "name: e2e-storage-class", "name: e2e-storage-class-2")

	tmpFile, err := ioutil.TempFile("", "sc-file")
	if err != nil {
		return errors.Wrapf(err, "failed to create temp file  when install storage class")
	}

	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(newContent); err != nil {
		return errors.Wrapf(err, "failed to write content into temp file %s when install storage class", tmpFile.Name())
	}
	return InstallStorageClass(ctx, tmpFile.Name())
}
