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
	"strings"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	. "github.com/vmware-tanzu/velero/test/e2e"
	. "github.com/vmware-tanzu/velero/test/e2e/test"
	. "github.com/vmware-tanzu/velero/test/e2e/util/k8s"
)

/*
include-namespaces
Backup a namespace and it's objects.

velero backup create <backup-name> --include-namespaces <namespace>
Restore two namespaces and their objects.

velero restore create <backup-name> --include-namespaces <namespace1>,<namespace2>
*/

type IncludeNamespaces struct {
	allTestNamespaces  *[]string
	namespacesIncluded int
	FilteringCase
}

var BackupWithIncludeNamespaces func() = TestFunc(&IncludeNamespaces{FilteringCase: testInBackup})
var RestoreWithIncludeNamespaces func() = TestFunc(&IncludeNamespaces{FilteringCase: testInRestore})

func (i *IncludeNamespaces) Init() error {
	i.FilteringCase.Init()
	i.namespacesIncluded = i.NamespacesTotal / 2
	i.allTestNamespaces = &[]string{}
	i.NSBaseName = "include-namespaces-" + UUIDgen.String()
	for nsNum := 0; nsNum < i.NamespacesTotal; nsNum++ {
		createNSName := fmt.Sprintf("%s-%00000d", i.NSBaseName, nsNum)
		if nsNum < i.namespacesIncluded {
			*i.NSIncluded = append(*i.NSIncluded, createNSName)
		}
		*i.allTestNamespaces = append(*i.allTestNamespaces, createNSName)
	}

	if i.IsTestInBackup {
		i.BackupName = "backup-include-namespaces-" + UUIDgen.String()
		i.RestoreName = "restore-" + UUIDgen.String()
		i.TestMsg = &TestMSG{
			Desc:      "Backup resources with include namespace test",
			FailedMSG: "Failed to backup with namespace include",
			Text:      fmt.Sprintf("should backup %d namespaces of %d", i.namespacesIncluded, i.NamespacesTotal),
		}
		i.BackupArgs = []string{
			"create", "--namespace", VeleroCfg.VeleroNamespace, "backup", i.BackupName,
			"--include-namespaces", strings.Join(*i.NSIncluded, ","),
			"--default-volumes-to-fs-backup", "--wait",
		}

		i.RestoreArgs = []string{
			"create", "--namespace", VeleroCfg.VeleroNamespace, "restore", i.RestoreName,
			"--from-backup", i.BackupName, "--wait",
		}

	} else {
		i.BackupName = "backup-" + UUIDgen.String()
		i.RestoreName = "restore-include-namespaces-" + UUIDgen.String()
		i.TestMsg = &TestMSG{
			Desc:      "Restore resources with include namespace test",
			FailedMSG: "Failed to restore with namespace include",
			Text:      fmt.Sprintf("should restore %d namespaces of %d", i.namespacesIncluded, i.NamespacesTotal),
		}
		i.BackupArgs = []string{
			"create", "--namespace", VeleroCfg.VeleroNamespace, "backup", i.BackupName,
			"--include-namespaces", strings.Join(*i.allTestNamespaces, ","),
			"--default-volumes-to-fs-backup", "--wait",
		}

		i.RestoreArgs = []string{
			"create", "--namespace", VeleroCfg.VeleroNamespace, "restore", i.RestoreName,
			"--include-namespaces", strings.Join(*i.NSIncluded, ","),
			"--from-backup", i.BackupName, "--wait",
		}
	}
	return nil
}

func (i *IncludeNamespaces) CreateResources() error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	for nsNum := 0; nsNum < i.NamespacesTotal; nsNum++ {
		createNSName := fmt.Sprintf("%s-%00000d", i.NSBaseName, nsNum)
		fmt.Printf("Creating namespaces ...%s\n", createNSName)
		if err := CreateNamespace(ctx, i.Client, createNSName); err != nil {
			return errors.Wrapf(err, "Failed to create namespace %s", createNSName)
		}
	}
	return nil
}

func (i *IncludeNamespaces) Verify() error {
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer ctxCancel()
	// Verify that we got back all of the namespaces we created
	for nsNum := 0; nsNum < i.namespacesIncluded; nsNum++ {
		checkNSName := fmt.Sprintf("%s-%00000d", i.NSBaseName, nsNum)
		checkNS, err := GetNamespace(ctx, i.Client, checkNSName)
		if err != nil {
			return errors.Wrapf(err, "Could not retrieve test namespace %s", checkNSName)
		}
		if checkNS.Name != checkNSName {
			return errors.Errorf("Retrieved namespace for %s has name %s instead", checkNSName, checkNS.Name)
		}
	}

	for nsNum := i.namespacesIncluded; nsNum < i.NamespacesTotal; nsNum++ {
		excludeNSName := fmt.Sprintf("%s-%00000d", i.NSBaseName, nsNum)
		_, err := GetNamespace(ctx, i.Client, excludeNSName)
		if err == nil {
			return errors.Wrapf(err, "Resource filtering with include namespace but exclude namespace %s exist", excludeNSName)
		}

		if !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "Resource filtering with include namespace failed with checking namespace %s", excludeNSName)
		}
	}
	return nil
}
