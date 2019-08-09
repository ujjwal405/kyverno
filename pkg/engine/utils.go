package engine

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/glog"

	"github.com/minio/minio/pkg/wildcard"
	types "github.com/nirmata/kyverno/pkg/apis/policy/v1alpha1"
	v1alpha1 "github.com/nirmata/kyverno/pkg/apis/policy/v1alpha1"
	client "github.com/nirmata/kyverno/pkg/dclient"
	"github.com/nirmata/kyverno/pkg/utils"
	v1helper "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

//ListResourcesThatApplyToPolicy returns list of resources that are filtered by policy rules
func ListResourcesThatApplyToPolicy(client *client.Client, policy *types.Policy, filterK8Resources []utils.K8Resource) map[string]resourceInfo {
	// key uid
	resourceMap := map[string]resourceInfo{}
	for _, rule := range policy.Spec.Rules {
		// Match
		for _, k := range rule.MatchResources.Kinds {
			namespaces := []string{}
			if k == "Namespace" {
				namespaces = []string{""}
			} else {
				if rule.MatchResources.Namespace != nil {
					// if namespace is specified then we add the namespace
					namespaces = append(namespaces, *rule.MatchResources.Namespace)
				} else {
					// no namespace specified, refer to all namespaces
					namespaces = getAllNamespaces(client)
				}

				// Check if exclude namespace is not clashing
				namespaces = excludeNamespaces(namespaces, rule.ExcludeResources.Namespace)
			}

			// If kind is namespace then namespace is "", override
			// Get resources in the namespace
			for _, ns := range namespaces {
				rMap := getResourcesPerNamespace(k, client, ns, rule, filterK8Resources)
				mergeresources(resourceMap, rMap)
			}
		}
	}
	return resourceMap
}

func getResourcesPerNamespace(kind string, client *client.Client, namespace string, rule types.Rule, filterK8Resources []utils.K8Resource) map[string]resourceInfo {
	resourceMap := map[string]resourceInfo{}
	// List resources
	list, err := client.ListResource(kind, namespace, rule.MatchResources.Selector)
	if err != nil {
		glog.Errorf("unable to list resource for %s with label selector %s", kind, rule.MatchResources.Selector.String())
		return nil
	}
	var selector labels.Selector
	// exclude label selector
	if rule.ExcludeResources.Selector != nil {
		selector, err = v1helper.LabelSelectorAsSelector(rule.ExcludeResources.Selector)
		if err != nil {
			glog.Error(err)
		}
	}
	for _, res := range list.Items {
		// exclude label selectors
		if selector != nil {
			set := labels.Set(res.GetLabels())
			if selector.Matches(set) {
				// if matches
				continue
			}
		}
		var name *string
		// match
		// name
		// wild card matching
		name = rule.MatchResources.Name
		if name != nil {
			// if does not match then we skip
			if !wildcard.Match(*name, res.GetName()) {
				continue
			}
		}
		// exclude
		// name
		// wild card matching
		name = rule.ExcludeResources.Name
		if name != nil {
			// if matches then we skip
			if wildcard.Match(*name, res.GetName()) {
				continue
			}
		}
		gvk := res.GroupVersionKind()

		ri := resourceInfo{Resource: res, Gvk: &metav1.GroupVersionKind{Group: gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind}}
		// Skip the filtered resources
		if utils.SkipFilteredResources(gvk.Kind, res.GetNamespace(), res.GetName(), filterK8Resources) {
			continue
		}

		resourceMap[string(res.GetUID())] = ri
	}
	return resourceMap
}

// merge b into a map
func mergeresources(a, b map[string]resourceInfo) {
	for k, v := range b {
		a[k] = v
	}
}

func getAllNamespaces(client *client.Client) []string {
	namespaces := []string{}
	// get all namespaces
	nsList, err := client.ListResource("Namespace", "", nil)
	if err != nil {
		glog.Error(err)
		return namespaces
	}
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.GetName())
	}
	return namespaces
}

func excludeNamespaces(namespaces []string, excludeNs *string) []string {
	if excludeNs == nil {
		return namespaces
	}
	filteredNamespaces := []string{}
	for _, n := range namespaces {
		if n == *excludeNs {
			continue
		}
		filteredNamespaces = append(filteredNamespaces, n)
	}
	return filteredNamespaces
}

