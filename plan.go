package main

import (
	"fmt"
	"knative.dev/pkg/apis"
	knservingv1 "knative.dev/serving/pkg/apis/serving/v1"
)

func main() {
	var ksvc knservingv1.Service
	cond := ksvc.Status.GetCondition(apis.ConditionReady)
	fmt.Printf("%T\n", cond)
}
