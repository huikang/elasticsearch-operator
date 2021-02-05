package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ViaQ/logerr/kverrors"
	consolev1 "github.com/openshift/api/console/v1"
	routev1 "github.com/openshift/api/route/v1"
	loggingv1 "github.com/openshift/elasticsearch-operator/apis/logging/v1"
	"github.com/openshift/elasticsearch-operator/test/utils"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	retryInterval        = time.Second * 3
	timeout              = time.Second * 300
	cleanupRetryInterval = time.Second * 3
	cleanupTimeout       = time.Second * 30
	elasticsearchCRName  = "elasticsearch"
	kibanaCRName         = "kibana"
	exampleSecretsPath   = "/tmp/example-secrets"
)

func setupK8sClient(t *testing.T) {
	operatorNamespace = os.Getenv(TestOperatorNamespaceEnv)
	if operatorNamespace == "" {
		t.Fatal("TEST_OPERATOR_NAMESPACE is unset")
	}
	t.Logf("Found namespace: %q", operatorNamespace)

	projectRootDir = getProjectRootPath("elasticsearch-operator")

	cfg, err := config.GetConfig()
	if err != nil {
		t.Fatalf("Error get config: %s", err)
	}
	if cfg == nil {
		t.Fatal("config is nil")
	}

	registerSchemes(t)

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("Error get k8sClient: %s", err)
	}
	if k8sClient == nil {
		t.Fatal("k8sClient is nil")
	}
}

func registerSchemes(t *testing.T) {
	err := loggingv1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("failed to add custom resource scheme to framework: %v", err)
	}

	err = consolev1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("failed to add custom resource scheme to framework: %v", err)
	}

	err = routev1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("failed to add custom resource scheme to framework: %v", err)
	}
}

func elasticsearchNameFor(uuid string) string {
	return fmt.Sprintf("%s-%s", elasticsearchCRName, uuid)
}

func createElasticsearchCR(t *testing.T, f client.Client, esUUID, dataUUID string, replicas int) (*loggingv1.Elasticsearch, error) {
	namespace := operatorNamespace

	cpuValue := resource.MustParse("256m")
	memValue := resource.MustParse("1Gi")

	storageClassSize := resource.MustParse("2Gi")

	esDataNode := loggingv1.ElasticsearchNode{
		Roles: []loggingv1.ElasticsearchNodeRole{
			loggingv1.ElasticsearchRoleClient,
			loggingv1.ElasticsearchRoleData,
			loggingv1.ElasticsearchRoleMaster,
		},
		Storage: loggingv1.ElasticsearchStorageSpec{
			Size: &storageClassSize,
		},
		NodeCount: int32(replicas),
		GenUUID:   &dataUUID,
	}

	// create elasticsearch custom resource
	cr := &loggingv1.Elasticsearch{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Elasticsearch",
			APIVersion: loggingv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      elasticsearchNameFor(esUUID),
			Namespace: namespace,
			Annotations: map[string]string{
				"loggingv1.openshift.io/develLogAppender": "console",
				"loggingv1.openshift.io/loglevel":         "trace",
			},
		},
		Spec: loggingv1.ElasticsearchSpec{
			Spec: loggingv1.ElasticsearchNodeSpec{
				Image: "",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("5Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    cpuValue,
						corev1.ResourceMemory: memValue,
					},
				},
			},
			Nodes: []loggingv1.ElasticsearchNode{
				esDataNode,
			},
			ManagementState:  loggingv1.ManagementStateManaged,
			RedundancyPolicy: loggingv1.ZeroRedundancy,
		},
	}

	t.Logf("Creating Elasticsearch CR: %v", cr)

	err := f.Create(context.TODO(), cr)
	if err != nil {
		return nil, kverrors.Wrap(err, "failed to create elasticsearch CR",
			"name", cr.Name,
			"namespace", cr.Namespace)
	}

	return cr, nil
}

func updateElasticsearchSpec(t *testing.T, f client.Client, desired *loggingv1.Elasticsearch) error {
	return wait.Poll(retryInterval, timeout, func() (bool, error) {
		current := &loggingv1.Elasticsearch{}
		key := client.ObjectKey{Name: desired.GetName(), Namespace: desired.GetNamespace()}

		if err := f.Get(context.TODO(), key, current); err != nil {
			if apierrors.IsNotFound(kverrors.Root(err)) {
				// Stop retry because CR not found
				return false, err
			}

			// Transient error retry
			return false, nil
		}

		current.Spec = desired.Spec

		t.Logf("Update Spec: %#v", current.Spec)

		if err := f.Update(context.TODO(), current); err != nil {
			if apierrors.IsConflict(kverrors.Root(err)) {
				// Retry update because resource needs to get updated
				return false, nil
			}

			// Stop retry not recoverable error
			return false, err
		}

		return true, nil
	})
}

// Create the secret that would be generated by CLO normally
func createElasticsearchSecret(t *testing.T, f client.Client, uuid string) error {
	t.Log("Creating required secret")
	namespace := operatorNamespace

	if err := generateCertificates(t, namespace, uuid); err != nil {
		return err
	}

	elasticsearchSecret := utils.Secret(
		elasticsearchNameFor(uuid),
		namespace,
		map[string][]byte{
			"elasticsearch.key": getCertificateContents("elasticsearch.key", uuid),
			"elasticsearch.crt": getCertificateContents("elasticsearch.crt", uuid),
			"logging-es.key":    getCertificateContents("logging-es.key", uuid),
			"logging-es.crt":    getCertificateContents("logging-es.crt", uuid),
			"admin-key":         getCertificateContents("system.admin.key", uuid),
			"admin-cert":        getCertificateContents("system.admin.crt", uuid),
			"admin-ca":          getCertificateContents("ca.crt", uuid),
		},
	)

	t.Logf("Creating secret %s/%s", elasticsearchSecret.Namespace, elasticsearchSecret.Name)

	err := f.Create(context.TODO(), elasticsearchSecret)
	if err != nil {
		return kverrors.Wrap(err, "failed to create elasticsearch secret",
			"name", elasticsearchSecret.Name,
			"namespace", elasticsearchSecret.Namespace)
	}

	return nil
}

