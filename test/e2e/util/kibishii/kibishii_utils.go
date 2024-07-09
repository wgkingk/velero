/*
Copyright the Velero contributors.

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

package kibishii

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/net/context"

	veleroexec "github.com/vmware-tanzu/velero/pkg/util/exec"
	. "github.com/vmware-tanzu/velero/test/e2e"
	. "github.com/vmware-tanzu/velero/test/e2e/util/k8s"
	. "github.com/vmware-tanzu/velero/test/e2e/util/providers"
	. "github.com/vmware-tanzu/velero/test/e2e/util/velero"
)

const (
	jumpPadPod = "jump-pad"
)

type KibishiiData struct {
	Levels        int
	DirsPerLevel  int
	FilesPerLevel int
	FileLength    int
	BlockSize     int
	PassNum       int
	ExpectedNodes int
}

var DefaultKibishiiData = &KibishiiData{2, 10, 10, 1024, 1024, 0, 2}
var KibishiiPodNameList = []string{"kibishii-deployment-0", "kibishii-deployment-1"}

// RunKibishiiTests runs kibishii tests on the provider.
func RunKibishiiTests(client TestClient, veleroCfg VeleroConfig, backupName, restoreName, backupLocation, kibishiiNamespace string,
	useVolumeSnapshots bool) error {
	oneHourTimeout, _ := context.WithTimeout(context.Background(), time.Minute*60)
	veleroCLI := VeleroCfg.VeleroCLI
	providerName := VeleroCfg.CloudProvider
	veleroNamespace := VeleroCfg.VeleroNamespace
	registryCredentialFile := VeleroCfg.RegistryCredentialFile
	veleroFeatures := VeleroCfg.Features
	kibishiiDirectory := VeleroCfg.KibishiiDirectory
	if _, err := GetNamespace(context.Background(), client, kibishiiNamespace); err == nil {
		fmt.Printf("Workload namespace %s exists, delete it first.\n", kibishiiNamespace)
		if err = DeleteNamespace(context.Background(), client, kibishiiNamespace, true); err != nil {
			fmt.Println(errors.Wrapf(err, "failed to delete the namespace %q", kibishiiNamespace))
		}
	}
	if err := CreateNamespace(oneHourTimeout, client, kibishiiNamespace); err != nil {
		return errors.Wrapf(err, "Failed to create namespace %s to install Kibishii workload", kibishiiNamespace)
	}
	defer func() {
		if !veleroCfg.Debug {
			if err := DeleteNamespace(context.Background(), client, kibishiiNamespace, true); err != nil {
				fmt.Println(errors.Wrapf(err, "failed to delete the namespace %q", kibishiiNamespace))
			}
		}
	}()

	if err := KibishiiPrepareBeforeBackup(oneHourTimeout, client, providerName,
		kibishiiNamespace, registryCredentialFile, veleroFeatures,
		kibishiiDirectory, useVolumeSnapshots, DefaultKibishiiData); err != nil {
		return errors.Wrapf(err, "Failed to install and prepare data for kibishii %s", kibishiiNamespace)
	}

	var BackupCfg BackupConfig
	BackupCfg.BackupName = backupName
	BackupCfg.Namespace = kibishiiNamespace
	BackupCfg.BackupLocation = backupLocation
	BackupCfg.UseVolumeSnapshots = useVolumeSnapshots
	BackupCfg.Selector = ""
	if err := VeleroBackupNamespace(oneHourTimeout, veleroCLI, veleroNamespace, BackupCfg); err != nil {
		RunDebug(context.Background(), veleroCLI, veleroNamespace, backupName, "")
		return errors.Wrapf(err, "Failed to backup kibishii namespace %s", kibishiiNamespace)
	}
	var snapshotCheckPoint SnapshotCheckPoint
	var err error
	if useVolumeSnapshots {
		if providerName == "vsphere" {
			// Wait for uploads started by the Velero Plug-in for vSphere to complete
			// TODO - remove after upload progress monitoring is implemented
			fmt.Println("Waiting for vSphere uploads to complete")
			if err := WaitForVSphereUploadCompletion(oneHourTimeout, time.Hour, kibishiiNamespace); err != nil {
				return errors.Wrapf(err, "Error waiting for uploads to complete")
			}
		}
		snapshotCheckPoint, err = GetSnapshotCheckPoint(client, VeleroCfg, 2, kibishiiNamespace, backupName, KibishiiPodNameList)
		if err != nil {
			return errors.Wrap(err, "Fail to get snapshot checkpoint")
		}
		err = SnapshotsShouldBeCreatedInCloud(VeleroCfg.CloudProvider,
			VeleroCfg.CloudCredentialsFile, VeleroCfg.BSLBucket, veleroCfg.BSLConfig,
			backupName, snapshotCheckPoint)
		if err != nil {
			return errors.Wrap(err, "exceed waiting for snapshot created in cloud")
		}
	}

	fmt.Printf("Simulating a disaster by removing namespace %s\n", kibishiiNamespace)
	if err := DeleteNamespace(oneHourTimeout, client, kibishiiNamespace, true); err != nil {
		return errors.Wrapf(err, "failed to delete namespace %s", kibishiiNamespace)
	}

	// the snapshots of AWS may be still in pending status when do the restore, wait for a while
	// to avoid this https://github.com/vmware-tanzu/velero/issues/1799
	// TODO remove this after https://github.com/vmware-tanzu/velero/issues/3533 is fixed
	if useVolumeSnapshots {
		fmt.Println("Waiting 5 minutes to make sure the snapshots are ready...")
		time.Sleep(5 * time.Minute)
	}

	if err := VeleroRestore(oneHourTimeout, veleroCLI, veleroNamespace, restoreName, backupName, ""); err != nil {
		RunDebug(context.Background(), veleroCLI, veleroNamespace, "", restoreName)
		return errors.Wrapf(err, "Restore %s failed from backup %s", restoreName, backupName)
	}

	if err := KibishiiVerifyAfterRestore(client, kibishiiNamespace, oneHourTimeout, DefaultKibishiiData); err != nil {
		return errors.Wrapf(err, "Error verifying kibishii after restore")
	}
	fmt.Printf("kibishii test completed successfully\n")
	return nil
}

func installKibishii(ctx context.Context, namespace string, cloudPlatform, veleroFeatures,
	kibishiiDirectory string, useVolumeSnapshots bool) error {
	if strings.EqualFold(cloudPlatform, "azure") &&
		strings.EqualFold(veleroFeatures, "EnableCSI") {
		cloudPlatform = "azure-csi"
	}
	// We use kustomize to generate YAML for Kibishii from the checked-in yaml directories
	kibishiiInstallCmd := exec.CommandContext(ctx, "kubectl", "apply", "-n", namespace, "-k",
		kibishiiDirectory+cloudPlatform, "--timeout=90s")
	_, stderr, err := veleroexec.RunCommand(kibishiiInstallCmd)
	fmt.Printf("Install Kibishii cmd: %s\n", kibishiiInstallCmd)
	if err != nil {
		return errors.Wrapf(err, "failed to install kibishii, stderr=%s", stderr)
	}

	labelNamespaceCmd := exec.CommandContext(ctx, "kubectl", "label", "namespace", namespace, "pod-security.kubernetes.io/enforce=baseline", "pod-security.kubernetes.io/enforce-version=latest", "--overwrite=true")
	_, stderr, err = veleroexec.RunCommand(labelNamespaceCmd)
	fmt.Printf("Label namespace with PSA policy: %s\n", labelNamespaceCmd)
	if err != nil {
		return errors.Wrapf(err, "failed to label namespace with PSA policy, stderr=%s", stderr)
	}

	kibishiiSetWaitCmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "statefulset.apps/kibishii-deployment",
		"-n", namespace, "-w", "--timeout=30m")
	_, stderr, err = veleroexec.RunCommand(kibishiiSetWaitCmd)
	if err != nil {
		return errors.Wrapf(err, "failed to rollout, stderr=%s", stderr)
	}

	fmt.Printf("Waiting for kibishii jump-pad pod to be ready\n")
	jumpPadWaitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=ready", "-n", namespace, "pod/jump-pad")
	_, stderr, err = veleroexec.RunCommand(jumpPadWaitCmd)
	if err != nil {
		return errors.Wrapf(err, "Failed to wait for ready status of pod %s/%s, stderr=%s", namespace, jumpPadPod, stderr)
	}

	return err
}

func generateData(ctx context.Context, namespace string, kibishiiData *KibishiiData) error {
	timeout, _ := context.WithTimeout(context.Background(), time.Minute*20)
	kibishiiGenerateCmd := exec.CommandContext(timeout, "kubectl", "exec", "-n", namespace, "jump-pad", "--",
		"/usr/local/bin/generate.sh", strconv.Itoa(kibishiiData.Levels), strconv.Itoa(kibishiiData.DirsPerLevel),
		strconv.Itoa(kibishiiData.FilesPerLevel), strconv.Itoa(kibishiiData.FileLength),
		strconv.Itoa(kibishiiData.BlockSize), strconv.Itoa(kibishiiData.PassNum), strconv.Itoa(kibishiiData.ExpectedNodes))
	fmt.Printf("kibishiiGenerateCmd cmd =%v\n", kibishiiGenerateCmd)

	_, stderr, err := veleroexec.RunCommand(kibishiiGenerateCmd)
	if err != nil {
		return errors.Wrapf(err, "failed to generate, stderr=%s", stderr)
	}

	return nil
}

func verifyData(ctx context.Context, namespace string, kibishiiData *KibishiiData) error {
	timeout, _ := context.WithTimeout(context.Background(), time.Minute*10)
	kibishiiVerifyCmd := exec.CommandContext(timeout, "kubectl", "exec", "-n", namespace, "jump-pad", "--",
		"/usr/local/bin/verify.sh", strconv.Itoa(kibishiiData.Levels), strconv.Itoa(kibishiiData.DirsPerLevel),
		strconv.Itoa(kibishiiData.FilesPerLevel), strconv.Itoa(kibishiiData.FileLength),
		strconv.Itoa(kibishiiData.BlockSize), strconv.Itoa(kibishiiData.PassNum),
		strconv.Itoa(kibishiiData.ExpectedNodes))
	fmt.Printf("kibishiiVerifyCmd cmd =%v\n", kibishiiVerifyCmd)

	stdout, stderr, err := veleroexec.RunCommand(kibishiiVerifyCmd)
	if err != nil {
		return errors.Wrapf(err, "failed to verify, stderr=%s, stdout=%s", stderr, stdout)
	}
	return nil
}

func waitForKibishiiPods(ctx context.Context, client TestClient, kibishiiNamespace string) error {
	return WaitForPods(ctx, client, kibishiiNamespace, []string{"jump-pad", "etcd0", "etcd1", "etcd2", "kibishii-deployment-0", "kibishii-deployment-1"})
}

func KibishiiPrepareBeforeBackup(oneHourTimeout context.Context, client TestClient,
	providerName, kibishiiNamespace, registryCredentialFile, veleroFeatures,
	kibishiiDirectory string, useVolumeSnapshots bool, kibishiiData *KibishiiData) error {
	serviceAccountName := "default"

	// wait until the service account is created before patch the image pull secret
	if err := WaitUntilServiceAccountCreated(oneHourTimeout, client, kibishiiNamespace, serviceAccountName, 10*time.Minute); err != nil {
		return errors.Wrapf(err, "failed to wait the service account %q created under the namespace %q", serviceAccountName, kibishiiNamespace)
	}

	// add the image pull secret to avoid the image pull limit issue of Docker Hub
	if err := PatchServiceAccountWithImagePullSecret(oneHourTimeout, client, kibishiiNamespace, serviceAccountName, registryCredentialFile); err != nil {
		return errors.Wrapf(err, "failed to patch the service account %q under the namespace %q", serviceAccountName, kibishiiNamespace)
	}

	if err := installKibishii(oneHourTimeout, kibishiiNamespace, providerName, veleroFeatures,
		kibishiiDirectory, useVolumeSnapshots); err != nil {
		return errors.Wrap(err, "Failed to install Kibishii workload")
	}

	// wait for kibishii pod startup
	// TODO - Fix kibishii so we can check that it is ready to go
	fmt.Printf("Waiting for kibishii pods to be ready\n")
	if err := waitForKibishiiPods(oneHourTimeout, client, kibishiiNamespace); err != nil {
		return errors.Wrapf(err, "Failed to wait for ready status of kibishii pods in %s", kibishiiNamespace)
	}
	if kibishiiData == nil {
		kibishiiData = DefaultKibishiiData
	}
	if err := generateData(oneHourTimeout, kibishiiNamespace, kibishiiData); err != nil {
		return errors.Wrap(err, "Failed to generate data")
	}
	return nil
}

func KibishiiVerifyAfterRestore(client TestClient, kibishiiNamespace string, oneHourTimeout context.Context,
	kibishiiData *KibishiiData) error {
	if kibishiiData == nil {
		kibishiiData = DefaultKibishiiData
	}
	// wait for kibishii pod startup
	// TODO - Fix kibishii so we can check that it is ready to go
	fmt.Printf("Waiting for kibishii pods to be ready\n")
	if err := waitForKibishiiPods(oneHourTimeout, client, kibishiiNamespace); err != nil {
		return errors.Wrapf(err, "Failed to wait for ready status of kibishii pods in %s", kibishiiNamespace)
	}
	time.Sleep(60 * time.Second)
	// TODO - check that namespace exists
	fmt.Printf("running kibishii verify\n")
	if err := verifyData(oneHourTimeout, kibishiiNamespace, kibishiiData); err != nil {
		return errors.Wrap(err, "Failed to verify data generated by kibishii")
	}
	return nil
}
