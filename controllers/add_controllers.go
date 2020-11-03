package controller

import (
	"github.com/openshift/elasticsearch-operator/internal/controller/elasticsearch"
	"github.com/openshift/elasticsearch-operator/internal/controller/kibana"
)

func init() {
	AddToManagerFuncs = append(AddToManagerFuncs,
		kibana.Add,
		elasticsearch.Add)
}