func updateElasticsearchSecret(t *testing.T, f client.Client, uuid string) error {
	namespace := operatorNamespace

	elasticsearchSecret := &corev1.Secret{}

	name := elasticsearchNameFor(uuid)
	secretName := types.NamespacedName{Name: name, Namespace: namespace}

	if err := f.Get(context.TODO(), secretName, elasticsearchSecret); err != nil {
		return kverrors.Wrap(err, "failed to get secret",
			"name", secretName,
			"namespace", namespace)
	}

	elasticsearchSecret.Data = map[string][]byte{
		"elasticsearch.key": getCertificateContents("elasticsearch.key", uuid),
		"elasticsearch.crt": getCertificateContents("elasticsearch.crt", uuid),
		"logging-es.key":    getCertificateContents("logging-es.key", uuid),
		"logging-es.crt":    getCertificateContents("logging-es.crt", uuid),
		"admin-key":         getCertificateContents("system.admin.key", uuid),
		"admin-cert":        getCertificateContents("system.admin.crt", uuid),
		"admin-ca":          getCertificateContents("ca.crt", uuid),
		"dummy":             []byte("blah"),
	}

	t.Log("Updating required secret...")
	err := f.Update(context.TODO(), elasticsearchSecret)
	if err != nil {
		return err
	}

	return nil
}

func createKibanaCR(namespace string) *loggingv1.Kibana {
	cpuValue, _ := resource.ParseQuantity("100m")
	memValue, _ := resource.ParseQuantity("256Mi")

	return &loggingv1.Kibana{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kibana",
			Namespace: namespace,
		},
		Spec: loggingv1.KibanaSpec{
			ManagementState: loggingv1.ManagementStateManaged,
			Replicas:        1,
			Resources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: memValue,
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    cpuValue,
					corev1.ResourceMemory: memValue,
				},
			},
		},
	}
}

func createKibanaSecret(f client.Client, esUUID string) error {
	namespace := operatorNamespace

	kibanaSecret := utils.Secret(
		kibanaCRName,
		namespace,
		map[string][]byte{
			"key":  getCertificateContents("system.logging.kibana.key", esUUID),
			"cert": getCertificateContents("system.logging.kibana.crt", esUUID),
			"ca":   getCertificateContents("ca.crt", esUUID),
		},
	)

	err := f.Create(context.TODO(), kibanaSecret)
	if err != nil {
		return err
	}

	return nil
}

func createKibanaProxySecret(f client.Client, esUUID string) error {
	namespace := operatorNamespace

	kibanaProxySecret := utils.Secret(
		fmt.Sprintf("%s-proxy", kibanaCRName),
		namespace,
		map[string][]byte{
			"server-key":     getCertificateContents("kibana-internal.key", esUUID),
			"server-cert":    getCertificateContents("kibana-internal.crt", esUUID),
			"session-secret": []byte("TG85VUMyUHBqbWJ1eXo1R1FBOGZtYTNLTmZFWDBmbkY="),
		},
	)

	err := f.Create(context.TODO(), kibanaProxySecret)
	if err != nil {
		return err
	}

	return nil
}

func generateCertificates(t *testing.T, namespace, uuid string) error {
	workDir := fmt.Sprintf("%s/%s", exampleSecretsPath, uuid)
	storeName := elasticsearchNameFor(uuid)

	err := os.MkdirAll(workDir, os.ModePerm)
	if err != nil {
		return kverrors.Wrap(err, "failed to create certificate tmp dir",
			"dir", workDir)
	}

	cmd := exec.Command(projectRootDir+"/hack/cert_generation.sh", workDir, namespace, storeName)
	out, err := cmd.Output()
	if err != nil {
		return kverrors.Wrap(err, "failed to generate certificate",
			"store_name", storeName,
			"output", string(out))
	}

	return nil
}

func getCertificateContents(name, uuid string) []byte {
	filename := fmt.Sprintf("%s/%s/%s", exampleSecretsPath, uuid, name)
	return utils.GetFileContents(filename)
}

func getProjectRootPath(projectRootDir string) string {
	fmt.Println("### getProjectRootPath is running")
	cwd, err := os.Getwd()
	cwdOrig := cwd
	if err != nil {
		panic(err)
	}
	for {
		if strings.HasSuffix(cwd, "/"+projectRootDir) {
			return cwd
		}
		lastSlashIndex := strings.LastIndex(cwd, "/")
		if lastSlashIndex == -1 {
			panic(cwdOrig + " did not contain /" + projectRootDir)
		}
		cwd = cwd[0:lastSlashIndex]
		fmt.Printf("cwd %v\n", cwd)
	}
}

func waitForDeleteObject(t *testing.T, client client.Client, key types.NamespacedName, obj runtime.Object, retryInterval, timout time.Duration) error {
	return wait.Poll(retryInterval, timeout, func() (done bool, err error) {
		err = client.Get(context.Background(), key, obj)
		if err != nil {
			if apierrors.IsNotFound(err) {
				t.Logf("Object not found %s", key.String())
				return true, nil
			}
			return false, nil
		}
		err = client.Delete(context.Background(), obj)
		if err != nil {
			return false, nil
		}
		t.Logf("Deleting object %s", key.String())
		return true, nil
	})
}