//MatchesResourceDescription checks if the resource matches resource desription of the rule or not
func MatchesResourceDescription(resource *unstructured.Unstructured, rule v1alpha1.Rule, gvk metav1.GroupVersionKind) bool {
	matches := rule.MatchResources.ResourceDescription
	exclude := rule.ExcludeResources.ResourceDescription

	if !findKind(matches.Kinds, gvk.Kind) {
		return false
	}

	// meta := parseMetadataFromObject(resourceRaw)
	name := resource.GetName()
	namespace := resource.GetNamespace()

	if matches.Name != nil {
		// Matches
		if !wildcard.Match(*matches.Name, name) {
			return false
		}
	}
	// Exclude
	// the resource name matches the exclude resource name then reject
	if exclude.Name != nil {
		if wildcard.Match(*exclude.Name, name) {
			return false
		}
	}
	// Matches
	if matches.Namespace != nil && *matches.Namespace != namespace {
		return false
	}
	// Exclude
	if exclude.Namespace != nil && *exclude.Namespace == namespace {
		return false
	}
	// Matches
	if matches.Selector != nil {
		selector, err := metav1.LabelSelectorAsSelector(matches.Selector)
		if err != nil {
			glog.Error(err)
			return false
		}
		if !selector.Matches(labels.Set(resource.GetLabels())) {
			return false
		}
	}
	// Exclude
	if exclude.Selector != nil {
		selector, err := metav1.LabelSelectorAsSelector(exclude.Selector)
		// if the label selector is incorrect, should be fail or
		if err != nil {
			glog.Error(err)
			return false
		}
		if selector.Matches(labels.Set(resource.GetLabels())) {
			return false
		}
	}
	return true
}

// ResourceMeetsDescription checks requests kind, name and labels to fit the policy rule
func ResourceMeetsDescription(resourceRaw []byte, matches v1alpha1.ResourceDescription, exclude v1alpha1.ResourceDescription, gvk metav1.GroupVersionKind) bool {
	if !findKind(matches.Kinds, gvk.Kind) {
		return false
	}

	if resourceRaw != nil {
		meta := parseMetadataFromObject(resourceRaw)
		name := ParseNameFromObject(resourceRaw)
		namespace := ParseNamespaceFromObject(resourceRaw)

		if matches.Name != nil {
			// Matches
			if !wildcard.Match(*matches.Name, name) {
				return false
			}
		}
		// Exclude
		// the resource name matches the exclude resource name then reject
		if exclude.Name != nil {
			if wildcard.Match(*exclude.Name, name) {
				return false
			}
		}
		// Matches
		if matches.Namespace != nil && *matches.Namespace != namespace {
			return false
		}
		// Exclude
		if exclude.Namespace != nil && *exclude.Namespace == namespace {
			return false
		}
		// Matches
		if matches.Selector != nil {
			selector, err := metav1.LabelSelectorAsSelector(matches.Selector)
			if err != nil {
				glog.Error(err)
				return false
			}
			if meta != nil {
				labelMap := parseLabelsFromMetadata(meta)
				if !selector.Matches(labelMap) {
					return false
				}
			}
		}
		// Exclude
		if exclude.Selector != nil {
			selector, err := metav1.LabelSelectorAsSelector(exclude.Selector)
			// if the label selector is incorrect, should be fail or
			if err != nil {
				glog.Error(err)
				return false
			}

			if meta != nil {
				labelMap := parseLabelsFromMetadata(meta)
				if selector.Matches(labelMap) {
					return false
				}
			}
		}

	}
	return true
}

func parseMetadataFromObject(bytes []byte) map[string]interface{} {
	var objectJSON map[string]interface{}
	json.Unmarshal(bytes, &objectJSON)
	meta, ok := objectJSON["metadata"].(map[string]interface{})
	if !ok {
		return nil
	}
	return meta
}

// ParseResourceInfoFromObject get kind/namepace/name from resource
func ParseResourceInfoFromObject(rawResource []byte) string {

	kind := ParseKindFromObject(rawResource)
	namespace := ParseNamespaceFromObject(rawResource)
	name := ParseNameFromObject(rawResource)
	return strings.Join([]string{kind, namespace, name}, "/")
}

//ParseKindFromObject get kind from resource
func ParseKindFromObject(bytes []byte) string {
	var objectJSON map[string]interface{}
	json.Unmarshal(bytes, &objectJSON)

	return objectJSON["kind"].(string)
}

func parseLabelsFromMetadata(meta map[string]interface{}) labels.Set {
	if interfaceMap, ok := meta["labels"].(map[string]interface{}); ok {
		labelMap := make(labels.Set, len(interfaceMap))

		for key, value := range interfaceMap {
			labelMap[key] = value.(string)
		}
		return labelMap
	}
	return nil
}

//ParseNameFromObject extracts resource name from JSON obj
func ParseNameFromObject(bytes []byte) string {
	var objectJSON map[string]interface{}
	json.Unmarshal(bytes, &objectJSON)
	meta, ok := objectJSON["metadata"]
	if !ok {
		return ""
	}

	metaMap, ok := meta.(map[string]interface{})
	if !ok {
		return ""
	}
	if name, ok := metaMap["name"].(string); ok {
		return name
	}
	return ""
}

