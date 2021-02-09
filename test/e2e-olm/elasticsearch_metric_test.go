package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/operator-framework/operator-sdk/pkg/test"
	"github.com/operator-framework/operator-sdk/pkg/test/e2eutil"
	corev1 "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/elasticsearch-operator/test/utils"
)

var (
	unauthorizedSaName string
	authorizedSaName   string
	clusterRoleName    string
)

func TestElasticsearchOperatorMetrics(t *testing.T) {
	if unauthorizedSaName = os.Getenv("UNAUTHORIZED_SA"); unauthorizedSaName == "" {
		t.Fatal("UNAUTHORIZED_SA is unset")
	}

	if authorizedSaName = os.Getenv("AUTHORIZED_SA"); authorizedSaName == "" {
		t.Fatal("AUTHORIZED_SA is unset")
	}

	if clusterRoleName = os.Getenv("CLUSTERROLE"); clusterRoleName == "" {
		t.Fatal("CLUSTERROLE is unset")
	}

	registerSchemes(t)
	t.Run("Operator metrics", operatorMetricsTest)
}

func operatorMetricsTest(t *testing.T) {
	f := test.Global

	ctx := test.NewContext(t)
	namespace, err := ctx.GetWatchNamespace()
	if err != nil {
		t.Fatal(err)
	}

	// Deploy a single node cluster, wait for success
	esUUID := utils.GenerateUUID()
	t.Logf("Using UUID for elasticsearch CR: %v", esUUID)

	if err = createElasticsearchSecret(t, f, ctx, esUUID); err != nil {
		t.Fatal(err)
	}

	dataUUID := utils.GenerateUUID()
	t.Logf("Using GenUUID for data nodes: %v", dataUUID)

	cr, err := createElasticsearchCR(t, f, ctx, esUUID, dataUUID, 1)
	if err != nil {
		t.Fatalf("could not create exampleElasticsearch: %v", err)
	}

	dplName := fmt.Sprintf("elasticsearch-%v-cdm-%v-1", esUUID, dataUUID)
	err = e2eutil.WaitForDeployment(t, f.KubeClient, namespace, dplName, 1, retryInterval, timeout)
	if err != nil {
		t.Fatalf("timed out waiting for first node deployment %v: %v", dplName, err)
	}
	matchingLabels := map[string]string{
		"cluster-name": cr.GetName(),
		"component":    "elasticsearch",
	}
	pods, err := utils.WaitForPods(t, f, namespace, matchingLabels, retryInterval, timeout)
	if err != nil {
		t.Fatalf("failed to wait for pods: %v", err)
	}

	// create two service accounts
	authorizedSA, err := newServiceAccount(t, f, ctx, namespace, authorizedSaName)
	if err != nil {
		t.Fatal(err)
	}

	unauthorizedSA, err := newServiceAccount(t, f, ctx, namespace, unauthorizedSaName)
	if err != nil {
		t.Fatal(err)
	}

	// Creating RBAC for authorised serviceaccount to verify metrics
	_, err = newClusterRole(t, f, ctx, clusterRoleName)

	bindClusterRoleWithSA(t, f, ctx, clusterRoleName, clusterRoleName, authorizedSA)
	bindClusterRoleWithSA(t, f, ctx, "system:basic-user", "view-"+clusterRoleName, authorizedSA)

	//  Creating RBAC for unauthorised serviceaccount to verify metrics
	bindClusterRoleWithSA(t, f, ctx, "system:basic-user", "view-"+clusterRoleName+"-unauth", unauthorizedSA)

	// get serviceAccount token
	var getSaToken func(saName string) string
	getSaToken = func(saName string) string {
		sa := &corev1.ServiceAccount{}
		key := client.ObjectKey{Name: saName, Namespace: namespace}
		if err := f.Client.Get(context.TODO(), key, sa); err != nil {
			t.Errorf("can not get sa %s", saName)
		}

		secret := &corev1.Secret{}
		for _, se := range sa.Secrets {
			if strings.Index(se.DeepCopy().Name, saName+"-token") >= 0 {
				key := client.ObjectKey{Name: se.Name, Namespace: namespace}
				if err := f.Client.Get(context.TODO(), key, secret); err != nil {
					t.Errorf("cannot get secret %s", se.Name)
				}
				break
			}
		}
		if secret == nil {
			return ""
		}
		token := string(secret.Data["token"])
		return token
	}

	podName := pods.Items[0].GetName()
	token := getSaToken(authorizedSaName)
	if token == "" {
		t.Errorf("secret token not exist for %s", authorizedSaName)
	}
	cmd := fmt.Sprintf("curl -ks -o /tmp/mymetrics.txt https://%s-metrics.%s.svc:60001/_prometheus/metrics -H Authorization:'Bearer %s' -w '%%{response_code}\\n'", cr.GetName(), namespace, token)
	code, _, err := ExecInPod(f.KubeConfig, namespace, podName, cmd, "elasticsearch")
	if code != "200" {
		t.Errorf("Authorized service account should have access to es metrics")
	}
	token = getSaToken(unauthorizedSaName)
	if token == "" {
		t.Errorf("secret token not exist for %s", unauthorizedSaName)
	}
	cmd = fmt.Sprintf("curl -ks -o /tmp/mymetrics.txt https://%s-metrics.%s.svc:60001/_prometheus/metrics -H Authorization:'Bearer %s' -w '%%{response_code}\\n'", cr.GetName(), namespace, token)
	code, _, err = ExecInPod(f.KubeConfig, namespace, podName, cmd, "elasticsearch")
	if code != "403" {
		t.Errorf("Unauthorized service account must not have access to es metrics")
	}

	ctx.Cleanup()
	_ = e2eutil.WaitForDeletion(t, f.Client.Client, cr, cleanupRetryInterval, cleanupTimeout)
	t.Log("Finished successfully")
}

func newServiceAccount(t *testing.T, f *test.Framework, ctx *test.Context, namespace, name string) (*corev1.ServiceAccount, error) {
	sa := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}

	cleanupOpts := &test.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}

	err := f.Client.Create(context.TODO(), sa, cleanupOpts)
	if err != nil {
		return nil, err
	}

	return sa, nil
}

func newClusterRole(t *testing.T, f *test.Framework, ctx *test.Context, name string) (*rbac.ClusterRole, error) {
	cr := &rbac.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRole",
			APIVersion: rbac.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Rules: []rbac.PolicyRule{
			{
				NonResourceURLs: []string{"/metrics"},
				Verbs:           []string{"get"},
			},
		},
	}

	cleanupOpts := &test.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}

	err := f.Client.Create(context.TODO(), cr, cleanupOpts)
	if err != nil {
		return nil, err
	}
	return cr, nil
}

func bindClusterRoleWithSA(t *testing.T, f *test.Framework, ctx *test.Context, roleName, name string, sa *corev1.ServiceAccount) (*rbac.ClusterRoleBinding, error) {
	crb := &rbac.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Subjects: []rbac.Subject{{
			Kind:      "ServiceAccount",
			Name:      sa.GetName(),
			Namespace: sa.GetNamespace(),
		}},
		RoleRef: rbac.RoleRef{
			Kind: "ClusterRole",
			Name: roleName,
		},
	}
	cleanupOpts := &test.CleanupOptions{
		TestContext:   ctx,
		Timeout:       cleanupTimeout,
		RetryInterval: cleanupRetryInterval,
	}
	err := f.Client.Create(context.TODO(), crb, cleanupOpts)
	if err != nil {
		return nil, err
	}
	return crb, nil
}
