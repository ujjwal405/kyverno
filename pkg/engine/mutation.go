package engine

import (
	"github.com/golang/glog"
	kubepolicy "github.com/nirmata/kyverno/pkg/apis/policy/v1alpha1"
	"github.com/nirmata/kyverno/pkg/info"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Mutate performs mutation. Overlay first and then mutation patches
//TODO: check if gvk needs to be passed or can be set in resource
func Mutate(policy kubepolicy.Policy, rawResource []byte, gvk metav1.GroupVersionKind) ([][]byte, []*info.RuleInfo) {
	//TODO: convert rawResource to unstructured to avoid unmarhalling all the time for get some resource information
	//TODO: pass unstructured instead of rawResource ?
	resource, err := convertToUnstructured(rawResource)
	if err != nil {
		glog.Errorf("unable to convert raw resource to unstructured: %v", err)
	}
	var patches [][]byte
	var ruleInfos []*info.RuleInfo

	for _, rule := range policy.Spec.Rules {
		if rule.Mutation == nil {
			continue
		}

		// check if the resource satisfies the filter conditions defined in the rule
		//TODO: this needs to be extracted, to filter the resource so that we can avoid passing resources that
		// dont statisfy a policy rule resource description
		ok := MatchesResourceDescription(resource, rule, gvk)
		if !ok {
			glog.V(4).Infof("resource %s/%s does not satisfy the resource description for the rule ", resource.GetNamespace(), resource.GetName())
			continue
		}

		ruleInfo := info.NewRuleInfo(rule.Name, info.Mutation)

		// Process Overlay
		if rule.Mutation.Overlay != nil {
			oPatches, err := processOverlay(resource, rule, rawResource)
			if err == nil {
				if len(oPatches) == 0 {
					// if array elements dont match then we skip(nil patch, no error)
					// or if acnohor is defined and doenst match
					// policy is not applicable
					glog.V(4).Info("overlay does not match, so skipping applying rule")
					continue
				}

				glog.V(4).Infof("overlay applied succesfully on resource %s/%s", resource.GetNamespace(), resource.GetName())
				ruleInfo.Add("Overlay succesfully applied")

				// update rule information
				// strip slashes from string
				patch := JoinPatches(oPatches)
				ruleInfo.Changes = string(patch)
				patches = append(patches, oPatches...)
			} else {
				glog.V(4).Infof("failed to apply overlay: %v", err)
				ruleInfo.Fail()
				ruleInfo.Addf("failed to apply overlay: %v", err)
			}
		}

		// Process Patches
		if len(rule.Mutation.Patches) != 0 {
			jsonPatches, errs := processPatches(rule, rawResource)
			if len(errs) > 0 {
				ruleInfo.Fail()
				for _, err := range errs {
					glog.V(4).Infof("failed to apply patches: %v", err)
					ruleInfo.Addf("patches application has failed, err %v.", err)
				}
			} else {
				glog.V(4).Infof("patches applied succesfully on resource %s/%s", resource.GetNamespace(), resource.GetName())
				ruleInfo.Addf("Patches succesfully applied.")
				patches = append(patches, jsonPatches...)
			}
		}
		ruleInfos = append(ruleInfos, ruleInfo)
	}
	return patches, ruleInfos
}