// ParseNamespaceFromObject extracts the namespace from the JSON obj
func ParseNamespaceFromObject(bytes []byte) string {
	var objectJSON map[string]interface{}
	json.Unmarshal(bytes, &objectJSON)
	meta, ok := objectJSON["metadata"]
	if !ok {
		return ""
	}
	metaMap, ok := meta.(map[string]interface{})
	if !ok {
		return ""
	}

	if name, ok := metaMap["namespace"].(string); ok {
		return name
	}

	return ""
}

// ParseRegexPolicyResourceName returns true if policyResourceName is a regexp
func ParseRegexPolicyResourceName(policyResourceName string) (string, bool) {
	regex := strings.Split(policyResourceName, "regex:")
	if len(regex) == 1 {
		return regex[0], false
	}
	return strings.Trim(regex[1], " "), true
}

func getAnchorsFromMap(anchorsMap map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	for key, value := range anchorsMap {
		if isConditionAnchor(key) || isExistanceAnchor(key) {
			result[key] = value
		}
	}

	return result
}

func getElementsFromMap(anchorsMap map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	anchors := make(map[string]interface{})
	elementsWithoutanchor := make(map[string]interface{})
	for key, value := range anchorsMap {
		if isConditionAnchor(key) || isExistanceAnchor(key) {
			anchors[key] = value
		} else if !isAddingAnchor(key) {
			elementsWithoutanchor[key] = value
		}
	}

	return anchors, elementsWithoutanchor
}

func getAnchorFromMap(anchorsMap map[string]interface{}) (string, interface{}) {
	for key, value := range anchorsMap {
		if isConditionAnchor(key) || isExistanceAnchor(key) {
			return key, value
		}
	}

	return "", nil
}

func findKind(kinds []string, kindGVK string) bool {
	for _, kind := range kinds {
		if kind == kindGVK {
			return true
		}
	}
	return false
}

func isConditionAnchor(str string) bool {
	if len(str) < 2 {
		return false
	}

	return (str[0] == '(' && str[len(str)-1] == ')')
}

func getRawKeyIfWrappedWithAttributes(str string) string {
	if len(str) < 2 {
		return str
	}

	if str[0] == '(' && str[len(str)-1] == ')' {
		return str[1 : len(str)-1]
	} else if (str[0] == '$' || str[0] == '^' || str[0] == '+') && (str[1] == '(' && str[len(str)-1] == ')') {
		return str[2 : len(str)-1]
	} else {
		return str
	}
}

func isStringIsReference(str string) bool {
	if len(str) < len(referenceSign) {
		return false
	}

	return str[0] == '$' && str[1] == '(' && str[len(str)-1] == ')'
}

func isExistanceAnchor(str string) bool {
	left := "^("
	right := ")"

	if len(str) < len(left)+len(right) {
		return false
	}

	return (str[:len(left)] == left && str[len(str)-len(right):] == right)
}

func isAddingAnchor(key string) bool {
	const left = "+("
	const right = ")"

	if len(key) < len(left)+len(right) {
		return false
	}

	return left == key[:len(left)] && right == key[len(key)-len(right):]
}

// Checks if array object matches anchors. If not - skip - return true
func skipArrayObject(object, anchors map[string]interface{}) bool {
	for key, pattern := range anchors {
		key = key[1 : len(key)-1]

		value, ok := object[key]
		if !ok {
			return true
		}

		if !ValidateValueWithPattern(value, pattern) {
			return true
		}
	}

	return false
}

// removeAnchor remove special characters around anchored key
func removeAnchor(key string) string {
	if isConditionAnchor(key) {
		return key[1 : len(key)-1]
	}

	if isExistanceAnchor(key) || isAddingAnchor(key) {
		return key[2 : len(key)-1]
	}

	return key
}

// convertToFloat converts string and any other value to float64
func convertToFloat(value interface{}) (float64, error) {
	switch typed := value.(type) {
	case string:
		var err error
		floatValue, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, err
		}

		return floatValue, nil
	case float64:
		return typed, nil
	case int64:
		return float64(typed), nil
	case int:
		return float64(typed), nil
	default:
		return 0, fmt.Errorf("Could not convert %T to float64", value)
	}
}

type resourceInfo struct {
	Resource unstructured.Unstructured
	Gvk      *metav1.GroupVersionKind
}

func convertToUnstructured(data []byte) (*unstructured.Unstructured, error) {
	resource := &unstructured.Unstructured{}
	err := resource.UnmarshalJSON(data)
	if err != nil {
		glog.V(4).Infof("failed to unmarshall resource: %v", err)
		return nil, err
	}
	return resource, nil
}
