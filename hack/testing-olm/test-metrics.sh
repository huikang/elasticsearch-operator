#!/bin/bash
# This test verifies only serviceaccounts with the desired rolebindings are 
# allowed to retrieve metrices from elasticsearch
set -euo pipefail

repo_dir="$(dirname $0)/../.."
source "${repo_dir}/hack/lib/init.sh"
source "${repo_dir}/hack/testing-olm/utils"

test_artifact_dir=$ARTIFACT_DIR/$(basename ${BASH_SOURCE[0]})
if [ ! -d $test_artifact_dir ] ; then
  mkdir -p $test_artifact_dir
fi

os::test::junit::declare_suite_start "[Elasticsearch] Verify Metrics Access"

suffix=001
TEST_NAMESPACE="openshift-operators-redhat"
UNAUTHORIZED_SA="unauthorized-sa-${suffix}"
AUTHORIZED_SA="authorized-sa-${suffix}"
CLUSTERROLE="prometheus-k8s-${suffix}"
es_pod="elasticsearch-2eddwtr6-cdm-40wx2lzk-1-756f4f8965-q2cjj"

start_seconds=$(date +%s)
cleanup(){
  local return_code="$?"

  os::test::junit::declare_suite_end

  set +e
  os::log::info "Running cleanup"
  end_seconds=$(date +%s)
  runtime="$(($end_seconds - $start_seconds))s"
  
  if [ "${DO_CLEANUP:-true}" == "true" ] ; then
    if [ "$return_code" != "0" ] ; then
      gather_logging_resources ${TEST_NAMESPACE} $test_artifact_dir
    fi
    for item in "ns/${TEST_NAMESPACE}" "ns/openshift-operators-redhat"; do
      oc delete $item --wait=true --ignore-not-found --force --grace-period=0
    done
    oc delete clusterrole ${CLUSTERROLE} >> $test_artifact_dir/cleanup.log 2>&1 ||:
    oc delete clusterrolebinding ${CLUSTERROLE} >> $test_artifact_dir/cleanup.log 2>&1 ||:
    oc delete clusterrolebinding view-${CLUSTERROLE} >> $test_artifact_dir/cleanup.log 2>&1 ||:
  fi
  
  set -e
  exit ${return_code}
}
# trap cleanup exit

# TEST_WATCH_NAMESPACE=${TEST_NAMESPACE} go test -v ./test/e2e-olm/... -root=$(pwd) -kubeconfig=${KUBECONFIG} -globalMan test/files/dummycrd.yaml -parallel=1 -timeout 1500s -run TestElasticsearchOperatorMetrics

pod=$(oc -n $TEST_NAMESPACE get pod -l component=elasticsearch -o jsonpath={.items[0].metadata.name})
os::log::info "---------------------------------------------------------------"
os::log::info Attempt to autocreate an index without a '-write' suffix...
os::log::info "---------------------------------------------------------------"
os::cmd::expect_success_and_text "oc -n $TEST_NAMESPACE exec $pod -c elasticsearch -- es_util --query=foo/_doc/2 -d '{\"key\":\"value\"}' -XPUT -w %{http_code}" ".*201"
