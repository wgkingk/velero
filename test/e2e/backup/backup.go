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
package backup

import (
	"context"
	"flag"
	"fmt"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	. "github.com/vmware-tanzu/velero/test/e2e"
	. "github.com/vmware-tanzu/velero/test/e2e/util/k8s"
	. "github.com/vmware-tanzu/velero/test/e2e/util/kibishii"
	. "github.com/vmware-tanzu/velero/test/e2e/util/velero"
)

func BackupRestoreWithSnapshots() {
	BackupRestoreTest(true)
}

func BackupRestoreWithRestic() {
	BackupRestoreTest(false)
}

func BackupRestoreTest(useVolumeSnapshots bool) {
	kibishiiNamespace := "kibishii-workload"
	var (
		backupName, restoreName string
		err                     error
	)

	BeforeEach(func() {
		if useVolumeSnapshots && VeleroCfg.CloudProvider == "kind" {
			Skip("Volume snapshots not supported on kind")
		}
		var err error
		flag.Parse()
		UUIDgen, err = uuid.NewRandom()
		Expect(err).To(Succeed())
		if VeleroCfg.InstallVelero {
			Expect(VeleroInstall(context.Background(), &VeleroCfg, useVolumeSnapshots)).To(Succeed())
		}
	})

	AfterEach(func() {
		if !VeleroCfg.Debug {
			By("Clean backups after test", func() {
				DeleteBackups(context.Background(), *VeleroCfg.ClientToInstallVelero)
			})
			if VeleroCfg.InstallVelero {
				err = VeleroUninstall(context.Background(), VeleroCfg.VeleroCLI, VeleroCfg.VeleroNamespace)
				Expect(err).To(Succeed())
			}
		}
	})

	When("kibishii is the sample workload", func() {
		It("should be successfully backed up and restored to the default BackupStorageLocation", func() {
			backupName = "backup-" + UUIDgen.String()
			restoreName = "restore-" + UUIDgen.String()
			// Even though we are using Velero's CloudProvider plugin for object storage, the kubernetes cluster is running on
			// KinD. So use the kind installation for Kibishii.
			Expect(RunKibishiiTests(*VeleroCfg.ClientToInstallVelero, VeleroCfg, backupName, restoreName, "", kibishiiNamespace, useVolumeSnapshots)).To(Succeed(),
				"Failed to successfully backup and restore Kibishii namespace")
		})

		It("should successfully back up and restore to an additional BackupStorageLocation with unique credentials", func() {
			if VeleroCfg.AdditionalBSLProvider == "" {
				Skip("no additional BSL provider given, not running multiple BackupStorageLocation with unique credentials tests")
			}

			if VeleroCfg.AdditionalBSLBucket == "" {
				Skip("no additional BSL bucket given, not running multiple BackupStorageLocation with unique credentials tests")
			}

			if VeleroCfg.AdditionalBSLCredentials == "" {
				Skip("no additional BSL credentials given, not running multiple BackupStorageLocation with unique credentials tests")
			}

			Expect(VeleroAddPluginsForProvider(context.TODO(), VeleroCfg.VeleroCLI,
				VeleroCfg.VeleroNamespace, VeleroCfg.AdditionalBSLProvider,
				VeleroCfg.AddBSLPlugins, VeleroCfg.Features)).To(Succeed())

			// Create Secret for additional BSL
			secretName := fmt.Sprintf("bsl-credentials-%s", UUIDgen)
			secretKey := fmt.Sprintf("creds-%s", VeleroCfg.AdditionalBSLProvider)
			files := map[string]string{
				secretKey: VeleroCfg.AdditionalBSLCredentials,
			}

			Expect(CreateSecretFromFiles(context.TODO(), *VeleroCfg.ClientToInstallVelero, VeleroCfg.VeleroNamespace, secretName, files)).To(Succeed())

			// Create additional BSL using credential
			additionalBsl := fmt.Sprintf("bsl-%s", UUIDgen)
			Expect(VeleroCreateBackupLocation(context.TODO(),
				VeleroCfg.VeleroCLI,
				VeleroCfg.VeleroNamespace,
				additionalBsl,
				VeleroCfg.AdditionalBSLProvider,
				VeleroCfg.AdditionalBSLBucket,
				VeleroCfg.AdditionalBSLPrefix,
				VeleroCfg.AdditionalBSLConfig,
				secretName,
				secretKey,
			)).To(Succeed())

			bsls := []string{"default", additionalBsl}

			for _, bsl := range bsls {
				backupName = fmt.Sprintf("backup-%s", bsl)
				restoreName = fmt.Sprintf("restore-%s", bsl)
				// We limit the length of backup name here to avoid the issue of vsphere plugin https://github.com/vmware-tanzu/velero-plugin-for-vsphere/issues/370
				// We can remove the logic once the issue is fixed
				if bsl == "default" {
					backupName = fmt.Sprintf("%s-%s", backupName, UUIDgen)
					restoreName = fmt.Sprintf("%s-%s", restoreName, UUIDgen)
				}
				Expect(RunKibishiiTests(*VeleroCfg.ClientToInstallVelero, VeleroCfg, backupName, restoreName, bsl, kibishiiNamespace, useVolumeSnapshots)).To(Succeed(),
					"Failed to successfully backup and restore Kibishii namespace using BSL %s", bsl)
			}
		})
	})
}
